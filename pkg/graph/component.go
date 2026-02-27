package graph

// UnionFind implements a disjoint-set data structure with path compression
// and union by rank.
type UnionFind struct {
	parent []uint32
	rank   []byte // byte is sufficient — max rank ~30 for realistic graphs
	size   []uint32
}

// NewUnionFind creates a UnionFind for n elements.
func NewUnionFind(n uint32) *UnionFind {
	parent := make([]uint32, n)
	size := make([]uint32, n)
	for i := range n {
		parent[i] = i
		size[i] = 1
	}
	return &UnionFind{
		parent: parent,
		rank:   make([]byte, n),
		size:   size,
	}
}

// Find returns the representative of the set containing x, with path halving.
func (uf *UnionFind) Find(x uint32) uint32 {
	for uf.parent[x] != x {
		uf.parent[x] = uf.parent[uf.parent[x]] // path halving
		x = uf.parent[x]
	}
	return x
}

// Union merges the sets containing x and y. Returns false if already same set.
func (uf *UnionFind) Union(x, y uint32) bool {
	rx := uf.Find(x)
	ry := uf.Find(y)
	if rx == ry {
		return false
	}

	// Union by rank.
	if uf.rank[rx] < uf.rank[ry] {
		rx, ry = ry, rx
	}
	uf.parent[ry] = rx
	uf.size[rx] += uf.size[ry]
	if uf.rank[rx] == uf.rank[ry] {
		uf.rank[rx]++
	}
	return true
}

// LargestComponent returns the node indices belonging to the largest
// weakly connected component (treating the directed graph as undirected).
func LargestComponent(g *Graph) []uint32 {
	if g.NumNodes == 0 {
		return nil
	}

	uf := NewUnionFind(g.NumNodes)

	// Union all edges (both directions treated as undirected).
	for u := uint32(0); u < g.NumNodes; u++ {
		start, end := g.EdgesFrom(u)
		for e := start; e < end; e++ {
			uf.Union(u, g.Head[e])
		}
	}

	// Find the representative with the largest size.
	bestRoot := uint32(0)
	bestSize := uint32(0)
	for i := uint32(0); i < g.NumNodes; i++ {
		root := uf.Find(i)
		if uf.size[root] > bestSize {
			bestRoot = root
			bestSize = uf.size[root]
		}
	}

	// Collect all nodes in the largest component.
	nodes := make([]uint32, 0, bestSize)
	for i := uint32(0); i < g.NumNodes; i++ {
		if uf.Find(i) == bestRoot {
			nodes = append(nodes, i)
		}
	}

	return nodes
}

// FilterToComponent creates a new graph containing only the specified nodes.
func FilterToComponent(g *Graph, nodes []uint32) *Graph {
	if len(nodes) == 0 {
		return &Graph{}
	}

	// Build old→new node index mapping.
	oldToNew := make(map[uint32]uint32, len(nodes))
	for newIdx, oldIdx := range nodes {
		oldToNew[oldIdx] = uint32(newIdx)
	}

	numNodes := uint32(len(nodes))

	// Collect edges that are fully within the component.
	type edge struct {
		from, to, weight uint32
		shapeLats        []float64
		shapeLons        []float64
	}
	var edges []edge

	for _, oldU := range nodes {
		start, end := g.EdgesFrom(oldU)
		for e := start; e < end; e++ {
			oldV := g.Head[e]
			if newV, ok := oldToNew[oldV]; ok {
				var shapeLats, shapeLons []float64
				if g.GeoFirstOut != nil {
					geoStart := g.GeoFirstOut[e]
					geoEnd := g.GeoFirstOut[e+1]
					if geoEnd > geoStart {
						shapeLats = make([]float64, geoEnd-geoStart)
						copy(shapeLats, g.GeoShapeLat[geoStart:geoEnd])
						shapeLons = make([]float64, geoEnd-geoStart)
						copy(shapeLons, g.GeoShapeLon[geoStart:geoEnd])
					}
				}
				edges = append(edges, edge{
					from:      oldToNew[oldU],
					to:        newV,
					weight:    g.Weight[e],
					shapeLats: shapeLats,
					shapeLons: shapeLons,
				})
			}
		}
	}

	numEdges := uint32(len(edges))

	// Build CSR arrays.
	firstOut := make([]uint32, numNodes+1)
	head := make([]uint32, numEdges)
	weight := make([]uint32, numEdges)
	geoFirstOut := make([]uint32, numEdges+1)
	var geoShapeLat, geoShapeLon []float64

	// Count edges per node.
	for _, e := range edges {
		firstOut[e.from+1]++
	}
	for i := uint32(1); i <= numNodes; i++ {
		firstOut[i] += firstOut[i-1]
	}

	// Place edges into CSR order.
	pos := make([]uint32, numNodes)
	copy(pos, firstOut[:numNodes])
	for _, e := range edges {
		idx := pos[e.from]
		head[idx] = e.to
		weight[idx] = e.weight
		geoFirstOut[idx] = uint32(len(geoShapeLat))
		geoShapeLat = append(geoShapeLat, e.shapeLats...)
		geoShapeLon = append(geoShapeLon, e.shapeLons...)
		pos[e.from]++
	}
	geoFirstOut[numEdges] = uint32(len(geoShapeLat))

	// Copy node coordinates.
	nodeLat := make([]float64, numNodes)
	nodeLon := make([]float64, numNodes)
	for newIdx, oldIdx := range nodes {
		nodeLat[newIdx] = g.NodeLat[oldIdx]
		nodeLon[newIdx] = g.NodeLon[oldIdx]
	}

	return &Graph{
		NumNodes:    numNodes,
		NumEdges:    numEdges,
		FirstOut:    firstOut,
		Head:        head,
		Weight:      weight,
		NodeLat:     nodeLat,
		NodeLon:     nodeLon,
		GeoFirstOut: geoFirstOut,
		GeoShapeLat: geoShapeLat,
		GeoShapeLon: geoShapeLon,
	}
}
