package graph

import (
	"testing"

	"github.com/paulmach/osm"

	osmparser "github.com/azybler/map_router/pkg/osm"
)

func hasEdgeByLon(g *Graph, fromLon, toLon float64) bool {
	for u := uint32(0); u < g.NumNodes; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			if g.NodeLon[u] == fromLon && g.NodeLon[g.Head[e]] == toLon {
				return true
			}
		}
	}
	return false
}

// culDeSacParse: public chain A(.80)<->B(.81)<->C(.82) plus a restricted spur
// B<->D(.815). D hangs off the public network only at B (1 public touch) => keep.
func culDeSacParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 100}, {FromNodeID: 2, ToNodeID: 1, Weight: 100},
			{FromNodeID: 2, ToNodeID: 3, Weight: 100}, {FromNodeID: 3, ToNodeID: 2, Weight: 100},
			{FromNodeID: 2, ToNodeID: 4, Weight: 100, Restricted: true}, {FromNodeID: 4, ToNodeID: 2, Weight: 100, Restricted: true},
		},
		NodeLat: map[osm.NodeID]float64{1: 1.300, 2: 1.300, 3: 1.300, 4: 1.301},
		NodeLon: map[osm.NodeID]float64{1: 103.80, 2: 103.81, 3: 103.82, 4: 103.815},
	}
}

// bridgeParse: public A(.80)<->B(.81)<->C(.82) PLUS a short restricted edge A<->C
// directly. The restricted cluster touches public at TWO nodes (A and C) => drop.
func bridgeParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 100}, {FromNodeID: 2, ToNodeID: 1, Weight: 100},
			{FromNodeID: 2, ToNodeID: 3, Weight: 100}, {FromNodeID: 3, ToNodeID: 2, Weight: 100},
			{FromNodeID: 1, ToNodeID: 3, Weight: 10, Restricted: true}, {FromNodeID: 3, ToNodeID: 1, Weight: 10, Restricted: true},
		},
		NodeLat: map[osm.NodeID]float64{1: 1.300, 2: 1.300, 3: 1.300},
		NodeLon: map[osm.NodeID]float64{1: 103.80, 2: 103.81, 3: 103.82},
	}
}

func TestFilterKeepsCulDeSac(t *testing.T) {
	g := FilterBridgingRestricted(Build(culDeSacParse()))
	if !hasEdgeByLon(g, 103.81, 103.815) || !hasEdgeByLon(g, 103.815, 103.81) {
		t.Error("cul-de-sac spur B<->D should be inlined (kept)")
	}
	if g.EdgeRestricted != nil {
		t.Error("output graph must not carry restricted flags")
	}
	// public edges intact
	if !hasEdgeByLon(g, 103.80, 103.81) || !hasEdgeByLon(g, 103.81, 103.82) {
		t.Error("public edges must be preserved")
	}
}

func TestFilterDropsBridge(t *testing.T) {
	g := FilterBridgingRestricted(Build(bridgeParse()))
	if hasEdgeByLon(g, 103.80, 103.82) || hasEdgeByLon(g, 103.82, 103.80) {
		t.Error("bridging restricted edge A<->C should be dropped")
	}
	if !hasEdgeByLon(g, 103.80, 103.81) || !hasEdgeByLon(g, 103.81, 103.82) {
		t.Error("public edges must be preserved")
	}
}

// interiorBridgeParse: public chain P0(.80)<->P1(.81)<->P2(.82), plus a restricted
// path P0<->R<->P2 through an interior-only node R(.815). The restricted cluster
// {P0,R,P2} touches the public network at TWO nodes (P0 and P2) => must be dropped,
// even though R itself is private-only.
func interiorBridgeParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 100}, {FromNodeID: 2, ToNodeID: 1, Weight: 100},
			{FromNodeID: 2, ToNodeID: 3, Weight: 100}, {FromNodeID: 3, ToNodeID: 2, Weight: 100},
			{FromNodeID: 1, ToNodeID: 9, Weight: 5, Restricted: true}, {FromNodeID: 9, ToNodeID: 1, Weight: 5, Restricted: true},
			{FromNodeID: 9, ToNodeID: 3, Weight: 5, Restricted: true}, {FromNodeID: 3, ToNodeID: 9, Weight: 5, Restricted: true},
		},
		NodeLat: map[osm.NodeID]float64{1: 1.300, 2: 1.300, 3: 1.300, 9: 1.301},
		NodeLon: map[osm.NodeID]float64{1: 103.80, 2: 103.81, 3: 103.82, 9: 103.815},
	}
}

func TestFilterDropsInteriorBridge(t *testing.T) {
	g := FilterBridgingRestricted(Build(interiorBridgeParse()))
	// The restricted cut-through via interior node R must be gone entirely.
	if hasEdgeByLon(g, 103.80, 103.815) || hasEdgeByLon(g, 103.815, 103.82) ||
		hasEdgeByLon(g, 103.815, 103.80) || hasEdgeByLon(g, 103.82, 103.815) {
		t.Error("restricted interior-blob bridge P0<->R<->P2 should be dropped (2 public touches)")
	}
	// Public chain intact.
	if !hasEdgeByLon(g, 103.80, 103.81) || !hasEdgeByLon(g, 103.81, 103.82) {
		t.Error("public chain must be preserved")
	}
}
