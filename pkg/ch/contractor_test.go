package ch

import (
	"math"
	"testing"

	"github.com/paulmach/osm"

	"map_router/pkg/graph"
	osmparser "map_router/pkg/osm"
)

// buildTestGraph creates a small graph for testing:
//
//	0 ---100--- 1 ---200--- 2
//	|                       |
//	300                    400
//	|                       |
//	3 ---500--- 4 ---600--- 5
//
// All edges are bidirectional.
func buildTestGraph() *graph.Graph {
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			// Row 1: 0-1-2
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 20, ToNodeID: 10, Weight: 100},
			{FromNodeID: 20, ToNodeID: 30, Weight: 200},
			{FromNodeID: 30, ToNodeID: 20, Weight: 200},
			// Columns: 0-3, 2-5
			{FromNodeID: 10, ToNodeID: 40, Weight: 300},
			{FromNodeID: 40, ToNodeID: 10, Weight: 300},
			{FromNodeID: 30, ToNodeID: 60, Weight: 400},
			{FromNodeID: 60, ToNodeID: 30, Weight: 400},
			// Row 2: 3-4-5
			{FromNodeID: 40, ToNodeID: 50, Weight: 500},
			{FromNodeID: 50, ToNodeID: 40, Weight: 500},
			{FromNodeID: 50, ToNodeID: 60, Weight: 600},
			{FromNodeID: 60, ToNodeID: 50, Weight: 600},
		},
		NodeLat: map[osm.NodeID]float64{10: 1.0, 20: 1.0, 30: 1.0, 40: 1.1, 50: 1.1, 60: 1.1},
		NodeLon: map[osm.NodeID]float64{10: 103.0, 20: 103.1, 30: 103.2, 40: 103.0, 50: 103.1, 60: 103.2},
	}
	return graph.Build(result)
}

// plainDijkstra runs standard Dijkstra on the original CSR graph.
func plainDijkstra(g *graph.Graph, source, target uint32) uint32 {
	dist := make([]uint32, g.NumNodes)
	for i := range dist {
		dist[i] = math.MaxUint32
	}
	dist[source] = 0

	type item struct {
		node uint32
		dist uint32
	}
	var pq []item
	pq = append(pq, item{source, 0})

	for len(pq) > 0 {
		// Find min.
		minIdx := 0
		for i := 1; i < len(pq); i++ {
			if pq[i].dist < pq[minIdx].dist {
				minIdx = i
			}
		}
		cur := pq[minIdx]
		pq[minIdx] = pq[len(pq)-1]
		pq = pq[:len(pq)-1]

		if cur.dist > dist[cur.node] {
			continue
		}

		if cur.node == target {
			return cur.dist
		}

		start, end := g.EdgesFrom(cur.node)
		for e := start; e < end; e++ {
			v := g.Head[e]
			newDist := cur.dist + g.Weight[e]
			if newDist < dist[v] {
				dist[v] = newDist
				pq = append(pq, item{v, newDist})
			}
		}
	}

	return dist[target]
}

// chDijkstra runs bidirectional CH Dijkstra on the overlay.
func chDijkstra(ch *CHResult, source, target uint32) uint32 {
	distFwd := make([]uint32, ch.NumNodes)
	distBwd := make([]uint32, ch.NumNodes)
	for i := range distFwd {
		distFwd[i] = math.MaxUint32
		distBwd[i] = math.MaxUint32
	}
	distFwd[source] = 0
	distBwd[target] = 0

	type item struct {
		node uint32
		dist uint32
	}

	var fwdPQ, bwdPQ []item
	fwdPQ = append(fwdPQ, item{source, 0})
	bwdPQ = append(bwdPQ, item{target, 0})

	mu := uint32(math.MaxUint32)

	popMin := func(pq *[]item) item {
		minIdx := 0
		for i := 1; i < len(*pq); i++ {
			if (*pq)[i].dist < (*pq)[minIdx].dist {
				minIdx = i
			}
		}
		cur := (*pq)[minIdx]
		(*pq)[minIdx] = (*pq)[len(*pq)-1]
		*pq = (*pq)[:len(*pq)-1]
		return cur
	}

	peekMin := func(pq []item) uint32 {
		if len(pq) == 0 {
			return math.MaxUint32
		}
		min := pq[0].dist
		for _, it := range pq[1:] {
			if it.dist < min {
				min = it.dist
			}
		}
		return min
	}

	for len(fwdPQ) > 0 || len(bwdPQ) > 0 {
		// Forward step.
		if len(fwdPQ) > 0 && peekMin(fwdPQ) < mu {
			cur := popMin(&fwdPQ)
			if cur.dist <= distFwd[cur.node] {
				// Check meet condition.
				if distBwd[cur.node] < math.MaxUint32 {
					cand := cur.dist + distBwd[cur.node]
					if cand < mu {
						mu = cand
					}
				}
				// Relax forward upward edges.
				fStart := ch.FwdFirstOut[cur.node]
				fEnd := ch.FwdFirstOut[cur.node+1]
				for e := fStart; e < fEnd; e++ {
					v := ch.FwdHead[e]
					newDist := cur.dist + ch.FwdWeight[e]
					if newDist < distFwd[v] {
						distFwd[v] = newDist
						fwdPQ = append(fwdPQ, item{v, newDist})
					}
				}
			}
		}

		// Backward step.
		if len(bwdPQ) > 0 && peekMin(bwdPQ) < mu {
			cur := popMin(&bwdPQ)
			if cur.dist <= distBwd[cur.node] {
				// Check meet condition.
				if distFwd[cur.node] < math.MaxUint32 {
					cand := distFwd[cur.node] + cur.dist
					if cand < mu {
						mu = cand
					}
				}
				// Relax backward upward edges.
				bStart := ch.BwdFirstOut[cur.node]
				bEnd := ch.BwdFirstOut[cur.node+1]
				for e := bStart; e < bEnd; e++ {
					v := ch.BwdHead[e]
					newDist := cur.dist + ch.BwdWeight[e]
					if newDist < distBwd[v] {
						distBwd[v] = newDist
						bwdPQ = append(bwdPQ, item{v, newDist})
					}
				}
			}
		}

		// Termination check.
		fwdMin := peekMin(fwdPQ)
		bwdMin := peekMin(bwdPQ)
		if fwdMin >= mu && bwdMin >= mu {
			break
		}
	}

	return mu
}

func TestContractSmallGraph(t *testing.T) {
	g := buildTestGraph()

	if g.NumNodes != 6 {
		t.Fatalf("test graph has %d nodes, want 6", g.NumNodes)
	}

	ch := Contract(g)

	if ch.NumNodes != 6 {
		t.Fatalf("CH has %d nodes, want 6", ch.NumNodes)
	}

	// Verify ranks are a permutation of 0..5.
	rankSeen := make(map[uint32]bool)
	for _, r := range ch.Rank {
		if r >= ch.NumNodes {
			t.Errorf("rank %d >= NumNodes %d", r, ch.NumNodes)
		}
		rankSeen[r] = true
	}
	if len(rankSeen) != int(ch.NumNodes) {
		t.Errorf("ranks are not a permutation: saw %d unique values, want %d", len(rankSeen), ch.NumNodes)
	}
}

func TestCHCorrectnessAllPairs(t *testing.T) {
	g := buildTestGraph()
	ch := Contract(g)

	// Compare CH Dijkstra against plain Dijkstra for all pairs.
	for s := uint32(0); s < g.NumNodes; s++ {
		for d := uint32(0); d < g.NumNodes; d++ {
			if s == d {
				continue
			}
			plainDist := plainDijkstra(g, s, d)
			chDist := chDijkstra(ch, s, d)
			if chDist != plainDist {
				t.Errorf("s=%d d=%d: CH=%d, Dijkstra=%d", s, d, chDist, plainDist)
			}
		}
	}
}

func TestContractSingleNode(t *testing.T) {
	result := &osmparser.ParseResult{
		Edges:   nil,
		NodeLat: map[osm.NodeID]float64{1: 1.0},
		NodeLon: map[osm.NodeID]float64{1: 103.0},
	}
	g := graph.Build(result)
	ch := Contract(g)
	if ch.NumNodes != 0 {
		// Empty graph since no edges.
		t.Logf("NumNodes=%d (empty graph expected)", ch.NumNodes)
	}
}

func TestContractLinearGraph(t *testing.T) {
	// Linear chain: 0 -> 1 -> 2 -> 3 -> 4 (all one-way)
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 100},
			{FromNodeID: 2, ToNodeID: 3, Weight: 200},
			{FromNodeID: 3, ToNodeID: 4, Weight: 300},
			{FromNodeID: 4, ToNodeID: 5, Weight: 400},
		},
		NodeLat: map[osm.NodeID]float64{1: 1.0, 2: 1.1, 3: 1.2, 4: 1.3, 5: 1.4},
		NodeLon: map[osm.NodeID]float64{1: 103.0, 2: 103.1, 3: 103.2, 4: 103.3, 5: 103.4},
	}
	g := graph.Build(result)
	ch := Contract(g)

	// Check that 0→4 distance = 100+200+300+400 = 1000.
	dist := chDijkstra(ch, 0, 4)
	expected := plainDijkstra(g, 0, 4)
	if dist != expected {
		t.Errorf("linear chain: CH=%d, Dijkstra=%d", dist, expected)
	}
}
