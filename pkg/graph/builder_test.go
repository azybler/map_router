package graph

import (
	"testing"

	"github.com/paulmach/osm"

	osmparser "map_router/pkg/osm"
)

func TestBuildSimpleGraph(t *testing.T) {
	// Create a simple triangle graph: 0 -> 1 -> 2 -> 0
	//   Node 100: (1.0, 103.0)
	//   Node 200: (1.1, 103.0)
	//   Node 300: (1.0, 103.1)
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 100, ToNodeID: 200, Weight: 1000},
			{FromNodeID: 200, ToNodeID: 300, Weight: 2000},
			{FromNodeID: 300, ToNodeID: 100, Weight: 3000},
		},
		NodeLat: map[osm.NodeID]float64{100: 1.0, 200: 1.1, 300: 1.0},
		NodeLon: map[osm.NodeID]float64{100: 103.0, 200: 103.0, 300: 103.1},
	}

	g := Build(result)

	if g.NumNodes != 3 {
		t.Fatalf("NumNodes = %d, want 3", g.NumNodes)
	}
	if g.NumEdges != 3 {
		t.Fatalf("NumEdges = %d, want 3", g.NumEdges)
	}

	// Verify each node has exactly 1 outgoing edge.
	for i := uint32(0); i < g.NumNodes; i++ {
		start, end := g.EdgesFrom(i)
		count := end - start
		if count != 1 {
			t.Errorf("Node %d has %d edges, want 1", i, count)
		}
	}

	// Verify total weight.
	var totalWeight uint32
	for _, w := range g.Weight {
		totalWeight += w
	}
	if totalWeight != 6000 {
		t.Errorf("total weight = %d, want 6000", totalWeight)
	}
}

func TestBuildEmptyGraph(t *testing.T) {
	result := &osmparser.ParseResult{
		Edges:   nil,
		NodeLat: map[osm.NodeID]float64{},
		NodeLon: map[osm.NodeID]float64{},
	}

	g := Build(result)

	if g.NumNodes != 0 {
		t.Errorf("NumNodes = %d, want 0", g.NumNodes)
	}
	if g.NumEdges != 0 {
		t.Errorf("NumEdges = %d, want 0", g.NumEdges)
	}
}

func TestBuildBidirectionalEdges(t *testing.T) {
	// A <-> B (bidirectional = 2 directed edges)
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 500},
			{FromNodeID: 2, ToNodeID: 1, Weight: 500},
		},
		NodeLat: map[osm.NodeID]float64{1: 1.0, 2: 1.1},
		NodeLon: map[osm.NodeID]float64{1: 103.0, 2: 103.1},
	}

	g := Build(result)

	if g.NumNodes != 2 {
		t.Fatalf("NumNodes = %d, want 2", g.NumNodes)
	}
	if g.NumEdges != 2 {
		t.Fatalf("NumEdges = %d, want 2", g.NumEdges)
	}

	// Each node should have exactly 1 outgoing edge.
	for i := uint32(0); i < g.NumNodes; i++ {
		start, end := g.EdgesFrom(i)
		if end-start != 1 {
			t.Errorf("Node %d has %d edges, want 1", i, end-start)
		}
	}
}

func TestBuildCSRInvariants(t *testing.T) {
	// Star graph: center -> A, center -> B, center -> C
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 10, ToNodeID: 30, Weight: 200},
			{FromNodeID: 10, ToNodeID: 40, Weight: 300},
			{FromNodeID: 20, ToNodeID: 10, Weight: 100},
		},
		NodeLat: map[osm.NodeID]float64{10: 1.0, 20: 1.1, 30: 1.2, 40: 1.3},
		NodeLon: map[osm.NodeID]float64{10: 103.0, 20: 103.1, 30: 103.2, 40: 103.3},
	}

	g := Build(result)

	if g.NumNodes != 4 {
		t.Fatalf("NumNodes = %d, want 4", g.NumNodes)
	}
	if g.NumEdges != 4 {
		t.Fatalf("NumEdges = %d, want 4", g.NumEdges)
	}

	// CSR invariant: FirstOut is monotonically non-decreasing.
	for i := uint32(1); i <= g.NumNodes; i++ {
		if g.FirstOut[i] < g.FirstOut[i-1] {
			t.Errorf("FirstOut[%d]=%d < FirstOut[%d]=%d — not monotonic", i, g.FirstOut[i], i-1, g.FirstOut[i-1])
		}
	}

	// CSR invariant: FirstOut[NumNodes] == NumEdges.
	if g.FirstOut[g.NumNodes] != g.NumEdges {
		t.Errorf("FirstOut[%d]=%d != NumEdges=%d", g.NumNodes, g.FirstOut[g.NumNodes], g.NumEdges)
	}

	// All Head values < NumNodes.
	for i, h := range g.Head {
		if h >= g.NumNodes {
			t.Errorf("Head[%d]=%d >= NumNodes=%d", i, h, g.NumNodes)
		}
	}
}
