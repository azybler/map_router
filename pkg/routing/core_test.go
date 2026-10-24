package routing

import (
	"context"
	"math/rand"
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/ch"
	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

// buildGridGraph returns a side×side grid of two-way roads with random weights,
// a few of them one-way, so shortest paths wind through the grid.
func buildGridGraph(side int, seed int64) *graph.Graph {
	rng := rand.New(rand.NewSource(seed))
	res := &osmparser.ParseResult{NodeLat: map[osm.NodeID]float64{}, NodeLon: map[osm.NodeID]float64{}}
	id := func(r, c int) osm.NodeID { return osm.NodeID(r*side + c + 1) }
	for r := 0; r < side; r++ {
		for c := 0; c < side; c++ {
			res.NodeLat[id(r, c)] = 1.3 + float64(r)*0.001
			res.NodeLon[id(r, c)] = 103.8 + float64(c)*0.001
		}
	}
	link := func(a, b osm.NodeID) {
		w := uint32(50 + rng.Intn(500))
		res.Edges = append(res.Edges, osmparser.RawEdge{FromNodeID: a, ToNodeID: b, Weight: w})
		if rng.Intn(6) != 0 {
			res.Edges = append(res.Edges, osmparser.RawEdge{FromNodeID: b, ToNodeID: a, Weight: w})
		}
	}
	for r := 0; r < side; r++ {
		for c := 0; c < side; c++ {
			if c+1 < side {
				link(id(r, c), id(r, c+1))
			}
			if r+1 < side {
				link(id(r, c), id(r+1, c))
			}
		}
	}
	g := graph.Build(res)
	return graph.FilterToComponent(g, graph.LargestComponent(g))
}

func chQuery(chg *graph.CHGraph, s, d uint32) (uint32, []uint32) {
	qs := NewQueryState(chg.NumNodes)
	qs.touchFwd(s, 0)
	qs.FwdPQ.Push(s, 0)
	qs.touchBwd(d, 0)
	qs.BwdPQ.Push(d, 0)
	eng := &Engine{chg: chg}
	mu, meet := eng.runCHDijkstra(context.Background(), qs)
	if meet == noNode {
		return mu, nil
	}
	return mu, unpackOverlayPath(chg, eng.reconstructOverlayPath(meet, qs.PredFwd, qs.PredBwd))
}

// TestClosedCoreIsExact forces a large uncontracted core and checks that, with
// the core closed, every pair's CH distance and unpacked path match plain
// Dijkstra.
func TestClosedCoreIsExact(t *testing.T) {
	g := buildGridGraph(9, 7)
	chg := ch.ContractWith(g, ch.Options{MaxShortcutsPerNode: 2, CloseCore: true})
	for s := uint32(0); s < g.NumNodes; s++ {
		for d := uint32(0); d < g.NumNodes; d++ {
			if s == d {
				continue
			}
			want := plainDijkstra(g, s, d)
			mu, path := chQuery(chg, s, d)
			if mu != want {
				t.Fatalf("s=%d d=%d: CH=%d, Dijkstra=%d", s, d, mu, want)
			}
			if path[0] != s || path[len(path)-1] != d {
				t.Fatalf("s=%d d=%d: path runs %d→%d", s, d, path[0], path[len(path)-1])
			}
			var sum uint32
			for i := 0; i+1 < len(path); i++ {
				e := cheapestEdge(g, path[i], path[i+1])
				if e == noNode {
					t.Fatalf("s=%d d=%d: unpacked hop %d→%d is not an edge", s, d, path[i], path[i+1])
				}
				sum += g.Weight[e]
			}
			if sum != want {
				t.Fatalf("s=%d d=%d: unpacked path weighs %d, want %d", s, d, sum, want)
			}
		}
	}
}

// TestOpenCoreMissesRoutes documents what CloseCore fixes: on the same graph
// and core, the plain upward query misses the optimum for some pairs.
func TestOpenCoreMissesRoutes(t *testing.T) {
	g := buildGridGraph(9, 7)
	chg := ch.ContractWith(g, ch.Options{MaxShortcutsPerNode: 2})
	wrong := 0
	for s := uint32(0); s < g.NumNodes; s++ {
		for d := uint32(0); d < g.NumNodes; d++ {
			if s == d {
				continue
			}
			if mu, _ := chQuery(chg, s, d); mu != plainDijkstra(g, s, d) {
				if mu < plainDijkstra(g, s, d) {
					t.Fatalf("s=%d d=%d: CH=%d beats Dijkstra", s, d, mu)
				}
				wrong++
			}
		}
	}
	if wrong == 0 {
		t.Fatal("expected the open core to miss some optima; the test graph no longer exercises the core")
	}
	t.Logf("open core: %d pairs above the optimum", wrong)
}
