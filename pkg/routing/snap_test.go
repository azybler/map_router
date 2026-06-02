package routing

import (
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

// snapTestGraph: road A (0<->1) and a separate road B (2<->3) ~30 m north.
func snapTestGraph() *graph.Graph {
	return graph.Build(&osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 20, ToNodeID: 10, Weight: 100},
			{FromNodeID: 30, ToNodeID: 40, Weight: 100},
			{FromNodeID: 40, ToNodeID: 30, Weight: 100},
		},
		NodeLat: map[osm.NodeID]float64{10: 1.30000, 20: 1.30000, 30: 1.30027, 40: 1.30027},
		NodeLon: map[osm.NodeID]float64{10: 103.800, 20: 103.801, 30: 103.800, 40: 103.801},
	})
}

func TestSnapCandidatesDistinctAndSorted(t *testing.T) {
	s := NewSnapper(snapTestGraph())
	cands := s.SnapCandidates(1.30005, 103.8005, 4, 500.0)
	if len(cands) < 2 {
		t.Fatalf("expected at least 2 distinct candidates, got %d", len(cands))
	}
	for i := 1; i < len(cands); i++ {
		if cands[i].Dist < cands[i-1].Dist {
			t.Errorf("candidates not sorted by distance: %v", cands)
		}
	}
	seen := map[[2]uint32]bool{}
	for _, c := range cands {
		a, b := c.NodeU, c.NodeV
		if a > b {
			a, b = b, a
		}
		if seen[[2]uint32{a, b}] {
			t.Errorf("duplicate node-pair candidate %d-%d", a, b)
		}
		seen[[2]uint32{a, b}] = true
	}
}

func TestSnapCandidatesRespectsRadius(t *testing.T) {
	s := NewSnapper(snapTestGraph())
	cands := s.SnapCandidates(1.4, 103.9, 4, 50.0)
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates far from roads, got %d", len(cands))
	}
}

func TestSnapCandidatesRespectsK(t *testing.T) {
	s := NewSnapper(snapTestGraph())
	// Two distinct roads are nearby, but k=1 must cap the result to one.
	cands := s.SnapCandidates(1.30005, 103.8005, 1, 500.0)
	if len(cands) != 1 {
		t.Errorf("expected exactly 1 candidate with k=1, got %d", len(cands))
	}
	// k <= 0 must not panic (negative make cap) and returns nothing.
	if got := s.SnapCandidates(1.30005, 103.8005, 0, 500.0); len(got) != 0 {
		t.Errorf("expected 0 candidates with k=0, got %d", len(got))
	}
}
