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
// strongly connected component (SCC) of the directed graph.
//
// Routing requires strong connectivity: every node in the returned set must be
// able to reach every other node while respecting edge direction (one-way
// streets). A weakly connected component (treating the graph as undirected)
// would retain nodes reachable in only one direction — e.g. the dead-end of a
// one-way street with no path back. When the snapper later snaps a query
// endpoint onto such a node, routes to or from that point fail with
// "no route found" even though the road is reachable in reality.
//
// Uses iterative Kosaraju's algorithm (two linear passes over the graph and its
// transpose) for O(V+E) time without recursion, which would overflow the stack
// on a million-node graph.
func LargestComponent(g *Graph) []uint32 {
	n := g.NumNodes
	if n == 0 {
		return nil
	}

	// Build the transpose (reverse) adjacency in CSR form.
	revFirstOut := make([]uint32, n+1)
	for _, v := range g.Head {
		revFirstOut[v+1]++
	}
	for i := uint32(1); i <= n; i++ {
		revFirstOut[i] += revFirstOut[i-1]
	}
	revHead := make([]uint32, len(g.Head))
	fillPos := make([]uint32, n)
	copy(fillPos, revFirstOut[:n])
	for u := uint32(0); u < n; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			v := g.Head[e]
			revHead[fillPos[v]] = u
			fillPos[v]++
		}
	}

	// Pass 1: iterative post-order DFS on G, recording finish order.
	visited := make([]bool, n)
	order := make([]uint32, 0, n)
	type frame struct{ node, edge uint32 }
	stack := make([]frame, 0, 1024)
	for s := uint32(0); s < n; s++ {
		if visited[s] {
			continue
		}
		visited[s] = true
		stack = append(stack, frame{s, g.FirstOut[s]})
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.edge < g.FirstOut[top.node+1] {
				v := g.Head[top.edge]
				top.edge++
				if !visited[v] {
					visited[v] = true
					stack = append(stack, frame{v, g.FirstOut[v]})
				}
			} else {
				order = append(order, top.node)
				stack = stack[:len(stack)-1]
			}
		}
	}

	// Pass 2: assign SCC ids on the transpose, processing nodes in reverse
	// finish order.
	const unassigned = ^uint32(0)
	comp := make([]uint32, n)
	for i := range comp {
		comp[i] = unassigned
	}
	dfs := make([]uint32, 0, 1024)
	numComps := uint32(0)
	for i := len(order) - 1; i >= 0; i-- {
		root := order[i]
		if comp[root] != unassigned {
			continue
		}
		comp[root] = numComps
		dfs = append(dfs, root)
		for len(dfs) > 0 {
			u := dfs[len(dfs)-1]
			dfs = dfs[:len(dfs)-1]
			for e := revFirstOut[u]; e < revFirstOut[u+1]; e++ {
				w := revHead[e]
				if comp[w] == unassigned {
					comp[w] = numComps
					dfs = append(dfs, w)
				}
			}
		}
		numComps++
	}

	// Find the largest SCC by node count.
	sizes := make([]uint32, numComps)
	for _, c := range comp {
		sizes[c]++
	}
	best := uint32(0)
	for c := uint32(1); c < numComps; c++ {
		if sizes[c] > sizes[best] {
			best = c
		}
	}

	// Collect its nodes in ascending index order.
	nodes := make([]uint32, 0, sizes[best])
	for i := uint32(0); i < n; i++ {
		if comp[i] == best {
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
