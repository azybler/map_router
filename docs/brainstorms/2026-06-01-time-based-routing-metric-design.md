# Time-Based Routing Metric + Robust Snapping

**Date:** 2026-06-01
**Status:** Design (approved for planning; revised after 5-lens adversarial review)
**Author:** brainstorming session, grounded in the Elevete route-cache benchmark

## Problem

map_router is in production (vendored into Elevete ERP). The recurring complaint
from delivery drivers is that **routes are "too short."** Drivers are paid by
distance, so the complaint is self-interested — but the benchmark below shows it
also reflects a *real* deficiency: the routes are genuinely shorter-but-slower
than what Google produces.

### Benchmark (44 genuine Google routes, the ground-truth set)

Ran map_router (live `graph.bin`, via the HTTP API) on the 44 Google reference
O/D pairs using the road-snapped endpoints as-is. Harness:
`datasets/elevete_route_cache/compare_google.py`.

**Priority 1 — Validity: 44/44 routed OK, 0 failures.** map_router is not broken.
(All 44 fall inside the deployed KL/Selangor graph bbox; long-haul Johor/Singapore
pairs live in the 262-row `custom` set and are out of graph scope — a separate
coverage concern, not part of this work.)

**Priority 2 — Similarity:**

| metric | median | mean | p90 | worst |
|---|---|---|---|---|
| distance ratio (mr / google) | 0.87 | 0.92 | 1.01 | 0.64 … 2.09 |
| distance error % | −12.9% | −8.1% | +1.4% | −36% |
| Hausdorff (max geom divergence) | 1.9 km | 2.3 km | 5.1 km | 6.9 km |
| overlap (mr within 25 m of Google) | 49% | 46% | 94% | 0% |

- 32 of 44 routes meaningfully shorter than Google; 3 longer; 9 about equal.
- Divergence **grows with trip length** (short trips agree almost perfectly):

| trip length | n | median dist err | median overlap | median Hausdorff |
|---|---|---|---|---|
| 0–3 km | 4 | +1.1% | 1.00 | 15 m |
| 3–8 km | 5 | −0.6% | 0.49 | 672 m |
| 8–15 km | 15 | −17.1% | 0.35 | 1.9 km |
| 15 km+ | 20 | −13.2% | 0.48 | 3.4 km |

> **Dataset sampling caveat (important for calibration):** the 44 routes are not
> 44 independent samples. There are only **19 distinct origins / 35 distinct
> destinations**, and a single depot origin `(101.603148, 3.115883)` starts **21
> of 44** routes. Effective sample size is ~19, dominated by one depot's road
> preferences. Treat any speed tuning as "nudging priors," not "fitting," and
> split held-out folds *by origin group* so a depot never appears in both
> train and test.

### Root cause #1 — distance vs. time

map_router's edge weight is **pure geometric distance**: `pkg/osm/parser.go`
computes `weightMM = round(haversine_m × 1000)`. There is no speed or travel-time
model anywhere; the parser even discards the `highway` class and `maxspeed` tag
after the access check. Google minimizes **time**, which favors highways/trunk
roads that are longer in distance but faster. On short trips shortest-distance ≈
shortest-time (they agree); on longer trips map_router cuts through shorter,
slower surface streets. This is the systematic shortness.

### Root cause #2 — single-nearest-edge snapping (the anomalies)

**One** route exceeds 1.5× Google: `02c22eea` at **2.09×** (11.0 km vs 5.3 km for
endpoints 1.5 km apart straight-line). Two further routes are mildly longer —
`a75c5265` (1.20×) and `a08d8f2a` (1.13×, but **0% overlap** with Google).
`02c22eea` and `a08d8f2a` **share the node `[101.599, 3.155]`** and are the focus
here. Investigation of that node:

- A 250 m hop from it in **every** compass direction costs 7.5–9.3 km.
- Two probe points 250 m either side of it route to each other in 749 m — the
  surrounding network is healthy.
- The point snaps **128 m** away onto an edge connected to the main network only
  via a ~4 km umbilical (an isolated stub / wrong-carriageway segment).

Two compounding causes in the code:

1. `Snapper.Snap` (`pkg/routing/snap.go`) returns the single geometrically-nearest
   edge with no fallback; when that edge is an isolated stub it forces a detour.
2. `seedForward`/`seedBackward` (`pkg/routing/engine.go:185-224`) seed **both**
   endpoints of the snapped edge using `origGraph.Weight[snap.EdgeIdx]` **without
   checking edge direction** — so on a one-way road the search may depart the snap
   point *backward against* the legal direction. This is itself a latent bug and
   exactly the wrong-carriageway failure mode.

## Goal & Measurable Success Criteria

Route on **estimated travel time** instead of distance, and make snapping robust.
Measured by `compare_google.py` on the 44 Google routes.

> **Scope of validation (be honest):** the dataset has **no travel time** (Google's
> `duration` was trimmed before caching). This benchmark therefore validates route
> **shape and length parity** with Google — *not* travel-time accuracy. We cannot
> confirm the speed model is physically correct, only that it reproduces Google's
> road choices. Absolute speeds are unpinned (route choice depends only on
> *relative* class speeds). See "Calibration" for how we contain this.

**Hard gates (must pass to swap):**

| metric | today | hard gate |
|---|---|---|
| validity | 44/44 | 44/44 (no regression) |
| `02c22eea` detour ratio | 2.09× | < 1.3× |
| any route > 1.5× Google | 1 | 0 |
| median \|distance error\| | 12.9% | ≤ 6% |
| median Hausdorff | 1.9 km | ≤ ~1.0 km (≈ halved) |
| query p50/p99 latency | baseline | no regression |
| graph build time | baseline | within ~2× |
| **driver non-regression** (262 real pairs) | — | **no route becomes materially shorter (< −2%) than today's production graph; report total distance delta** |

**Soft targets (report, don't block):**

| metric | today | soft target |
|---|---|---|
| symmetric overlap@25 m (mean of both directions) | 0.48 | ≥ 0.60 (stretch 0.70) |
| median signed distance error | −12.9% | → ≈ 0 (routes become time-optimal, hence appropriately longer) |

Rationale for the hard/soft split: anomaly-elimination and Hausdorff-halving are
**directly attributable** to these changes; overlap@0.70 may be unreachable with
free-flow time alone because Google's remaining divergence is driven by traffic /
turn costs that are explicitly **out of scope** (Phase 2). Length parity does not
imply geometric agreement (verified: routes at ratio ≈1.0 still show ~800 m
Hausdorff), so overlap is reported, not gated.

**Business-vs-engineering decoupling:** "match Google" (engineering) is not the
same as "drivers paid more" (business). 7 of 44 routes are already ≥ Google; a
time metric could *shorten* some of those toward a faster highway path. The driver
non-regression check above guards the actual business goal independently.

## Approach — two sequenced, independently-gated changes

The two root causes touch disjoint code and have disjoint metrics. Ship them
separately so a regression in one is isolated and independently reversible.

### Change A — Robust snapping (ship first; query-time only, NO graph rebuild)

A server-only change: no re-preprocess, no binary version bump (snapping reads
`OrigHead`/`OrigWeight` at query time). Expected to drop `02c22eea` below 1.3×
and bring any-route-over-1.5× to 0, with everything else unchanged.

**A1. Direction-aware seeding (correctness fix).** In `seedForward`/`seedBackward`,
only seed an endpoint reachable by *legal* travel from the snap point: seed `v`
forward only if edge `u→v` exists; seed `u` only if the reverse edge `v→u` exists
(check via `findEdge` on `origGraph.FirstOut/Head`). Mirror for the backward
search. This removes the illegal reverse-travel escape that causes wrong-carriageway
routes — independent of, and a prerequisite for, multi-candidate snapping.

**A2. Multi-candidate snapping.** Add
`SnapCandidates(lat, lng float64, k int, radiusM float64) []SnapResult` returning
up to `k` nearest **distinct** edges (deduped by `(min(u,v), max(u,v))` so they are
different roads, not segments of the same way). The engine seeds all candidates
(direction-aware per A1) in both searches, each with an **access penalty** added to
its seed cost so the bidirectional search picks the lowest-total-cost start. The
nearest snap stays preferred unless a farther candidate yields a materially better
route — which is exactly what rescues the isolated-stub point.

- Start params: `k = 4`, `radiusM = maxSnapDistMeters` (500 m, **not** 300 m, so
  multi-candidate never rejects a point single-nearest would have accepted —
  preserves the 44/44 validity guarantee).
- **Access penalty is tracked separately from in-graph cost** (see Change B) so it
  never pollutes any reported metric, and is used for candidate *selection* only.
- Failure semantics unchanged: no candidate within `maxSnapDistMeters` →
  `ErrPointTooFar` (422); candidates exist but none reachable → `ErrNoRoute` (404).

> **Lighter-fix check before building A2:** first re-run the 2 anomaly pairs with
> just A1 plus a small off-road-distance tie-break penalty inside the existing
> `Snap` loop. If that alone drops `02c22eea` < 1.3×, prefer it (no new API, no
> engine seeding change). Build full multi-candidate seeding only if the cheap fix
> is insufficient.

### Change B — Time metric (ship second; graph rebuild + version bump + swap)

**B1. Speed model in `pkg/osm`.** New `speedKmh(tags) float64`:
- Parse `maxspeed`: numeric km/h (`"60"`); `"<n> mph"` → ×1.609; zone codes
  `MY:urban`/`MY:rural`/`RM:*` → mapped defaults; `maxspeed:forward/backward`,
  `:conditional`, `none`, `walk`, non-numeric → **silent fallback** to class
  default (logged at debug). (Note: real Malaysian OSM uses `MY:*`, not only
  `RM:*`.)
- Else per-`highway`-class default (Malaysian urban free-flow **priors**, km/h):
  motorway 90, trunk 70, primary 55, secondary 45, tertiary 38, unclassified 35,
  residential 25, living_street 12, service 15. `*_link` = **0.7× its tuned
  parent** (derived, not an independent tuning parameter — keeps the vector small).
- Edge weight = `round(dist_m / (kmh / 3.6) × 1000)` ms, min 1. Used immediately at
  parse time; the `highway` class does **not** need to propagate through
  `graph.Build`/`FilterToComponent`/the binary (tuning re-runs preprocess, below).
- The speed table is a **versioned input file** read by `cmd/preprocess` via a flag
  (`--speeds path`); its identity/hash is logged and recorded so a given `graph.bin`
  is traceable to the speeds that built it. Single source of truth.

**B2. Binary `version` 2 → 3** (`pkg/graph/binary.go`). Identical layout (weights
stay `uint32`, now interpreted as ms). The bump makes an old server refuse a
time-graph (no silent "seconds reported as meters"). New server + new graph deploy
together.

**B3. Distance reporting + duration (`pkg/routing/engine.go`).**
- `mu` is now total travel time (ms). Compute an internal
  `RouteResult.DurationSeconds = (mu − totalAccessPenalty) / 1000`, subtracting the
  access-penalty seed cost of the chosen meet endpoints so it reflects in-graph
  time only. **Do NOT expose it over the API in Phase 1** — there is no time
  ground truth to validate it; exposing an uncalibrated ETA invites callers to
  treat noise as minutes. Keep it internal/log-only.
- Recompute `TotalDistanceMeters` and per-`Segment.DistanceMeters` from the route
  geometry. **Correctness fix:** today's `mu/1000` distance *includes* the partial
  first/last edge (the `du`/`dv` seed costs), but `buildGeometry` starts at the
  first graph **node**, not the snap point, and the "intermediate shape points"
  loop is **dead code** (the parser never populates `ShapeLats/ShapeLons`; ways are
  split per node-pair, so geometry == the node polyline). So summing haversine over
  the node polyline alone would silently **drop** the partial end segments — a new
  shortfall on exactly the short trips that currently match Google. Fix: prepend the
  start snap point and append the end snap point to the returned geometry, and sum
  haversine over that, so the reported distance and the drawn polyline both include
  the partial edges and begin/end at the requested (snapped) points.

**B4. Speed tuning (lightweight — NO `cmd/calibrate` tool).** The success gates are
geometric, and route choice depends on relative class *ratios* that are already
roughly known for Malaysian roads. So:
- Bake the prior speed table, run `cmd/preprocess` → start server → run
  `compare_google.py` (this scores against the **real CH query path**, avoiding any
  "Dijkstra-shape ≠ CH-shape" tie-breaking mismatch and reusing verified scoring).
- If a hard gate is missed, hand-adjust 1–2 class ratios and repeat (each iteration
  is a few minutes of preprocess + benchmark; only a handful expected).
- Guards against over-fitting ~19 effective samples: keep speeds within real-world
  bounds; preserve class-speed *spread* (don't let ratios collapse toward equality,
  which degenerates back to the distance metric); exclude the 4 short trips (no
  speed signal) and a ~200 m endpoint buffer (snap-displacement confound up to
  211 m) from the tuning judgement; report signed (not just absolute) per-route
  error and per-origin-group held-out scores.
- Only build a small automated grid-sweep-over-ratios (shelling out to the real
  server + `compare_google.py`, *not* a reimplemented Dijkstra) if hand-tuning
  demonstrably cannot hit the gates.

**B5. `refresh_graph.sh` + `cmd/preprocess`.** Add the `--speeds` flag to
preprocess; update `refresh_graph.sh` to pass the versioned table and record its
hash alongside `graph.bin`. Without this, a routine refresh silently reverts to
prior/stale speeds.

**B6. Rollout.** Build to a side file `graph.time.bin`, run the full benchmark +
262-route non-regression report, and only then atomically swap into `graph.bin`
(keeping `graph.bin.bak`). The version bump enforces server/graph lockstep.

## Data Flow

```
                          versioned speeds.json ──┐ (--speeds)
OSM PBF ──parse(speedKmh)──▶ base graph (weight=time ms) ──▶ cmd/preprocess (CH on
                                                              time metric) ─▶ graph.time.bin (v3)
                                                                    │
                                          start server + compare_google.py (44 Google
                                          + 262 non-regression) — hand-tune speeds,
                                          repeat until hard gates pass
                                                                    │
                                                       atomic swap ─▶ graph.bin (+ .bak)
                                                                    │
   server (load v3) ──▶ direction-aware multi-candidate snap ──▶ CH Dijkstra (time)
        ──▶ unpack ──▶ geometry (snap pt … nodes … snap pt)
                          │
        distance = Σ haversine(geometry incl. partials);  duration (internal) =
        (mu − accessPenalty)/1000
```

## Error Handling
- `maxspeed` parse failures / zone codes / conditional / per-direction → silent
  fallback to class default (debug log). "Uses real data when present" is therefore
  partial coverage, not universal.
- Snapping: no candidate in radius → `ErrPointTooFar` (422); candidates exist but
  none reachable → `ErrNoRoute` (404). No new error codes. Candidate radius =
  `maxSnapDistMeters` so validity cannot regress.
- uint32 time range: a single OSM segment is short; worst-case full-graph route time
  ≪ uint32 max (~49 days in ms). Assert during build as a guard.
- Version mismatch on load is already a hard error (prevents bad swaps).

## Testing Strategy
- `pkg/osm`: table tests for `speedKmh` — each class, numeric/mph/`MY:*`/`RM:*`/
  conditional/forward-backward/`none`/garbage `maxspeed`, and `*_link` = 0.7× parent.
- `pkg/graph`: round-trip binary test at v3; a v2 file is rejected.
- `pkg/routing` (the critical additions — the existing 6-node single-seed test does
  **not** cover the new paths):
  - **Multi-seed exactness:** seed multiple forward *and* backward nodes at nonzero,
    unequal distances on a graph large enough to form shortcuts; assert `mu` equals a
    brute-force multi-source/multi-target plain Dijkstra, and that the reconstructed +
    unpacked path weight equals `mu`.
  - **One-way seeding:** synthetic graph with a one-way edge; assert direction-aware
    seeding does **not** permit reverse departure.
  - **Time-metric range:** weights spanning a wide dynamic range.
  - **Distance-from-geometry:** a route whose endpoints snap mid-edge (ratio ≠ 0/1);
    assert Σ-haversine(geometry incl. snap points) equals the expected traveled
    distance (this is what the B3 fix protects).
  - **Isolated-stub snapping:** synthetic stub + main road; assert multi-candidate
    picks the connected road.
- End-to-end: `compare_google.py` gate asserting the **hard** criteria, with the
  anomaly assertion keyed to the specific route ID `02c22eea` and its measured ratio
  (< 1.3×), not a vague count.
- Regression: 262-route report — driver non-regression (no route < −2% vs today;
  total distance delta) is a **hard** gate; shape "drift" is informational only
  (those rows are map_router's own current output, so they have no independent
  ground truth).

## Harness corrections (`compare_google.py`)
- **Drop discrete Fréchet** from reported metrics (density-biased: map_router emits
  ~1.9× more vertices than Google, inflating Fréchet 6–9× on near-identical paths).
  No gate uses it.
- Report **symmetric overlap** (mean of both directions) since the gate uses it;
  `mr-in-google` alone rewards mr being a subset.
- Annotate the equirectangular projection as valid only for sub-50 km KL-scale
  routes (degrades on the long-haul Johor/Singapore pairs).
- Minor: drop the duplicated final endpoint from the resample set. (The "corner
  bias" flagged in review is mostly expected — Euclidean gaps near a kink are
  naturally < step under correct arc-length sampling — so no overhaul.)

## Risks & Mitigations
- **CH correctness on time metric** — contractor/witness are metric-agnostic
  (verified); covered by the new multi-seed/time-range exactness test.
- **Distance vs exact snap point** — fixed in B3 (geometry now begins/ends at the
  snap points and includes partial edges); re-baseline the 44-route distance numbers
  through the corrected pipeline **before** judging the ≤6% gate.
- **Overfitting speeds to ~19 effective samples** — priors + bounds + spread guard +
  group-held-out reporting + hand-tuning ("nudge, don't fit").
- **No time ground truth** — Phase 1 validates shape/length only; do **not** expose
  `duration_seconds`. Recommend pulling a fresh Google set that retains `duration`
  (re-run `build_dataset.sh` after the June-1 quota reset) as a sanity check and for
  Phase 2.
- **Production rollout** — two independent swaps; side file + benchmark gate + atomic
  swap + retained `.bak`; version bump enforces lockstep. Confirm the vendored
  Elevete client does not use a strict (`DisallowUnknownFields`) decoder before any
  future response-field addition (moot for Phase 1 since no field is added).

## Out of Scope (Phase 2+)
- Exposing a calibrated `duration_seconds` ETA (needs a data pull preserving Google
  `duration` + timestamps to fit absolute speeds).
- Turn/junction/U-turn penalties (needs edge-based graph or turn tables).
- Inferred traffic/congestion (same data-pull dependency).
- Expanding graph coverage to Johor/Singapore long-haul pairs.
- Connectivity *healing* of weakly-connected pockets in preprocessing (multi-candidate
  snapping addresses the symptom; pocket detection is larger and riskier).

## Appendix — review findings folded in
Revised after a 5-lens adversarial review. Key changes vs the first draft:
split into two sequenced changes; **dropped the `cmd/calibrate` tool** in favor of
hand-tuning via the real pipeline; added **direction-aware seeding** as a correctness
prerequisite; **fixed distance reporting** to include partial edges/snap points and
corrected the dead "shape points" claim; **deferred `duration_seconds`** and stated
the no-time-ground-truth limitation; corrected the anomaly baseline (1 route > 1.5×,
not 2); reworked success criteria into hard gates + soft targets with a **driver
non-regression** check; specified `maxspeed` `MY:*`/conditional handling and
links-derived speeds; made the speed table a versioned `--speeds` input and added
`refresh_graph.sh`; set snap radius = 500 m to protect validity; dropped Fréchet and
switched to symmetric overlap in the harness.
