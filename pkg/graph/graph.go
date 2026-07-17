package graph

// CHGraph holds the output of contraction hierarchies preprocessing.
type CHGraph struct {
	NumNodes uint32
	NodeLat  []float64
	NodeLon  []float64
	Rank     []uint32

	// Forward upward graph (edges where rank[source] < rank[target]).
	FwdFirstOut []uint32
	FwdHead     []uint32
	FwdWeight   []uint32
	FwdMiddle   []int32

	// Backward upward graph (reversed edges where rank[source] < rank[target]).
	BwdFirstOut []uint32
	BwdHead     []uint32
	BwdWeight   []uint32
	BwdMiddle   []int32

	// Original graph edges (needed for R-tree snapping and geometry).
	OrigFirstOut []uint32
	OrigHead     []uint32
	OrigWeight   []uint32

	// Original edge geometry (carried through from the base graph).
	GeoFirstOut []uint32
	GeoShapeLat []float64
	GeoShapeLon []float64
}

// BaseGraph holds the metric-independent parts of a CH graph: node coordinates,
// the original (uncontracted) edge topology, and edge geometry. Everything here
// is derived purely from the road network and is identical no matter which
// metric (time, distance, ...) the graph is weighted for.
//
// It is the shared half of the split on-disk format: one base is loaded once and
// referenced by every metric overlay built from the same source, so the server
// holds a single copy in RAM (and can build a single Snapper over it) regardless
// of how many metrics it serves. See WriteBase/ReadBase and the overlay
// counterpart WriteOverlay/ReadOverlay.
type BaseGraph struct {
	NumNodes uint32
	NodeLat  []float64
	NodeLon  []float64

	// Original graph topology (needed for R-tree snapping and geometry). Edge
	// WEIGHTS are metric-specific and live in the overlay, not here.
	OrigFirstOut []uint32
	OrigHead     []uint32

	// Original edge geometry.
	GeoFirstOut []uint32
	GeoShapeLat []float64
	GeoShapeLon []float64

	// Identity is a content hash over the topology (NumNodes + coords + original
	// CSR). It is written into every overlay so a base/overlay mismatch is
	// rejected at load time instead of silently addressing the wrong roads.
	Identity uint32
}

// Graph builds a *Graph view over this base for snapping/geometry, using the
// supplied per-metric weights for the original edges. Pass nil weights when the
// caller (e.g. the Snapper) never reads them. The returned Graph shares the
// base's backing slices — it is a view, not a copy.
func (b *BaseGraph) Graph(origWeight []uint32) *Graph {
	return &Graph{
		NumNodes:    b.NumNodes,
		NumEdges:    uint32(len(b.OrigHead)),
		FirstOut:    b.OrigFirstOut,
		Head:        b.OrigHead,
		Weight:      origWeight,
		NodeLat:     b.NodeLat,
		NodeLon:     b.NodeLon,
		GeoFirstOut: b.GeoFirstOut,
		GeoShapeLat: b.GeoShapeLat,
		GeoShapeLon: b.GeoShapeLon,
	}
}

// Graph represents a directed graph in CSR (Compressed Sparse Row) format.
type Graph struct {
	NumNodes uint32
	NumEdges uint32
	FirstOut []uint32 // len: NumNodes + 1; FirstOut[i]..FirstOut[i+1] are edges from node i
	Head     []uint32 // len: NumEdges; target node for each edge
	Weight   []uint32 // len: NumEdges; travel time in milliseconds (v3 metric)

	// EdgeRestricted[i] flags edge i as gated/private. Populated by Build and
	// consumed by FilterBridgingRestricted at preprocess time; NOT serialized
	// (nil after a binary load — the server treats all edges as normal).
	EdgeRestricted []bool // len: NumEdges (build-time only)

	NodeLat []float64 // len: NumNodes
	NodeLon []float64 // len: NumNodes

	// Edge geometry: intermediate shape nodes for rendering.
	// GeoFirstOut[i]..GeoFirstOut[i+1] indexes into GeoShapeLat/Lon for edge i.
	GeoFirstOut []uint32  // len: NumEdges + 1
	GeoShapeLat []float64 // flattened intermediate lat coords
	GeoShapeLon []float64 // flattened intermediate lon coords
}

// EdgesFrom returns the range of edge indices for edges originating from node u.
func (g *Graph) EdgesFrom(u uint32) (start, end uint32) {
	return g.FirstOut[u], g.FirstOut[u+1]
}
