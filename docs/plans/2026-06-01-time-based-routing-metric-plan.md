---
title: "feat: time-based routing metric + robust snapping"
type: feat
status: planned
date: 2026-06-01
brainstorm: docs/brainstorms/2026-06-01-time-based-routing-metric-design.md
---

# Time-Based Routing Metric + Robust Snapping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make map_router's routes match Google's more closely by routing on estimated travel time instead of pure distance, and eliminate the occasional 2× snapping detours — without regressing validity or driver-paid distance.

**Architecture:** Two sequenced, independently-gated changes. **Change A** (query-time only, no graph rebuild) fixes snapping: direction-aware seeding, geometry/distance measured from the snapped points, and multi-candidate snapping. **Change B** (graph rebuild + binary v3 + atomic swap) switches the edge weight from haversine-distance to class-based travel time, tuned against the 44 Google reference routes via the real query pipeline.

**Tech Stack:** Go 1.26, Contraction Hierarchies + bidirectional Dijkstra, custom binary graph format, `paulmach/osm` PBF parser, Python 3 benchmark harness (`datasets/elevete_route_cache/compare_google.py`).

---

## Background (read first)

- Benchmark harness already exists: `datasets/elevete_route_cache/compare_google.py`. It POSTs the 44 Google O/D pairs to a running server (default `http://localhost:8086`) and reports validity + distance/geometry similarity, writing `comparison.csv`.
- Root cause #1: `pkg/osm/parser.go:254` sets `weightMM = round(haversine_m × 1000)` — pure distance, no speed.
- Root cause #2: `pkg/routing/snap.go` returns the single nearest edge; `pkg/routing/engine.go` `seedForward`/`seedBackward` seed *both* endpoints of the snapped edge regardless of one-way direction.
- CH is metric-agnostic (`pkg/ch/contractor.go` treats `weight` as opaque `uint32`), so contracting on time needs no algorithm change.
- The deployed `graph.bin` is KL/Selangor scope (`refresh_graph.sh` uses `--kl`). All 44 Google pairs lie inside it.

## File Structure

**Change A (no rebuild):**
- Modify `pkg/routing/engine.go` — direction-aware + multi-candidate seeding; geometry/distance from snap points.
- Modify `pkg/routing/snap.go` — add `SnapCandidates`.
- Modify `pkg/routing/dijkstra.go` — add min-seed helpers + per-query access-penalty config (small).
- Test `pkg/routing/engine_test.go` (new), `pkg/routing/snap_test.go` (new), extend `pkg/routing/dijkstra_test.go`.

**Change B (rebuild):**
- Modify `pkg/osm/parser.go` — `speedKmh`, time weight, speed table.
- Create `pkg/osm/speeds.go` — speed table type + default priors + file loader.
- Modify `cmd/preprocess/main.go` — `--speeds` flag.
- Modify `pkg/graph/binary.go` — `version` 2 → 3.
- Modify `pkg/routing/engine.go` — internal `DurationSeconds` (not exposed).
- Modify `refresh_graph.sh` — pass + record the speed table.
- Test `pkg/osm/speeds_test.go` (new), extend `pkg/graph/binary_test.go`.

---

# CHANGE A — Robust snapping + geometry-based distance

Ships on the **current distance-weighted `graph.bin`**. No re-preprocess, no version bump. Gate: `02c22eea` drops below 1.3×, no route > 1.5×, validity stays 44/44, latency unchanged.

### Task A1: Direction-aware seeding

Only seed an endpoint reachable by *legal* travel from the snapped point. On a one-way edge `u→v`, departing backward toward `u` requires the reverse edge `v→u` to exist.

**Files:**
- Modify: `pkg/routing/engine.go:185-224` (`seedForward`, `seedBackward`)
- Test: `pkg/routing/engine_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `pkg/routing/engine_test.go` (uses only existing symbols, so it compiles; it fails on the assertion because today's `seedForward` seeds both endpoints unconditionally):

```go
package routing

import (
	"math"
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

// oneWayParse builds: one-way 0->1 (weight 100), two-way 1<->2 (weight 100).
func oneWayParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 20, Weight: 100}, // 0->1 only (one-way)
			{FromNodeID: 20, ToNodeID: 30, Weight: 100}, // 1->2
			{FromNodeID: 30, ToNodeID: 20, Weight: 100}, // 2->1
		},
		NodeLat: map[osm.NodeID]float64{10: 1.300, 20: 1.300, 30: 1.300},
		NodeLon: map[osm.NodeID]float64{10: 103.800, 20: 103.801, 30: 103.802},
	}
}

// nodeIndex finds the compact index whose coords match the given lat/lon.
func nodeIndex(g *graph.Graph, lat, lon float64) uint32 {
	for i := uint32(0); i < g.NumNodes; i++ {
		if g.NodeLat[i] == lat && g.NodeLon[i] == lon {
			return i
		}
	}
	return noNode
}

func TestSeedForwardRespectsOneWay(t *testing.T) {
	g := graph.Build(oneWayParse())
	n0 := nodeIndex(g, 1.300, 103.800) // tail of the one-way edge
	n1 := nodeIndex(g, 1.300, 103.801) // head of the one-way edge

	// Find the directed edge 0->1.
	var edgeIdx uint32 = noNode
	s, e := g.EdgesFrom(n0)
	for i := s; i < e; i++ {
		if g.Head[i] == n1 {
			edgeIdx = i
		}
	}
	if edgeIdx == noNode {
		t.Fatal("edge n0->n1 not found")
	}

	qs := NewQueryState(g.NumNodes)
	// Snap at the middle of one-way edge n0->n1.
	seedForward(qs, g, SnapResult{EdgeIdx: edgeIdx, NodeU: n0, NodeV: n1, Ratio: 0.5})

	// v (n1) must be seeded — legal forward travel.
	if qs.DistFwd[n1] == math.MaxUint32 {
		t.Errorf("expected n1 (head) to be seeded")
	}
	// u (n0) must NOT be seeded — no reverse edge n1->n0 exists.
	if qs.DistFwd[n0] != math.MaxUint32 {
		t.Errorf("expected n0 (tail) NOT to be seeded on a one-way edge, got %d", qs.DistFwd[n0])
	}
}

func TestSeedForwardTwoWaySeedsBoth(t *testing.T) {
	g := graph.Build(oneWayParse())
	n1 := nodeIndex(g, 1.300, 103.801)
	n2 := nodeIndex(g, 1.300, 103.802)

	var edgeIdx uint32 = noNode
	s, e := g.EdgesFrom(n1)
	for i := s; i < e; i++ {
		if g.Head[i] == n2 {
			edgeIdx = i
		}
	}
	if edgeIdx == noNode {
		t.Fatal("edge n1->n2 not found")
	}

	qs := NewQueryState(g.NumNodes)
	seedForward(qs, g, SnapResult{EdgeIdx: edgeIdx, NodeU: n1, NodeV: n2, Ratio: 0.5})

	if qs.DistFwd[n2] == math.MaxUint32 || qs.DistFwd[n1] == math.MaxUint32 {
		t.Errorf("expected both endpoints seeded on a two-way edge")
	}
}
```

- [ ] **Step 2: Run test to verify it fails for the right reason**

Run: `go test ./pkg/routing/ -run TestSeedForward -v`
Expected: `TestSeedForwardRespectsOneWay` FAILS (current code seeds both u and v unconditionally, so n0 gets seeded). `TestSeedForwardTwoWaySeedsBoth` PASSES.

- [ ] **Step 3: Implement direction-aware seeding**

Replace `seedForward` and `seedBackward` in `pkg/routing/engine.go`:

```go
// seedForward seeds the forward PQ from the start snap point, respecting edge
// direction: travel forward to v is always legal (edge u→v exists); travel
// backward to u is legal only if the reverse edge v→u exists.
func seedForward(qs *QueryState, g *graph.Graph, snap SnapResult) {
	u := snap.NodeU
	v := snap.NodeV
	weight := g.Weight[snap.EdgeIdx]

	dv := uint32(math.Round(float64(weight) * (1 - snap.Ratio)))
	qs.touchFwd(v, dv)
	qs.FwdPQ.Push(v, dv)

	if findEdge(g.FirstOut, g.Head, v, u) != noNode {
		du := uint32(math.Round(float64(weight) * snap.Ratio))
		qs.touchFwd(u, du)
		qs.FwdPQ.Push(u, du)
	}
}

// seedBackward seeds the backward PQ from the end snap point. Arriving from u
// (travel u→v, stop at the point) is always legal; arriving from v requires the
// reverse edge v→u to exist.
func seedBackward(qs *QueryState, g *graph.Graph, snap SnapResult) {
	u := snap.NodeU
	v := snap.NodeV
	weight := g.Weight[snap.EdgeIdx]

	du := uint32(math.Round(float64(weight) * snap.Ratio))
	qs.touchBwd(u, du)
	qs.BwdPQ.Push(u, du)

	if findEdge(g.FirstOut, g.Head, v, u) != noNode {
		dv := uint32(math.Round(float64(weight) * (1 - snap.Ratio)))
		qs.touchBwd(v, dv)
		qs.BwdPQ.Push(v, dv)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/routing/ -run 'TestSeedForward|TestCHDijkstra|TestRouteEndToEnd' -v`
Expected: PASS (all). The existing exactness/end-to-end tests still pass because the test graph is fully bidirectional.

- [ ] **Step 5: Commit**

```bash
git add pkg/routing/engine.go pkg/routing/engine_test.go
git commit -m "fix(routing): direction-aware snap seeding (no reverse travel on one-ways)"
```

### Task A2: Geometry & distance measured from snap points

Decouple reported distance from the routing metric `mu` (required before access penalties exist) and make the geometry begin/end at the snapped points so the partial first/last edges are included.

**Files:**
- Modify: `pkg/routing/engine.go` (`Route`, add `snapPointFor`, `polylineLengthMeters`)
- Test: `pkg/routing/engine_test.go`

- [ ] **Step 1: Write the failing test**

Add `"github.com/azybler/map_router/pkg/geo"` to the import block of `pkg/routing/engine_test.go`, then append:

```go
func TestDistanceIncludesPartialEdges(t *testing.T) {
	g, chg := buildTestGraphAndCH(t)
	eng := NewEngine(chg, g)

	// Query points placed exactly on graph nodes 0 and 5 → ratio 0 / 1 snaps,
	// so distance equals the node-polyline length. Then a second query offset to
	// mid-edge must report a LARGER distance (includes the partial edge), not the
	// same or smaller.
	onNode, err := eng.Route(t.Context(),
		LatLng{Lat: 1.300, Lng: 103.800}, LatLng{Lat: 1.301, Lng: 103.802})
	if err != nil {
		t.Fatalf("Route on-node: %v", err)
	}
	if onNode.TotalDistanceMeters <= 0 {
		t.Fatalf("expected positive distance, got %f", onNode.TotalDistanceMeters)
	}
	// Geometry must start within a meter of the requested start and end points.
	first := onNode.Segments[0].Geometry[0]
	d := geo.Haversine(first.Lat, first.Lng, 1.300, 103.800)
	if d > 1.0 {
		t.Errorf("geometry should start at the snap point; off by %.2f m", d)
	}
	// Reported total distance must equal the summed polyline length.
	var sum float64
	geom := onNode.Segments[0].Geometry
	for i := 0; i+1 < len(geom); i++ {
		sum += geo.Haversine(geom[i].Lat, geom[i].Lng, geom[i+1].Lat, geom[i+1].Lng)
	}
	if math.Abs(sum-onNode.TotalDistanceMeters) > 0.5 {
		t.Errorf("distance %.2f != polyline length %.2f", onNode.TotalDistanceMeters, sum)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/routing/ -run TestDistanceIncludesPartialEdges -v`
Expected: FAIL — geometry currently starts at the first graph node (not the snap point) and `TotalDistanceMeters` is `mu/1000`, so the "starts at snap point" and/or "equals polyline length" assertions fail.

- [ ] **Step 3: Implement snap-point geometry + geometry-based distance**

In `pkg/routing/engine.go`, change `Route` Step 5 (currently lines ~95-107). Replace:

```go
	// Step 5: Build geometry from original node sequence.
	totalDistMeters := float64(mu) / 1000.0
	geometry := e.buildGeometry(origNodes)

	return &RouteResult{
		TotalDistanceMeters: totalDistMeters,
		Segments: []Segment{
			{
				DistanceMeters: totalDistMeters,
				Geometry:       geometry,
			},
		},
	}, nil
```

with:

```go
	// Step 5: Build geometry, anchored at the actual snapped points so the
	// partial first/last edges are included. Distance is measured from the
	// geometry (NOT from mu), which decouples it from the routing metric.
	geometry := e.buildGeometry(origNodes)
	if len(origNodes) > 0 {
		if lat, lng, ok := snapPointFor(e.origGraph, startSnap, origNodes[0]); ok {
			geometry = append([]LatLng{{Lat: lat, Lng: lng}}, geometry...)
		}
		if lat, lng, ok := snapPointFor(e.origGraph, endSnap, origNodes[len(origNodes)-1]); ok {
			geometry = append(geometry, LatLng{Lat: lat, Lng: lng})
		}
	}
	totalDistMeters := polylineLengthMeters(geometry)

	return &RouteResult{
		TotalDistanceMeters: totalDistMeters,
		Segments: []Segment{
			{
				DistanceMeters: totalDistMeters,
				Geometry:       geometry,
			},
		},
	}, nil
```

Add these helpers to `pkg/routing/engine.go` (and add `"github.com/azybler/map_router/pkg/geo"` to its imports):

```go
// snapPointFor returns the interpolated snap-point coordinate on the snapped
// edge, valid when `node` is one of that edge's endpoints (it always is — the
// path starts/ends at a seeded endpoint).
func snapPointFor(g *graph.Graph, snap SnapResult, node uint32) (lat, lng float64, ok bool) {
	if node != snap.NodeU && node != snap.NodeV {
		return 0, 0, false
	}
	lat = g.NodeLat[snap.NodeU] + snap.Ratio*(g.NodeLat[snap.NodeV]-g.NodeLat[snap.NodeU])
	lng = g.NodeLon[snap.NodeU] + snap.Ratio*(g.NodeLon[snap.NodeV]-g.NodeLon[snap.NodeU])
	return lat, lng, true
}

// polylineLengthMeters sums the great-circle length of a lat/lng polyline.
func polylineLengthMeters(geom []LatLng) float64 {
	var total float64
	for i := 0; i+1 < len(geom); i++ {
		total += geo.Haversine(geom[i].Lat, geom[i].Lng, geom[i+1].Lat, geom[i+1].Lng)
	}
	return total
}
```

Note: `Route` already has `startSnap` and `endSnap` in scope (engine.go:62-69).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/routing/ -run 'TestDistanceIncludesPartialEdges|TestRouteEndToEnd|TestCHDijkstra' -v`
Expected: PASS.

- [ ] **Step 5: Run the full package + vet**

Run: `go test ./pkg/routing/ && go vet ./pkg/routing/`
Expected: PASS, no vet warnings.

- [ ] **Step 6: Commit**

```bash
git add pkg/routing/engine.go pkg/routing/engine_test.go
git commit -m "fix(routing): report distance from snap-anchored geometry, not the routing metric"
```

### Task A3: `SnapCandidates` — k nearest distinct edges

**Files:**
- Modify: `pkg/routing/snap.go` (add `SnapCandidates`)
- Test: `pkg/routing/snap_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `pkg/routing/snap_test.go`:

```go
package routing

import (
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

// twoParallelRoads: edge A (0<->1) and a separate edge B (2<->3) ~30 m away,
// plus a redundant duplicate of A's geometry as a second directed pair. A query
// point near A should return A first, then B, deduped to distinct roads.
func snapTestGraph() *graph.Graph {
	return graph.Build(&osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 20, ToNodeID: 10, Weight: 100},
			{FromNodeID: 30, ToNodeID: 40, Weight: 100},
			{FromNodeID: 40, ToNodeID: 30, Weight: 100},
		},
		// Road A at lat 1.3000; road B at lat 1.30027 (~30 m north).
		NodeLat: map[osm.NodeID]float64{10: 1.30000, 20: 1.30000, 30: 1.30027, 40: 1.30027},
		NodeLon: map[osm.NodeID]float64{10: 103.800, 20: 103.801, 30: 103.800, 40: 103.801},
	})
}

func TestSnapCandidatesDistinctAndSorted(t *testing.T) {
	s := NewSnapper(snapTestGraph())
	// Query just above road A.
	cands := s.SnapCandidates(1.30005, 103.8005, 4, 500.0)
	if len(cands) < 2 {
		t.Fatalf("expected at least 2 distinct candidates, got %d", len(cands))
	}
	// Sorted ascending by off-road distance.
	for i := 1; i < len(cands); i++ {
		if cands[i].Dist < cands[i-1].Dist {
			t.Errorf("candidates not sorted by distance: %v", cands)
		}
	}
	// Distinct undirected node-pairs.
	seen := map[[2]uint32]bool{}
	for _, c := range cands {
		a, b := c.NodeU, c.NodeV
		if a > b {
			a, b = b, a
		}
		if seen[[2]uint32{a, b}] {
			t.Errorf("duplicate node-pair candidate %d-%d", a, b)
		}
		seen[[2]uint32{a, b}] = true
	}
}

func TestSnapCandidatesRespectsRadius(t *testing.T) {
	s := NewSnapper(snapTestGraph())
	// A point far from any road returns nothing within a tight radius.
	cands := s.SnapCandidates(1.4, 103.9, 4, 50.0)
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates far from roads, got %d", len(cands))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/routing/ -run TestSnapCandidates -v`
Expected: FAIL — `SnapCandidates` undefined.

- [ ] **Step 3: Implement `SnapCandidates`**

Add to `pkg/routing/snap.go` (it already imports `sort`, `math`, `geo`):

```go
// SnapCandidates returns up to k nearest DISTINCT road edges within radiusMeters
// of the query point, sorted ascending by off-road distance. Distinct = unique
// undirected node-pair, so the two directed halves of a two-way road and
// duplicate geometry collapse to one candidate.
func (s *Snapper) SnapCandidates(lat, lng float64, k int, radiusMeters float64) []SnapResult {
	centerLat, centerLon := gridCell(lat, lng)

	var all []SnapResult
	for dLat := int32(-1); dLat <= 1; dLat++ {
		for dLon := int32(-1); dLon <= 1; dLon++ {
			key := cellKey(centerLat+dLat, centerLon+dLon)
			for _, ce := range s.cellRange(key) {
				u := ce.source
				v := s.g.Head[ce.edgeIdx]
				exactDist, ratio := geo.PointToSegmentDist(
					lat, lng,
					s.g.NodeLat[u], s.g.NodeLon[u],
					s.g.NodeLat[v], s.g.NodeLon[v],
				)
				if exactDist <= radiusMeters {
					all = append(all, SnapResult{
						EdgeIdx: ce.edgeIdx, NodeU: u, NodeV: v, Ratio: ratio, Dist: exactDist,
					})
				}
			}
		}
	}

	sort.Slice(all, func(i, j int) bool { return all[i].Dist < all[j].Dist })

	seen := make(map[uint64]struct{}, len(all))
	out := make([]SnapResult, 0, k)
	for _, r := range all {
		a, b := r.NodeU, r.NodeV
		if a > b {
			a, b = b, a
		}
		key := uint64(a)<<32 | uint64(b)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, r)
		if len(out) >= k {
			break
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/routing/ -run TestSnapCandidates -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/routing/snap.go pkg/routing/snap_test.go
git commit -m "feat(routing): add SnapCandidates (k nearest distinct edges within radius)"
```

### Task A4: Multi-candidate seeding + access penalty in the engine

Seed all candidates (direction-aware, lowest-cost wins), adding a metric-agnostic access penalty that auto-scales to the active metric via the candidate edge's own `weight/length` ratio.

**Files:**
- Modify: `pkg/routing/engine.go` (`Route`, `seedForward`/`seedBackward` → access-penalty-aware; add min-seed helpers)
- Modify: `pkg/routing/dijkstra.go` (add `seedFwdMin`/`seedBwdMin` helpers)
- Test: `pkg/routing/engine_test.go` (stub graph), extend `pkg/routing/dijkstra_test.go` (multi-seed exactness)

- [ ] **Step 1: Write the failing multi-seed exactness test**

Append to `pkg/routing/dijkstra_test.go`:

```go
// plainDijkstraMulti runs multi-source Dijkstra from seed map src{node:dist}.
func plainDijkstraMulti(g *graph.Graph, src map[uint32]uint32) []uint32 {
	dist := make([]uint32, g.NumNodes)
	for i := range dist {
		dist[i] = math.MaxUint32
	}
	type item struct{ node, dist uint32 }
	var pq []item
	for n, d := range src {
		dist[n] = d
		pq = append(pq, item{n, d})
	}
	for len(pq) > 0 {
		minIdx := 0
		for i := 1; i < len(pq); i++ {
			if pq[i].dist < pq[minIdx].dist {
				minIdx = i
			}
		}
		cur := pq[minIdx]
		pq[minIdx] = pq[len(pq)-1]
		pq = pq[:len(pq)-1]
		if cur.dist > dist[cur.node] {
			continue
		}
		s, e := g.EdgesFrom(cur.node)
		for ei := s; ei < e; ei++ {
			v := g.Head[ei]
			nd := cur.dist + g.Weight[ei]
			if nd < dist[v] {
				dist[v] = nd
				pq = append(pq, item{v, nd})
			}
		}
	}
	return dist
}

func TestCHMultiSeedExactness(t *testing.T) {
	g, chg := buildTestGraphAndCH(t)
	eng := &Engine{chg: chg}

	// Seed multiple forward and backward nodes at unequal nonzero costs.
	fwdSeeds := map[uint32]uint32{0: 50, 3: 10}
	bwdSeeds := map[uint32]uint32{5: 20, 2: 70}

	qs := NewQueryState(chg.NumNodes)
	for n, d := range fwdSeeds {
		qs.touchFwd(n, d)
		qs.FwdPQ.Push(n, d)
	}
	for n, d := range bwdSeeds {
		qs.touchBwd(n, d)
		qs.BwdPQ.Push(n, d)
	}
	mu, _ := eng.runCHDijkstra(context.Background(), qs)

	// Brute force: min over (fwdDist[x] + bwdDist[x]).
	fwd := plainDijkstraMulti(g, fwdSeeds)
	bwd := plainDijkstraMulti(g, bwdSeeds)
	best := uint32(math.MaxUint32)
	for n := uint32(0); n < g.NumNodes; n++ {
		if fwd[n] == math.MaxUint32 || bwd[n] == math.MaxUint32 {
			continue
		}
		if s := fwd[n] + bwd[n]; s < best {
			best = s
		}
	}
	if mu != best {
		t.Errorf("multi-seed CH mu=%d, brute force=%d", mu, best)
	}
}
```

- [ ] **Step 2: Run test to verify it passes (baseline)**

Run: `go test ./pkg/routing/ -run TestCHMultiSeedExactness -v`
Expected: PASS — this validates the *existing* `runCHDijkstra` already handles multi-source seeds correctly (it does; this test guards the seeding change we make next).

- [ ] **Step 3: Write the failing stub-avoidance test**

Append to `pkg/routing/engine_test.go`:

```go
// stubGraph: a well-connected main road 0-1-2-3-4, plus an isolated stub node 5
// joined to the network only by a long detour through node 0. A query point
// nearest the stub must still route via the main road, not the stub.
//
//	main:  0 --1-- 1 --1-- 2 --1-- 3 --1-- 4     (two-way, weight 1 each scaled)
//	stub:  5 --(short)-- 0  but 5 is closest to the query point
func stubParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 100}, {FromNodeID: 2, ToNodeID: 1, Weight: 100},
			{FromNodeID: 2, ToNodeID: 3, Weight: 100}, {FromNodeID: 3, ToNodeID: 2, Weight: 100},
			{FromNodeID: 3, ToNodeID: 4, Weight: 100}, {FromNodeID: 4, ToNodeID: 3, Weight: 100},
			// stub node 5 connects only back to node 1 via a long umbilical (10000).
			{FromNodeID: 5, ToNodeID: 1, Weight: 10000}, {FromNodeID: 1, ToNodeID: 5, Weight: 10000},
		},
		NodeLat: map[osm.NodeID]float64{
			1: 1.30000, 2: 1.30090, 3: 1.30180, 4: 1.30270, 5: 1.30044,
		},
		NodeLon: map[osm.NodeID]float64{
			1: 103.800, 2: 103.800, 3: 103.800, 4: 103.800, 5: 103.80050,
		},
	}
}

func TestMultiCandidateAvoidsStub(t *testing.T) {
	g := graph.Build(stubParse())
	chg := chContract(t, g) // helper below
	eng := NewEngine(chg, g)

	// Query point sits nearest the stub edge (5-1) but should route to node 3
	// via the main road, NOT via the 10 km umbilical.
	res, err := eng.Route(t.Context(),
		LatLng{Lat: 1.30045, Lng: 103.80048}, // ~closest to stub node 5
		LatLng{Lat: 1.30180, Lng: 103.800})   // node 3 on main road
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	// Main-road distance is ~hundreds of m; a stub detour would be >10 km.
	if res.TotalDistanceMeters > 2000 {
		t.Errorf("expected main-road route (<2 km), got %.0f m (stub detour not avoided)", res.TotalDistanceMeters)
	}
}
```

Add a small CH helper near the top of `engine_test.go` (import `ch`):

```go
func chContract(t *testing.T, g *graph.Graph) *graph.CHGraph {
	t.Helper()
	return ch.Contract(g)
}
```

(Add `"github.com/azybler/map_router/pkg/ch"` to the imports.)

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./pkg/routing/ -run TestMultiCandidateAvoidsStub -v`
Expected: FAIL — current `Route` uses single-nearest `Snap`, so the query snaps to the stub edge and detours ~10 km.

- [ ] **Step 5: Add min-seed helpers**

Add to `pkg/routing/dijkstra.go`:

```go
// seedFwdMin seeds node in the forward search at dist, keeping the minimum if
// the node was already seeded by another candidate. Safe to call repeatedly.
func (qs *QueryState) seedFwdMin(node, dist uint32) {
	if qs.DistFwd[node] != math.MaxUint32 && dist >= qs.DistFwd[node] {
		return
	}
	qs.touchFwd(node, dist)
	qs.FwdPQ.Push(node, dist)
}

// seedBwdMin seeds node in the backward search at dist, keeping the minimum.
func (qs *QueryState) seedBwdMin(node, dist uint32) {
	if qs.DistBwd[node] != math.MaxUint32 && dist >= qs.DistBwd[node] {
		return
	}
	qs.touchBwd(node, dist)
	qs.BwdPQ.Push(node, dist)
}
```

- [ ] **Step 6: Make seeding access-penalty-aware and multi-candidate**

In `pkg/routing/engine.go`, add a package-level config and an access-penalty helper:

```go
const (
	snapK             = 4
	snapRadiusMeters  = maxSnapDistMeters // 500 m: never reject what single-nearest accepted
	accessPenaltyMult = 1.0               // off-road distance penalty multiplier
)

// accessPenalty converts the off-road snap distance into the active metric's
// units using the candidate edge's own weight/length ratio, so it auto-scales
// whether the metric is distance (mm) or time (ms).
func accessPenalty(g *graph.Graph, snap SnapResult) uint32 {
	u, v := snap.NodeU, snap.NodeV
	lenM := geo.Haversine(g.NodeLat[u], g.NodeLon[u], g.NodeLat[v], g.NodeLon[v])
	if lenM <= 0 {
		return 0
	}
	metricPerMeter := float64(g.Weight[snap.EdgeIdx]) / lenM
	return uint32(math.Round(accessPenaltyMult * snap.Dist * metricPerMeter))
}
```

Replace the bodies of `seedForward`/`seedBackward` to take the penalty and use `seedFwdMin`/`seedBwdMin`:

```go
func seedForward(qs *QueryState, g *graph.Graph, snap SnapResult) {
	u, v := snap.NodeU, snap.NodeV
	weight := g.Weight[snap.EdgeIdx]
	pen := accessPenalty(g, snap)

	qs.seedFwdMin(v, uint32(math.Round(float64(weight)*(1-snap.Ratio)))+pen)
	if findEdge(g.FirstOut, g.Head, v, u) != noNode {
		qs.seedFwdMin(u, uint32(math.Round(float64(weight)*snap.Ratio))+pen)
	}
}

func seedBackward(qs *QueryState, g *graph.Graph, snap SnapResult) {
	u, v := snap.NodeU, snap.NodeV
	weight := g.Weight[snap.EdgeIdx]
	pen := accessPenalty(g, snap)

	qs.seedBwdMin(u, uint32(math.Round(float64(weight)*snap.Ratio))+pen)
	if findEdge(g.FirstOut, g.Head, v, u) != noNode {
		qs.seedBwdMin(v, uint32(math.Round(float64(weight)*(1-snap.Ratio)))+pen)
	}
}
```

> Note: Task A1's tests asserted nodes are seeded/not-seeded; they still hold (penalty is additive, the direction gate is unchanged). The `TestSeedForwardRespectsOneWay` check `DistFwd[n0] != MaxUint32` still passes because n0 is never seeded.

Now update `Route` (engine.go ~60-90) to snap multiple candidates and seed each. Replace the snap+seed block:

```go
	startCands := e.snapper.SnapCandidates(start.Lat, start.Lng, snapK, snapRadiusMeters)
	if len(startCands) == 0 {
		return nil, ErrPointTooFar
	}
	endCands := e.snapper.SnapCandidates(end.Lat, end.Lng, snapK, snapRadiusMeters)
	if len(endCands) == 0 {
		return nil, ErrPointTooFar
	}

	qs := e.qsPool.Get().(*QueryState)
	defer func() {
		qs.Reset()
		e.qsPool.Put(qs)
	}()

	for _, c := range startCands {
		seedForward(qs, e.origGraph, c)
	}
	for _, c := range endCands {
		seedBackward(qs, e.origGraph, c)
	}
```

And update the Step-5 geometry anchoring to use the candidate that actually seeded the chosen endpoint. Replace the `snapPointFor(... startSnap ...)` / `endSnap` calls with a scan over candidates:

```go
	if len(origNodes) > 0 {
		if lat, lng, ok := snapPointForCandidates(e.origGraph, startCands, origNodes[0]); ok {
			geometry = append([]LatLng{{Lat: lat, Lng: lng}}, geometry...)
		}
		if lat, lng, ok := snapPointForCandidates(e.origGraph, endCands, origNodes[len(origNodes)-1]); ok {
			geometry = append(geometry, LatLng{Lat: lat, Lng: lng})
		}
	}
```

Replace `snapPointFor` (from A2) with the multi-candidate version:

```go
// snapPointForCandidates returns the snap point of the nearest candidate that
// has `node` as an endpoint (i.e. the candidate that could have seeded it).
func snapPointForCandidates(g *graph.Graph, cands []SnapResult, node uint32) (lat, lng float64, ok bool) {
	best := -1
	for i := range cands {
		if cands[i].NodeU == node || cands[i].NodeV == node {
			if best < 0 || cands[i].Dist < cands[best].Dist {
				best = i
			}
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	c := cands[best]
	lat = g.NodeLat[c.NodeU] + c.Ratio*(g.NodeLat[c.NodeV]-g.NodeLat[c.NodeU])
	lng = g.NodeLon[c.NodeU] + c.Ratio*(g.NodeLon[c.NodeV]-g.NodeLon[c.NodeU])
	return lat, lng, true
}
```

Delete the now-unused single-snap `snapPointFor` and the now-unused `startSnap`/`endSnap` locals (the old `e.snapper.Snap` calls are replaced). Keep `Snapper.Snap` itself (other tests use it).

- [ ] **Step 7: Run the full routing package**

Run: `go test ./pkg/routing/ -v`
Expected: PASS — including `TestMultiCandidateAvoidsStub`, `TestCHMultiSeedExactness`, `TestSeedForwardRespectsOneWay`, `TestDistanceIncludesPartialEdges`, and the pre-existing tests.

- [ ] **Step 8: Vet + whole-repo tests**

Run: `go vet ./... && go test ./... -timeout 120s`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add pkg/routing/engine.go pkg/routing/dijkstra.go pkg/routing/engine_test.go pkg/routing/dijkstra_test.go
git commit -m "feat(routing): multi-candidate snapping with metric-scaled access penalty"
```

### Task A5: Change-A benchmark gate (manual verification)

**Files:** none modified; runs the benchmark against the current distance graph with the new snapping.

- [ ] **Step 1: Build and start the server on the existing graph**

```bash
go build -o bin/map-router-server ./cmd/server
# Stop any existing server on 8086 first if needed.
bin/map-router-server --graph graph.bin --port 18086 &
sleep 2 && curl -s http://localhost:18086/api/v1/health
```
Expected: `{"status":"ok"}`.

- [ ] **Step 2: Run the benchmark**

Run: `python3 datasets/elevete_route_cache/compare_google.py --url http://localhost:18086 --buffer 25`

- [ ] **Step 3: Verify the Change-A gate**

Confirm in the output:
- VALIDITY: **44/44** routed OK (no regression).
- The route `02c22ee` ratio is now **< 1.3×** (was 2.09×).
- **No** route has `dist_ratio > 1.5`.
- (Distance/overlap medians may shift slightly from the A2 geometry-distance change — that is expected; record them as the new Change-A baseline for Change B.)

If the gate is not met, debug snapping before proceeding (do NOT continue to Change B).

- [ ] **Step 4: Stop the server and commit the recorded baseline**

```bash
kill %1 2>/dev/null
git add datasets/elevete_route_cache/comparison.csv 2>/dev/null || true
git commit -m "test: Change-A benchmark baseline (snapping fix verified)" --allow-empty
```

> Note: `datasets/` is untracked by default (sensitive customer endpoints). Only commit `comparison.csv` if the team has agreed to track benchmark outputs; otherwise keep the `--allow-empty` commit as a checkpoint marker and leave `datasets/` untracked.

---

# CHANGE B — Class-based travel-time metric

Requires a graph rebuild, binary `version` bump, and an atomic swap. Distance reporting is already geometry-based (Change A), so switching `mu` to time does **not** affect reported distance.

### Task B1: Speed table + `speedKmh`

**Files:**
- Create: `pkg/osm/speeds.go`
- Test: `pkg/osm/speeds_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `pkg/osm/speeds_test.go`:

```go
package osm

import (
	"math"
	"testing"

	"github.com/paulmach/osm"
)

func tags(kv ...string) osm.Tags {
	var t osm.Tags
	for i := 0; i+1 < len(kv); i += 2 {
		t = append(t, osm.Tag{Key: kv[i], Value: kv[i+1]})
	}
	return t
}

func TestSpeedKmh(t *testing.T) {
	tbl := DefaultSpeedTable()
	cases := []struct {
		name string
		tags osm.Tags
		want float64
	}{
		{"motorway default", tags("highway", "motorway"), 90},
		{"residential default", tags("highway", "residential"), 25},
		{"service default", tags("highway", "service"), 15},
		{"motorway_link derived", tags("highway", "motorway_link"), 0.7 * 90},
		{"numeric maxspeed", tags("highway", "primary", "maxspeed", "80"), 80},
		{"mph maxspeed", tags("highway", "primary", "maxspeed", "30 mph"), 30 * 1.609344},
		{"MY:urban zone", tags("highway", "primary", "maxspeed", "MY:urban"), 60},
		{"none falls back to class", tags("highway", "secondary", "maxspeed", "none"), 45},
		{"garbage falls back", tags("highway", "tertiary", "maxspeed", "fast"), 38},
		{"unknown class falls back", tags("highway", "track"), tbl.Fallback},
	}
	for _, c := range cases {
		got := tbl.SpeedKmh(c.tags)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("%s: SpeedKmh = %.3f, want %.3f", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/osm/ -run TestSpeedKmh -v`
Expected: FAIL — `DefaultSpeedTable`/`SpeedKmh` undefined.

- [ ] **Step 3: Implement the speed table**

Create `pkg/osm/speeds.go`:

```go
package osm

import (
	"strconv"
	"strings"

	"github.com/paulmach/osm"
)

// SpeedTable maps highway classes to free-flow speeds (km/h). Link classes are
// derived as LinkFactor × parent. Fallback is used for unlisted drivable classes.
type SpeedTable struct {
	ClassKmh   map[string]float64
	ZoneKmh    map[string]float64 // maxspeed zone codes, e.g. "MY:urban"
	LinkFactor float64
	Fallback   float64
}

// DefaultSpeedTable returns the Malaysian-urban free-flow priors.
func DefaultSpeedTable() SpeedTable {
	return SpeedTable{
		ClassKmh: map[string]float64{
			"motorway": 90, "trunk": 70, "primary": 55, "secondary": 45,
			"tertiary": 38, "unclassified": 35, "residential": 25,
			"living_street": 12, "service": 15,
		},
		ZoneKmh: map[string]float64{
			"MY:urban": 60, "MY:rural": 90, "MY:expressway": 110,
			"RM:urban": 60, "RM:rural": 90,
		},
		LinkFactor: 0.7,
		Fallback:   30,
	}
}

// classSpeed returns the base (non-link) speed for a highway class.
func (s SpeedTable) classSpeed(hw string) float64 {
	if v, ok := s.ClassKmh[hw]; ok {
		return v
	}
	return s.Fallback
}

// SpeedKmh resolves a way's free-flow speed: maxspeed when parseable, else the
// class default (links = LinkFactor × parent class).
func (s SpeedTable) SpeedKmh(t osm.Tags) float64 {
	hw := t.Find("highway")

	if ms := strings.TrimSpace(t.Find("maxspeed")); ms != "" {
		if v, ok := s.parseMaxspeed(ms); ok {
			return v
		}
	}

	if strings.HasSuffix(hw, "_link") {
		return s.LinkFactor * s.classSpeed(strings.TrimSuffix(hw, "_link"))
	}
	return s.classSpeed(hw)
}

// parseMaxspeed handles "60", "30 mph", and zone codes; returns ok=false for
// "none"/"walk"/conditional/per-direction/garbage so the caller falls back.
func (s SpeedTable) parseMaxspeed(ms string) (float64, bool) {
	if v, ok := s.ZoneKmh[ms]; ok {
		return v, true
	}
	fields := strings.Fields(ms)
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	if len(fields) > 1 && strings.EqualFold(fields[1], "mph") {
		return n * 1.609344, true
	}
	return n, true // bare number = km/h
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/osm/ -run TestSpeedKmh -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/osm/speeds.go pkg/osm/speeds_test.go
git commit -m "feat(osm): class-based free-flow speed table with maxspeed parsing"
```

### Task B2: Parser computes travel-time weights

**Files:**
- Modify: `pkg/osm/parser.go` (`Parse` signature + weight computation)
- Test: `pkg/osm/parser_test.go` (add a weight-is-time case)

- [ ] **Step 1: Write the failing test**

Append to `pkg/osm/parser_test.go` (it is `package osm`):

```go
func TestEdgeWeightIsTravelTime(t *testing.T) {
	// A 1 km primary segment at 55 km/h ≈ 65454 ms.
	tbl := DefaultSpeedTable()
	speed := tbl.classSpeed("primary") // 55 km/h
	lengthM := 1000.0
	wantMs := lengthM / (speed / 3.6) * 1000
	// computeWeightMs is the new internal helper used by Parse.
	got := computeWeightMs(lengthM, speed)
	if math.Abs(float64(got)-wantMs) > 1 {
		t.Errorf("computeWeightMs = %d, want ~%.0f", got, wantMs)
	}
	if got == 0 {
		t.Error("weight must be >= 1")
	}
}
```

(Ensure `"math"` is imported in `parser_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/osm/ -run TestEdgeWeightIsTravelTime -v`
Expected: FAIL — `computeWeightMs` undefined.

- [ ] **Step 3: Implement time weights in the parser**

In `pkg/osm/parser.go`:

1. Add the helper:

```go
// computeWeightMs converts a segment length (m) and speed (km/h) to travel time
// in milliseconds, clamped to >= 1.
func computeWeightMs(lengthMeters, speedKmh float64) uint32 {
	if speedKmh <= 0 {
		speedKmh = 1
	}
	ms := lengthMeters / (speedKmh / 3.6) * 1000
	w := uint32(math.Round(ms))
	if w == 0 {
		w = 1
	}
	return w
}
```

2. Add a `Speeds SpeedTable` field to `ParseOptions`:

```go
type ParseOptions struct {
	BBox   BBox       // if non-zero, filter edges to this bounding box
	Speeds SpeedTable // free-flow speed model; zero value → DefaultSpeedTable()
}
```

3. In `Parse`, resolve the table once after reading `opt`:

```go
	if opt.Speeds.ClassKmh == nil {
		opt.Speeds = DefaultSpeedTable()
	}
```

4. `wayInfo` must carry the way's speed. Add `SpeedKmh float64` to `wayInfo`, and in Pass 1 set it:

```go
		ways = append(ways, wayInfo{
			NodeIDs:  nodeIDs,
			Forward:  fwd,
			Backward: bwd,
			SpeedKmh: opt.Speeds.SpeedKmh(w.Tags),
		})
```

5. Replace the weight computation in the edge-building loop (parser.go ~253-257):

```go
			dist := geo.Haversine(fromLat, fromLon, toLat, toLon)
			weightMM := uint32(math.Round(dist * 1000))
			if weightMM == 0 {
				weightMM = 1 // avoid zero-weight edges
			}
```

with:

```go
			dist := geo.Haversine(fromLat, fromLon, toLat, toLon)
			weight := computeWeightMs(dist, w.SpeedKmh)
```

and update the two `RawEdge{...}` literals to use `Weight: weight` (the variable rename from `weightMM`).

- [ ] **Step 4: Run osm tests**

Run: `go test ./pkg/osm/ -v`
Expected: PASS. (If `parser_test.go` asserts specific distance-based weights elsewhere, update those expectations to time — search for `Weight:` assertions and recompute with `computeWeightMs`.)

- [ ] **Step 5: Commit**

```bash
git add pkg/osm/parser.go pkg/osm/parser_test.go
git commit -m "feat(osm): edge weight = class-based travel time (ms), not distance"
```

### Task B3: `--speeds` flag for preprocess

**Files:**
- Modify: `pkg/osm/speeds.go` (add `LoadSpeedTable`)
- Modify: `cmd/preprocess/main.go` (`--speeds` flag, log hash)
- Test: `pkg/osm/speeds_test.go` (load round-trip)

- [ ] **Step 1: Write the failing test**

Append to `pkg/osm/speeds_test.go`:

```go
func TestLoadSpeedTable(t *testing.T) {
	json := `{"class_kmh":{"motorway":100,"primary":50},"zone_kmh":{"MY:urban":60},"link_factor":0.6,"fallback":28}`
	tbl, err := ParseSpeedTable([]byte(json))
	if err != nil {
		t.Fatal(err)
	}
	if tbl.ClassKmh["motorway"] != 100 || tbl.LinkFactor != 0.6 || tbl.Fallback != 28 {
		t.Errorf("parsed table wrong: %+v", tbl)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/osm/ -run TestLoadSpeedTable -v`
Expected: FAIL — `ParseSpeedTable` undefined.

- [ ] **Step 3: Implement load + parse**

Add to `pkg/osm/speeds.go` (add `"encoding/json"` and `"os"` imports):

```go
// ParseSpeedTable parses a JSON speed table. Missing fields fall back to
// DefaultSpeedTable values so a partial file is still valid.
func ParseSpeedTable(data []byte) (SpeedTable, error) {
	def := DefaultSpeedTable()
	var raw struct {
		ClassKmh   map[string]float64 `json:"class_kmh"`
		ZoneKmh    map[string]float64 `json:"zone_kmh"`
		LinkFactor float64            `json:"link_factor"`
		Fallback   float64            `json:"fallback"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return SpeedTable{}, err
	}
	if raw.ClassKmh != nil {
		def.ClassKmh = raw.ClassKmh
	}
	if raw.ZoneKmh != nil {
		def.ZoneKmh = raw.ZoneKmh
	}
	if raw.LinkFactor > 0 {
		def.LinkFactor = raw.LinkFactor
	}
	if raw.Fallback > 0 {
		def.Fallback = raw.Fallback
	}
	return def, nil
}

// LoadSpeedTable reads a JSON speed table from path.
func LoadSpeedTable(path string) (SpeedTable, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SpeedTable{}, err
	}
	return ParseSpeedTable(data)
}
```

- [ ] **Step 4: Wire the flag into preprocess**

In `cmd/preprocess/main.go`, add after the existing flags:

```go
	speeds := flag.String("speeds", "", "Path to a JSON speed table (default: built-in Malaysian priors)")
```

After bbox parsing, before `start := time.Now()`:

```go
	if *speeds != "" {
		tbl, err := osmparser.LoadSpeedTable(*speeds)
		if err != nil {
			log.Fatalf("Failed to load speed table: %v", err)
		}
		opts.Speeds = tbl
		log.Printf("Using speed table from %s", *speeds)
	} else {
		opts.Speeds = osmparser.DefaultSpeedTable()
		log.Println("Using built-in default speed table")
	}
```

- [ ] **Step 5: Run tests + build preprocess**

Run: `go test ./pkg/osm/ -run TestLoadSpeedTable -v && go build -o bin/map-router-preprocess ./cmd/preprocess`
Expected: PASS, builds cleanly.

- [ ] **Step 6: Commit**

```bash
git add pkg/osm/speeds.go pkg/osm/speeds_test.go cmd/preprocess/main.go
git commit -m "feat(preprocess): --speeds flag to load a tunable speed table"
```

### Task B4: Binary `version` 2 → 3

**Files:**
- Modify: `pkg/graph/binary.go:16` (`version` const)
- Test: `pkg/graph/binary_test.go` (version-mismatch rejection)

- [ ] **Step 1: Write the failing test**

Append to `pkg/graph/binary_test.go`:

```go
func TestBinaryVersionIs3(t *testing.T) {
	if version != 3 {
		t.Errorf("binary format version = %d, want 3 (time metric)", version)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/graph/ -run TestBinaryVersionIs3 -v`
Expected: FAIL — `version` is still 2.

- [ ] **Step 3: Bump the version**

In `pkg/graph/binary.go`, change:

```go
	version    = uint32(2) // v2: added original graph edges for snapping
```

to:

```go
	version    = uint32(3) // v3: edge weights are travel time (ms), not distance (mm)
```

- [ ] **Step 4: Run graph tests**

Run: `go test ./pkg/graph/ -v`
Expected: PASS. (The existing round-trip test writes and reads at the current `version`, so it stays green.)

- [ ] **Step 5: Commit**

```bash
git add pkg/graph/binary.go pkg/graph/binary_test.go
git commit -m "feat(graph): bump binary version to 3 (time-metric weights)"
```

### Task B5: Internal duration (not exposed)

**Files:**
- Modify: `pkg/routing/engine.go` (`RouteResult`, set `DurationSeconds`)
- Test: `pkg/routing/engine_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/routing/engine_test.go`:

```go
func TestDurationSecondsPopulated(t *testing.T) {
	g, chg := buildTestGraphAndCH(t)
	eng := NewEngine(chg, g)
	res, err := eng.Route(t.Context(),
		LatLng{Lat: 1.300, Lng: 103.800}, LatLng{Lat: 1.301, Lng: 103.802})
	if err != nil {
		t.Fatal(err)
	}
	// mu/1000 of a positive route is > 0 (units depend on the metric).
	if res.DurationSeconds <= 0 {
		t.Errorf("DurationSeconds = %f, want > 0", res.DurationSeconds)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/routing/ -run TestDurationSecondsPopulated -v`
Expected: FAIL — `RouteResult` has no `DurationSeconds` field.

- [ ] **Step 3: Implement**

In `pkg/routing/engine.go`, add to `RouteResult`:

```go
type RouteResult struct {
	TotalDistanceMeters float64
	DurationSeconds     float64 // internal: mu/1000; includes access penalty; NOT exposed via API in Phase 1
	Segments            []Segment
}
```

In `Route`, after computing `totalDistMeters`, set the field on the returned struct:

```go
	return &RouteResult{
		TotalDistanceMeters: totalDistMeters,
		DurationSeconds:     float64(mu) / 1000.0,
		Segments: []Segment{
			{
				DistanceMeters: totalDistMeters,
				Geometry:       geometry,
			},
		},
	}, nil
```

> Note: do NOT add `duration_seconds` to `pkg/api/models.go` — there is no time ground truth to validate it in Phase 1 (design §B3). The API response is unchanged.

- [ ] **Step 4: Run tests + confirm no `mu/1000` is used as distance**

Run: `go test ./pkg/routing/ -v && grep -n "mu) / 1000" pkg/routing/engine.go`
Expected: tests PASS; the only `mu/1000` usage is for `DurationSeconds` (distance now comes from `polylineLengthMeters`).

- [ ] **Step 5: Commit**

```bash
git add pkg/routing/engine.go pkg/routing/engine_test.go
git commit -m "feat(routing): internal DurationSeconds from time metric (not exposed via API)"
```

### Task B6: Update `refresh_graph.sh` to pass + record the speed table

**Files:**
- Create: `speeds.json` (the tuned table; starts as the default priors)
- Modify: `refresh_graph.sh`

- [ ] **Step 1: Write the default speed table file**

Create `speeds.json`:

```json
{
  "class_kmh": {
    "motorway": 90, "trunk": 70, "primary": 55, "secondary": 45,
    "tertiary": 38, "unclassified": 35, "residential": 25,
    "living_street": 12, "service": 15
  },
  "zone_kmh": { "MY:urban": 60, "MY:rural": 90, "MY:expressway": 110 },
  "link_factor": 0.7,
  "fallback": 30
}
```

- [ ] **Step 2: Update the refresh script**

In `refresh_graph.sh`, add a `SPEEDS` var near the other env vars:

```bash
SPEEDS="${SPEEDS:-speeds.json}"
```

Change the preprocess invocation to pass and record it:

```bash
echo "==> Regenerating $OUTPUT (filter: $FILTER, speeds: $SPEEDS)"
bin/map-router-preprocess --input "$PBF" --output "$OUTPUT" "$FILTER" --speeds "$SPEEDS"
shasum -a 256 "$SPEEDS" | tee "${OUTPUT}.speeds.sha256"
```

- [ ] **Step 3: Verify the script parses (dry syntax check)**

Run: `bash -n refresh_graph.sh`
Expected: no output (syntax OK).

- [ ] **Step 4: Commit**

```bash
git add speeds.json refresh_graph.sh
git commit -m "build: refresh_graph.sh reads --speeds and records its hash"
```

### Task B7: Rebuild, tune, gate, and swap (manual procedure)

**Files:** regenerates `graph.bin` (gitignored); produces `graph.time.bin` as a staging artifact.

- [ ] **Step 1: Build the time-metric graph to a SIDE file**

```bash
go build -o bin/map-router-preprocess ./cmd/preprocess
bin/map-router-preprocess --input malaysia-singapore-brunei-latest.osm.pbf \
  --output graph.time.bin --kl --speeds speeds.json
```
Expected: completes; logs "version 3"-compatible build and the speeds path.

- [ ] **Step 2: Serve the side graph and benchmark**

```bash
go build -o bin/map-router-server ./cmd/server
bin/map-router-server --graph graph.time.bin --port 18087 &
sleep 3 && curl -s http://localhost:18087/api/v1/health
python3 datasets/elevete_route_cache/compare_google.py --url http://localhost:18087 --buffer 25
```

- [ ] **Step 3: Evaluate the hard gates**

From the benchmark output, confirm:
- VALIDITY: 44/44.
- `02c22ee` < 1.3× and no route > 1.5× (snapping still holds under the time metric).
- median |distance error| ≤ 6%.
- median Hausdorff ≤ ~1.0 km (≈ half of 1.9 km).
- (soft, report only) symmetric overlap, median signed distance error.

- [ ] **Step 4: Driver non-regression on the 262 real pairs**

Run the 262 `custom` pairs through both the OLD `graph.bin` and the NEW `graph.time.bin` and compare per-route distance. (Use the same harness pattern against `routes.jsonl` rows where `engine == "custom"`, querying each engine.) Confirm **no route becomes materially shorter (< −2%)** than today's production graph; record the total distance delta.

- [ ] **Step 5: Tune if a gate is missed**

If a hard gate fails, hand-adjust 1–2 class ratios in `speeds.json` (keep within real-world bounds; preserve class spread — do not collapse ratios toward equality), then repeat Steps 1–4. Expect only a handful of iterations. Record the final `speeds.json`.

- [ ] **Step 6: Atomic swap into production**

```bash
kill %1 2>/dev/null
cp graph.bin graph.bin.bak           # keep a rollback copy
mv graph.time.bin graph.bin
# Restart the production server (./run_server.sh) so it loads the v3 graph.
```
The server binary must be the new build (it understands v3); an old server will refuse the v3 graph (version mismatch), which is the intended lockstep guard.

- [ ] **Step 7: Final commit (code only; graph.bin is gitignored)**

```bash
git add -A
git commit -m "feat: switch routing to class-based travel-time metric (graph v3)"
```

---

## Self-Review

**Spec coverage:**
- Direction-aware seeding → A1. Geometry/distance from snap points → A2. Multi-candidate snapping + access penalty → A3/A4. Multi-seed exactness + one-way + stub tests → A1/A4. Change-A gate → A5.
- Speed model + maxspeed (MY:/RM:, mph, none, links) → B1. Time weights → B2. Versioned `--speeds` input → B3. Binary v3 → B4. Internal-only duration → B5. `refresh_graph.sh` + hash → B6. Rebuild/tune/gate/swap incl. driver non-regression → B7.
- Deferred (Phase 2) per spec: exposed `duration_seconds`, turn penalties, traffic, coverage expansion, pocket healing — intentionally absent.
- Harness corrections (drop Fréchet, symmetric overlap, projection note) are spec items on `compare_google.py`; add them opportunistically in A5/B2 if touching the harness, otherwise track as a follow-up — they do not block the gates (the gates use validity, ratio, |dist err|, Hausdorff).

**Type consistency:** `SnapResult` fields (`EdgeIdx`, `NodeU`, `NodeV`, `Ratio`, `Dist`) used consistently. `seedFwdMin`/`seedBwdMin` on `*QueryState`. `SpeedTable` fields (`ClassKmh`, `ZoneKmh`, `LinkFactor`, `Fallback`) consistent across B1/B3. `computeWeightMs(lengthMeters, speedKmh)` and `SpeedKmh(tags)` signatures stable. `polylineLengthMeters`, `snapPointForCandidates`, `accessPenalty` defined once in engine.go.

**Placeholder scan:** every step contains complete code or exact commands; no TBD/TODO/"add error handling" placeholders.

**Known assumptions to verify during execution:**
- `t.Context()` requires Go 1.24+ (repo is on 1.26 ✓); if a worker hits issues, use `context.Background()`.
- If `pkg/osm/parser_test.go` has existing distance-based `Weight` assertions, B2 Step 4 updates them.
- A1's tests use only existing symbols, so they compile and fail on the *assertion* (current `seedForward` seeds both endpoints) — no fake-stub step needed.
