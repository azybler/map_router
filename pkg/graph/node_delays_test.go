package graph

import (
	"testing"

	"github.com/paulmach/osm"

	osmparser "github.com/azybler/map_router/pkg/osm"
)

// TestAddNodeDelaysJunctions checks each junction approach class on a small network:
//
//	1 ==primary(100)== 2 ==primary(100)== 3
//	                   |
//	          residential(200)
//	                   |
//	                   4 --residential(300)-- 5 --residential(400)-- 6
//	                                          |
//	                                  residential(400)
//	                                          |
//	                                          7
//
// Node 2 is a T where a residential road meets a primary one; node 5 joins two
// residential roads; every other node has fewer than three neighbours.
func TestAddNodeDelaysJunctions(t *testing.T) {
	type seg struct {
		a, b  osm.NodeID
		way   osm.WayID
		class uint8
	}
	segs := []seg{
		{1, 2, 100, 5}, {2, 3, 100, 5},
		{2, 4, 200, 1},
		{4, 5, 300, 1},
		{5, 6, 400, 1}, {5, 7, 400, 1},
	}
	res := &osmparser.ParseResult{NodeLat: map[osm.NodeID]float64{}, NodeLon: map[osm.NodeID]float64{}}
	for i := osm.NodeID(1); i <= 7; i++ {
		res.NodeLat[i], res.NodeLon[i] = 1.3+float64(i)*0.001, 103.8
	}
	for _, s := range segs {
		res.Edges = append(res.Edges,
			osmparser.RawEdge{FromNodeID: s.a, ToNodeID: s.b, Weight: 1000, Way: s.way, Class: s.class},
			osmparser.RawEdge{FromNodeID: s.b, ToNodeID: s.a, Weight: 1000, Way: s.way, Class: s.class})
	}
	g := Build(res)
	d := NodeDelays{JunctionMajor: 100, JunctionEqual: 200, JunctionMinor: 300}
	if n := AddNodeDelays(g, d); n != 6 {
		t.Errorf("delayed %d edges, want 6 (three into node 2, three into node 5)", n)
	}

	// Index nodes by OSM id: Build numbers them in order of first appearance.
	idx := map[osm.NodeID]uint32{}
	for _, e := range res.Edges {
		for _, id := range []osm.NodeID{e.FromNodeID, e.ToNodeID} {
			if _, ok := idx[id]; !ok {
				idx[id] = uint32(len(idx))
			}
		}
	}
	weight := func(a, b osm.NodeID) uint32 {
		u, v := idx[a], idx[b]
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			if g.Head[e] == v {
				return g.Weight[e]
			}
		}
		t.Fatalf("no edge %d→%d", a, b)
		return 0
	}
	for _, c := range []struct {
		a, b osm.NodeID
		want uint32
		why  string
	}{
		{1, 2, 1100, "primary into a T with a residential road: major"},
		{3, 2, 1100, "primary from the other side: major"},
		{4, 2, 1300, "residential into the primary road: minor"},
		{4, 5, 1200, "residential meeting another residential road: equal"},
		{6, 5, 1200, "the other residential road, its own way excluded: equal"},
		{2, 1, 1000, "node 1 is a dead end"},
		{2, 4, 1000, "node 4 has two neighbours"},
	} {
		if got := weight(c.a, c.b); got != c.want {
			t.Errorf("%d→%d weighs %d, want %d (%s)", c.a, c.b, got, c.want, c.why)
		}
	}
}

// TestAddNodeDelaysSignalAndToll charges a signal or toll booth on entering
// its node, even mid-road.
func TestAddNodeDelaysSignalAndToll(t *testing.T) {
	res := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 1, ToNodeID: 2, Weight: 1000, Way: 1, Class: 3, ToSignal: true},
			{FromNodeID: 2, ToNodeID: 1, Weight: 1000, Way: 1, Class: 3},
			{FromNodeID: 2, ToNodeID: 3, Weight: 1000, Way: 1, Class: 3, ToToll: true},
			{FromNodeID: 3, ToNodeID: 2, Weight: 1000, Way: 1, Class: 3, ToSignal: true},
		},
		NodeLat: map[osm.NodeID]float64{1: 1.300, 2: 1.301, 3: 1.302},
		NodeLon: map[osm.NodeID]float64{1: 103.8, 2: 103.8, 3: 103.8},
	}
	g := Build(res)
	d := NodeDelays{JunctionMajor: 7, JunctionEqual: 7, JunctionMinor: 7, Signal: 500, TollBooth: 900}
	if n := AddNodeDelays(g, d); n != 3 {
		t.Errorf("delayed %d edges, want the 2 entering the signal and the 1 entering the toll booth", n)
	}
	for u := uint32(0); u < g.NumNodes; u++ {
		for e := g.FirstOut[u]; e < g.FirstOut[u+1]; e++ {
			want := uint32(1000)
			switch {
			case g.EdgeToSignal[e]:
				want = 1500
			case g.EdgeToToll[e]:
				want = 1900
			}
			if g.Weight[e] != want {
				t.Errorf("edge %d→%d weighs %d, want %d (no junction on this road)", u, g.Head[e], g.Weight[e], want)
			}
		}
	}
}

func TestAddNodeDelaysNeedsBuildData(t *testing.T) {
	g := &Graph{NumNodes: 1, NumEdges: 1, FirstOut: []uint32{0, 1}, Head: []uint32{0}, Weight: []uint32{5}}
	if n := AddNodeDelays(g, NodeDelays{JunctionMajor: 1, JunctionEqual: 1, JunctionMinor: 1, Signal: 1}); n != 0 || g.Weight[0] != 5 {
		t.Errorf("graph without EdgeWay/EdgeClass: delayed %d, weight %d", n, g.Weight[0])
	}
}
