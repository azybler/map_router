package routing

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/ch"
	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

// estateParse builds a public road with a gated estate beside it:
//
//	E1 ======== E2      estate road, restricted, ~670 m north
//	|            |      restricted gate roads
//	A ---------- B      public, but publicDetourMs slow (the long way round)
//	|            |
//	P0           P3     public
//
// Every restricted edge weighs 5 s, so cutting through A→E1→E2→B (15 s) beats
// the public A→B edge and FilterBridgingRestricted penalizes the whole estate —
// by 7× for a 100 s detour, by the 50× cap for anything much slower. With
// restricted=false the same roads are all public and nothing is penalized.
func estateParse(restricted bool, publicDetourMs uint32) *osmparser.ParseResult {
	const estateMs = 5000
	two := func(a, b osm.NodeID, w uint32, r bool) []osmparser.RawEdge {
		return []osmparser.RawEdge{
			{FromNodeID: a, ToNodeID: b, Weight: w, Restricted: r},
			{FromNodeID: b, ToNodeID: a, Weight: w, Restricted: r},
		}
	}
	var edges []osmparser.RawEdge
	edges = append(edges, two(1, 2, 10000, false)...) // P0–A
	edges = append(edges, two(2, 3, publicDetourMs, false)...)
	edges = append(edges, two(3, 4, 10000, false)...) // B–P3
	edges = append(edges, two(2, 5, estateMs, restricted)...)
	edges = append(edges, two(5, 6, estateMs, restricted)...)
	edges = append(edges, two(6, 3, estateMs, restricted)...)
	return &osmparser.ParseResult{
		Edges:   edges,
		NodeLat: map[osm.NodeID]float64{1: 3.000, 2: 3.000, 3: 3.000, 4: 3.000, 5: 3.006, 6: 3.006},
		NodeLon: map[osm.NodeID]float64{1: 101.590, 2: 101.600, 3: 101.620, 4: 101.630, 5: 101.600, 6: 101.620},
	}
}

// preprocessed runs a parse through the preprocessing pipeline the server's
// graphs go through, returning the routing graph and an engine over it.
func preprocessed(t *testing.T, p *osmparser.ParseResult) (*graph.Graph, *Engine) {
	t.Helper()
	g := graph.FilterBridgingRestricted(graph.Build(p))
	g = graph.FilterToComponent(g, graph.LargestComponent(g))
	return g, NewEngine(ch.Contract(g), g)
}

// TestDurationIgnoresEstatePenalty is #1937: a route that ends inside a
// penalized estate must report the time it takes to drive, which is the same
// duration the identical route has on a graph where nothing is penalized.
// Before the fix it reported the penalized weight — 7× or 50× too slow inside
// the estate.
func TestDurationIgnoresEstatePenalty(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		detourMs uint32
		factor   uint32 // the estate's expected penalty
	}{
		{"7x penalty", 100000, 7},
		{"50x cap", 10000000, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gPen, penalized := preprocessed(t, estateParse(true, tc.detourMs))
			gPlain, plain := preprocessed(t, estateParse(false, tc.detourMs))
			nP0, nA := nodeIndex(gPen, 3.000, 101.590), nodeIndex(gPen, 3.000, 101.600)
			nE1, nE2 := nodeIndex(gPen, 3.006, 101.600), nodeIndex(gPen, 3.006, 101.620)
			if w := gPen.Weight[findDirectedEdge(t, gPen, nE1, nE2)]; w != 5000*tc.factor {
				t.Fatalf("fixture: estate edge weighs %d, want 5000 × %d", w, tc.factor)
			}

			// From mid P0–A to mid E1–E2: drive to A (5 s), up to E1 (5 s), half of
			// E1–E2 (2.5 s) = 12.5 s on either graph.
			const wantSec = 12.5
			wantCost := 5000 + 5000*tc.factor + 2500*tc.factor

			t.Run("RouteBetweenSnaps", func(t *testing.T) {
				res, err := penalized.RouteBetweenSnaps(ctx, snapOnEdge(t, gPen, nP0, nA, 0.5), snapOnEdge(t, gPen, nE1, nE2, 0.5))
				if err != nil {
					t.Fatal(err)
				}
				ref, err := plain.RouteBetweenSnaps(ctx, snapOnEdge(t, gPlain, nP0, nA, 0.5), snapOnEdge(t, gPlain, nE1, nE2, 0.5))
				if err != nil {
					t.Fatal(err)
				}
				if res.DurationSeconds != wantSec || ref.DurationSeconds != wantSec {
					t.Errorf("duration %.3f s (unpenalized graph: %.3f s), want %.1f s on both", res.DurationSeconds, ref.DurationSeconds, wantSec)
				}
				if res.Cost != wantCost {
					t.Errorf("Cost = %d, want the penalized total %d", res.Cost, wantCost)
				}
				if math.Abs(res.TotalDistanceMeters-ref.TotalDistanceMeters) > 0.01 {
					t.Errorf("fixture: routes differ (%.1f m vs %.1f m), so durations are not comparable", res.TotalDistanceMeters, ref.TotalDistanceMeters)
				}
			})

			// Route snaps free points ~22 m off the road (the only road within 500 m
			// of each), and prices that with an access penalty. The penalty steers
			// the choice of road; it is not driving time.
			t.Run("Route", func(t *testing.T) {
				from, to := LatLng{Lat: 2.9998, Lng: 101.595}, LatLng{Lat: 3.0058, Lng: 101.610}
				res, err := penalized.Route(ctx, from, to)
				if err != nil {
					t.Fatal(err)
				}
				ref, err := plain.Route(ctx, from, to)
				if err != nil {
					t.Fatal(err)
				}
				if res.DurationSeconds != wantSec || ref.DurationSeconds != wantSec {
					t.Errorf("duration %.3f s (unpenalized graph: %.3f s), want %.1f s on both", res.DurationSeconds, ref.DurationSeconds, wantSec)
				}
				if res.Cost <= wantCost {
					t.Errorf("Cost = %d, want the penalized total %d plus access penalties", res.Cost, wantCost)
				}
			})

			t.Run("same edge", func(t *testing.T) {
				res, err := penalized.RouteBetweenSnaps(ctx, snapOnEdge(t, gPen, nE1, nE2, 0.2), snapOnEdge(t, gPen, nE1, nE2, 0.6))
				if err != nil {
					t.Fatal(err)
				}
				if res.DurationSeconds != 2 {
					t.Errorf("duration %.3f s for 40%% of a 5 s estate road, want 2 s", res.DurationSeconds)
				}
				if res.Cost != 2000*tc.factor {
					t.Errorf("Cost = %d, want %d", res.Cost, 2000*tc.factor)
				}
			})
		})
	}
}

// TestDurationExcludesAccessPenalty: Route's off-road access penalty never
// reaches the reported duration.
func TestDurationExcludesAccessPenalty(t *testing.T) {
	g := graph.Build(&osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 20000}, {FromNodeID: 2, ToNodeID: 1, Weight: 20000},
			{FromNodeID: 2, ToNodeID: 3, Weight: 30000}, {FromNodeID: 3, ToNodeID: 2, Weight: 30000},
		},
		NodeLat: map[osm.NodeID]float64{1: 3.000, 2: 3.000, 3: 3.000},
		NodeLon: map[osm.NodeID]float64{1: 101.600, 2: 101.602, 3: 101.604},
	})
	e := NewEngine(ch.Contract(g), g)
	// ~50 m south of the middle of each edge: half of each edge = 10 s + 15 s.
	res, err := e.Route(context.Background(), LatLng{Lat: 2.99955, Lng: 101.601}, LatLng{Lat: 2.99955, Lng: 101.603})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.DurationSeconds-25) > 0.002 {
		t.Errorf("duration %.3f s, want 25 s: the drive along the road, without access penalty", res.DurationSeconds)
	}
	if float64(res.Cost) < 25000+2*accessPenaltyMult*45*100 {
		t.Errorf("Cost = %d, want the drive plus both ~50 m access penalties", res.Cost)
	}
}

// TestDurationMatchesSearchWithoutPenalties: on a graph with nothing penalized,
// RouteBetweenSnaps' duration is exactly what the search minimized. That holds
// only if the path walk prices the same edges and partial edges the search
// did, so it guards every route shape — parallel edges, one-ways, both seed
// directions, same-edge trips — not just the estate fixtures above.
func TestDurationMatchesSearchWithoutPenalties(t *testing.T) {
	rng := rand.New(rand.NewPCG(1937, 1))
	for trial := range 40 {
		g := randomGrid(rng, 6)
		e := NewEngine(ch.Contract(g), g)
		for range 50 {
			from, to := randomSnap(rng, g), randomSnap(rng, g)
			res, err := e.RouteBetweenSnaps(context.Background(), from, to)
			if err != nil {
				continue // one-way corners can be unreachable
			}
			if got := math.Round(res.DurationSeconds * 1000); got != float64(res.Cost) {
				t.Fatalf("trial %d: duration %.0f ms != Cost %d ms for %+v → %+v", trial, got, res.Cost, from, to)
			}
		}
	}
}

// randomGrid builds an n×n grid of streets with random weights, some one-way,
// and some node pairs joined twice (parallel edges of different weights).
func randomGrid(rng *rand.Rand, n int) *graph.Graph {
	p := &osmparser.ParseResult{NodeLat: map[osm.NodeID]float64{}, NodeLon: map[osm.NodeID]float64{}}
	id := func(r, c int) osm.NodeID { return osm.NodeID(r*n + c + 1) }
	link := func(a, b osm.NodeID) {
		w := uint32(1000 + rng.IntN(60000))
		p.Edges = append(p.Edges, osmparser.RawEdge{FromNodeID: a, ToNodeID: b, Weight: w})
		if rng.IntN(4) > 0 { // 1 in 4 streets is one-way
			p.Edges = append(p.Edges, osmparser.RawEdge{FromNodeID: b, ToNodeID: a, Weight: w})
		}
		if rng.IntN(8) == 0 {
			p.Edges = append(p.Edges, osmparser.RawEdge{FromNodeID: a, ToNodeID: b, Weight: w/2 + 1})
		}
	}
	for r := range n {
		for c := range n {
			p.NodeLat[id(r, c)] = 3.0 + float64(r)*0.001
			p.NodeLon[id(r, c)] = 101.6 + float64(c)*0.001
			if c+1 < n {
				link(id(r, c), id(r, c+1))
			}
			if r+1 < n {
				link(id(r, c), id(r+1, c))
			}
		}
	}
	return graph.Build(p)
}

// randomSnap returns a position on a random edge.
func randomSnap(rng *rand.Rand, g *graph.Graph) SnapResult {
	e := uint32(rng.IntN(int(g.NumEdges)))
	u := uint32(0)
	for g.FirstOut[u+1] <= e {
		u++
	}
	return SnapResult{EdgeIdx: e, NodeU: u, NodeV: g.Head[e], Ratio: rng.Float64()}
}

func TestCheapestEdgePicksLowestWeightParallel(t *testing.T) {
	g := graph.Build(&osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 900},
			{FromNodeID: 1, ToNodeID: 2, Weight: 300},
			{FromNodeID: 1, ToNodeID: 2, Weight: 600},
		},
		NodeLat: map[osm.NodeID]float64{1: 3.0, 2: 3.0},
		NodeLon: map[osm.NodeID]float64{1: 101.600, 2: 101.601},
	})
	if e := cheapestEdge(g, 0, 1); e == noNode || g.Weight[e] != 300 {
		t.Errorf("cheapestEdge picked edge %d, want the 300 one", e)
	}
	if e := cheapestEdge(g, 1, 0); e != noNode {
		t.Errorf("cheapestEdge(1, 0) = %d, want noNode (no such edge)", e)
	}
}
