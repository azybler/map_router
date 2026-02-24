package routing

import (
	"context"
	"math"
	"testing"

	"github.com/paulmach/osm"

	"map_router/pkg/ch"
	"map_router/pkg/graph"
	osmparser "map_router/pkg/osm"
)

// buildTestGraphAndCH creates a test graph and its CH overlay.
//
//	0 ---100--- 1 ---200--- 2
//	|                       |
//	300                    400
//	|                       |
//	3 ---500--- 4 ---600--- 5
//
// All edges bidirectional. Weights in millimeters.
func buildTestGraphAndCH(t *testing.T) (*graph.Graph, *graph.CHGraph) {
	t.Helper()
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 20, ToNodeID: 10, Weight: 100},
			{FromNodeID: 20, ToNodeID: 30, Weight: 200},
			{FromNodeID: 30, ToNodeID: 20, Weight: 200},
			{FromNodeID: 10, ToNodeID: 40, Weight: 300},
			{FromNodeID: 40, ToNodeID: 10, Weight: 300},
			{FromNodeID: 30, ToNodeID: 60, Weight: 400},
			{FromNodeID: 60, ToNodeID: 30, Weight: 400},
			{FromNodeID: 40, ToNodeID: 50, Weight: 500},
			{FromNodeID: 50, ToNodeID: 40, Weight: 500},
			{FromNodeID: 50, ToNodeID: 60, Weight: 600},
			{FromNodeID: 60, ToNodeID: 50, Weight: 600},
		},
		NodeLat: map[osm.NodeID]float64{10: 1.300, 20: 1.300, 30: 1.300, 40: 1.301, 50: 1.301, 60: 1.301},
		NodeLon: map[osm.NodeID]float64{10: 103.800, 20: 103.801, 30: 103.802, 40: 103.800, 50: 103.801, 60: 103.802},
	}
	g := graph.Build(result)
	chg := ch.Contract(g)
	return g, chg
}

// plainDijkstra runs standard Dijkstra on the original graph.
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

func TestCHDijkstraCorrectness(t *testing.T) {
	g, chg := buildTestGraphAndCH(t)

	// Test all pairs using the CH Dijkstra directly (node-to-node).
	for s := uint32(0); s < g.NumNodes; s++ {
		for d := uint32(0); d < g.NumNodes; d++ {
			if s == d {
				continue
			}

			expected := plainDijkstra(g, s, d)

			// Run CH Dijkstra.
			qs := NewQueryState(chg.NumNodes)
			qs.touchFwd(s, 0)
			qs.FwdPQ.Push(s, 0)
			qs.touchBwd(d, 0)
			qs.BwdPQ.Push(d, 0)

			eng := &Engine{chg: chg}
			mu, _ := eng.runCHDijkstra(context.Background(), qs)

			if mu != expected {
				t.Errorf("s=%d d=%d: CH=%d, Dijkstra=%d", s, d, mu, expected)
			}
		}
	}
}

func TestMinHeap(t *testing.T) {
	var h MinHeap

	h.Push(1, 30)
	h.Push(2, 10)
	h.Push(3, 20)

	if h.PeekDist() != 10 {
		t.Errorf("PeekDist = %d, want 10", h.PeekDist())
	}

	item := h.Pop()
	if item.Node != 2 || item.Dist != 10 {
		t.Errorf("Pop = {%d, %d}, want {2, 10}", item.Node, item.Dist)
	}

	item = h.Pop()
	if item.Node != 3 || item.Dist != 20 {
		t.Errorf("Pop = {%d, %d}, want {3, 20}", item.Node, item.Dist)
	}

	item = h.Pop()
	if item.Node != 1 || item.Dist != 30 {
		t.Errorf("Pop = {%d, %d}, want {1, 30}", item.Node, item.Dist)
	}

	if h.Len() != 0 {
		t.Errorf("Len = %d, want 0", h.Len())
	}
}

func BenchmarkCHDijkstra(b *testing.B) {
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 20, ToNodeID: 10, Weight: 100},
			{FromNodeID: 20, ToNodeID: 30, Weight: 200},
			{FromNodeID: 30, ToNodeID: 20, Weight: 200},
			{FromNodeID: 10, ToNodeID: 40, Weight: 300},
			{FromNodeID: 40, ToNodeID: 10, Weight: 300},
			{FromNodeID: 30, ToNodeID: 60, Weight: 400},
			{FromNodeID: 60, ToNodeID: 30, Weight: 400},
			{FromNodeID: 40, ToNodeID: 50, Weight: 500},
			{FromNodeID: 50, ToNodeID: 40, Weight: 500},
			{FromNodeID: 50, ToNodeID: 60, Weight: 600},
			{FromNodeID: 60, ToNodeID: 50, Weight: 600},
		},
		NodeLat: map[osm.NodeID]float64{10: 1.300, 20: 1.300, 30: 1.300, 40: 1.301, 50: 1.301, 60: 1.301},
		NodeLon: map[osm.NodeID]float64{10: 103.800, 20: 103.801, 30: 103.802, 40: 103.800, 50: 103.801, 60: 103.802},
	}
	g := graph.Build(result)
	chg := ch.Contract(g)
	eng := NewEngine(chg, g)

	ctx := context.Background()
	start := LatLng{Lat: 1.300, Lng: 103.800}
	end := LatLng{Lat: 1.301, Lng: 103.802}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = eng.Route(ctx, start, end)
	}
}

func TestRouteEndToEnd(t *testing.T) {
	g, chg := buildTestGraphAndCH(t)
	eng := NewEngine(chg, g)

	// Route from near node 0 to near node 5.
	result, err := eng.Route(context.Background(),
		LatLng{Lat: 1.300, Lng: 103.800},   // near node 0
		LatLng{Lat: 1.301, Lng: 103.802},   // near node 5
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if result.TotalDistanceMeters <= 0 {
		t.Errorf("TotalDistanceMeters = %f, want > 0", result.TotalDistanceMeters)
	}
}
