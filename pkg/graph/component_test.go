package graph

import (
	"testing"

	"github.com/paulmach/osm"

	osmparser "github.com/azybler/map_router/pkg/osm"
)

func TestUnionFind(t *testing.T) {
	uf := NewUnionFind(5)

	// Initially all separate.
	for i := range uint32(5) {
		if uf.Find(i) != i {
			t.Errorf("Find(%d) = %d, want %d", i, uf.Find(i), i)
		}
	}

	// Union 0 and 1.
	uf.Union(0, 1)
	if uf.Find(0) != uf.Find(1) {
		t.Error("0 and 1 should be in same set")
	}

	// Union 2 and 3.
	uf.Union(2, 3)
	if uf.Find(2) != uf.Find(3) {
		t.Error("2 and 3 should be in same set")
	}

	// 0 and 2 should be different.
	if uf.Find(0) == uf.Find(2) {
		t.Error("0 and 2 should be in different sets")
	}

	// Union the two groups.
	uf.Union(1, 3)
	if uf.Find(0) != uf.Find(3) {
		t.Error("0 and 3 should now be in same set")
	}
}

func TestLargestComponent(t *testing.T) {
	// Graph with two components:
	// Component 1: 0 <-> 1 <-> 2 (3 nodes)
	// Component 2: 3 <-> 4 (2 nodes)
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			// Component 1
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 20, ToNodeID: 10, Weight: 100},
			{FromNodeID: 20, ToNodeID: 30, Weight: 200},
			{FromNodeID: 30, ToNodeID: 20, Weight: 200},
			// Component 2
			{FromNodeID: 40, ToNodeID: 50, Weight: 300},
			{FromNodeID: 50, ToNodeID: 40, Weight: 300},
		},
		NodeLat: map[osm.NodeID]float64{10: 1.0, 20: 1.1, 30: 1.2, 40: 2.0, 50: 2.1},
		NodeLon: map[osm.NodeID]float64{10: 103.0, 20: 103.1, 30: 103.2, 40: 104.0, 50: 104.1},
	}

	g := Build(result)
	nodes := LargestComponent(g)

	if len(nodes) != 3 {
		t.Fatalf("LargestComponent has %d nodes, want 3", len(nodes))
	}
}

func TestFilterToComponent(t *testing.T) {
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			// Component 1: triangle
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 20, ToNodeID: 30, Weight: 200},
			{FromNodeID: 30, ToNodeID: 10, Weight: 300},
			// Component 2: isolated pair
			{FromNodeID: 40, ToNodeID: 50, Weight: 400},
		},
		NodeLat: map[osm.NodeID]float64{10: 1.0, 20: 1.1, 30: 1.2, 40: 2.0, 50: 2.1},
		NodeLon: map[osm.NodeID]float64{10: 103.0, 20: 103.1, 30: 103.2, 40: 104.0, 50: 104.1},
	}

	g := Build(result)
	nodes := LargestComponent(g)
	filtered := FilterToComponent(g, nodes)

	if filtered.NumNodes != 3 {
		t.Fatalf("filtered NumNodes = %d, want 3", filtered.NumNodes)
	}
	if filtered.NumEdges != 3 {
		t.Fatalf("filtered NumEdges = %d, want 3", filtered.NumEdges)
	}

	// Verify CSR invariants on filtered graph.
	for i := uint32(1); i <= filtered.NumNodes; i++ {
		if filtered.FirstOut[i] < filtered.FirstOut[i-1] {
			t.Errorf("FirstOut not monotonic at %d", i)
		}
	}
	if filtered.FirstOut[filtered.NumNodes] != filtered.NumEdges {
		t.Error("FirstOut[NumNodes] != NumEdges")
	}
	for i, h := range filtered.Head {
		if h >= filtered.NumNodes {
			t.Errorf("Head[%d] = %d >= NumNodes %d", i, h, filtered.NumNodes)
		}
	}

	// Total weight should only include the triangle (100+200+300=600).
	var total uint32
	for _, w := range filtered.Weight {
		total += w
	}
	if total != 600 {
		t.Errorf("total weight = %d, want 600", total)
	}
}

func TestFilterToComponentEmptyGraph(t *testing.T) {
	g := &Graph{}
	nodes := LargestComponent(g)
	if nodes != nil {
		t.Errorf("expected nil for empty graph, got %v", nodes)
	}

	filtered := FilterToComponent(g, nil)
	if filtered.NumNodes != 0 || filtered.NumEdges != 0 {
		t.Errorf("expected empty graph, got %d nodes, %d edges", filtered.NumNodes, filtered.NumEdges)
	}
}
