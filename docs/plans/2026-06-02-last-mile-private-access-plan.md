---
title: "feat: last-mile private-road access (Phase 2a)"
type: feat
status: partial — leak-aware filter shipped (T1–T4); fixes ~2-4 gated routes; connector deferred (see design doc Outcome)
date: 2026-06-02
brainstorm: docs/brainstorms/2026-06-02-last-mile-private-access-design.md
---

# Last-Mile Private-Road Access (Phase 2a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let map_router reach delivery origins/destinations on gated/private (`access=private`/`permit`/`residents`) roads by inlining the *cul-de-sac* ones into the normal routable graph — without ever using a private road as a through-shortcut.

**Architecture:** Un-drop gated private car roads in the parser (flagged `restricted`); carry the flag through `graph.Build`; a new build-time pass `FilterBridgingRestricted` keeps only restricted clusters that touch the public network at ≤1 node (provably non-leaking cul-de-sacs) and drops potential cut-throughs; everything downstream (`LargestComponent` → `FilterToComponent` → `ch.Contract` → binary) is **unchanged** — inlined edges are ordinary edges. No CH/routing/binary-format changes.

**Tech Stack:** Go 1.26, `paulmach/osm` PBF parser, CSR graph + Contraction Hierarchies, Python benchmark harness (`datasets/elevete_route_cache/compare_google.py`).

---

## Background (read first)
- Phase 1 (branch `feat/time-based-routing-metric`, this branch is stacked on it) gives travel-time weights + robust snapping. 38/137 Google routes have a gated endpoint snapping >50 m away; `a29476` is 1.87× because its destination is in a gated estate 362 m from the nearest public road.
- Current parser drop rule (`pkg/osm/parser.go` `isCarAccessible`, lines 64–85): drops non-car highways, `area=yes`, `access=no`, `access=private`, `motor_vehicle=no`. **`access=destination`/`customers` are NOT dropped today** — leave them public.
- The graph build pipeline (`cmd/preprocess/main.go:79–96`): `graph.Build` → `graph.LargestComponent` → `graph.FilterToComponent` → `ch.Contract` → `graph.WriteBinary`.
- `pkg/graph/component.go` already provides a `UnionFind` (path-halving + union-by-rank) we reuse.

## File Structure
- Modify `pkg/osm/parser.go` — add `classifyAccess`; `RawEdge.Restricted`, `wayInfo.Restricted`; emit restricted edges.
- Modify `pkg/osm/parser_test.go` — update one `TestIsCarAccessible` case; add `TestClassifyAccess`.
- Modify `pkg/graph/graph.go` — add `Graph.EdgeRestricted []bool` (transient, not serialized).
- Modify `pkg/graph/builder.go` — carry the restricted flag into `EdgeRestricted`.
- Create `pkg/graph/restricted.go` — `FilterBridgingRestricted`.
- Create `pkg/graph/restricted_test.go` — cul-de-sac kept / bridge dropped / no-leakage.
- Modify `cmd/preprocess/main.go` — call `FilterBridgingRestricted` after `Build`, log counts.

---

### Task 1: Access classification in the parser

**Files:**
- Modify: `pkg/osm/parser.go`
- Test: `pkg/osm/parser_test.go`

- [ ] **Step 1: Write the failing test** — append to `pkg/osm/parser_test.go`:

```go
func TestClassifyAccess(t *testing.T) {
	cases := []struct {
		name            string
		tags            osm.Tags
		wantKeep        bool
		wantRestricted  bool
	}{
		{"plain residential", osm.Tags{{Key: "highway", Value: "residential"}}, true, false},
		{"access=private gated", osm.Tags{{Key: "highway", Value: "residential"}, {Key: "access", Value: "private"}}, true, true},
		{"access=permit gated", osm.Tags{{Key: "highway", Value: "residential"}, {Key: "access", Value: "permit"}}, true, true},
		{"access=residents gated", osm.Tags{{Key: "highway", Value: "service"}, {Key: "access", Value: "residents"}}, true, true},
		{"access=private + motor_vehicle=no (access governs)", osm.Tags{{Key: "highway", Value: "residential"}, {Key: "access", Value: "private"}, {Key: "motor_vehicle", Value: "no"}}, true, true},
		{"access=destination stays public", osm.Tags{{Key: "highway", Value: "tertiary"}, {Key: "access", Value: "destination"}}, true, false},
		{"access=customers stays public", osm.Tags{{Key: "highway", Value: "service"}, {Key: "access", Value: "customers"}}, true, false},
		{"access=no dropped", osm.Tags{{Key: "highway", Value: "residential"}, {Key: "access", Value: "no"}}, false, false},
		{"plain motor_vehicle=no dropped", osm.Tags{{Key: "highway", Value: "residential"}, {Key: "motor_vehicle", Value: "no"}}, false, false},
		{"motor_vehicle=private restricted", osm.Tags{{Key: "highway", Value: "service"}, {Key: "motor_vehicle", Value: "private"}}, true, true},
		{"footway dropped", osm.Tags{{Key: "highway", Value: "footway"}}, false, false},
		{"area=yes dropped", osm.Tags{{Key: "highway", Value: "service"}, {Key: "area", Value: "yes"}}, false, false},
		{"no highway dropped", osm.Tags{{Key: "name", Value: "X"}}, false, false},
	}
	for _, c := range cases {
		keep, restricted := classifyAccess(c.tags)
		if keep != c.wantKeep || restricted != c.wantRestricted {
			t.Errorf("%s: classifyAccess = (%v,%v), want (%v,%v)", c.name, keep, restricted, c.wantKeep, c.wantRestricted)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/osm/ -run TestClassifyAccess -v`
Expected: FAIL — `classifyAccess` undefined.

- [ ] **Step 3: Implement `classifyAccess` and wire it in** — in `pkg/osm/parser.go`, replace the `isCarAccessible` function (lines 63–85) with:

```go
// classifyAccess decides whether a car-class way is kept, and if kept whether it
// is "restricted" (gated/private — usable for last-mile access, inlined later only
// if it forms a cul-de-sac). access governs over motor_vehicle. access=destination
// and access=customers stay PUBLIC (they route normally today).
func classifyAccess(tags osm.Tags) (keep, restricted bool) {
	hw := tags.Find("highway")
	if !carHighways[hw] || tags.Find("area") == "yes" {
		return false, false
	}
	switch tags.Find("access") {
	case "no":
		return false, false
	case "private", "permit", "residents":
		return true, true
	}
	switch tags.Find("motor_vehicle") {
	case "no":
		return false, false
	case "private", "destination", "customers":
		return true, true
	}
	return true, false
}

// isCarAccessible reports whether a way is kept for car routing (ignoring the
// restricted distinction). Thin wrapper over classifyAccess.
func isCarAccessible(tags osm.Tags) bool {
	keep, _ := classifyAccess(tags)
	return keep
}
```

Add `Restricted bool` to `wayInfo` (after `SpeedKmh float64`):

```go
type wayInfo struct {
	NodeIDs    []osm.NodeID
	Forward    bool
	Backward   bool
	SpeedKmh   float64
	Restricted bool
}
```

In Pass 1, replace the keep check + the `ways = append(...)` (parser.go ~179–203):

```go
		keep, restricted := classifyAccess(w.Tags)
		if !keep {
			continue
		}

		if len(w.Nodes) < 2 {
			continue
		}

		fwd, bwd := directionFlags(w.Tags)
		if !fwd && !bwd {
			continue
		}

		nodeIDs := make([]osm.NodeID, len(w.Nodes))
		for i, wn := range w.Nodes {
			nodeIDs[i] = wn.ID
			referencedNodes[wn.ID] = struct{}{}
		}

		ways = append(ways, wayInfo{
			NodeIDs:    nodeIDs,
			Forward:    fwd,
			Backward:   bwd,
			SpeedKmh:   opt.Speeds.SpeedKmh(w.Tags),
			Restricted: restricted,
		})
```

Add `Restricted bool` to `RawEdge` (after `ShapeLons`):

```go
type RawEdge struct {
	FromNodeID osm.NodeID
	ToNodeID   osm.NodeID
	Weight     uint32    // travel time in milliseconds
	ShapeLats  []float64 // intermediate shape node latitudes (excluding from/to)
	ShapeLons  []float64 // intermediate shape node longitudes (excluding from/to)
	Restricted bool      // gated/private (access=private/permit/residents); last-mile only
}
```

In the edge-building loop (parser.go ~276–289), set `Restricted: w.Restricted` on BOTH RawEdge literals:

```go
			if w.Forward {
				edges = append(edges, RawEdge{
					FromNodeID: fromID,
					ToNodeID:   toID,
					Weight:     weight,
					Restricted: w.Restricted,
				})
			}
			if w.Backward {
				edges = append(edges, RawEdge{
					FromNodeID: toID,
					ToNodeID:   fromID,
					Weight:     weight,
					Restricted: w.Restricted,
				})
			}
```

- [ ] **Step 4: Update the `TestIsCarAccessible` "private access" case** — in `pkg/osm/parser_test.go`, the case at lines 36–43 currently expects `want: false`; private is now kept. Change that case's `want` to `true`:

```go
		{
			name: "private access (now kept as restricted)",
			tags: osm.Tags{
				{Key: "highway", Value: "residential"},
				{Key: "access", Value: "private"},
			},
			want: true,
		},
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/osm/ -v`
Expected: PASS — `TestClassifyAccess`, `TestIsCarAccessible`, `TestDirectionFlags`, `TestEdgeWeightIsTravelTime`, `TestSpeedKmh`, `TestLoadSpeedTable`.

- [ ] **Step 6: Commit**

```bash
git add pkg/osm/parser.go pkg/osm/parser_test.go
git commit -m "feat(osm): classify gated private roads as restricted (kept, not dropped)"
```

---

### Task 2: Carry the restricted flag through `graph.Build`

**Files:**
- Modify: `pkg/graph/graph.go`
- Modify: `pkg/graph/builder.go`
- Test: `pkg/graph/builder_test.go`

- [ ] **Step 1: Write the failing test** — append to `pkg/graph/builder_test.go`:

```go
func TestBuildCarriesRestrictedFlag(t *testing.T) {
	pr := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 100, Restricted: false},
			{FromNodeID: 2, ToNodeID: 3, Weight: 100, Restricted: true},
		},
		NodeLat: map[osm.NodeID]float64{1: 1.30, 2: 1.30, 3: 1.30},
		NodeLon: map[osm.NodeID]float64{1: 103.80, 2: 103.81, 3: 103.82},
	}
	g := Build(pr)
	if uint32(len(g.EdgeRestricted)) != g.NumEdges {
		t.Fatalf("EdgeRestricted len %d != NumEdges %d", len(g.EdgeRestricted), g.NumEdges)
	}
	// The 2->3 edge must be flagged restricted; the 1->2 edge must not.
	for u := uint32(0); u < g.NumNodes; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			from, to := u, g.Head[e]
			isRestricted := g.EdgeRestricted[e]
			if g.NodeLon[from] == 103.81 && g.NodeLon[to] == 103.82 && !isRestricted {
				t.Error("edge 2->3 should be restricted")
			}
			if g.NodeLon[from] == 103.80 && g.NodeLon[to] == 103.81 && isRestricted {
				t.Error("edge 1->2 should not be restricted")
			}
		}
	}
}
```

(`builder_test.go` is `package graph` and already imports `osm` and `osmparser`; confirm those imports exist, add if missing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/graph/ -run TestBuildCarriesRestrictedFlag -v`
Expected: FAIL — `g.EdgeRestricted` undefined.

- [ ] **Step 3: Add the field and populate it**

In `pkg/graph/graph.go`, add to the `Graph` struct (after `Weight []uint32`):

```go
	// EdgeRestricted[i] flags edge i as gated/private. Populated by Build and
	// consumed by FilterBridgingRestricted at preprocess time; NOT serialized
	// (nil after a binary load — the server treats all edges as normal).
	EdgeRestricted []bool // len: NumEdges (build-time only)
```

In `pkg/graph/builder.go`, add `restricted` to the `compactEdge` struct:

```go
	type compactEdge struct {
		from      uint32
		to        uint32
		weight    uint32
		shapeLats []float64
		shapeLons []float64
		restricted bool
	}
```

Populate it where `compact[i]` is built (in the `for i, e := range edges` loop):

```go
		compact[i] = compactEdge{
			from:       nodeSet[e.FromNodeID],
			to:         nodeSet[e.ToNodeID],
			weight:     e.Weight,
			shapeLats:  e.ShapeLats,
			shapeLons:  e.ShapeLons,
			restricted: e.Restricted,
		}
```

Add the `edgeRestricted` array next to `weight` in the CSR section:

```go
	head := make([]uint32, numEdges)
	weight := make([]uint32, numEdges)
	edgeRestricted := make([]bool, numEdges)
```

In the `for i, e := range compact` placement loop, set it alongside `weight[i]`:

```go
		head[i] = e.to
		weight[i] = e.weight
		edgeRestricted[i] = e.restricted
```

Add `EdgeRestricted: edgeRestricted` to the returned `&Graph{...}`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/graph/ -run TestBuildCarriesRestrictedFlag -v && go test ./pkg/graph/`
Expected: PASS (existing graph tests unaffected — the new field defaults appropriately).

- [ ] **Step 5: Commit**

```bash
git add pkg/graph/graph.go pkg/graph/builder.go pkg/graph/builder_test.go
git commit -m "feat(graph): carry per-edge restricted flag through Build (build-time only)"
```

---

### Task 3: `FilterBridgingRestricted` — inline cul-de-sacs, drop bridges

**Files:**
- Create: `pkg/graph/restricted.go`
- Test: `pkg/graph/restricted_test.go`

- [ ] **Step 1: Write the failing tests** — create `pkg/graph/restricted_test.go`:

```go
package graph

import (
	"testing"

	"github.com/paulmach/osm"

	osmparser "github.com/azybler/map_router/pkg/osm"
)

// hasEdge reports whether a directed edge between the nodes at the given lons exists.
func hasEdgeByLon(g *Graph, fromLon, toLon float64) bool {
	for u := uint32(0); u < g.NumNodes; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			if g.NodeLon[u] == fromLon && g.NodeLon[g.Head[e]] == toLon {
				return true
			}
		}
	}
	return false
}

// culDeSacParse: public chain A(.80)<->B(.81)<->C(.82), plus a restricted spur
// B<->D(.815,*north*). D hangs off the public network only at B (1 public touch).
func culDeSacParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 100}, {FromNodeID: 2, ToNodeID: 1, Weight: 100},
			{FromNodeID: 2, ToNodeID: 3, Weight: 100}, {FromNodeID: 3, ToNodeID: 2, Weight: 100},
			{FromNodeID: 2, ToNodeID: 4, Weight: 100, Restricted: true}, {FromNodeID: 4, ToNodeID: 2, Weight: 100, Restricted: true},
		},
		NodeLat: map[osm.NodeID]float64{1: 1.300, 2: 1.300, 3: 1.300, 4: 1.301},
		NodeLon: map[osm.NodeID]float64{1: 103.80, 2: 103.81, 3: 103.82, 4: 103.815},
	}
}

// bridgeParse: public A(.80) and C(.82) connected by a long public path via B(.81),
// PLUS a short restricted edge A<->C directly (touches public at TWO nodes A and C).
func bridgeParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 100}, {FromNodeID: 2, ToNodeID: 1, Weight: 100},
			{FromNodeID: 2, ToNodeID: 3, Weight: 100}, {FromNodeID: 3, ToNodeID: 2, Weight: 100},
			{FromNodeID: 1, ToNodeID: 3, Weight: 10, Restricted: true}, {FromNodeID: 3, ToNodeID: 1, Weight: 10, Restricted: true},
		},
		NodeLat: map[osm.NodeID]float64{1: 1.300, 2: 1.300, 3: 1.300},
		NodeLon: map[osm.NodeID]float64{1: 103.80, 2: 103.81, 3: 103.82},
	}
}

func TestFilterKeepsCulDeSac(t *testing.T) {
	g := FilterBridgingRestricted(Build(culDeSacParse()))
	if !hasEdgeByLon(g, 103.81, 103.815) {
		t.Error("cul-de-sac spur B->D should be inlined (kept)")
	}
	if g.EdgeRestricted != nil {
		t.Error("output graph must not carry restricted flags (all surviving edges are normal)")
	}
}

func TestFilterDropsBridge(t *testing.T) {
	g := FilterBridgingRestricted(Build(bridgeParse()))
	// The restricted A<->C cut-through (touches public at 2 nodes) must be removed.
	if hasEdgeByLon(g, 103.80, 103.82) || hasEdgeByLon(g, 103.82, 103.80) {
		t.Error("bridging restricted edge A<->C should be dropped")
	}
	// The public chain must remain intact.
	if !hasEdgeByLon(g, 103.80, 103.81) || !hasEdgeByLon(g, 103.81, 103.82) {
		t.Error("public edges must be preserved")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/graph/ -run 'TestFilter' -v`
Expected: FAIL — `FilterBridgingRestricted` undefined.

- [ ] **Step 3: Implement `FilterBridgingRestricted`** — create `pkg/graph/restricted.go`:

```go
package graph

// FilterBridgingRestricted returns a copy of g in which restricted edges are kept
// ONLY when their restricted cluster touches the public network at ≤1 node (a
// cul-de-sac, which can never be a through-shortcut). Restricted clusters that
// touch the public network at ≥2 nodes (potential private cut-throughs) have their
// restricted edges dropped. Public edges are always kept. The returned graph carries
// no restricted flags — every surviving edge is an ordinary edge.
//
// If g.EdgeRestricted is nil (no restricted edges), g is returned unchanged.
func FilterBridgingRestricted(g *Graph) *Graph {
	if g.EdgeRestricted == nil || g.NumEdges == 0 {
		return g
	}
	n := g.NumNodes

	// Mark which nodes are "public" (have ≥1 public incident edge, out or in) and
	// which are incident to a restricted edge.
	isPublic := make([]bool, n)
	inRestricted := make([]bool, n)
	for u := uint32(0); u < n; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			v := g.Head[e]
			if g.EdgeRestricted[e] {
				inRestricted[u], inRestricted[v] = true, true
			} else {
				isPublic[u], isPublic[v] = true, true
			}
		}
	}

	// Group nodes into restricted clusters via union-find over restricted edges.
	uf := NewUnionFind(n)
	for u := uint32(0); u < n; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			if g.EdgeRestricted[e] {
				uf.Union(u, g.Head[e])
			}
		}
	}

	// Count distinct public-touch nodes per restricted cluster.
	publicTouch := make(map[uint32]map[uint32]struct{})
	for u := uint32(0); u < n; u++ {
		if inRestricted[u] && isPublic[u] {
			root := uf.Find(u)
			set := publicTouch[root]
			if set == nil {
				set = make(map[uint32]struct{})
				publicTouch[root] = set
			}
			set[u] = struct{}{}
		}
	}

	keepRestrictedCluster := func(node uint32) bool {
		return len(publicTouch[uf.Find(node)]) <= 1
	}

	// Rebuild CSR keeping all public edges + restricted edges in cul-de-sac clusters.
	firstOut := make([]uint32, n+1)
	var head, weight []uint32
	var geoFirstOut []uint32
	var geoShapeLat, geoShapeLon []float64
	hasGeo := g.GeoFirstOut != nil
	if hasGeo {
		geoFirstOut = make([]uint32, 0, g.NumEdges+1)
	}

	// First pass: count survivors per node.
	survive := make([]bool, g.NumEdges)
	for u := uint32(0); u < n; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			if !g.EdgeRestricted[e] || keepRestrictedCluster(u) {
				survive[e] = true
				firstOut[u+1]++
			}
		}
	}
	for i := uint32(1); i <= n; i++ {
		firstOut[i] += firstOut[i-1]
	}

	// Second pass: emit survivors in node order (same ordering FilterToComponent uses).
	for u := uint32(0); u < n; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			if !survive[e] {
				continue
			}
			if hasGeo {
				geoFirstOut = append(geoFirstOut, uint32(len(geoShapeLat)))
				gs, ge := g.GeoFirstOut[e], g.GeoFirstOut[e+1]
				geoShapeLat = append(geoShapeLat, g.GeoShapeLat[gs:ge]...)
				geoShapeLon = append(geoShapeLon, g.GeoShapeLon[gs:ge]...)
			}
			head = append(head, g.Head[e])
			weight = append(weight, g.Weight[e])
		}
	}
	if hasGeo {
		geoFirstOut = append(geoFirstOut, uint32(len(geoShapeLat)))
	}

	return &Graph{
		NumNodes:    n,
		NumEdges:    uint32(len(head)),
		FirstOut:    firstOut,
		Head:        head,
		Weight:      weight,
		NodeLat:     g.NodeLat,
		NodeLon:     g.NodeLon,
		GeoFirstOut: geoFirstOut,
		GeoShapeLat: geoShapeLat,
		GeoShapeLon: geoShapeLon,
		// EdgeRestricted intentionally nil: survivors are ordinary edges.
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/graph/ -run 'TestFilter' -v && go test ./pkg/graph/ && go vet ./pkg/graph/`
Expected: PASS, no vet warnings.

- [ ] **Step 5: Commit**

```bash
git add pkg/graph/restricted.go pkg/graph/restricted_test.go
git commit -m "feat(graph): FilterBridgingRestricted — inline cul-de-sac private roads, drop bridges"
```

---

### Task 4: Wire `FilterBridgingRestricted` into preprocessing

**Files:**
- Modify: `cmd/preprocess/main.go`

- [ ] **Step 1: Insert the filter call** — in `cmd/preprocess/main.go`, after `g := graph.Build(parseResult)` and its log line (line 79), and BEFORE `componentNodes := graph.LargestComponent(g)` (line 84), insert:

```go
	// Inline cul-de-sac private/gated roads (access=private/permit/residents) so
	// gated delivery endpoints are reachable; drop restricted clusters that could
	// be through-shortcuts. Must run before component extraction + contraction.
	beforeEdges := g.NumEdges
	g = graph.FilterBridgingRestricted(g)
	log.Printf("Private-road filter: %d -> %d edges (dropped %d bridging-restricted)",
		beforeEdges, g.NumEdges, beforeEdges-g.NumEdges)
```

- [ ] **Step 2: Build to verify it compiles**

Run: `go build -o bin/map-router-preprocess ./cmd/preprocess && echo OK`
Expected: `OK`.

- [ ] **Step 3: Run the whole repo test suite + vet**

Run: `go test ./... -timeout 120s && go vet ./...`
Expected: PASS, clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/preprocess/main.go
git commit -m "feat(preprocess): inline cul-de-sac private roads before contraction"
```

---

### Task 5: Rebuild, validate against Google, and verify no leakage (controller-run)

**Files:** none modified — regenerates `graph.time.bin` (gitignored) and runs the benchmark.

- [ ] **Step 1: Rebuild the graph with the new pipeline**

```bash
go build -o bin/map-router-preprocess ./cmd/preprocess
bin/map-router-preprocess --input malaysia-singapore-brunei-latest.osm.pbf \
  --output graph.time.bin --kl --speeds speeds.json
```
Expected: completes; logs a "Private-road filter: N -> M edges" line with a non-zero inline count.

- [ ] **Step 2: Serve and run the 137-route Google comparison**

```bash
go build -o bin/map-router-server ./cmd/server
bin/map-router-server --graph graph.time.bin --port 18097 &
sleep 3 && curl -s http://localhost:18097/api/v1/health
python3 datasets/elevete_route_cache/compare_google.py --url http://localhost:18097 --buffer 25
```

- [ ] **Step 3: Verify the gated-improvement gate**

Confirm in the output / comparison.csv:
- VALIDITY 137/137.
- The single-gate gated endpoints' snap displacement drops (the >50 m count falls); `a29476` ratio < 1.5×.
- Median |distance error| / overlap on all 137 is **no worse** than Phase 1 (≈5.8% / 0.67).

- [ ] **Step 4: Verify no through-leakage (the safety gate)**

Route the 99 non-gated Google + 262 `custom` pairs against BOTH the Phase-1 graph (build it from the parent branch's `speeds.json` to a side file, or reuse a saved Phase-1 `graph.time.bin`) and the new graph; for each non-gated pair assert `total_distance_meters` changes by **< 0.5%** (equal-cost tie-break jitter only — a leak would shorten a route more). Any route exceeding the tolerance: inspect its geometry for a private-road segment and treat as a leak to debug. (The structural cul-de-sac guarantee from Task 3's `TestFilterDropsBridge` is the primary proof; this is the empirical backstop.)

- [ ] **Step 5: Stop the server and checkpoint**

```bash
kill %1 2>/dev/null
git commit -m "test: Phase-2a benchmark — gated endpoints reachable, no leakage" --allow-empty
```

---

## Self-Review

**Spec coverage:** access classification → Task 1; restricted flag through Build → Task 2; cul-de-sac inline / bridge drop (`FilterBridgingRestricted`) → Task 3; preprocess wiring + logging → Task 4; CH/routing/binary unchanged → (no task, by design); validation incl. the cost/leakage gate → Task 5. Out-of-scope items (node `barrier=gate`, `motorcar`/`vehicle`, ≥2-gate cut-throughs) intentionally have no task.

**Placeholder scan:** every step has complete code or exact commands; no TBD/TODO.

**Type consistency:** `classifyAccess(tags) (keep, restricted bool)`, `RawEdge.Restricted`, `wayInfo.Restricted`, `Graph.EdgeRestricted []bool`, `compactEdge.restricted`, `FilterBridgingRestricted(*Graph) *Graph` are used consistently across tasks. `UnionFind`/`NewUnionFind`/`Find`/`Union` match `component.go`.

**Known execution notes:**
- `FilterBridgingRestricted` reuses the existing `UnionFind` in `component.go` (same package) — no import needed.
- The output graph sets `EdgeRestricted = nil` deliberately; downstream (`LargestComponent`, `FilterToComponent`, `ch.Contract`, `WriteBinary`) never reads it, so the binary format is unchanged (still v3).
- Task 5 Step 4 needs a Phase-1 reference graph; if not saved, rebuild one from the parent branch before swapping `speeds.json`/code.
