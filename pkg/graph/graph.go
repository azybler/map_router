package graph

// Graph represents a directed graph in CSR (Compressed Sparse Row) format.
type Graph struct {
	NumNodes uint32
	NumEdges uint32
	FirstOut []uint32  // len: NumNodes + 1; FirstOut[i]..FirstOut[i+1] are edges from node i
	Head     []uint32  // len: NumEdges; target node for each edge
	Weight   []uint32  // len: NumEdges; distance in millimeters
	NodeLat  []float64 // len: NumNodes
	NodeLon  []float64 // len: NumNodes

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
