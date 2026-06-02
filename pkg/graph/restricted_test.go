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

// culDeSacParse: public chain A(.80)<->B(.81)<->C(.82) + restricted spur B<->D(.815).
// D touches public at ≤1 node => always inlined.
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

// fastBridgeParse: public A(.80)<->B(.81)<->C(.82) (A→C public = 200) + a FAST
// restricted cut-through A<->C (weight 10). through(A,C)=10 < 200 => shortcut => DROP.
func fastBridgeParse() *osmparser.ParseResult {
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

// interiorFastBridgeParse: public A(.80)<->B(.81)<->C(.82) + a fast restricted path
// A<->R(.815)<->C through interior node R (5+5=10 < 200) => shortcut => DROP.
func interiorFastBridgeParse() *osmparser.ParseResult {
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

// slowEstateParse: public A(.80)<->B(.81)<->C(.82) (A→C public = 200) + a SLOW
// multi-gate estate A<->D(.815)<->C (5000+5000=10000 >> 200). through > public =>
// NOT a shortcut => INLINE (D reachable via either gate). This is the key case the
// cul-de-sac rule wrongly dropped.
func slowEstateParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 100}, {FromNodeID: 2, ToNodeID: 1, Weight: 100},
			{FromNodeID: 2, ToNodeID: 3, Weight: 100}, {FromNodeID: 3, ToNodeID: 2, Weight: 100},
			{FromNodeID: 1, ToNodeID: 4, Weight: 5000, Restricted: true}, {FromNodeID: 4, ToNodeID: 1, Weight: 5000, Restricted: true},
			{FromNodeID: 4, ToNodeID: 3, Weight: 5000, Restricted: true}, {FromNodeID: 3, ToNodeID: 4, Weight: 5000, Restricted: true},
		},
		NodeLat: map[osm.NodeID]float64{1: 1.300, 2: 1.300, 3: 1.300, 4: 1.301},
		NodeLon: map[osm.NodeID]float64{1: 103.80, 2: 103.81, 3: 103.82, 4: 103.815},
	}
}

func TestFilterKeepsCulDeSac(t *testing.T) {
	g := FilterBridgingRestricted(Build(culDeSacParse()))
	if !hasEdgeByLon(g, 103.81, 103.815) || !hasEdgeByLon(g, 103.815, 103.81) {
		t.Error("cul-de-sac spur should be inlined")
	}
	if g.EdgeRestricted != nil {
		t.Error("output must carry no restricted flags")
	}
}

func TestFilterDropsFastBridge(t *testing.T) {
	g := FilterBridgingRestricted(Build(fastBridgeParse()))
	if hasEdgeByLon(g, 103.80, 103.82) || hasEdgeByLon(g, 103.82, 103.80) {
		t.Error("fast restricted cut-through A<->C should be dropped")
	}
	if !hasEdgeByLon(g, 103.80, 103.81) || !hasEdgeByLon(g, 103.81, 103.82) {
		t.Error("public chain must be preserved")
	}
}

func TestFilterDropsInteriorFastBridge(t *testing.T) {
	g := FilterBridgingRestricted(Build(interiorFastBridgeParse()))
	if hasEdgeByLon(g, 103.80, 103.815) || hasEdgeByLon(g, 103.815, 103.82) ||
		hasEdgeByLon(g, 103.815, 103.80) || hasEdgeByLon(g, 103.82, 103.815) {
		t.Error("fast restricted interior cut-through should be dropped")
	}
}

func TestFilterKeepsSlowMultiGateEstate(t *testing.T) {
	g := FilterBridgingRestricted(Build(slowEstateParse()))
	// Slow multi-gate estate is NOT a shortcut => inlined (both spurs kept).
	if !hasEdgeByLon(g, 103.80, 103.815) || !hasEdgeByLon(g, 103.815, 103.82) {
		t.Error("slow multi-gate estate should be inlined (through-time > public-time)")
	}
	if !hasEdgeByLon(g, 103.80, 103.81) || !hasEdgeByLon(g, 103.81, 103.82) {
		t.Error("public chain must be preserved")
	}
}
