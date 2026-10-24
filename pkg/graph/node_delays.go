package graph

// NodeDelays is the time, in the graph's weight units (ms), a car loses
// passing through a node: a junction, a traffic signal or a toll booth.
//
// Without them the router prices a road only by its length and speed, so a
// tertiary street with a side road every 50 m costs the same per kilometre as
// one with none, and it threads routes through dense street grids that Google
// avoids. Calibrated on Google's routes for real delivery trips (speeds.json).
type NodeDelays struct {
	// A junction is a node joining three or more neighbours. Its delay depends
	// on how the road a car arrives on ranks (osm.ClassRank) against the
	// biggest other road there.
	JunctionMajor uint32 // the arriving road outranks every other road there
	JunctionEqual uint32 // the biggest other road ranks the same
	JunctionMinor uint32 // a bigger road crosses or joins it

	// Signal is charged on entering a node tagged highway=traffic_signals,
	// junction or not: OSM usually puts the signal on each approach's stop
	// line rather than on the junction node itself.
	Signal uint32

	// TollBooth is charged on entering a node tagged barrier=toll_booth.
	TollBooth uint32
}

// AddNodeDelays adds d to the weight of every edge that enters a junction, a
// traffic signal or a toll booth, so a route pays it once per such node it
// passes through. It needs Build's EdgeWay, EdgeClass, EdgeToSignal and
// EdgeToToll (an edge's own way is not an "other road", which keeps a road
// continuing through a junction from ranking against itself), and must run
// before FilterBridgingRestricted, which drops them. Returns the number of
// edges delayed.
func AddNodeDelays(g *Graph, d NodeDelays) int {
	if g.EdgeWay == nil || g.EdgeClass == nil || g.NumEdges == 0 {
		return 0
	}
	n := g.NumNodes
	delayed := make([]bool, g.NumEdges)
	charge := func(e uint32, delay uint32) {
		if delay != 0 {
			g.Weight[e] = addSat(g.Weight[e], delay)
			delayed[e] = true
		}
	}
	for e := range g.NumEdges {
		if g.EdgeToSignal != nil && g.EdgeToSignal[e] {
			charge(e, d.Signal)
		}
		if g.EdgeToToll != nil && g.EdgeToToll[e] {
			charge(e, d.TollBooth)
		}
	}

	// Incoming edges per node.
	tail := make([]uint32, g.NumEdges)
	for u := uint32(0); u < n; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			tail[e] = u
		}
	}
	inFirst := make([]uint32, n+1)
	for _, v := range g.Head {
		inFirst[v+1]++
	}
	for i := uint32(1); i <= n; i++ {
		inFirst[i] += inFirst[i-1]
	}
	inEdge := make([]uint32, g.NumEdges)
	pos := make([]uint32, n)
	copy(pos, inFirst[:n])
	for e, v := range g.Head {
		inEdge[pos[v]] = uint32(e)
		pos[v]++
	}

	neighbours := make(map[uint32]struct{}, 8)
	for x := uint32(0); x < n; x++ {
		clear(neighbours)
		for e := g.FirstOut[x]; e < g.FirstOut[x+1]; e++ {
			neighbours[g.Head[e]] = struct{}{}
		}
		for i := inFirst[x]; i < inFirst[x+1]; i++ {
			neighbours[tail[inEdge[i]]] = struct{}{}
		}
		if len(neighbours) < 3 {
			continue
		}
		for i := inFirst[x]; i < inFirst[x+1]; i++ {
			e := inEdge[i]
			way, rank := g.EdgeWay[e], int(g.EdgeClass[e])
			other := -1
			for f := g.FirstOut[x]; f < g.FirstOut[x+1]; f++ {
				if g.EdgeWay[f] != way && int(g.EdgeClass[f]) > other {
					other = int(g.EdgeClass[f])
				}
			}
			for j := inFirst[x]; j < inFirst[x+1]; j++ {
				f := inEdge[j]
				if g.EdgeWay[f] != way && int(g.EdgeClass[f]) > other {
					other = int(g.EdgeClass[f])
				}
			}
			switch {
			case other < rank:
				charge(e, d.JunctionMajor)
			case other == rank:
				charge(e, d.JunctionEqual)
			default:
				charge(e, d.JunctionMinor)
			}
		}
	}

	count := 0
	for _, x := range delayed {
		if x {
			count++
		}
	}
	return count
}
