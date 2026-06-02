# Last-Mile Private-Road Access (Phase 2a)

**Date:** 2026-06-02
**Status:** Partially shipped — leak-aware inline filter merged; full connector deferred (see Outcome)
**Builds on:** the time-metric + robust-snapping work (`feat/time-based-routing-metric`, binary v3)
**Branch:** `feat/last-mile-private-access`

## Problem

After Phase 1, map_router matches Google well on the 137 Google ground-truth routes
(median |distance error| ~5.8%, overlap ~0.67) **except** at delivery endpoints inside
gated/private areas. **38 of 137** Google routes have an endpoint that snaps >50 m from
Google's point (10 at >200 m, 14 at 100–200 m, 14 at 50–100 m), and the one remaining
>1.5× route (`a29476`, 1.87×) is one of these: its destination is inside a gated estate
whose nearest *public* road is 362 m away.

Root cause (confirmed against the OSM extract): these destinations sit on `access=private`
gated-estate roads (Sungai Long, Tropicana TR; private driveways). The parser
(`pkg/osm/parser.go` `isCarAccessible`, lines 64–85) currently drops a car-class way only
when `access=no`, `access=private`, or `motor_vehicle=no`. So `access=private` estates are
dropped and map_router snaps to the nearest public road instead; Google routes *into* the
estate for last-mile delivery. Drivers can enter (gate codes / guardhouse), so map_router
should too — but **never as a through-shortcut**.

> **Current-behavior note (corrected after review):** `access=destination` and
> `access=customers` are **not** dropped today — they already route as normal public roads,
> and in Malaysian OSM `access=destination` is typically a genuine public no-through-traffic
> tag on through-roads. This design leaves them untouched. Only `access=private` (plus the
> gated values `permit`/`residents`) is un-dropped.

## Goal & Measurable Success Criteria

Let map_router reach origins/destinations on gated/private roads by including those roads in
the routable graph — but only where doing so cannot create a through-shortcut. Measured on
the 137 Google routes via `compare_google.py`:

| metric | before (Phase 1) | target |
|---|---|---|
| gated endpoints (>50 m snap displacement) | 38 routes | endpoint displacement → small on the single-gate estates |
| route `a29476` ratio | 1.87× | < 1.5× |
| median distance error / overlap (all 137) | −1.6% / 0.67 | improve on the gated subset; no worse overall |
| validity | 137/137 | 137/137 |

**Hard safety gate — no through-leakage (corrected metric):** for the 99 non-gated Google
routes **and** the 262 real `custom` routes, the **total route cost (`mu`) must be unchanged**
vs the Phase-1 graph. (A leak would route a non-gated query through a newly-added private
road and *lower* its cost; unchanged cost ⇔ no leak affected it.) We do **not** require
byte-for-byte geometry — adding edges shifts node indices and CH tie-breaking, which changes
some equal-cost paths' geometry at zero leakage, so cost-equality is the correct gate.

## Approach (A-prime) — inline only cul-de-sac private roads, contracted normally

**Insight from the review:** gated estates and private driveways are **cul-de-sacs** w.r.t.
the public network — they attach at a single gate (articulation node). A shortest path
between two public nodes can never route through a single-gate cul-de-sac (it would enter
and leave by the same node — a loop), so CH's witness search never builds a leaking shortcut
across one. Therefore such roads can be added to the **single, unified graph and contracted
normally** — no separate CH tier, no local connector, no query-time access logic, no binary
format change. The destination snaps onto the estate road (snapper already indexes the full
graph) and the ordinary bidirectional CH search drives in through the gate.

Only restricted clusters that touch the public network at **≥2 distinct nodes** (a potential
private cut-through / "bridge") are left excluded (today's behavior), since those *could*
leak. These are rare; revisit only if they appear.

### Key Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Un-dropped (restricted) access set | `access ∈ {private, permit, residents}` | The gated-estate values in MY data; cars enter for last-mile via gate code/guard |
| Left as-is (already public) | `access ∈ {destination, customers}` | Already routable today; demoting them would break legitimate through-routes |
| Still excluded | `access=no`, non-car highway, `area=yes`, `motor_vehicle=no` (without a permissive override) | Genuinely closed / non-drivable |
| Inline vs exclude a restricted cluster | inline iff it touches the public graph at **≤1 node** (cul-de-sac); else exclude | Single-gate cul-de-sacs provably can't be a through-shortcut; ≥2-touch could leak |
| CH / routing / binary | **unchanged** | Inlined edges are ordinary edges; no two-tier, no connector, no v4 |
| Safety gate | total cost unchanged on non-gated + custom routes | Detects any leak; robust to tie-break geometry shifts |

## Component Changes (small, isolated)

### 1. `pkg/osm` — access classification
- New `classifyAccess(tags) (keep bool, restricted bool)`:
  - non-car highway / `area=yes` → drop.
  - `access=no` → drop. `motor_vehicle=no` and no permissive `access` → drop.
  - `access ∈ {private, permit, residents}` → keep, **restricted=true**.
  - else → keep, restricted=false (public; includes `destination`/`customers` as today).
- `RawEdge` gains a transient `Restricted bool`. `Parse` now emits restricted ways too
  (instead of dropping `access=private`), flagged. Weight = travel time as today.
- (Out of scope for v1: `motorcar=`/`vehicle=` hierarchy and node-level `barrier=gate` —
  see Out of Scope. Documented as known gaps, not silently ignored.)

### 2. `pkg/graph` — build-time cul-de-sac filter (the one real new algorithm)
- `Build` carries the `Restricted` flag into a transient per-edge `restricted []bool`
  (aligned with the CSR), used only during preprocessing — **not** serialized.
- New `FilterBridgingRestricted(g, restricted) *Graph`:
  - Union-find over the **restricted** edges (by node) → restricted clusters.
  - For each cluster, count distinct **public-touch** nodes (cluster nodes that also have a
    public incident edge). Inline the cluster (keep its edges as normal) iff ≤1 public-touch
    node; otherwise drop the cluster's restricted edges.
  - Returns a graph whose remaining edges are all "normal" (no access distinction survives).
- After this filter, the existing `LargestComponent` + `FilterToComponent` + `ch.Contract`
  run **unchanged** (they already operate on whatever graph they're handed). Isolated estates
  not strongly connected to the main component are dropped as today.

### 3. `cmd/preprocess` — wiring
- Call `FilterBridgingRestricted` after `Build` and before `LargestComponent`. Log how many
  restricted clusters were inlined vs excluded (visibility). No flag or binary changes; the
  output is still a v3 graph.

`pkg/ch`, `pkg/routing`, `pkg/graph/binary.go`, and the server are **unchanged.**

## Data Flow

```
OSM PBF ─parse(classifyAccess)→ edges (public + restricted, flagged)
       → graph.Build (combined CSR + transient restricted[])
       → FilterBridgingRestricted (inline ≤1-touch cul-de-sac clusters, drop ≥2-touch)
       → LargestComponent → FilterToComponent → ch.Contract   (ALL UNCHANGED)
       → graph.bin (v3, no format change)
server (unchanged): snap (now finds estate roads) → CH search drives in via the gate
```

## Error Handling
- Estate not strongly connected to the main component (e.g. one-way gate that breaks the
  SCC) → dropped by `FilterToComponent` as today → endpoint falls back to nearest-public
  snap (current behavior). Acceptable for v1.
- `≥2`-touch restricted clusters excluded → those gated areas remain unreachable as today.
- No new error codes; no format/version change.

## Testing Strategy
- `pkg/osm`: `classifyAccess` table tests — `access=private/permit/residents` → keep+restricted;
  `access=no` → drop; `access=private`+`motor_vehicle=no` → keep+restricted (access governs);
  plain `motor_vehicle=no` → drop; `access=destination`/`customers` → keep+public (unchanged).
- `pkg/graph`: `FilterBridgingRestricted` unit tests — (a) single-gate cul-de-sac estate is
  inlined and reachable; (b) a restricted cluster bridging two public nodes is **excluded**;
  (c) a restricted edge whose inclusion would shortcut two public points is dropped.
- **No-leakage test (the safety property):** synthetic graph with a private cut-through
  between two public points that is geometrically shorter — assert it is excluded and a
  public→public route does **not** use it.
- **Into-estate test:** synthetic public ring + single-gate private cul-de-sac — assert a
  destination inside routes in through the gate.
- End-to-end: `compare_google.py` on 137 — single-gate gated endpoints improve, `a29476` < 1.5×.
- **Non-regression cost gate (hard):** route the 99 non-gated Google + 262 custom pairs on the
  Phase-1 graph vs the new graph; assert **total cost unchanged** for every one (proves no leak).

## Risks & Mitigations
- **Through-leakage** (the critical risk): structurally prevented — only ≤1-public-touch
  cul-de-sac clusters are inlined (provably non-bridging); ≥2-touch clusters stay excluded.
  Proven by the no-leakage unit test + the non-regression cost gate.
- **Cul-de-sac misclassification:** a genuinely-bridging cluster mis-counted as ≤1-touch would
  leak — covered by the cost gate (it would change a non-gated route's cost) and by precise
  public-touch counting in the unit tests.
- **One-way-gate estates dropped by SCC:** accepted (today's behavior); a small subset of
  gated endpoints won't improve. Quantified by the 137-route eval.

## Out of Scope (documented gaps / later threads)
- **Node-level `barrier=gate`/`lift_gate` gating** (~14k restrictive gate nodes in the extract):
  the dominant real-world gating mechanism. Ignored in v1 (way-level only). This is both a
  reachability gap (publicly-tagged spurs gated only at the entrance node) and a latent
  leakage source (a public way through a gate node). **Highest-value follow-up.**
- **`motorcar=`/`vehicle=` access hierarchy** (~525 car-prohibited public roads currently
  drivable; a few gated driveways tagged via `motorcar`): a pre-existing parser gap, not
  introduced here; worth a separate fix.
- **≥2-public-touch private cut-throughs:** left excluded (unreachable) in v1.
- **Inferred traffic / congestion** and **turn/junction penalties** (other Phase 2 threads).

## Outcome (2026-06-02) — supersedes the ≤1-touch rule described above

The ≤1-touch cul-de-sac rule (above) was **superseded during implementation** by a
**leak-aware filter**: a restricted cluster is inlined unless the path *through* it is
faster than the public network between its gateways (a genuine time-shortcut), in which
case it is excluded. This is leak-free (validated over ~700k fuzzed graphs) and is what
shipped on this branch.

**What we learned (and why Phase 2a stops here):**
- The cul-de-sac (≤1-touch) rule fixed only **4 of 38** gated routes — real Malaysian
  estates are **multi-gate**, so the rule excluded them.
- The leak-aware rule fixed only **~2** — the target estates turn out to be genuine
  **time-shortcuts** (cutting through is faster than the public detour), so the filter
  correctly excludes them to avoid leakage.
- **Inlining ALL** private roads fixes ~31 routes (overlap 0.67→0.75, error 5.8→3.8%,
  anomaly gone) but **leaks** (~15–18 non-gated routes cut through private roads).
- Conclusion: **inlining cannot distinguish "enter to deliver" from "cut through."**
  Only the **connector (the original Approach A)** — restricted edges kept out of the
  CH overlay and bridged in only at a snapped origin/destination — can reach these
  estates with zero leakage. It is the documented next step if gated last-mile accuracy
  becomes a priority; **deferred by decision** (the shipped leak-aware filter's small,
  safe gain was judged sufficient for now).

**Shipped:** parser access classification + `Graph.EdgeRestricted` + leak-aware
`FilterBridgingRestricted` + preprocess wiring. **No** CH/routing/binary changes.
