package graph

import (
	"slices"

	"github.com/paulmach/osm"
)

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

	// Penalties lists the original edges whose OrigWeight carries a
	// restricted-road penalty, with their weights without it. nil when the graph
	// was written before penalties were recorded: OrigWeight is then all there is.
	Penalties *EdgePenalties
}

// EdgePenalties records which edges FilterBridgingRestricted penalized and what
// each would weigh without its penalty. A penalty is a routing preference (stay
// out of gated estates unless one is the destination), not a cost of driving
// the road, so anything reporting travel time must undo it.
//
// It is sparse: penalized edges are a small fraction of a road network, and
// every edge not listed weighs exactly its Weight.
type EdgePenalties struct {
	Edges       []uint32 // ascending edge indices
	Unpenalized []uint32 // Unpenalized[i] is edge Edges[i]'s weight without its penalty
}

// UnpenalizedWeight returns the weight of edge e without its restricted-road
// penalty, given its (possibly penalized) weight w. p may be nil.
func (p *EdgePenalties) UnpenalizedWeight(e, w uint32) uint32 {
	if u, ok := p.lookup(e); ok {
		return u
	}
	return w
}

// lookup returns edge e's unpenalized weight, ok=false if e is not penalized.
func (p *EdgePenalties) lookup(e uint32) (uint32, bool) {
	if p == nil {
		return 0, false
	}
	if i, ok := slices.BinarySearch(p.Edges, e); ok {
		return p.Unpenalized[i], true
	}
	return 0, false
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

	// EdgeWay[i] and EdgeClass[i] are edge i's OSM way and osm.ClassRank;
	// EdgeToSignal[i] and EdgeToToll[i] say whether its head node is a traffic
	// signal or a toll booth. Populated by Build for AddNodeDelays at
	// preprocess time; NOT serialized, and dropped by FilterBridgingRestricted.
	EdgeWay      []osm.WayID // len: NumEdges (build-time only)
	EdgeClass    []uint8     // len: NumEdges (build-time only)
	EdgeToSignal []bool      // len: NumEdges (build-time only)
	EdgeToToll   []bool      // len: NumEdges (build-time only)

	// Penalties records the edges FilterBridgingRestricted penalized (build-time;
	// ch.Contract carries it into the CHGraph). nil if the filter has not run.
	Penalties *EdgePenalties

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
