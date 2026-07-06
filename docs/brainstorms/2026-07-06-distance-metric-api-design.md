# Shortest-Distance Routing via a `metric` Request Field

**Date:** 2026-07-06
**Status:** Design (approved for planning)
**Author:** brainstorming session

## Problem

The deployed API (`run_server.sh` → `graph.time.bin`, KL/Selangor, port 8086)
routes on **estimated travel time** — the metric introduced on 2026-06-01 to
match Google's highway-favoring routes
(`docs/brainstorms/2026-06-01-time-based-routing-metric-design.md`). That was the
right default for Google-parity, but it removed the ability to ask for a **true
shortest-distance** route (minimum physical road length), which some callers
want back as an explicit option (e.g. distance-paid delivery, or "give me the
literally shortest way, not the fastest").

We want to expose shortest-distance routing **alongside** the existing time-based
routing, without changing the behavior of any current client.

## Key insight: the engine is already metric-agnostic

Nothing about shortest-distance routing needs new routing math. The metric is
entirely a property of the loaded graph's edge weights:

- `pkg/osm/parser.go` already computes either travel time (`computeWeightMs`, ms)
  or physical road length (`computeWeightDistanceCm`, cm) depending on
  `ParseOptions.Distance`.
- `cmd/preprocess/main.go` already exposes `--distance` to build a
  distance-weighted CH graph. This is exactly how `graph.au.bin` (all-of-Australia
  shortest-distance) is built and served today via `run_server_au.sh`.
- CH contraction (`pkg/ch/contractor.go`) treats `weight` as an opaque `uint32`,
  so a distance graph needs no algorithm change.
- The HTTP response is already metric-agnostic: `pkg/routing/engine.go` measures
  `total_distance_meters` from the path **geometry** (haversine of the polyline),
  not from the routing metric `mu`. Internal `DurationSeconds` (`mu/1000`) is
  **not** exposed — good, because for a distance graph `mu` is centimeters and
  would be meaningless as a duration.

So a `routing.Engine` wrapping a time graph produces time-optimal paths, and a
second `Engine` wrapping a distance graph produces distance-optimal paths — from
the **same** engine code. This feature is therefore almost entirely **API
plumbing**: load a second graph and dispatch each request to the right engine.

## Requirements

1. `POST /api/v1/route` gains an optional `metric` field: `"time"` (default) or
   `"distance"`. Omitting it preserves today's behavior byte-for-byte.
2. One server process serves both metrics (chosen by the user over a separate
   second process — OSRM/Valhalla-style profiles on one endpoint).
3. The distance graph is **optional** at startup. Starting the server exactly as
   today (only `--graph`) must behave exactly as today; a `metric:"distance"`
   request then returns a clear error rather than silently falling back to time.
4. The response body is **unchanged** (`total_distance_meters` + geometry).
5. Same KL/Selangor region for both graphs.

Out of scope (YAGNI): exposing duration in the response; any third metric;
sharing snapping/geometry structures between the two engines (see Future work).

## Architecture

One server process holds **two independent `routing.Engine`s**, one per metric,
behind a metric→router map in `Handlers`.

```
                         ┌─────────────────────────────┐
  POST /api/v1/route  →  │ HandleRoute                 │
  { start, end,          │  resolve metric ("time"|    │
    metric? }            │   "distance"; default time) │
                         │  routers[metric] ──┐        │
                         └────────────────────┼────────┘
                                              │
                     ┌────────────────────────┴───────────────────────┐
                     ▼                                                 ▼
        routers["time"]  = Engine(graph.time.bin)      routers["distance"] = Engine(graph.kl.dist.bin)
        (required, from --graph)                       (optional, from --graph-distance)
```

Two engines means roughly **2× resident memory** (each builds its own R-tree /
snapper). This is the accepted cost of the unified-server choice.

## Components / changes

1. **`pkg/api/models.go`**
   - `RouteRequest`: add `Metric string \`json:"metric,omitempty"\``.
   - `StatsResponse`: add `AvailableMetrics []string \`json:"available_metrics"\``
     so a client can discover what a given deployment offers.

2. **`pkg/api/handlers.go`**
   - `Handlers` holds `routers map[string]routing.Router` instead of a single
     `router`. Metric-name constants: `metricTime = "time"`,
     `metricDistance = "distance"`.
   - `HandleRoute`: after coord validation, resolve the metric (empty →
     `"time"`), reject unknown names, look up `routers[metric]`, and dispatch.
     The response-building block is unchanged.
   - `NewHandlers(routers map[string]routing.Router, stats StatsResponse)`.

3. **`cmd/server/main.go`**
   - Add `--graph-distance <path>` flag (`--graph` stays the required time graph).
   - Load the time graph → `routers["time"]`. If `--graph-distance` is non-empty,
     load it into a second engine → `routers["distance"]`.
   - Populate `StatsResponse.AvailableMetrics` from the map keys.
   - `runtime.GC()` / `debug.FreeOSMemory()` once after both engines are built.

4. **`run_server.sh`**
   - Auto-build `graph.kl.dist.bin` if missing (mirroring `run_server_au.sh`'s
     build-on-first-run), then run with both `--graph graph.time.bin` and
     `--graph-distance graph.kl.dist.bin` on port 8086.

5. **`graph.kl.dist.bin`** (new artifact)
   - Built via `preprocess --input malaysia-singapore-brunei-latest.osm.pbf --kl
     --distance --output graph.kl.dist.bin` — same KL/Selangor bbox as the time
     graph, distance metric. Gitignored like the other side graphs
     (`graph.*.bin` is already in `.gitignore`).

## Data flow

`POST /api/v1/route` → enforce JSON content-type → decode → validate `start` /
`end` coords → resolve+validate `metric` (default `"time"`) → `routers[metric]`
(→ error if absent) → `router.Route(ctx, start, end)` → same response builder as
today.

## Error handling

| Condition | HTTP | Body |
|---|---|---|
| `metric` not in {`""`,`"time"`,`"distance"`} | 400 | `{"error":"invalid_request","field":"metric"}` |
| `metric:"distance"` but no distance graph loaded | 400 | `{"error":"metric_unavailable","field":"metric"}` |
| existing (point_too_far, no_route, timeout, bad coords) | unchanged | unchanged |

`metric_unavailable` is a distinct code (not a silent time fallback) so a client
can tell "this deployment doesn't offer distance" apart from a malformed request.

## Testing

**Unit (`pkg/api/handlers_test.go`)** — the new logic is dispatch, so test it with
two stub `routing.Router`s returning distinguishable `total_distance_meters`:

- `metric` omitted → time router invoked.
- `metric:"time"` explicit → time router invoked.
- `metric:"distance"` with a distance router present → distance router invoked.
- `metric:"distance"` with **no** distance router → 400 `metric_unavailable`.
- `metric:"walking"` → 400 `invalid_request` (field `metric`).

No engine/CH/snap tests change — those layers are untouched.

**Live verification** — build `graph.kl.dist.bin`, start the server with both
graphs, and send the **same** start/end pair (picked from
`datasets/elevete_route_cache/routes.jsonl`, chosen where a highway detour makes
the metrics diverge) with `metric:"time"` vs `metric:"distance"`. Confirm the
distance route reports a **smaller `total_distance_meters`**. Also confirm: a
default (no-metric) request is unchanged, and — with the distance graph omitted —
`metric:"distance"` returns `metric_unavailable`.

## Future work (explicitly not now)

- **Shared topology between engines.** The time and distance builds share
  identical node numbering and geometry (same OSM, same `--kl` filters; only edge
  weights differ), so the snapper/geometry could be built once and shared,
  loading only the second CH overlay. Deferred: it fragilely couples two build
  artifacts (any numbering drift silently corrupts geometry) for a memory win we
  don't yet need.
- Exposing an estimated duration alongside distance in the response.
