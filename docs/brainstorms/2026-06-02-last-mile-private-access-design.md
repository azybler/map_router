# Last-Mile Private-Road Access (Phase 2a)

**Date:** 2026-06-02
**Status:** Design (approved for planning)
**Builds on:** the time-metric + robust-snapping work (`feat/time-based-routing-metric`, binary v3)
**Branch:** `feat/last-mile-private-access`

## Problem

After the time-metric + snapping work, map_router matches Google well on the 137
Google ground-truth routes (median |distance error| ~5.8%, overlap ~0.67), **except**
at delivery endpoints inside gated/private areas. **38 of 137** Google routes have
an endpoint that snaps >50 m from Google's point (10 at >200 m, 14 at 100–200 m,
14 at 50–100 m), and the single remaining >1.5× route (`a29476`, 1.87×) is one of
these: its destination is inside a gated estate whose nearest *public* road is
362 m away.

Root cause (confirmed against the OSM extract): these destinations sit on roads
tagged `access=private` / `motor_vehicle=no` (gated estates like Sungai Long,
Tropicana TR; private driveways). The parser (`pkg/osm/parser.go` `isCarAccessible`,
lines 64–86) deliberately drops them, so map_router can't reach them and snaps to
the nearest public road instead. Google routes *into* these roads for last-mile
delivery. Drivers can enter (gate codes / guardhouse), so map_router should too —
but **only** to reach an origin/destination on them, never as a through-shortcut.

## Goal & Measurable Success Criteria

Let map_router route onto private/gated roads for the local first/last-mile
connection only. Measured on the 137 Google routes via `compare_google.py`:

| metric | before (Phase 1) | target |
|---|---|---|
| gated endpoints (>50 m snap displacement) | 38 routes | endpoint displacement → small (≤ ~30 m typical) on most |
| route `a29476` ratio | 1.87× | < 1.5× |
| median distance error / overlap (all 137) | −1.6% / 0.67 | improve on the gated subset; **no worse** overall |
| validity | 137/137 | 137/137 |

**Hard safety gate — zero through-leakage (the property that makes this safe):**
the 99 non-gated Google routes **and** the 262 real `custom` routes must be
**byte-for-byte unchanged** vs the Phase-1 build. Private roads must never become
a through-shortcut. This is asserted by a before/after route diff.

## Approach (A) — public CH overlay + restricted local connector

Contraction Hierarchies contract the whole graph into shortcuts; if private roads
are contracted normally, shortcuts span them and leak into through-routes. So we
keep a **two-tier** graph: the CH overlay covers the **public** sub-graph only
(shortcuts can't traverse restricted roads — leakage impossible by construction);
restricted edges live in the base graph and are used only for snapping and a
**bounded local connector** that bridges a gated endpoint to the public network.

### Key Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Restricted access set | `access ∈ {private, destination, customers}` OR `motor_vehicle ∈ {private, destination, customers}` | The gated estates are `access=private`; `access` governs even when `motor_vehicle=no` is also present |
| Hard-excluded | `access=no`, non-car highway, `area=yes`, `motor_vehicle=no` without a permissive `access` | Genuinely closed / non-drivable; matches "keep `access=no` off-limits" |
| Restricted usage | origin/destination connector only, never through-traffic | Matches Google + driver reality; prevents gated shortcuts |
| CH contraction | public sub-graph only | Structural no-leakage guarantee |
| Connector | bounded local Dijkstra over restricted edges → nearest public gateway node | Bridges endpoint to the routable overlay; bounded for safety |
| Binary format | v3 → v4 (add per-orig-edge access bit) | Server needs restricted edges + flags; lockstep via version |
| Both ends | origin AND destination connectors | Some origins are gated too (observed: a 352 m origin) |

## Component Changes (isolated, testable units)

### 1. `pkg/osm` — access classification
- Add `Access` to `RawEdge`: an enum `AccessPublic` / `AccessRestricted` (excluded
  ways are simply not emitted, as today).
- New `classifyAccess(tags) Access` (or fold into `isCarAccessible`): returns
  excluded / restricted / public per the table above. `access=no` → excluded;
  `access ∈ {private,destination,customers}` → restricted; else
  `motor_vehicle ∈ {private,destination,customers}` → restricted;
  `motor_vehicle=no` (no permissive access) → excluded; else public.
- `Parse` emits restricted edges too (flagged), instead of dropping them. Speed/
  weight computed as today (travel time); restricted estate roads get a low class
  speed naturally (residential/service), which is fine — they're connector-only.

### 2. `pkg/graph` — access bit + connectivity
- `Graph` gains `EdgeAccess []uint8` (len NumEdges, aligned with `Head`/`Weight`);
  `CHGraph` gains `OrigEdgeAccess []uint8` (aligned with `OrigHead`).
- `builder.go` carries the access flag through CSR construction.
- `component.go`: largest component computed over the **combined** graph (public +
  restricted) so estates connected via a private gate are retained. (A purely
  public component view would isolate them.)

### 3. `pkg/ch` — contract public sub-graph only
- `Contract` builds `outAdj`/`inAdj` from **public edges only** (skip restricted).
  Restricted edges are not contracted and produce no overlay edges. Estate-interior
  nodes (no public edges) get a rank but no overlay adjacency — present for
  snapping/geometry, unreachable via the overlay (only via the connector).
- `buildOverlay` unchanged in shape; it simply sees only public edges.
- The original-graph arrays carried into `CHGraph` (`OrigHead`/`OrigWeight` +
  the new `OrigEdgeAccess`) include **both** public and restricted edges.

### 4. `pkg/routing` — snapping + local connector (the core)
- The snapper is built from the full orig graph (already is), now including
  restricted edges, so gated endpoints snap to the actual estate road.
- Precompute `isPublic []bool` (node has ≥1 public incident orig edge) at engine
  init from `OrigEdgeAccess`.
- New `localConnect(origin/dest snap) []gateway{node, cost, geometry}`: a bounded
  Dijkstra over the orig graph that **expands restricted edges only** from the snap
  point, terminating at public gateway nodes (collecting node + accumulated time +
  the restricted path). Bound: cap settled nodes (e.g. 2048) / max cost; respects
  edge direction (forward for origin egress, reverse for destination ingress).
- `Route`:
  - For each start candidate: if the snapped edge is public → seed CH as today. If
    restricted → run `localConnect` (forward) and seed the CH forward search from
    each gateway node with `partial + connector cost + access penalty`; stash the
    restricted egress geometry. Mirror for end candidates (backward, ingress).
  - If a restricted endpoint yields no gateway within the bound → fall back to the
    nearest public snap (today's behavior) or `ErrNoRoute`.
  - Final geometry = `restricted egress` + `public CH path` + `restricted ingress`;
    distance summed over the full geometry (as today). `DurationSeconds` includes
    the connector time (internal only).

### 5. `pkg/graph/binary.go` — v3 → v4
- Serialize `OrigEdgeAccess` (1 byte/orig edge, or a packed bitset). Bump `version`
  to 4; `ReadBinary` reconstructs it. Overlay arrays unchanged.

### 6. `cmd/preprocess`, `refresh_graph.sh`
- No flag changes; the access classification is automatic. `refresh_graph.sh`
  unchanged except it now produces a v4 graph (document the bump).

## Data Flow

```
OSM PBF ─parse(classifyAccess)→ base graph (public + restricted edges, access bit)
              │                                   │
   largest combined component            CH contract (PUBLIC edges only)
              │                                   │
              └──────────────→ graph.bin (v4): public CH overlay + orig edges (+access)
                                                  │
 server: snap (public+restricted) ─ restricted endpoint? → localConnect (restricted-only,
   bounded) → public gateway seeds → CH search (public) → unpack → geometry =
   [restricted egress]+[public path]+[restricted ingress];  distance = Σ haversine
```

## Error Handling
- No gateway within the connector bound → fall back to nearest public snap; if still
  unreachable → `ErrNoRoute` (404). No new error codes.
- Connector hard-bounded (settled-node cap) so a pathological private maze can't hang
  a query; on hitting the bound with no gateway, fall back.
- `access=no` and non-car ways still excluded (unchanged).
- v4 load rejects a v3 graph (lockstep), as before.

## Testing Strategy
- `pkg/osm`: `classifyAccess` table tests (private/destination/customers → restricted;
  access=no → excluded; access=private+motor_vehicle=no → restricted; plain
  motor_vehicle=no → excluded; public unchanged).
- `pkg/graph`: v4 binary round-trip incl. `EdgeAccess`; v3 rejected. Component
  extraction keeps an estate connected via a single restricted gate.
- `pkg/ch`: overlay contains **no** restricted edges (assert every overlay edge maps
  to public orig edges); estate-interior nodes have no overlay adjacency.
- `pkg/routing`:
  - **into-estate:** synthetic graph (public ring + private cul-de-sac via one gate);
    a destination in the cul-de-sac routes INTO it via the gate; geometry includes
    the restricted segment.
  - **no-leakage:** two public points where the private road is a geometrically
    shorter shortcut; assert the route does NOT use it.
  - connector bound + fallback (private maze with no public gateway → fallback).
  - one-way restricted road respected.
- End-to-end: `compare_google.py` on 137 — gated endpoints improve, `a29476` < 1.5×.
- **Non-regression diff (hard gate):** route the 99 non-gated Google + 262 custom
  pairs on the Phase-1 graph vs the v4 graph; assert geometry + distance unchanged.

## Risks & Mitigations
- **Through-leakage** (the critical risk): structurally impossible — restricted edges
  are absent from the CH overlay, and the connector expands restricted edges only and
  stops at the first public gateway. Proven by the no-leakage unit test + the
  non-regression diff.
- **Runaway connector:** hard settled-node cap + fallback.
- **Snapping now prefers the restricted estate road** (nearest) over public — correct
  (matches Google); access penalty still applies.
- **Estate with no public gateway in OSM:** connector finds none → fallback. (Rare;
  surfaces as today's behavior, not a regression.)
- **Binary size:** +1 byte/orig edge (~2.4 M edges ≈ 2.4 MB) — negligible.

## Out of Scope (later Phase 2 threads)
- Inferred traffic / congestion (needs a data pull preserving Google `duration` +
  timestamps).
- Turn / junction / U-turn penalties.
- Distinguishing gate-specific entry points (we route to whichever gate the restricted
  network connects through; Google may pick a specific gate — accept minor differences).
- Intra-estate routing: if origin AND destination are inside the *same* private network,
  the route goes out to the public gateway and back rather than staying internal.
  Accepted — rare for depot→customer deliveries; revisit only if it shows up.
