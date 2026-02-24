---
title: "feat: Build routing service with Contraction Hierarchies"
type: feat
status: completed
date: 2026-02-25
deepened: 2026-02-25
brainstorm: docs/brainstorms/2026-02-24-routing-service-brainstorm.md
---

# Build Routing Service with Contraction Hierarchies

## Enhancement Summary

**Deepened on:** 2026-02-25
**Agents used:** Performance Oracle, Security Sentinel, Architecture Strategist, Code Simplicity Reviewer, Pattern Recognition Specialist, Best Practices Researcher, Context7 (tidwall/rtree, go-chi/chi)

### Key Improvements
1. **Binary I/O:** Replace `encoding/binary.Read/Write` with `unsafe.Slice` for 25-100x faster serialization/deserialization
2. **Stall-on-demand:** Add CH query optimization that reduces settled nodes by 2-5x (query time ~1ms instead of ~3-5ms)
3. **Security hardening:** Validate binary file header against file size, add CRC32 checksum, add concurrency limiter, restrict CORS
4. **Architecture:** Define `Router` interface for testability, thread `context.Context` throughout, define typed domain errors
5. **OSM memory:** Filter node coordinates during Pass 1 to only road-referenced nodes (~40MB instead of ~480MB)
6. **Binary format fix:** Split `NumTotalEdges` into `NumFwdEdges` + `NumBwdEdges` (forward/backward may differ)
7. **Simplifications:** Use `net/http.ServeMux` (Go 1.22+) instead of chi; defer `sync.Pool` to post-MVP; drop `segment.id` from response

### Critical Issues Found
- `encoding/binary.Write` uses reflection internally — 10-30x slower than unsafe slice cast
- Binary header values not validated against file size — crafted file can cause OOM
- No rate limiting or concurrency throttling — unlimited concurrent queries can exhaust memory
- `NumTotalEdges` is ambiguous — forward and backward upward graphs may have different edge counts
- Missing `context.Context` propagation — cancelled HTTP requests waste server resources

---

## Overview

A Golang routing service that loads OpenStreetMap data for Singapore, builds an in-memory weighted directed graph in CSR format, preprocesses it with Contraction Hierarchies, and serves shortest-path queries via HTTP API. The system is split into two binaries: a **preprocessor CLI** that converts `.osm.pbf` → optimized binary, and a **server** that loads the binary and serves route queries.

## Problem Statement / Motivation

We need a self-hosted routing engine that computes shortest driving routes in Singapore. Existing options (OSRM, Valhalla) are large C++ projects. We want a clean Go implementation focused on correctness and performance, using the well-established Contraction Hierarchies algorithm.

## Project Structure

```
map_router/
├── cmd/
│   ├── preprocess/
│   │   └── main.go              # CLI: .osm.pbf → graph.bin
│   └── server/
│       └── main.go              # HTTP server
├── pkg/
│   ├── osm/
│   │   ├── parser.go            # Two-pass PBF reader + highway filter + oneway logic
│   ├── graph/
│   │   ├── graph.go             # CSR graph types
│   │   ├── builder.go           # Build CSR from parsed edges
│   │   ├── component.go         # Connected component extraction
│   │   └── binary.go            # Serialize/deserialize binary format
│   ├── ch/
│   │   ├── contractor.go        # CH preprocessing (ordering + contraction + overlay build)
│   │   └── witness.go           # Witness search during contraction
│   ├── routing/
│   │   ├── engine.go            # Query engine (snap + search + unpack)
│   │   ├── dijkstra.go          # Bidirectional CH Dijkstra
│   │   ├── snap.go              # R-tree nearest-segment snapping
│   │   └── unpack.go            # Shortcut edge unpacking
│   ├── geo/
│   │   └── haversine.go         # Haversine distance + point-to-segment
│   └── api/
│       ├── server.go            # stdlib net/http setup + lifecycle
│       ├── handlers.go          # Route, health, stats handlers
│       └── models.go            # Request/response JSON structs
├── go.mod
├── go.sum
├── Makefile
└── docs/
    ├── brainstorms/
    └── plans/
```

#### Research Insights — Project Structure

**Simplifications (Code Simplicity Reviewer):**
- Merge `filter.go` into `parser.go` — the highway filter and oneway logic are only used by the parser, not independently testable
- Merge `overlay.go` into `contractor.go` — overlay construction is the final step of contraction, not a separate concern
- Replace `go-chi/chi` with stdlib `net/http.ServeMux` (Go 1.22+ supports method-based routing: `mux.HandleFunc("POST /api/v1/route", h)`)
- `pkg/geo/` has a single file — consider inlining `Haversine()` and `PointToSegmentDist()` into `pkg/graph/` or `pkg/routing/` if no other consumers emerge

**Architecture (Architecture Strategist):**
- Define a `Router` interface in `pkg/api/` so handlers depend on an interface, not a concrete engine — enables testing handlers with a mock
- Define a shared `RawEdge` type used by both OSM parser output and graph builder input
- Define typed domain errors (`SnapError`, `NoRouteError`, `InvalidInputError`) in a shared `pkg/errors/` or within `pkg/routing/`

## Technical Approach

### Binary File Format Specification

The preprocessed graph file (`.graph.bin`) has a fixed layout:

```
[Header: 32 bytes]
  Bytes 0-7:    Magic "MPROUTER" (8 bytes ASCII)
  Bytes 8-11:   Version (uint32, currently 1)
  Bytes 12-15:  NumNodes (uint32)
  Bytes 16-19:  NumOrigEdges (uint32) — original edges in upward graphs
  Bytes 20-23:  NumShortcuts (uint32) — shortcut edges in upward graphs
  Bytes 24-27:  NumTotalEdges (uint32) — NumOrigEdges + NumShortcuts
  Bytes 28-31:  Reserved (uint32, 0)

[Node Data]
  NodeLat  [NumNodes]float64    — latitude per node
  NodeLon  [NumNodes]float64    — longitude per node
  Rank     [NumNodes]uint32     — CH contraction rank per node

[Forward Upward Graph (CSR)]
  FwdFirstOut [NumNodes+1]uint32
  FwdHead     [NumTotalEdges]uint32
  FwdWeight   [NumTotalEdges]uint32   — distance in millimeters (uint32)
  FwdMiddle   [NumTotalEdges]int32    — -1 for original edges, else contracted node ID

[Backward Upward Graph (CSR, reversed)]
  BwdFirstOut [NumNodes+1]uint32
  BwdHead     [NumTotalEdges]uint32
  BwdWeight   [NumTotalEdges]uint32
  BwdMiddle   [NumTotalEdges]int32

[Original Edge Geometry]
  GeoFirstOut [NumNodes+1]uint32      — CSR offsets into geometry node arrays
  GeoHead     [NumOrigEdges]uint32    — target node per original edge
  GeoShapeCount [NumOrigEdges]uint32  — number of intermediate shape nodes
  GeoShapeLat [TotalShapeNodes]float64 — shape node latitudes (flattened)
  GeoShapeLon [TotalShapeNodes]float64 — shape node longitudes (flattened)
```

All multi-byte values are **little-endian**. Edge weights are stored as `uint32` in **millimeters** (max ~4,294 km per edge, sufficient for Singapore). Node coordinates are `float64` for precision.

#### Research Insights — Binary File Format

**Critical Fix (Pattern Recognition Specialist):**
- Split `NumTotalEdges` into `NumFwdEdges` (uint32) + `NumBwdEdges` (uint32) — forward and backward upward graphs may have different edge counts after contraction. Using a single count is ambiguous and leads to buffer over/under-read. Use the reserved bytes 24-31 for these two fields.

**Security (Security Sentinel — Critical):**
- **Validate header against file size:** After reading header, compute expected file size from counts and compare to `os.Stat` result. Reject if mismatch. A crafted header with `NumNodes=0xFFFFFFFF` causes `make([]float64, 4B)` → 32GB allocation → OOM.
- **Hard caps:** Reject files with `NumNodes > 10_000_000` or `NumEdges > 50_000_000` (Singapore has ~500K nodes). This prevents crafted files from being used as a DoS vector.
- **CRC32 checksum:** Append a 4-byte CRC32 trailer after all arrays. Validate on load to detect corruption.
- Add a `Validate()` method called after deserialization that checks CSR invariants: `FirstOut` is monotonically non-decreasing, `FirstOut[NumNodes] == NumEdges`, all `Head` values `< NumNodes`.

**Performance (Performance Oracle — P0):**
- Replace `encoding/binary.Read/Write` with `unsafe.Slice` for 25-100x faster I/O:
  ```go
  // Write: reinterpret []uint32 as []byte
  func writeUint32Slice(w io.Writer, s []uint32) error {
      b := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*4)
      _, err := w.Write(b)
      return err
  }
  // Read: allocate []uint32, read directly into backing bytes
  func readUint32Slice(r io.Reader, n int) ([]uint32, error) {
      s := make([]uint32, n)
      b := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), n*4)
      _, err := io.ReadFull(r, b)
      return s, err
  }
  ```
- `encoding/binary.Write` uses `reflect.ValueOf` internally — the reflection overhead dominates for large slices
- Alternative: memory-mapped file with `syscall.Mmap` for zero-copy reads (defer to post-MVP)

### Highway Types (Car Routing)

Include these 14 `highway` tag values:

| Tag | Description |
|-----|-------------|
| `motorway` | Controlled-access highways (implied oneway) |
| `motorway_link` | Motorway ramps (implied oneway) |
| `trunk` | Most important non-motorway roads |
| `trunk_link` | Trunk connectors |
| `primary` | Major national roads |
| `primary_link` | Primary connectors |
| `secondary` | Roads connecting towns |
| `secondary_link` | Secondary connectors |
| `tertiary` | Roads connecting villages |
| `tertiary_link` | Tertiary connectors |
| `unclassified` | Minor through roads |
| `residential` | Housing area roads |
| `living_street` | Pedestrian-priority, low speed |
| `service` | Access roads (parking lots, alleys) |

**Additional filtering:** Skip ways tagged with `access=no`, `access=private`, or `motor_vehicle=no`.

#### Research Insights — Highway Types

**Best Practices (Best Practices Researcher):**
- OSRM and Valhalla also include `track` (grade1-2 only) for some profiles — deliberately excluded here for car-only routing in Singapore (no unpaved tracks)
- `service` roads can include driveways and parking aisles — consider filtering `service=driveway` and `service=parking_aisle` if they produce noisy routes
- Check for `area=yes` on highway ways and skip them — these represent pedestrian plazas tagged as highway but are not routable roads

### Oneway Handling

```
Priority order (later overrides earlier):
1. Default: bidirectional (forward=true, backward=true)
2. Implied oneway: highway=motorway|motorway_link or junction=roundabout → forward only
3. Explicit oneway tag:
   - oneway=yes|true|1  → forward only
   - oneway=-1|reverse   → backward only
   - oneway=no           → bidirectional
   - oneway=reversible   → skip way entirely (time-dependent, unsafe to route)
```

### Virtual Edge Injection (Snapping → Graph)

When a query point (lat, lng) is snapped to segment `(u, v)` at ratio `r`:

```
                 r
    u ----[===X=====]---- v
         d_u          d_v

    d_u = r * weight(u,v)         — distance from u to snap point
    d_v = (1-r) * weight(u,v)     — distance from snap point to v
```

**For the start point**, create virtual edges:
- If the segment has a forward edge (u→v): virtual_start → v with weight d_v
- If the segment has a backward edge (v→u): virtual_start → u with weight d_u

**For the end point**, create virtual edges:
- If the segment has a forward edge (u→v): u → virtual_end with weight d_u
- If the segment has a backward edge (v→u): v → virtual_end with weight d_v

These virtual nodes/edges are injected at query time, not stored in the graph. The Dijkstra initialization seeds the forward PQ with the start's connected nodes at the appropriate distances, and the backward PQ with the end's connected nodes. No actual node is added to the CSR — we just initialize the Dijkstra distances for the segment endpoints.

#### Research Insights — Virtual Edge Injection

**Edge Cases (Spec-Flow Analyzer):**
- **Same-segment shortcut:** If start and end snap to the same segment with compatible direction and `ratio_start < ratio_end` (for forward edge), return the sub-segment directly without running Dijkstra
- **Adjacent segments:** Start and end may snap to different segments sharing a node — Dijkstra handles this correctly but the partial-distance seeding must not double-count the shared node
- **One-way mismatch:** If the snapped segment is one-way and the snap point is "behind" the direction of travel, the user effectively has no forward edge from the start — seed only valid directions to avoid finding paths that would require illegal U-turns

### CH Termination Condition

The bidirectional CH Dijkstra terminates when **both** priority queues have minimum keys exceeding `mu` (the best known path distance found so far):

```
mu = infinity
meetNode = -1

while fwdPQ.Len() > 0 || bwdPQ.Len() > 0:
    // Attempt forward step
    if fwdPQ.Len() > 0 and fwdPQ.PeekDist() < mu:
        u = fwdPQ.Pop()
        if u.dist > distFwd[u.node]: continue  // stale
        if distBwd[u.node] < infinity:
            candidate = u.dist + distBwd[u.node]
            if candidate < mu: mu = candidate; meetNode = u.node
        relax upward edges from u in forward graph

    // Attempt backward step (symmetric)
    if bwdPQ.Len() > 0 and bwdPQ.PeekDist() < mu:
        u = bwdPQ.Pop()
        // ... symmetric logic ...

    // Terminate when both queues exceed mu
    fwdMin = fwdPQ.PeekDist() if fwdPQ.Len() > 0 else infinity
    bwdMin = bwdPQ.PeekDist() if bwdPQ.Len() > 0 else infinity
    if fwdMin >= mu and bwdMin >= mu: break
```

#### Research Insights — CH Termination & Stall-on-Demand

**Performance (Performance Oracle — P0, reduces settled nodes 2-5x):**

Add **stall-on-demand** optimization. Before expanding a node `u` in the forward search, check if any *downward* edge `(u, v)` with `rank[v] < rank[u]` provides a shorter path to `u`:

```
stall_on_demand(u, dist_fwd):
    for each downward edge (u, v) in original graph where rank[v] < rank[u]:
        if dist_fwd[v] + weight(v, u) < dist_fwd[u]:
            return true   // u is stalled — do NOT expand
    return false
```

If stalled, skip relaxing edges from `u` but still check the `distFwd[u] + distBwd[u]` meet condition. This dramatically reduces the search space for CH queries.

**Implementation note:** Stall-on-demand requires access to downward edges. During overlay construction, store the *full* forward and backward graphs (not just upward), OR keep a separate "downward edge" array. The simpler approach: during the CH query, iterate the *backward* upward graph's edges from `u` — those edges point to higher-rank nodes when traversed in reverse, giving us the downward edges we need.

**Best Practices (Best Practices Researcher):**
- The LdDl/ch Go implementation uses this exact stall-on-demand pattern and reports ~3x speedup
- Without stall-on-demand, typical Singapore queries settle ~5,000-10,000 nodes; with it, ~1,000-3,000

### Connected Components

Use **weakly connected components** (treat graph as undirected) via union-find. Keep only the largest component. Accept that some node pairs within the weakly-connected component may have no directed path — return HTTP 404 with `"error": "no_route_found"` in those cases.

#### Research Insights — Connected Components

**Best Practices (Best Practices Researcher):**
- Use union-find with **path compression + union by rank** for O(α(n)) amortized per operation:
  ```go
  type UnionFind struct {
      parent []uint32
      rank   []byte   // byte is sufficient — max rank ~30 for 500K nodes
  }
  func (uf *UnionFind) Find(x uint32) uint32 {
      for uf.parent[x] != x {
          uf.parent[x] = uf.parent[uf.parent[x]]  // path halving
          x = uf.parent[x]
      }
      return x
  }
  ```
- Use `[]byte` for rank (not `[]int`) — rank never exceeds ~30 for realistic graph sizes, saves 7 bytes per node

**Edge Case:**
- Ensure geometry (shape nodes) survives component filtering — when removing edges not in the largest component, also remove their associated geometry entries from `GeoShapeLat`/`GeoShapeLon`

### Maximum Snap Distance

**500 meters.** If the nearest road segment is farther than 500m from the query point, return HTTP 422:
```json
{"error": "point_too_far_from_road", "field": "start", "distance_meters": 1234.5}
```

### JSON Response Schema

**Request:**
```json
POST /api/v1/route
{
  "start": {"lat": 1.3521, "lng": 103.8198},
  "end": {"lat": 1.2905, "lng": 103.8520}
}
```

**Success Response (200):**
```json
{
  "total_distance_meters": 8521.3,
  "segments": [
    {
      "id": 102345,
      "distance_meters": 87.2,
      "geometry": [
        {"lat": 1.3521, "lng": 103.8198},
        {"lat": 1.3525, "lng": 103.8201},
        {"lat": 1.3530, "lng": 103.8205}
      ]
    }
  ]
}
```

The first segment's geometry starts at the snapped start point. The last segment's geometry ends at the snapped end point. Intermediate segments include all shape nodes from OSM for smooth rendering.

#### Research Insights — JSON Response

**Simplification (Code Simplicity Reviewer):**
- Drop `"id"` field from segments — internal edge indices are meaningless to consumers and leak implementation details. Use array position as implicit ordering.

**Error Responses:**
| Condition | HTTP Status | Error Code |
|-----------|-------------|------------|
| Invalid JSON body | 400 | `invalid_request` |
| Missing start/end | 400 | `missing_field` |
| Coordinates out of range | 400 | `invalid_coordinates` |
| Point too far from road | 422 | `point_too_far_from_road` |
| No route found | 404 | `no_route_found` |
| Internal error | 500 | `internal_error` |

### Concurrent Query Safety

Each query allocates its own priority queue and distance tracking. Use `sync.Pool` to reuse per-query state:

```go
var queryStatePool = sync.Pool{
    New: func() any {
        return &QueryState{
            DistFwd:  make([]uint32, numNodes),
            DistBwd:  make([]uint32, numNodes),
            Touched:  make([]uint32, 0, 1024),
            // ...
        }
    },
}
```

Between queries, reset only the touched entries (not the full 500K array):
```go
for _, node := range state.Touched {
    state.DistFwd[node] = math.MaxUint32
    state.DistBwd[node] = math.MaxUint32
}
state.Touched = state.Touched[:0]
```

#### Research Insights — Concurrent Query Safety

**Simplification (Code Simplicity Reviewer):**
- **Defer `sync.Pool` to post-MVP.** For the first implementation, just allocate fresh `QueryState` per request. Profile first — Go's GC handles short-lived allocations well. Only add pooling if benchmarks show GC pressure is the bottleneck.

**Security (Security Sentinel — High):**
- Add a **concurrency limiter** (semaphore) to bound simultaneous queries. Each query holds ~4MB of state (2 × 500K × uint32 distance arrays). With unbounded concurrency, 100 simultaneous queries = 400MB just for distance arrays.
  ```go
  sem := make(chan struct{}, runtime.NumCPU()*2) // e.g., 16 on 8-core
  // In handler:
  sem <- struct{}{}
  defer func() { <-sem }()
  ```
- Return HTTP 503 with `Retry-After` header when the semaphore is full

**Performance (Performance Oracle):**
- Use a **custom concrete-typed min-heap** instead of `container/heap` with interface boxing:
  ```go
  type PQItem struct {
      Node uint32
      Dist uint32
  }
  type MinHeap struct {
      items []PQItem
  }
  func (h *MinHeap) Push(node, dist uint32) { ... }
  func (h *MinHeap) Pop() PQItem { ... }
  ```
- `container/heap` requires interface{} boxing → allocation per push. A concrete-typed heap avoids this entirely.

## Implementation Phases

### Phase 1: Foundation — Graph Types + OSM Parsing

Build the data pipeline from `.osm.pbf` to an in-memory edge list.

**Tasks:**

- [x] Initialize Go module (`go mod init map_router`) and directory structure
  - `cmd/preprocess/main.go`, `cmd/server/main.go`
  - `pkg/osm/`, `pkg/graph/`, `pkg/ch/`, `pkg/routing/`, `pkg/geo/`, `pkg/api/`

- [x] Implement Haversine distance in `pkg/geo/haversine.go`
  - `func Haversine(lat1, lon1, lat2, lon2 float64) float64` → meters
  - `func PointToSegmentDist(pLat, pLon, aLat, aLon, bLat, bLon float64) (dist float64, ratio float64)` → perpendicular distance + projection ratio

- [x] Implement OSM parser in `pkg/osm/parser.go`
  - Two-pass reader using `paulmach/osm` (`osmpbf.Scanner`)
  - Pass 1: collect node coordinates into `map[osm.NodeID][2]float64` (skip ways/relations)
  - Pass 2: iterate ways, apply highway filter + oneway logic, emit directed edges
  - Handle dangling node references (skip edge if either node is missing, log warning)
  - Handle degenerate ways (skip ways with < 2 nodes)

- [x] Implement highway filter in `pkg/osm/filter.go`
  - `carHighways` map with the 14 highway types
  - `IsCarAccessible(way) bool` — check highway tag + access/motor_vehicle restrictions
  - `DirectionFlags(way) (forward, backward bool)` — oneway logic per the spec above

- [x] Write tests for haversine, parser, and filter
  - Unit test haversine against known distances
  - Use a tiny .osm.pbf fixture (or XML) for parser tests

**Files:** `pkg/geo/haversine.go`, `pkg/geo/haversine_test.go`, `pkg/osm/parser.go`, `pkg/osm/parser_test.go`

#### Research Insights — Phase 1

**Memory Optimization (Performance Oracle — P0):**
- **Filter node coordinates in Pass 1:** Singapore OSM has ~6M total nodes but only ~500K are referenced by highway ways. Storing all 6M nodes' coordinates costs ~480MB (`6M × 2 × float64 × 8B`). Instead:
  - Pass 1a: Scan ways first, collect all referenced node IDs into a `map[osm.NodeID]struct{}`
  - Pass 1b: Scan nodes, only store coordinates for referenced IDs → ~40MB
  - This requires a third pass over the PBF, but `paulmach/osm`'s Scanner is fast and the memory savings are dramatic
- Alternative: use the two-pass approach but with Pass 1 = ways (collect node IDs + highway info), Pass 2 = nodes (filter to collected IDs) + edge emission

**Performance (Performance Oracle):**
- Use **equirectangular approximation** instead of Haversine for snap-distance comparisons (candidate filtering):
  ```go
  func EquirectangularDist(lat1, lon1, lat2, lon2 float64) float64 {
      x := (lon2 - lon1) * math.Cos((lat1+lat2)/2*math.Pi/180)
      y := lat2 - lat1
      return math.Sqrt(x*x+y*y) * 6371000 * math.Pi / 180
  }
  ```
  ~3x faster than Haversine; accurate to <0.1% at Singapore's latitude. Use Haversine only for final edge weight calculation.

**Best Practices (Best Practices Researcher — paulmach/osm):**
- Use `osmpbf.Scanner` with `osm.FilterWay` to pre-filter during PBF scanning — reduces allocations
- Call `scanner.Close()` in defer to prevent resource leaks
- The scanner streams objects — no need to buffer the entire PBF in memory

### Phase 2: Graph Construction + Connected Components

Convert the parsed edge list into a CSR graph and filter to the largest component.

**Tasks:**

- [x] Define CSR graph types in `pkg/graph/graph.go`
  ```go
  type Graph struct {
      NumNodes    uint32
      NumEdges    uint32
      FirstOut    []uint32    // len: NumNodes + 1
      Head        []uint32    // len: NumEdges
      Weight      []uint32    // len: NumEdges (millimeters)
      NodeLat     []float64   // len: NumNodes
      NodeLon     []float64   // len: NumNodes
  }
  ```

- [x] Implement graph builder in `pkg/graph/builder.go`
  - Accept parsed edges (fromOSMID, toOSMID, distance, shape nodes)
  - Remap OSM node IDs to compact 0..N-1 indices
  - Sort edges by source node, build `FirstOut` via prefix sum
  - Store shape nodes (intermediate OSM nodes along each edge) for geometry

- [x] Implement connected components in `pkg/graph/component.go`
  - Union-find on undirected interpretation of the directed graph
  - `LargestComponent(g *Graph) []uint32` → returns node indices in largest component
  - `FilterToComponent(g *Graph, nodes []uint32) *Graph` → new graph with only those nodes

- [x] Write tests
  - Build a small graph manually, verify CSR structure
  - Test component extraction with disconnected subgraphs

**Files:** `pkg/graph/graph.go`, `pkg/graph/builder.go`, `pkg/graph/component.go`, `pkg/graph/builder_test.go`, `pkg/graph/component_test.go`

#### Research Insights — Phase 2

**Architecture (Architecture Strategist):**
- Define a shared `RawEdge` struct for the parser → builder interface:
  ```go
  type RawEdge struct {
      From, To  uint32      // compact node indices (after remapping)
      Weight    uint32      // millimeters
      ShapeLats []float64   // intermediate shape nodes
      ShapeLons []float64
  }
  ```
- Accept `context.Context` in the builder to allow cancellation of long-running preprocessing

**Edge Cases:**
- Geometry must survive component filtering — when `FilterToComponent` removes nodes, re-index edges and their shape node arrays accordingly
- Parallel edges (same source→target, different weight) can exist from OSM — keep the shortest one, or keep all and let CH handle it

### Phase 3: Contraction Hierarchies Preprocessing

Implement the CH algorithm: node ordering, contraction with witness search, shortcut creation.

**Tasks:**

- [x] Implement node ordering in `pkg/ch/contractor.go`
  - Priority function: `edgeDifference + contractedNeighbors + level`
  - Indexed min-heap using `container/heap` with `heap.Fix` for priority updates
  - Lazy update strategy: pop node, recompute priority, re-insert if no longer minimum

- [x] Implement witness search in `pkg/ch/witness.go`
  - Local Dijkstra from source neighbor, excluding contracted node
  - **Hop limit: 5** (increase to 10 in dense areas if needed)
  - Upper-bound pruning: stop when all settled nodes exceed `weight(v,u) + weight(u,w)`
  - Max settled nodes limit: 1000 as a safety bound
  - Returns `true` if a witness path ≤ shortcut weight exists

- [x] Implement contraction loop in `pkg/ch/contractor.go`
  - Process nodes in priority order
  - For each contracted node `u`, check all neighbor pairs `(v, w)`:
    - Run witness search; if no witness, create shortcut `v→w` with middle=`u`
  - Record rank (contraction order) per node
  - Track all shortcuts created

- [x] Build forward/backward upward overlay in `pkg/ch/overlay.go`
  - From original edges + shortcuts, split by rank comparison:
    - Forward: edges where `rank[source] < rank[target]`
    - Backward: reversed edges where `rank[source] < rank[target]`
  - Build two CSR structures: `FwdGraph` and `BwdGraph`
  - Include `Middle []int32` arrays (-1 for original edges)

- [x] Write tests
  - Small hand-crafted graph (5-10 nodes), verify shortcuts are correct
  - Verify that CH Dijkstra on the overlay gives same distances as plain Dijkstra on original graph

**Files:** `pkg/ch/contractor.go`, `pkg/ch/witness.go`, `pkg/ch/contractor_test.go`

#### Research Insights — Phase 3

**Performance (Performance Oracle — P0):**
- **Witness search settle limit: 500** (not 1000). On Singapore-scale graphs, 500 is sufficient and reduces preprocessing time. Add early target termination: if the witness search reaches the target node `w` with distance ≤ shortcut weight, stop immediately.
- **Lazy update only:** Drop `heap.Fix` mention — use lazy update strategy exclusively: pop node, recompute priority, re-insert if priority increased. This is simpler and avoids the indexed-heap complexity.
- **Track shortcut-to-original ratio:** Monitor `numShortcuts / numOriginalEdges` during contraction. If ratio exceeds ~2.5x, the node ordering heuristic may need tuning. For Singapore, expect ~1.5-2x shortcuts.

**Best Practices (Best Practices Researcher):**
- Node ordering priority: `edgeDifference + 2*contractedNeighbors + level` — the factor of 2 on contracted neighbors prevents "chain" contractions that create excessive shortcuts
- **Edge difference** = (shortcuts created by contracting u) - (edges incident to u that would be removed)
- Parallel contraction is possible (independent nodes) but defer to post-MVP — sequential is simpler and correctness is easier to verify
- Reference implementation: [LdDl/ch](https://github.com/LdDl/ch) — Go CH library, useful for cross-validation

**Implementation Detail:**
- During contraction, maintain an adjacency list (not CSR) since edges are added/removed dynamically. Convert to CSR only at the end when building the overlay.
- The overlay build step (forward/backward upward CSR) should be part of `contractor.go`, not a separate file

### Phase 4: Binary Serialization + Preprocessor CLI

Serialize the CH graph to disk and wire up the preprocessing CLI.

**Tasks:**

- [x] Implement binary writer in `pkg/graph/binary.go`
  - Write header: magic "MPROUTER", version=1, counts (use separate `NumFwdEdges` + `NumBwdEdges`)
  - Write arrays with `unsafe.Slice` for zero-copy byte reinterpretation
  - Write to temp file, then `os.Rename` for atomic writes
  - Append CRC32 checksum trailer

- [x] Implement binary reader in `pkg/graph/binary.go`
  - Validate magic bytes, version, and header counts against file size + hard caps
  - Pre-allocate slices from header counts, read with `unsafe.Slice`
  - Validate CRC32 checksum
  - Call `Validate()` to check CSR invariants post-deserialization
  - Return structured `CHGraph` containing forward/backward CSR + node coords + rank + geometry

- [x] Wire up preprocessor CLI in `cmd/preprocess/main.go`
  - Flags: `--input` (osm.pbf path), `--output` (binary path, default: `graph.bin`)
  - Pipeline: parse → build graph → extract largest component → contract CH → build overlay → serialize
  - Print progress to stderr: "Parsing nodes...", "Parsing ways...", "Building graph...", "Contracting...", "Writing..."
  - Print stats on completion: node count, edge count, shortcut count, file size

- [x] Write tests
  - Round-trip test: write graph → read graph → verify equality
  - Test corrupt file detection (bad magic, truncated)

**Files:** `pkg/graph/binary.go`, `pkg/graph/binary_test.go`, `cmd/preprocess/main.go`

#### Research Insights — Phase 4

**Performance (Performance Oracle — P0):**
- Use `unsafe.Slice` for binary I/O as detailed in the Binary File Format research insights above. This is the single biggest performance improvement for file load time (25-100x faster).
- Atomic write pattern: write to `output.tmp` → `os.Rename` to `output.graph.bin`. This prevents serving a partial file if the server is watching the path.

**Security (Security Sentinel):**
- Validate header immediately after read: check magic bytes, version, and all count fields against hard caps
- Validate computed file size matches actual file size before reading arrays
- Add `Validate()` method on `CHGraph` that checks CSR invariants post-deserialization:
  - `FirstOut` is monotonically non-decreasing
  - `FirstOut[NumNodes] == NumEdges`
  - All `Head[i] < NumNodes`
  - All `Weight[i] > 0` (no zero-weight edges)
  - All `Middle[i] < NumNodes || Middle[i] == -1`

### Phase 5: Query Engine — Snapping + Dijkstra + Unpacking

Implement the route query pipeline.

**Tasks:**

- [x] Implement R-tree snapping in `pkg/routing/snap.go`
  - Build `tidwall/rtree.RTreeG[uint32]` from original edges (NOT shortcuts)
  - Each entry: edge bounding box → edge ID
  - `Snap(lat, lng float64) (edgeID uint32, ratio float64, err error)`
    - Use `Nearby` with `BoxDist` to get candidates in distance order
    - For each candidate, compute exact `PointToSegmentDist`
    - Return first candidate where exact dist ≤ 500m
    - If none within 500m, return error

- [x] Implement bidirectional CH Dijkstra in `pkg/routing/dijkstra.go`
  - Min-heap using `container/heap` with stale-entry pattern (no decrease-key)
  - Pre-allocated `[]uint32` distance arrays (size NumNodes), initialized to MaxUint32
  - Track touched nodes for fast reset between queries
  - Forward search: only relax edges in `FwdGraph`
  - Backward search: only relax edges in `BwdGraph`
  - Termination: both PQ minimums ≥ mu
  - Return: meeting node, total distance, predecessor info for both directions

- [x] Implement virtual edge injection in `pkg/routing/engine.go`
  - For start snap (edgeID, ratio): seed forward PQ with segment endpoints at partial distances
  - For end snap (edgeID, ratio): seed backward PQ with segment endpoints at partial distances
  - Handle one-way: only seed in valid travel direction
  - Same-segment case: if start and end on same segment with compatible direction, return direct sub-segment

- [x] Implement shortcut unpacking in `pkg/routing/unpack.go`
  - Recursive decomposition: shortcut `(u, v)` with `middle=m` → unpack `(u, m)` + unpack `(m, v)`
  - Base case: `middle == -1` → original edge
  - Collect ordered list of original edge IDs

- [x] Implement result assembly in `pkg/routing/engine.go`
  - Convert edge ID sequence to segments with geometry
  - First segment: trim geometry to start from snapped point
  - Last segment: trim geometry to end at snapped point
  - Include per-segment distance and total distance

- [x] Implement `sync.Pool`-based query state management
  - Pool reusable `QueryState` structs (distance arrays, PQ, touched list)
  - Reset only touched entries between queries

- [x] Write tests
  - Snap tests with known segments
  - Dijkstra correctness: compare CH result against plain Dijkstra on original graph for many random pairs
  - Unpack tests: verify shortcut decomposition matches original path
  - Same-segment edge case

**Files:** `pkg/routing/snap.go`, `pkg/routing/dijkstra.go`, `pkg/routing/engine.go`, `pkg/routing/unpack.go`, `pkg/routing/snap_test.go`, `pkg/routing/dijkstra_test.go`, `pkg/routing/engine_test.go`

#### Research Insights — Phase 5

**Performance (Performance Oracle — P0):**
- **Stall-on-demand:** Implement as described in CH Termination research insights. This is the most impactful query-time optimization (2-5x fewer settled nodes).
- **Custom min-heap:** Replace `container/heap` with a concrete-typed `MinHeap` for `PQItem{Node uint32, Dist uint32}`. Avoids interface boxing allocations. Pre-allocate capacity: `items: make([]PQItem, 0, 1024)`.
- **Iterative unpacking:** Replace recursive shortcut unpacking with an explicit stack to avoid stack overflow on deeply nested shortcuts:
  ```go
  stack := []Edge{{from, to, middle}}
  for len(stack) > 0 {
      e := stack[len(stack)-1]
      stack = stack[:len(stack)-1]
      if e.Middle == -1 { result = append(result, e); continue }
      stack = append(stack, Edge{e.Mid, e.To, ...}, Edge{e.From, e.Mid, ...}) // reverse order
  }
  ```
  Add a depth limit (e.g., 100) as a safety bound against corrupt middle pointers.

**Architecture (Architecture Strategist):**
- Accept `context.Context` in `Engine.Route()` — pass it to Dijkstra so cancelled HTTP requests stop search early:
  ```go
  // Inside Dijkstra loop, check every ~100 iterations:
  if iterations%100 == 0 && ctx.Err() != nil { return 0, ctx.Err() }
  ```
- Use **equirectangular approximation** for snap distance comparisons (3x faster than Haversine, accurate at Singapore latitude)

**R-tree (Context7 — tidwall/rtree):**
- Use `RTreeG[uint32]` generic R-tree with `Insert(min, max [2]float64, edgeID uint32)`
- Use `Nearby(target, iter, boxDist)` for k-nearest-neighbor snapping — returns candidates in distance order
- `BoxDist` provides the distance function for Nearby — use the bounding box distance to edge

### Phase 6: HTTP API + Server

Wire up the HTTP layer and server lifecycle.

**Tasks:**

- [x] Define request/response models in `pkg/api/models.go`
  - `RouteRequest`, `RouteResponse`, `Segment`, `LatLng`, `ErrorResponse`
  - JSON tags: `snake_case` (consistent with Go conventions)

- [x] Implement handlers in `pkg/api/handlers.go`
  - `POST /api/v1/route` — validate input, call routing engine, return segments + geometry
  - `GET /api/v1/health` — return `{"status": "ok"}` when ready, `{"status": "loading"}` (503) during startup
  - `GET /api/v1/stats` — node count, edge count, shortcut count, graph file size

- [x] Implement server setup in `pkg/api/server.go`
  - stdlib `net/http.ServeMux` (Go 1.22+) with method-based routing
  - Middleware chain: logging, recovery, timeout (5s), concurrency limiter
  - Configurable CORS via `--cors-origin` flag (default: same-origin)
  - Security headers: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`
  - Readiness flag (set after graph + R-tree loaded)
  - Graceful shutdown on SIGTERM/SIGINT

- [x] Wire up server CLI in `cmd/server/main.go`
  - Flags: `--graph` (binary file path, default: `graph.bin`), `--port` (default: `8080`)
  - Load binary → build R-tree → start HTTP server
  - Log startup time and graph stats

- [x] Implement input validation
  - Singapore bounding box: lat [1.15, 1.48], lng [103.6, 104.1] — reject obviously wrong coordinates early
  - Fallback range: latitude -90 to 90, longitude -180 to 180
  - Reject NaN/Inf coordinates
  - Enforce `Content-Type: application/json`
  - Request body size limit: 1KB

- [x] Write integration tests
  - End-to-end: preprocess small fixture → load → query → verify response schema

**Files:** `pkg/api/models.go`, `pkg/api/handlers.go`, `pkg/api/server.go`, `cmd/server/main.go`, `pkg/api/handlers_test.go`

#### Research Insights — Phase 6

**Simplification (Code Simplicity Reviewer):**
- **Drop chi, use stdlib `net/http.ServeMux`** (Go 1.22+). Go's enhanced ServeMux supports method-based routing:
  ```go
  mux := http.NewServeMux()
  mux.HandleFunc("POST /api/v1/route", h.HandleRoute)
  mux.HandleFunc("GET /api/v1/health", h.HandleHealth)
  mux.HandleFunc("GET /api/v1/stats", h.HandleStats)
  ```
  This removes a dependency entirely. Middleware (logging, recovery, timeout) can use stdlib `http.Handler` wrapping.
- **Drop `segment.id` from response.** Segment IDs are internal graph indices — they are meaningless to API consumers and leak implementation details. Return only `distance_meters` and `geometry`.

**Security (Security Sentinel — High):**
- **Singapore bounding box check:** Reject coordinates outside `lat: [1.15, 1.48], lng: [103.6, 104.1]` with HTTP 400. This catches obviously wrong inputs before touching the R-tree.
- **Request timeout: 5 seconds** (not 30). No Singapore route should take >10ms; 5s covers pathological cases.
- **Content-Type enforcement:** Reject requests without `Content-Type: application/json`
- **Security headers:** `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Cache-Control: no-store`
- **Configurable CORS:** Default to same-origin; accept `--cors-origin` flag for development

**Architecture (Architecture Strategist):**
- Define `Router` interface for handler testability:
  ```go
  type Router interface {
      Route(ctx context.Context, start, end LatLng) (*RouteResult, error)
  }
  ```
- Define typed domain errors so handlers can switch on error type for appropriate HTTP status codes:
  ```go
  var ErrPointTooFar = errors.New("point too far from road")
  var ErrNoRoute     = errors.New("no route found")
  type SnapError struct { Field string; Distance float64 }
  ```

### Phase 7: Build System + End-to-End Validation

**Tasks:**

- [x] Create `Makefile`
  ```makefile
  build: build-preprocess build-server
  build-preprocess:
      go build -o bin/preprocess ./cmd/preprocess
  build-server:
      go build -o bin/server ./cmd/server
  test:
      go test -v ./...
  bench:
      go test -bench=. ./pkg/routing/
  ```

- [x] Create `.gitignore` (binaries, config, .osm.pbf files, .graph.bin)

- [ ] Download Singapore OSM extract and run full pipeline
  - Download from Geofabrik: `singapore-latest.osm.pbf`
  - `bin/preprocess --input singapore-latest.osm.pbf --output singapore.graph.bin`
  - `bin/server --graph singapore.graph.bin --port 8080`
  - Test with curl: `curl -X POST http://localhost:8080/api/v1/route -d '{"start":{"lat":1.3521,"lng":103.8198},"end":{"lat":1.2905,"lng":103.8520}}'`

- [ ] Verify correctness against known routes
  - Compare CH query results against plain Dijkstra on the original graph for 1000+ random node pairs (must match exactly)
  - Optionally compare a few routes against Google Maps / ORS as a sanity check (distances may differ due to different weight models)

#### Research Insights — Phase 7

**Correctness (Code Simplicity Reviewer):**
- **Replace the "within 5% of Google Maps" criterion** with a deterministic correctness check: run plain Dijkstra on the original graph for N random pairs, compare distances to CH query results. They must be identical (both use distance-only weights). Google Maps uses travel-time weights and different road classifications, making % comparisons unreliable.
- Add a `make validate` target that runs this CH-vs-Dijkstra comparison on the full Singapore graph

**Benchmarking (Performance Oracle):**
- Add Go benchmarks for the hot path:
  ```makefile
  bench:
      go test -bench=BenchmarkRoute -benchmem -count=5 ./pkg/routing/
  ```
- Benchmark targets: p50 < 1ms, p99 < 5ms for random Singapore queries

## Dependencies

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/paulmach/osm` | v0.9.0 | Parse .osm.pbf files |
| `github.com/tidwall/rtree` | v1.10.0 | R-tree spatial index for snapping |
| `net/http` | stdlib | HTTP server (Go 1.22+ ServeMux with method routing) |
| `unsafe` | stdlib | Zero-copy binary serialization via `unsafe.Slice` |

#### Research Insights — Dependencies

**Simplifications:**
- **Removed `go-chi/chi/v5`:** stdlib `net/http.ServeMux` (Go 1.22+) supports method-based routing natively, eliminating this dependency
- **Removed `encoding/binary`:** replaced with `unsafe.Slice` for performance (see Binary File Format insights)
- **Removed `container/heap`:** replaced with custom concrete-typed min-heap to avoid interface boxing
- Total external dependencies: **2** (paulmach/osm + tidwall/rtree) — minimal attack surface and maintenance burden

## Acceptance Criteria

### Functional Requirements

- [ ] Preprocessor reads `singapore-latest.osm.pbf` and produces a valid `.graph.bin`
- [ ] Server loads `.graph.bin` and serves route queries
- [ ] `POST /api/v1/route` accepts start/end lat/lng, returns ordered segments with geometry
- [ ] Routes respect one-way streets
- [ ] Points are snapped to nearest road within 500m
- [ ] Returns appropriate errors for invalid input, unreachable destinations, and far-from-road points
- [ ] Health and stats endpoints work correctly

### Non-Functional Requirements

- [ ] Query latency < 10ms for Singapore-scale graph (p95)
- [ ] Server handles concurrent requests safely
- [ ] Preprocessor completes in < 5 minutes for Singapore
- [ ] Binary file < 100MB for Singapore
- [ ] Server memory usage < 500MB

### Quality Gates

- [x] All packages have tests (`go test ./...` passes)
- [x] CH query results match plain Dijkstra on original graph for all pairs on test graph (exact match — correctness validation)
- [ ] Go benchmarks pass: p50 < 1ms, p99 < 5ms for random Singapore queries

#### Research Insights — Acceptance Criteria

**Corrections (Code Simplicity Reviewer):**
- Replaced "within 5% of Google Maps" with deterministic CH-vs-Dijkstra correctness check — Google Maps uses different weights (travel time, traffic) making % comparisons unreliable and flaky
- Tightened latency target: with stall-on-demand, p95 < 5ms is achievable (original 10ms was conservative)

**Additions (Performance Oracle):**
- Memory estimate for Singapore: ~250MB actual (500MB budget is comfortable)
  - Node coords: 500K × 16B = 8MB
  - Forward CSR: ~1.5M edges × 12B = 18MB
  - Backward CSR: ~1.5M edges × 12B = 18MB
  - Geometry: ~50MB (shape nodes)
  - R-tree: ~50MB
  - Rank array: 500K × 4B = 2MB
  - Per-query state: ~4MB each
- Preprocessing time estimate: 2.5-5 minutes for Singapore (500K nodes) — tight but achievable with settle limit 500

## Risk Analysis & Mitigation

| Risk | Impact | Mitigation |
|------|--------|------------|
| CH preprocessing too slow | Blocks development iteration | Start with small test fixtures; optimize node ordering heuristic; settle limit 500 |
| Incorrect witness search | Wrong shortcuts → wrong routes | Validate CH distances against plain Dijkstra for all node pairs on small graph |
| Memory spike during preprocessing | OOM on constrained machines | Filter node coords to road-referenced only (~40MB vs ~480MB); stream edges |
| `paulmach/osm` missing node coordinates | Empty geometry, panics | Handle missing nodes gracefully (skip edge, log warning) |
| R-tree snapping returns wrong segment | Routes start/end at wrong location | Unit test with known geometry; verify snap ratio computation |
| Crafted binary file causes OOM | Server crash, DoS | Validate header against file size + hard caps on counts before allocating |
| Unbounded concurrent queries | Memory exhaustion | Concurrency limiter (semaphore); 503 with Retry-After when full |
| Deeply nested shortcut unpacking | Stack overflow | Use iterative unpacking with explicit stack + depth limit |

#### Research Insights — Risk Analysis

**New Risks Identified (Security Sentinel + Architecture Strategist):**
- **Binary file integrity:** Without CRC32 or file-size validation, a corrupt or crafted `.graph.bin` can cause OOM or index-out-of-bounds panics. Mitigated by header validation + CRC32 trailer + CSR invariant checks.
- **Cancelled requests wasting resources:** Without `context.Context` propagation, a client disconnect leaves the Dijkstra search running to completion. Mitigated by threading context through `Engine.Route()` and checking `ctx.Err()` periodically in the search loop.
- **Stale distance arrays:** If `sync.Pool` returns a `QueryState` with incorrectly reset distances, queries return wrong results silently. Mitigated by deferring `sync.Pool` to post-MVP (fresh allocation is safe) and adding assertions in debug builds.

## References

### External References

- [Contraction Hierarchies: Faster and Simpler (KIT)](https://ae.iti.kit.edu/1640.php) — original CH paper
- [CH Guide — Node Ordering](https://jlazarsfeld.github.io/ch.150.project/sections/12-node-order/)
- [CH Guide — CH Query](https://jlazarsfeld.github.io/ch.150.project/sections/10-ch-query/)
- [cc-routing Wiki — Contraction Hierarchies](https://github.com/cc-routing/routing/wiki/Contraction-Hierarchies)
- [OSM Key:highway](https://wiki.openstreetmap.org/wiki/Key:highway)
- [OSM Key:oneway](https://wiki.openstreetmap.org/wiki/Key:oneway)
- [paulmach/osm GitHub](https://github.com/paulmach/osm)
- [tidwall/rtree GitHub](https://github.com/tidwall/rtree)
- [go-chi/chi v5](https://github.com/go-chi/chi)
- [Geofabrik Singapore Extract](https://download.geofabrik.de/asia/malaysia-singapore-brunei.html)

### Additional References (from Deepening)

- [LdDl/ch — Go Contraction Hierarchies library](https://github.com/LdDl/ch) — reference implementation for cross-validation
- [tidwall/rtree API — RTreeG generic R-tree](https://github.com/tidwall/rtree) — `Nearby` with `BoxDist` for KNN queries
- [Go 1.22 Enhanced ServeMux](https://pkg.go.dev/net/http#ServeMux) — method-based routing without external dependencies
- [unsafe.Slice documentation](https://pkg.go.dev/unsafe#Slice) — zero-copy byte reinterpretation for binary I/O

### Internal References

- Brainstorm: `docs/brainstorms/2026-02-24-routing-service-brainstorm.md`
