package graph

// FilterBridgingRestricted returns a copy of g in which restricted edges are kept
// ONLY when their restricted cluster touches the public network at ≤1 node (a
// cul-de-sac, which can never be a through-shortcut). Restricted clusters that
// touch the public network at ≥2 nodes (potential private cut-throughs) have their
// restricted edges dropped. Public edges are always kept. The returned graph carries
// no restricted flags — every surviving edge is an ordinary edge.
//
// Touch counting is by node and direction-agnostic, so a one-way restricted spur
// that happens to touch public at 2 nodes is conservatively dropped (fails safe:
// we lose some legitimate last-mile access rather than risk a through-shortcut).
//
// If g.EdgeRestricted is nil, g is returned unchanged.
func FilterBridgingRestricted(g *Graph) *Graph {
	if g.EdgeRestricted == nil || g.NumEdges == 0 {
		return g
	}
	n := g.NumNodes

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

	uf := NewUnionFind(n)
	for u := uint32(0); u < n; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			if g.EdgeRestricted[e] {
				uf.Union(u, g.Head[e])
			}
		}
	}

	// Distinct public-touch nodes per restricted cluster.
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

	// Which edges survive.
	survive := make([]bool, g.NumEdges)
	firstOut := make([]uint32, n+1)
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

	hasGeo := g.GeoFirstOut != nil
	var head, weight []uint32
	var geoFirstOut []uint32
	var geoShapeLat, geoShapeLon []float64
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
		// EdgeRestricted intentionally nil.
	}
}
