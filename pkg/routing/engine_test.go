package routing

import (
	"math"
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

// oneWayParse builds: one-way 0->1 (weight 100), two-way 1<->2 (weight 100).
func oneWayParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 20, Weight: 100}, // 0->1 only (one-way)
			{FromNodeID: 20, ToNodeID: 30, Weight: 100}, // 1->2
			{FromNodeID: 30, ToNodeID: 20, Weight: 100}, // 2->1
		},
		NodeLat: map[osm.NodeID]float64{10: 1.300, 20: 1.300, 30: 1.300},
		NodeLon: map[osm.NodeID]float64{10: 103.800, 20: 103.801, 30: 103.802},
	}
}

// nodeIndex finds the compact index whose coords match the given lat/lon.
func nodeIndex(g *graph.Graph, lat, lon float64) uint32 {
	for i := uint32(0); i < g.NumNodes; i++ {
		if g.NodeLat[i] == lat && g.NodeLon[i] == lon {
			return i
		}
	}
	return noNode
}

func TestSeedForwardRespectsOneWay(t *testing.T) {
	g := graph.Build(oneWayParse())
	n0 := nodeIndex(g, 1.300, 103.800) // tail of the one-way edge
	n1 := nodeIndex(g, 1.300, 103.801) // head of the one-way edge

	// Find the directed edge 0->1.
	var edgeIdx uint32 = noNode
	s, e := g.EdgesFrom(n0)
	for i := s; i < e; i++ {
		if g.Head[i] == n1 {
			edgeIdx = i
			break
		}
	}
	if edgeIdx == noNode {
		t.Fatal("edge n0->n1 not found")
	}

	qs := NewQueryState(g.NumNodes)
	// Snap at the middle of one-way edge n0->n1.
	seedForward(qs, g, SnapResult{EdgeIdx: edgeIdx, NodeU: n0, NodeV: n1, Ratio: 0.5})

	// v (n1) must be seeded — legal forward travel.
	if qs.DistFwd[n1] == math.MaxUint32 {
		t.Errorf("expected n1 (head) to be seeded")
	}
	// u (n0) must NOT be seeded — no reverse edge n1->n0 exists.
	if qs.DistFwd[n0] != math.MaxUint32 {
		t.Errorf("expected n0 (tail) NOT to be seeded on a one-way edge, got %d", qs.DistFwd[n0])
	}
}

func TestSeedForwardTwoWaySeedsBoth(t *testing.T) {
	g := graph.Build(oneWayParse())
	n1 := nodeIndex(g, 1.300, 103.801)
	n2 := nodeIndex(g, 1.300, 103.802)

	var edgeIdx uint32 = noNode
	s, e := g.EdgesFrom(n1)
	for i := s; i < e; i++ {
		if g.Head[i] == n2 {
			edgeIdx = i
			break
		}
	}
	if edgeIdx == noNode {
		t.Fatal("edge n1->n2 not found")
	}

	qs := NewQueryState(g.NumNodes)
	seedForward(qs, g, SnapResult{EdgeIdx: edgeIdx, NodeU: n1, NodeV: n2, Ratio: 0.5})

	if qs.DistFwd[n2] == math.MaxUint32 || qs.DistFwd[n1] == math.MaxUint32 {
		t.Errorf("expected both endpoints seeded on a two-way edge")
	}
}

func TestSeedBackwardRespectsOneWay(t *testing.T) {
	g := graph.Build(oneWayParse())
	n0 := nodeIndex(g, 1.300, 103.800) // tail of the one-way edge
	n1 := nodeIndex(g, 1.300, 103.801) // head of the one-way edge

	var edgeIdx uint32 = noNode
	s, e := g.EdgesFrom(n0)
	for i := s; i < e; i++ {
		if g.Head[i] == n1 {
			edgeIdx = i
			break
		}
	}
	if edgeIdx == noNode {
		t.Fatal("edge n0->n1 not found")
	}

	qs := NewQueryState(g.NumNodes)
	seedBackward(qs, g, SnapResult{EdgeIdx: edgeIdx, NodeU: n0, NodeV: n1, Ratio: 0.5})

	// u (n0) must be seeded — arriving from u (travel u->v) is always legal.
	if qs.DistBwd[n0] == math.MaxUint32 {
		t.Errorf("expected n0 (tail) to be seeded in backward search")
	}
	// v (n1) must NOT be seeded — no reverse edge n1->n0 exists.
	if qs.DistBwd[n1] != math.MaxUint32 {
		t.Errorf("expected n1 (head) NOT to be seeded backward on a one-way edge, got %d", qs.DistBwd[n1])
	}
}
