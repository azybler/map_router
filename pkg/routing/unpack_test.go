package routing

import (
	"context"
	"math"
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/ch"
	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

// parallelShortcutParse builds a graph where:
//   - A (lat=1.300) and B (lat=1.302) have a direct edge weight=100
//   - A-X (lat=1.301) costs 10, X-B costs 10, so cheapest A->B = 20 via X
//   - A also connects to C (lat=1.310), D (lat=1.320) at weight=200, and
//     B connects to E (lat=1.330) at weight=200, so A and B have higher
//     edge-difference scores than X. CH contracts X first.
//   - When X is contracted, scWeight=20 < direct A-B=100, so witness search
//     sees the 100-weight direct edge but 100 > 20, and a shortcut
//     A->B (middle=X, weight=20) is created PARALLEL to original A->B (weight=100).
//
// The resulting overlay has two Bwd edges B->A: one with middle=-1 (weight=100)
// and one with middle=X (weight=20). A naive findMiddle (first match) returns
// middle=-1 and unpack yields [A,B] at cost=100, not the correct [A,X,B] at cost=20.
func parallelShortcutParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1000, ToNodeID: 3000, Weight: 100}, {FromNodeID: 3000, ToNodeID: 1000, Weight: 100}, // A-B direct (expensive)
			{FromNodeID: 1000, ToNodeID: 2000, Weight: 10},  {FromNodeID: 2000, ToNodeID: 1000, Weight: 10},  // A-X cheap
			{FromNodeID: 2000, ToNodeID: 3000, Weight: 10},  {FromNodeID: 3000, ToNodeID: 2000, Weight: 10},  // X-B cheap
			{FromNodeID: 1000, ToNodeID: 5000, Weight: 200}, {FromNodeID: 5000, ToNodeID: 1000, Weight: 200}, // A-C (raises A priority)
			{FromNodeID: 1000, ToNodeID: 6000, Weight: 200}, {FromNodeID: 6000, ToNodeID: 1000, Weight: 200}, // A-D (raises A priority)
			{FromNodeID: 3000, ToNodeID: 7000, Weight: 200}, {FromNodeID: 7000, ToNodeID: 3000, Weight: 200}, // B-E (raises B priority)
		},
		NodeLat: map[osm.NodeID]float64{1000: 1.300, 2000: 1.301, 3000: 1.302, 5000: 1.310, 6000: 1.320, 7000: 1.330},
		NodeLon: map[osm.NodeID]float64{1000: 103.80, 2000: 103.80, 3000: 103.80, 5000: 103.80, 6000: 103.80, 7000: 103.80},
	}
}

// pathCostInOriginalGraph sums the minimum original-edge weight between each
// consecutive node pair, and fails if any pair is not connected by a real edge.
func pathCostInOriginalGraph(t *testing.T, g *graph.Graph, nodes []uint32) uint32 {
	t.Helper()
	var total uint32
	for i := 0; i+1 < len(nodes); i++ {
		u, v := nodes[i], nodes[i+1]
		best := uint32(math.MaxUint32)
		s, e := g.EdgesFrom(u)
		for j := s; j < e; j++ {
			if g.Head[j] == v && g.Weight[j] < best {
				best = g.Weight[j]
			}
		}
		if best == math.MaxUint32 {
			t.Fatalf("unpacked path has no real edge %d->%d (invalid path)", u, v)
		}
		total += best
	}
	return total
}

func TestUnpackParallelShortcut(t *testing.T) {
	g := graph.Build(parallelShortcutParse())
	chg := ch.Contract(g)
	eng := &Engine{chg: chg}

	idx := func(lat, lon float64) uint32 {
		for i := uint32(0); i < g.NumNodes; i++ {
			if g.NodeLat[i] == lat && g.NodeLon[i] == lon {
				return i
			}
		}
		t.Fatalf("node not found lat=%.3f lon=%.2f", lat, lon)
		return 0
	}
	s := idx(1.300, 103.80) // node A
	d := idx(1.302, 103.80) // node B

	qs := NewQueryState(chg.NumNodes)
	qs.touchFwd(s, 0)
	qs.FwdPQ.Push(s, 0)
	qs.touchBwd(d, 0)
	qs.BwdPQ.Push(d, 0)
	mu, meet := eng.runCHDijkstra(context.Background(), qs)
	if meet == noNode {
		t.Fatal("no route")
	}

	overlay := eng.reconstructOverlayPath(meet, qs.PredFwd, qs.PredBwd)
	origNodes := unpackOverlayPath(chg, overlay)

	// The unpacked path must be a VALID original-graph path whose summed
	// original-edge weight equals mu (the CH cost). With the bug, unpack returns
	// [A,B] whose direct edge weight is 100 != mu (20).
	cost := pathCostInOriginalGraph(t, g, origNodes)
	if cost != mu {
		t.Errorf("unpacked path cost %d != mu %d (nodes=%v) — findMiddle picked wrong parallel edge", cost, mu, origNodes)
	}
	if mu != 20 {
		t.Errorf("expected mu=20 via the cheap path A->X->B, got %d", mu)
	}
}
