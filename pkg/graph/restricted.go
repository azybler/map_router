package graph

import "log"

// addSat returns a+b, saturating at the uint32 max instead of wrapping. Guards
// the safety-critical distance accumulation in the local Dijkstras below.
func addSat(a, b uint32) uint32 {
	s := a + b
	if s < a {
		return ^uint32(0)
	}
	return s
}

// maxRestrictedPenaltyFactor caps the weight multiplier applied to shortcut
// clusters. 50× makes any cut-through unattractive (even skipping a long
// public detour through a short gated segment) while keeping the roads
// reachable for genuine last-mile access (e.g. deliveries into gated estates);
// forced last-mile paths are unaffected by the factor's size.
const maxRestrictedPenaltyFactor = 50.0

// FilterBridgingRestricted returns a copy of g in which restricted (gated/private)
// edges are kept at normal weight where inlining them cannot create a faster
// public→public path, and kept at a PENALIZED weight otherwise. Restricted edges
// form clusters (connected components over restricted edges); a cluster's
// "gateways" are its nodes that also touch a public edge. A cluster is inlined
// at normal weight unless, for some ordered gateway pair (gi,gj), the shortest
// path THROUGH the cluster (over restricted edges only) is strictly faster than
// the shortest PUBLIC path gi→gj — i.e. it is a genuine through-shortcut, which
// would leak. Such clusters get their edge weights multiplied by the smallest
// factor that neutralizes every through-advantage (capped at
// maxRestrictedPenaltyFactor), so gated estates stay reachable for last-mile
// destinations — matching Google's behavior — without becoming cut-throughs.
// Clusters with ≤1 gateway are always inlined at normal weight (no pair to
// shortcut). Public edges are always kept. The returned graph carries no
// restricted flags. Note the reported route distance is measured from geometry,
// not weight, so penalties never distort distances.
//
// If g.EdgeRestricted is nil, g is returned unchanged.
func FilterBridgingRestricted(g *Graph) *Graph {
	if g.EdgeRestricted == nil || g.NumEdges == 0 {
		return g
	}
	n := g.NumNodes

	isPublic := make([]bool, n)
	inRestricted := make([]bool, n)
	uf := NewUnionFind(n)
	for u := uint32(0); u < n; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			v := g.Head[e]
			if g.EdgeRestricted[e] {
				inRestricted[u], inRestricted[v] = true, true
				uf.Union(u, v)
			} else {
				isPublic[u], isPublic[v] = true, true
			}
		}
	}

	gateways := make(map[uint32][]uint32)
	for u := uint32(0); u < n; u++ {
		if inRestricted[u] && isPublic[u] {
			r := uf.Find(u)
			gateways[r] = append(gateways[r], u)
		}
	}

	// factor 1.0 = inline at normal weight; >1.0 = penalized shortcut cluster.
	clusterFactor := make(map[uint32]float64, len(gateways))
	var nMulti, nInlined, nPenalized, nCapPenalized int
	for r, gw := range gateways {
		if len(gw) <= 1 {
			clusterFactor[r] = 1.0
		} else {
			f, byCap := clusterPenaltyFactor(g, gw)
			clusterFactor[r] = f
			nMulti++
			switch {
			case f <= 1.0:
				nInlined++
			case byCap:
				nCapPenalized++
			default:
				nPenalized++
			}
		}
	}
	log.Printf("restricted clusters: %d multi-gateway (%d inlined, %d penalized as shortcuts, %d penalized at settle-cap); %d cul-de-sac inlined",
		nMulti, nInlined, nPenalized, nCapPenalized, len(gateways)-nMulti)

	// Rebuild CSR: every edge survives; restricted edges of shortcut clusters
	// carry their cluster's penalty factor.
	hasGeo := g.GeoFirstOut != nil
	firstOut := make([]uint32, n+1)
	var head, weight, geoFirstOut []uint32
	var geoLat, geoLon []float64
	for u := uint32(0); u < n; u++ {
		firstOut[u] = uint32(len(head))
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			w := g.Weight[e]
			if g.EdgeRestricted[e] {
				if f := clusterFactor[uf.Find(u)]; f > 1.0 {
					scaled := float64(w) * f
					if scaled >= float64(^uint32(0)) {
						w = ^uint32(0)
					} else {
						w = uint32(scaled)
					}
				}
			}
			if hasGeo {
				geoFirstOut = append(geoFirstOut, uint32(len(geoLat)))
				gs, ge := g.GeoFirstOut[e], g.GeoFirstOut[e+1]
				geoLat = append(geoLat, g.GeoShapeLat[gs:ge]...)
				geoLon = append(geoLon, g.GeoShapeLon[gs:ge]...)
			}
			head = append(head, g.Head[e])
			weight = append(weight, w)
		}
	}
	firstOut[n] = uint32(len(head))
	if hasGeo {
		geoFirstOut = append(geoFirstOut, uint32(len(geoLat)))
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
		GeoShapeLat: geoLat,
		GeoShapeLon: geoLon,
		// EdgeRestricted intentionally nil — survivors are ordinary edges.
	}
}

// clusterPenaltyFactor returns the smallest weight multiplier that makes every
// through-cluster path at least as slow as its public alternative (1.0 when the
// cluster is not a shortcut at all), capped at maxRestrictedPenaltyFactor.
// byCap is true when the public-side search hit its settle cap or a public
// alternative was unreachable, in which case the cap factor is returned.
func clusterPenaltyFactor(g *Graph, gw []uint32) (factor float64, byCap bool) {
	const maxPublicSettle = 200000 // safety cap: if exceeded, apply the max penalty
	targets := make(map[uint32]bool, len(gw))
	for _, x := range gw {
		targets[x] = true
	}
	factor = 1.0
	for _, gi := range gw {
		through := restrictedDijkstra(g, gi)
		var maxT uint32
		any := false
		// Reverse ordering (gj as source) is covered when gj's own iteration runs.
		for _, gj := range gw {
			if gj != gi {
				if t, ok := through[gj]; ok {
					any = true
					if t > maxT {
						maxT = t
					}
				}
			}
		}
		if !any {
			continue
		}
		// Search the public side far enough to measure how much slower public
		// is, not just whether it is slower (bounded by the penalty cap).
		capCost := addSat(maxT, 0)
		if scaled := float64(maxT) * maxRestrictedPenaltyFactor; scaled < float64(^uint32(0)) {
			capCost = uint32(scaled)
		} else {
			capCost = ^uint32(0)
		}
		pub, capped := publicDijkstra(g, gi, capCost, maxPublicSettle, targets)
		if capped {
			return maxRestrictedPenaltyFactor, true
		}
		for _, gj := range gw {
			if gj == gi {
				continue
			}
			t, ok := through[gj]
			if !ok || t == 0 {
				continue
			}
			p, pok := pub[gj]
			if !pok {
				// No public alternative within cap: penalize at max (the
				// cluster may be the only access; it stays reachable).
				return maxRestrictedPenaltyFactor, true
			}
			if p > t {
				// Genuine shortcut: need factor ≥ p/t (5% margin) to neutralize.
				need := float64(p) / float64(t) * 1.05
				if need > factor {
					factor = need
				}
			}
		}
	}
	if factor > maxRestrictedPenaltyFactor {
		factor = maxRestrictedPenaltyFactor
	}
	return factor, false
}

// rdNode is a (node, dist) entry for the local Dijkstra heaps below.
type rdNode struct{ node, dist uint32 }

type rdHeap struct{ a []rdNode }

func (h *rdHeap) push(x rdNode) {
	h.a = append(h.a, x)
	i := len(h.a) - 1
	for i > 0 {
		p := (i - 1) / 2
		if h.a[p].dist <= h.a[i].dist {
			break
		}
		h.a[p], h.a[i] = h.a[i], h.a[p]
		i = p
	}
}

func (h *rdHeap) pop() rdNode {
	a := h.a
	top := a[0]
	a[0] = a[len(a)-1]
	a = a[:len(a)-1]
	i := 0
	for {
		l, r, s := 2*i+1, 2*i+2, i
		if l < len(a) && a[l].dist < a[s].dist {
			s = l
		}
		if r < len(a) && a[r].dist < a[s].dist {
			s = r
		}
		if s == i {
			break
		}
		a[s], a[i] = a[i], a[s]
		i = s
	}
	h.a = a
	return top
}

func (h *rdHeap) empty() bool { return len(h.a) == 0 }

// restrictedDijkstra returns shortest times from src over RESTRICTED edges only
// (i.e. within src's restricted cluster).
func restrictedDijkstra(g *Graph, src uint32) map[uint32]uint32 {
	const maxSettle = 100000
	dist := map[uint32]uint32{src: 0}
	h := &rdHeap{}
	h.push(rdNode{src, 0})
	settled := 0
	for !h.empty() {
		cur := h.pop()
		if d, ok := dist[cur.node]; ok && cur.dist > d {
			continue
		}
		settled++
		if settled > maxSettle {
			break
		}
		for e := g.FirstOut[cur.node]; e < g.FirstOut[cur.node+1]; e++ {
			if !g.EdgeRestricted[e] {
				continue
			}
			v := g.Head[e]
			nd := addSat(cur.dist, g.Weight[e])
			if old, ok := dist[v]; !ok || nd < old {
				dist[v] = nd
				h.push(rdNode{v, nd})
			}
		}
	}
	return dist
}

// publicDijkstra returns shortest times from src over PUBLIC edges only, bounded
// by capCost (nodes beyond capCost are not expanded) and by maxSettle. It stops
// early once all `targets` are settled. The bool is true if maxSettle was hit
// before all targets were found (caller treats that as "public can't compete").
func publicDijkstra(g *Graph, src, capCost uint32, maxSettle int, targets map[uint32]bool) (map[uint32]uint32, bool) {
	dist := map[uint32]uint32{src: 0}
	h := &rdHeap{}
	h.push(rdNode{src, 0})
	remaining := 0
	for t := range targets {
		if t != src {
			remaining++
		}
	}
	settled := 0
	for !h.empty() {
		cur := h.pop()
		if d, ok := dist[cur.node]; ok && cur.dist > d {
			continue
		}
		if cur.dist > capCost {
			break
		}
		if targets[cur.node] && cur.node != src {
			remaining--
			if remaining == 0 {
				return dist, false
			}
		}
		settled++
		if settled > maxSettle {
			return dist, true
		}
		for e := g.FirstOut[cur.node]; e < g.FirstOut[cur.node+1]; e++ {
			if g.EdgeRestricted[e] {
				continue
			}
			v := g.Head[e]
			nd := addSat(cur.dist, g.Weight[e])
			if nd > capCost {
				continue
			}
			if old, ok := dist[v]; !ok || nd < old {
				dist[v] = nd
				h.push(rdNode{v, nd})
			}
		}
	}
	return dist, false
}
