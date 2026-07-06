---
title: "feat: shortest-distance routing via metric field"
type: feat
status: planned
date: 2026-07-06
brainstorm: docs/brainstorms/2026-07-06-distance-metric-api-design.md
---

# Shortest-Distance Routing via `metric` Field — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional `metric` field (`"time"` default | `"distance"`) to `POST /api/v1/route` so one server can answer both lowest-time and true shortest-distance queries, without changing behavior for existing clients.

**Architecture:** The routing engine is already metric-agnostic — the metric is a property of the loaded CH graph's edge weights (travel-time ms vs road-length cm). So this is API plumbing: the server loads a required time graph and an **optional** distance graph into two independent `routing.Engine`s held in a metric→router map, and `HandleRoute` dispatches per request. No changes to the engine, snapper, CH, unpacking, parser, or binary format.

**Tech Stack:** Go 1.26, `net/http` (stdlib mux with method patterns), Contraction Hierarchies + bidirectional Dijkstra, custom binary graph format. Verification via `curl` + a small Python script against the running server.

## Global Constraints

- **Go 1.26+** (existing floor; no new dependencies).
- **Response schema unchanged** — `{total_distance_meters, segments[]}`; `total_distance_meters` is geometry-derived and already correct for either metric. Duration is NOT exposed.
- **`metric` defaults to `"time"`** — omitting the field reproduces today's behavior byte-for-byte.
- **Only `"time"` and `"distance"` are valid** metric names.
- **Distance graph is optional at startup.** Starting with only `--graph` must behave exactly as today.
- **Error codes:** `metric` unknown name → HTTP 400 `invalid_request` (field `metric`); known metric but graph not loaded → HTTP 400 `metric_unavailable` (field `metric`). No silent fallback.
- **Same KL/Selangor bbox** for both graphs (`--kl`: lat [2.75, 3.50], lng [101.20, 102.00]).
- **Metric name constants are the single source of truth** — exported `api.MetricTime` / `api.MetricDistance`, used by both the handler and `cmd/server`.
- Out of scope: `cmd/visualize`, exposing duration, sharing topology between the two engines.

## File Structure

- **`pkg/api/models.go`** (modify) — add `Metric` to `RouteRequest`; add `AvailableMetrics` to `StatsResponse`.
- **`pkg/api/handlers.go`** (modify) — exported metric constants; `Handlers.routers` map; `NewHandlersMulti` constructor + backward-compatible `NewHandlers` wrapper; metric resolution + dispatch in `HandleRoute`.
- **`pkg/api/handlers_test.go`** (modify) — new dispatch/error tests; existing tests unchanged (they use the `NewHandlers` wrapper).
- **`cmd/server/main.go`** (modify) — `--graph-distance` flag; `loadEngine` helper; build the routers map + `AvailableMetrics`.
- **`run_server.sh`** (rewrite) — auto-build `graph.kl.dist.bin` on first run; serve with both graphs.
- **`graph.kl.dist.bin`** (new build artifact; gitignored by `graph.*.bin`).
- **`README.md`** (modify) — document the `metric` field, the two new error rows, the `--graph-distance` flag, and `available_metrics`.

---

## Task 1: `metric` field + dispatch in the API layer

**Files:**
- Modify: `pkg/api/models.go`
- Modify: `pkg/api/handlers.go`
- Modify: `pkg/api/README` docs — `README.md:81-139`
- Test: `pkg/api/handlers_test.go`

**Interfaces:**
- Consumes: `routing.Router` interface (`Route(ctx, start, end LatLng) (*RouteResult, error)`), existing `writeError(w, status, code, field)`.
- Produces:
  - `const MetricTime = "time"`, `const MetricDistance = "distance"` (exported).
  - `func NewHandlersMulti(routers map[string]routing.Router, stats StatsResponse) *Handlers`
  - `func NewHandlers(router routing.Router, stats StatsResponse) *Handlers` (unchanged signature; now a wrapper that registers `router` under `MetricTime`).
  - `RouteRequest.Metric string` (json `metric,omitempty`).
  - `StatsResponse.AvailableMetrics []string` (json `available_metrics`).

- [ ] **Step 1: Write the failing tests**

Append to `pkg/api/handlers_test.go`:

```go
// routeResult builds a RouteResult whose distance identifies which router ran.
func routeResult(dist float64) *routing.RouteResult {
	return &routing.RouteResult{
		TotalDistanceMeters: dist,
		Segments: []routing.Segment{
			{DistanceMeters: dist, Geometry: []routing.LatLng{{Lat: 1, Lng: 2}}},
		},
	}
}

func postRoute(t *testing.T, h *Handlers, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/route", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleRoute(w, req)
	return w
}

func TestHandleRoute_MetricDefaultsToTime(t *testing.T) {
	h := NewHandlersMulti(map[string]routing.Router{
		MetricTime:     &mockRouter{result: routeResult(111)},
		MetricDistance: &mockRouter{result: routeResult(222)},
	}, StatsResponse{})

	w := postRoute(t, h, `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", w.Code, w.Body.String())
	}
	var resp RouteResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.TotalDistanceMeters != 111 {
		t.Errorf("default metric used wrong router: got %v, want time (111)", resp.TotalDistanceMeters)
	}
}

func TestHandleRoute_MetricTimeExplicit(t *testing.T) {
	h := NewHandlersMulti(map[string]routing.Router{
		MetricTime:     &mockRouter{result: routeResult(111)},
		MetricDistance: &mockRouter{result: routeResult(222)},
	}, StatsResponse{})

	w := postRoute(t, h, `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85},"metric":"time"}`)
	var resp RouteResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.TotalDistanceMeters != 111 {
		t.Errorf("metric=time used wrong router: got %v, want 111", resp.TotalDistanceMeters)
	}
}

func TestHandleRoute_MetricDistanceUsesDistanceRouter(t *testing.T) {
	h := NewHandlersMulti(map[string]routing.Router{
		MetricTime:     &mockRouter{result: routeResult(111)},
		MetricDistance: &mockRouter{result: routeResult(222)},
	}, StatsResponse{})

	w := postRoute(t, h, `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85},"metric":"distance"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", w.Code, w.Body.String())
	}
	var resp RouteResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.TotalDistanceMeters != 222 {
		t.Errorf("metric=distance used wrong router: got %v, want distance (222)", resp.TotalDistanceMeters)
	}
}

func TestHandleRoute_MetricUnavailable(t *testing.T) {
	// Time-only handler (as when started without --graph-distance).
	h := NewHandlers(&mockRouter{result: routeResult(111)}, StatsResponse{})

	w := postRoute(t, h, `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85},"metric":"distance"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var e ErrorResponse
	json.Unmarshal(w.Body.Bytes(), &e)
	if e.Error != "metric_unavailable" || e.Field != "metric" {
		t.Errorf("error = %q field = %q, want metric_unavailable/metric", e.Error, e.Field)
	}
}

func TestHandleRoute_MetricInvalid(t *testing.T) {
	h := NewHandlers(&mockRouter{result: routeResult(111)}, StatsResponse{})

	w := postRoute(t, h, `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85},"metric":"walking"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var e ErrorResponse
	json.Unmarshal(w.Body.Bytes(), &e)
	if e.Error != "invalid_request" || e.Field != "metric" {
		t.Errorf("error = %q field = %q, want invalid_request/metric", e.Error, e.Field)
	}
}

func TestHandleStats_AvailableMetrics(t *testing.T) {
	h := NewHandlers(&mockRouter{}, StatsResponse{AvailableMetrics: []string{"time", "distance"}})

	req := httptest.NewRequest("GET", "/api/v1/stats", nil)
	w := httptest.NewRecorder()
	h.HandleStats(w, req)

	var resp StatsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.AvailableMetrics) != 2 || resp.AvailableMetrics[0] != "time" || resp.AvailableMetrics[1] != "distance" {
		t.Errorf("available_metrics = %v, want [time distance]", resp.AvailableMetrics)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail (compile error)**

Run: `go test ./pkg/api/ -run 'Metric|AvailableMetrics' -v`
Expected: FAIL — build error, `NewHandlersMulti` undefined and `MetricTime`/`MetricDistance`/`RouteRequest.Metric`/`StatsResponse.AvailableMetrics` undefined.

- [ ] **Step 3: Add the `Metric` and `AvailableMetrics` fields**

In `pkg/api/models.go`, replace the `RouteRequest` struct with:

```go
// RouteRequest is the JSON body for POST /api/v1/route.
type RouteRequest struct {
	Start  LatLngJSON `json:"start"`
	End    LatLngJSON `json:"end"`
	Metric string     `json:"metric,omitempty"` // "time" (default) or "distance"
}
```

In `pkg/api/models.go`, replace the `StatsResponse` struct with:

```go
// StatsResponse is the JSON response for GET /api/v1/stats.
type StatsResponse struct {
	NumNodes         uint32   `json:"num_nodes"`
	NumFwdEdges      int      `json:"num_fwd_edges"`
	NumBwdEdges      int      `json:"num_bwd_edges"`
	AvailableMetrics []string `json:"available_metrics"`
}
```

- [ ] **Step 4: Add metric constants, the routers map, and dispatch**

In `pkg/api/handlers.go`, replace the `Handlers` type and `NewHandlers` (lines ~14-26) with:

```go
// Routing metrics selectable via RouteRequest.Metric.
const (
	MetricTime     = "time"     // lowest travel time (default)
	MetricDistance = "distance" // shortest physical road distance
)

// Handlers holds the HTTP handlers and their dependencies.
type Handlers struct {
	routers map[string]routing.Router // keyed by metric name; MetricTime is required
	stats   StatsResponse
}

// NewHandlers creates handlers serving a single time-metric router.
// Convenience wrapper over NewHandlersMulti for the common (time-only) case.
func NewHandlers(router routing.Router, stats StatsResponse) *Handlers {
	return NewHandlersMulti(map[string]routing.Router{MetricTime: router}, stats)
}

// NewHandlersMulti creates handlers that dispatch on the request metric.
// routers must contain at least the MetricTime key.
func NewHandlersMulti(routers map[string]routing.Router, stats StatsResponse) *Handlers {
	return &Handlers{
		routers: routers,
		stats:   stats,
	}
}
```

In `pkg/api/handlers.go`, inside `HandleRoute`, replace the single routing call (currently the comment `// Route.` and the `result, err := h.router.Route(...)` line) with:

```go
	// Resolve the routing metric (default: time). Existing clients omit this field.
	metric := req.Metric
	if metric == "" {
		metric = MetricTime
	}
	if metric != MetricTime && metric != MetricDistance {
		writeError(w, http.StatusBadRequest, "invalid_request", "metric")
		return
	}
	router, ok := h.routers[metric]
	if !ok {
		writeError(w, http.StatusBadRequest, "metric_unavailable", "metric")
		return
	}

	// Route.
	result, err := router.Route(r.Context(), routing.LatLng{Lat: req.Start.Lat, Lng: req.Start.Lng}, routing.LatLng{Lat: req.End.Lat, Lng: req.End.Lng})
```

- [ ] **Step 5: Run the full api package tests to verify they pass**

Run: `go test ./pkg/api/ -v`
Expected: PASS — all new tests plus the 8 pre-existing tests (which construct via the `NewHandlers` wrapper and omit `metric`, so they hit the time router unchanged).

- [ ] **Step 6: Update the README API section**

In `README.md`, in the `### Route` request block (around line 92-97), change the JSON to show the optional field:

```json
{
  "start": { "lat": 1.3521, "lng": 103.8198 },
  "end": { "lat": 1.2903, "lng": 103.8515 },
  "metric": "distance"
}
```

Directly under that request block, add:

```markdown
`metric` is optional: `"time"` (default — lowest travel time) or `"distance"`
(true shortest road distance). `"distance"` requires the server to be started
with a distance graph (`--graph-distance`); otherwise it returns
`metric_unavailable`. Omitting the field is identical to `"time"`.
```

In the `README.md` Errors table (around line 118-123), add two rows:

```markdown
| 400 | `invalid_request` | Unknown `metric` (only `time`/`distance` are valid) |
| 400 | `metric_unavailable` | Requested metric's graph is not loaded on this server |
```

In the `### Stats` section (around line 133-139), change the description line to:

```markdown
Returns node and edge counts for the time graph, plus `available_metrics`
(e.g. `["time","distance"]`) listing which metrics this server can route.
```

- [ ] **Step 7: Vet and commit**

Run: `go vet ./pkg/api/ && go test ./pkg/api/`
Expected: no vet output; tests PASS.

```bash
git add pkg/api/models.go pkg/api/handlers.go pkg/api/handlers_test.go README.md
git commit -m "feat(api): add optional metric field for shortest-distance routing"
```

---

## Task 2: Load an optional distance graph in the server

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `README.md:69-79` (server flags)

**Interfaces:**
- Consumes: `api.MetricTime`, `api.MetricDistance`, `api.NewHandlersMulti`, `api.StatsResponse.AvailableMetrics` (from Task 1); `graph.ReadBinary`, `routing.NewEngine`, `routing.Router`.
- Produces: server binary accepting `--graph-distance <path>`; a `loadEngine(path string) (*routing.Engine, *graph.CHGraph, error)` helper in `package main`.

> This task modifies `package main`, which the repo does not unit-test. Its gate is: compiles, `go vet` clean, and a smoke run against the existing `graph.time.bin`.

- [ ] **Step 1: Add the `loadEngine` helper**

In `cmd/server/main.go`, add this function (e.g. below `main`):

```go
// loadEngine reads a CH graph binary and builds a routing engine over it,
// reconstructing the original graph needed for snapping and geometry.
func loadEngine(path string) (*routing.Engine, *graph.CHGraph, error) {
	chg, err := graph.ReadBinary(path)
	if err != nil {
		return nil, nil, err
	}
	origGraph := &graph.Graph{
		NumNodes:    chg.NumNodes,
		NumEdges:    uint32(len(chg.OrigHead)),
		FirstOut:    chg.OrigFirstOut,
		Head:        chg.OrigHead,
		Weight:      chg.OrigWeight,
		NodeLat:     chg.NodeLat,
		NodeLon:     chg.NodeLon,
		GeoFirstOut: chg.GeoFirstOut,
		GeoShapeLat: chg.GeoShapeLat,
		GeoShapeLon: chg.GeoShapeLon,
	}
	return routing.NewEngine(chg, origGraph), chg, nil
}
```

- [ ] **Step 2: Add the flag and dual-load in `main`**

In `cmd/server/main.go`, replace the body of `main` from the flag declarations through the `handlers := ...` line with:

```go
	graphPath := flag.String("graph", "graph.bin", "Path to preprocessed graph binary (time metric)")
	graphDistance := flag.String("graph-distance", "", "Optional distance-weighted graph binary; enables metric=\"distance\" routing")
	port := flag.Int("port", 8080, "HTTP port")
	corsOrigin := flag.String("cors-origin", "", "CORS allowed origin (empty = same-origin)")
	flag.Parse()

	start := time.Now()

	// Load the time graph (required).
	log.Printf("Loading time graph from %s...", *graphPath)
	timeEngine, timeCHG, err := loadEngine(*graphPath)
	if err != nil {
		log.Fatalf("Failed to load time graph: %v", err)
	}
	log.Printf("Loaded time graph: %d nodes, %d fwd edges, %d bwd edges",
		timeCHG.NumNodes, len(timeCHG.FwdHead), len(timeCHG.BwdHead))

	routers := map[string]routing.Router{api.MetricTime: timeEngine}
	availableMetrics := []string{api.MetricTime}

	// Load the distance graph (optional).
	if *graphDistance != "" {
		log.Printf("Loading distance graph from %s...", *graphDistance)
		distEngine, distCHG, err := loadEngine(*graphDistance)
		if err != nil {
			log.Fatalf("Failed to load distance graph: %v", err)
		}
		log.Printf("Loaded distance graph: %d nodes, %d fwd edges, %d bwd edges",
			distCHG.NumNodes, len(distCHG.FwdHead), len(distCHG.BwdHead))
		routers[api.MetricDistance] = distEngine
		availableMetrics = append(availableMetrics, api.MetricDistance)
	}

	// Reclaim memory from init-time temporaries (R-tree construction doubles the
	// heap each GC cycle). Return unused pages to the OS.
	runtime.GC()
	debug.FreeOSMemory()

	log.Printf("Ready in %s (metrics: %v)", time.Since(start).Round(time.Millisecond), availableMetrics)

	// Setup HTTP server.
	addr := fmt.Sprintf(":%d", *port)
	cfg := api.DefaultConfig(addr)
	cfg.CORSOrigin = *corsOrigin

	stats := api.StatsResponse{
		NumNodes:         timeCHG.NumNodes,
		NumFwdEdges:      len(timeCHG.FwdHead),
		NumBwdEdges:      len(timeCHG.BwdHead),
		AvailableMetrics: availableMetrics,
	}

	handlers := api.NewHandlersMulti(routers, stats)
```

Leave the remaining lines (`srv := api.NewServer(...)` through the end of `main`) unchanged.

- [ ] **Step 3: Build and vet**

Run: `go build -o bin/map-router-server ./cmd/server && go vet ./cmd/server`
Expected: builds with no output; no vet output. (If `go vet` flags an unused import, ensure `runtime`, `runtime/debug`, `os`, `fmt`, `time`, `log`, `flag`, and the three internal packages are all still referenced — they are.)

- [ ] **Step 4: Smoke test — time-only start still works, and distance is correctly unavailable**

Run (uses the existing time graph; test port 18086 to avoid a running prod server):

```bash
bin/map-router-server --graph graph.time.bin --port 18086 &
SRV=$!
sleep 3
echo "health:"; curl -s localhost:18086/api/v1/health
echo; echo "stats:"; curl -s localhost:18086/api/v1/stats
echo; echo "default route (regression):"
curl -s -X POST localhost:18086/api/v1/route -H 'Content-Type: application/json' \
  -d '{"start":{"lat":3.10,"lng":101.60},"end":{"lat":3.15,"lng":101.65}}' | head -c 120
echo; echo "distance requested but not loaded (want 400 metric_unavailable):"
curl -s -X POST localhost:18086/api/v1/route -H 'Content-Type: application/json' \
  -d '{"start":{"lat":3.10,"lng":101.60},"end":{"lat":3.15,"lng":101.65},"metric":"distance"}'
echo; kill $SRV
```

Expected: health `{"status":"ok"}`; stats includes `"available_metrics":["time"]`; default route returns a `total_distance_meters` object; the distance request returns `{"error":"metric_unavailable","field":"metric"}`.

- [ ] **Step 5: Update README server flags**

In `README.md`, in the "Run the Server" Flags list (around line 77-79), add under `--graph`:

```markdown
- `--graph-distance` — optional distance-weighted graph binary; enables `metric: "distance"` routing (omit for time-only)
```

- [ ] **Step 6: Commit**

```bash
git add cmd/server/main.go README.md
git commit -m "feat(server): --graph-distance flag to serve the distance metric"
```

---

## Task 3: Build the KL distance graph and wire up `run_server.sh`

**Files:**
- Create: `graph.kl.dist.bin` (build artifact, gitignored)
- Rewrite: `run_server.sh`

**Interfaces:**
- Consumes: `cmd/preprocess` (`--input`, `--kl`, `--distance`, `--output`), `cmd/server` `--graph`/`--graph-distance` (Task 2).
- Produces: `graph.kl.dist.bin`; a `run_server.sh` that serves both metrics on port 8086.

- [ ] **Step 1: Build the preprocess binary**

Run: `go build -o bin/map-router-preprocess ./cmd/preprocess`
Expected: builds with no output.

- [ ] **Step 2: Build the distance graph (same bbox as the time graph, distance metric)**

Run:

```bash
bin/map-router-preprocess \
  --input malaysia-singapore-brunei-latest.osm.pbf \
  --kl --distance \
  --output graph.kl.dist.bin
```

Expected: logs include `Distance metric: weighting edges by physical road length (cm); --speeds ignored`, `Using Selangor + KL bounding box filter`, and finally `Done in ... Output: graph.kl.dist.bin (NNN.N MB)`. Takes a few minutes.

- [ ] **Step 3: Sanity-check the artifact**

Run: `ls -lh graph.kl.dist.bin graph.time.bin`
Expected: `graph.kl.dist.bin` exists and is in the same order of magnitude as `graph.time.bin` (~120-140 MB — identical topology, only weights differ). If it is a few KB, the bbox/parse failed — stop and investigate before proceeding.

- [ ] **Step 4: Rewrite `run_server.sh` to serve both metrics**

Replace the entire contents of `run_server.sh` with:

```bash
#!/usr/bin/env bash
#
# Run the KL/Selangor routing server with BOTH metrics on one endpoint:
#   - "time"     (graph.time.bin, the default) — lowest travel time
#   - "distance" (graph.kl.dist.bin)           — true shortest road distance
# Clients choose per request via the "metric" field on POST /api/v1/route.
#
# On first run, graph.kl.dist.bin is built automatically from the local Malaysia
# OSM extract (same --kl bbox as the time graph, --distance metric). No other
# *.bin file is read, written, or deleted.
#
# Usage:  ./run_server.sh
# Overridable:  PORT=9000 DIST_GRAPH=graph.kl.dist.bin ./run_server.sh
set -euo pipefail

# Run from the repo root regardless of where it's called from.
cd "$(dirname "$0")"

TIME_GRAPH="${TIME_GRAPH:-graph.time.bin}"
DIST_GRAPH="${DIST_GRAPH:-graph.kl.dist.bin}"
PBF="${PBF:-malaysia-singapore-brunei-latest.osm.pbf}"
PORT="${PORT:-8086}"

# Build the distance graph on first run (won't touch any other .bin file).
if [[ ! -f "$DIST_GRAPH" ]]; then
	echo "==> $DIST_GRAPH not found; building it (preprocess --kl --distance)"
	go build -o bin/map-router-preprocess ./cmd/preprocess
	bin/map-router-preprocess --input "$PBF" --kl --distance --output "$DIST_GRAPH"
fi

echo "==> Building server binary"
go build -o bin/map-router-server ./cmd/server

echo "==> Serving $TIME_GRAPH (time) + $DIST_GRAPH (distance) on port $PORT"
bin/map-router-server --graph "$TIME_GRAPH" --graph-distance "$DIST_GRAPH" --port "$PORT"
```

- [ ] **Step 5: Verify the script starts and loads both graphs, then stop it**

Run:

```bash
PORT=18086 ./run_server.sh &
SRV=$!
sleep 5
curl -s localhost:18086/api/v1/stats
echo; kill $SRV
```

Expected: `stats` shows `"available_metrics":["time","distance"]`. (The script skips the build step because `graph.kl.dist.bin` already exists from Step 2.)

- [ ] **Step 6: Commit**

```bash
git add run_server.sh
git commit -m "chore: run_server.sh serves both time and distance metrics"
```

---

## Task 4: Live end-to-end verification (time vs distance)

**Files:**
- Create: `scratch/verify_metric.py` (throwaway; not committed)

**Interfaces:**
- Consumes: the running dual-metric server; `datasets/elevete_route_cache/routes.jsonl` (`origin`/`destination` are `[lng, lat]`, `distance_m` is meters).

> Acceptance test proving the feature end-to-end. Produces evidence, not code.

- [ ] **Step 1: Start the dual-metric server on a test port**

Run:

```bash
bin/map-router-server --graph graph.time.bin --graph-distance graph.kl.dist.bin --port 18086 &
SRV=$!
sleep 5
curl -s localhost:18086/api/v1/stats
```

Expected: `"available_metrics":["time","distance"]`.

- [ ] **Step 2: Write the verification script**

Create `scratch/verify_metric.py`:

```python
import json, urllib.request

BASE = "http://localhost:18086/api/v1/route"
# KL/Selangor bbox (matches preprocess --kl): minLat, maxLat, minLng, maxLng
MINLAT, MAXLAT, MINLNG, MAXLNG = 2.75, 3.5, 101.2, 102.0

def in_bbox(lng, lat):
    return MINLAT <= lat <= MAXLAT and MINLNG <= lng <= MAXLNG

# Pick the longest O/D pair fully inside the graph bbox — long trips are where
# a fast highway detour and the shortest surface route diverge most.
best = None
with open("datasets/elevete_route_cache/routes.jsonl") as f:
    for line in f:
        d = json.loads(line)
        o, dst = d["origin"], d["destination"]
        if in_bbox(*o) and in_bbox(*dst):
            if best is None or d["distance_m"] > best["distance_m"]:
                best = d

o, dst = best["origin"], best["destination"]
print(f"O/D: start=({o[1]:.5f},{o[0]:.5f}) end=({dst[1]:.5f},{dst[0]:.5f})  ref_dist={best['distance_m']:.0f} m")

def call(metric):
    body = {"start": {"lat": o[1], "lng": o[0]}, "end": {"lat": dst[1], "lng": dst[0]}}
    if metric:
        body["metric"] = metric
    req = urllib.request.Request(BASE, data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req))["total_distance_meters"]

t, di, de = call("time"), call("distance"), call(None)
print(f"time     : {t:.1f} m")
print(f"distance : {di:.1f} m")
print(f"default  : {de:.1f} m")

assert abs(de - t) < 1e-6, "default must equal time"
assert di <= t + 1e-6, "distance route must not be longer than the time route"
print(f"PASS: distance <= time (saved {t - di:.0f} m), default == time")
```

- [ ] **Step 3: Run the verification**

Run: `python3 scratch/verify_metric.py`
Expected: prints the three distances and `PASS`. The distance route's `total_distance_meters` is **≤** the time route's (strictly less for a divergent long pair — that's the whole point: shorter-but-slower vs longer-but-faster). If distance > time, the metric is being dispatched to the wrong graph — stop and debug before claiming done.

- [ ] **Step 4: Confirm the unavailable-metric path on a time-only server**

Run:

```bash
bin/map-router-server --graph graph.time.bin --port 18087 &
SRV2=$!
sleep 3
curl -s -o /dev/null -w "HTTP %{http_code}\n" -X POST localhost:18087/api/v1/route \
  -H 'Content-Type: application/json' \
  -d '{"start":{"lat":3.10,"lng":101.60},"end":{"lat":3.15,"lng":101.65},"metric":"distance"}'
curl -s -X POST localhost:18087/api/v1/route -H 'Content-Type: application/json' \
  -d '{"start":{"lat":3.10,"lng":101.60},"end":{"lat":3.15,"lng":101.65},"metric":"distance"}'
echo; kill $SRV2
```

Expected: `HTTP 400` and body `{"error":"metric_unavailable","field":"metric"}`.

- [ ] **Step 5: Stop the server and clean up**

Run: `kill $SRV; rm -f scratch/verify_metric.py`
Expected: server stops; scratch file removed (nothing to commit for this task).

---

## Self-Review

**Spec coverage** (design doc → task):
- `metric` field, default time, valid names → Task 1 (models + dispatch, tests).
- Response body unchanged → Task 1 (response builder untouched; asserted by pre-existing tests staying green).
- `available_metrics` in stats → Task 1 (field + test) populated in Task 2.
- Optional distance graph, one process, two engines → Task 2 (`--graph-distance`, routers map).
- `metric_unavailable` (400) vs `invalid_request` (400) → Task 1 (dispatch + tests), Task 2 & 4 (live).
- Same KL bbox distance graph built → Task 3 (`preprocess --kl --distance`).
- `run_server.sh` serves both → Task 3.
- README documents field/flag/errors/stats → Tasks 1 & 2.
- Live time-vs-distance divergence proven → Task 4.

**Placeholder scan:** none — every code/step block is complete.

**Type consistency:** `MetricTime`/`MetricDistance` (exported) used identically in `handlers.go`, `handlers_test.go`, and `cmd/server/main.go`. `NewHandlersMulti(map[string]routing.Router, StatsResponse)` and `loadEngine(string) (*routing.Engine, *graph.CHGraph, error)` signatures match across their consumers. `RouteRequest.Metric` / `StatsResponse.AvailableMetrics` JSON tags (`metric` / `available_metrics`) consistent between production code and tests.
