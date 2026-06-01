package routing

import (
	"math"
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/ch"
	"github.com/azybler/map_router/pkg/geo"
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

func TestDistanceIncludesPartialEdges(t *testing.T) {
	g, chg := buildTestGraphAndCH(t)
	eng := NewEngine(chg, g)

	// On-node: start/end land exactly on graph nodes (ratio ~0/1).
	t.Run("on_node", func(t *testing.T) {
		res, err := eng.Route(t.Context(),
			LatLng{Lat: 1.300, Lng: 103.800}, LatLng{Lat: 1.301, Lng: 103.802})
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		if res.TotalDistanceMeters <= 0 {
			t.Fatalf("expected positive distance, got %f", res.TotalDistanceMeters)
		}
		first := res.Segments[0].Geometry[0]
		if d := geo.Haversine(first.Lat, first.Lng, 1.300, 103.800); d > 1.0 {
			t.Errorf("geometry should start at the snap point; off by %.2f m", d)
		}
		assertDistanceEqualsPolyline(t, res)
	})

	// Mid-edge: start is the midpoint of edge (1.300,103.800)-(1.300,103.801),
	// end is the midpoint of edge (1.301,103.801)-(1.301,103.802). Snap ratio
	// ~0.5, so the prepended/appended snap point is ~50 m from the graph node
	// and the reported distance must include those partial edges.
	t.Run("partial_edge", func(t *testing.T) {
		res, err := eng.Route(t.Context(),
			LatLng{Lat: 1.300, Lng: 103.8005}, LatLng{Lat: 1.301, Lng: 103.8015})
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		first := res.Segments[0].Geometry[0]
		// Snap point must be well away from the nearest graph node (mid-edge).
		dNode := geo.Haversine(first.Lat, first.Lng, 1.300, 103.800)
		if dNode < 10.0 {
			t.Errorf("expected mid-edge snap point >10 m from graph node, got %.2f m", dNode)
		}
		assertDistanceEqualsPolyline(t, res)
	})
}

// assertDistanceEqualsPolyline checks the reported distance equals the summed
// great-circle length of the returned geometry.
func assertDistanceEqualsPolyline(t *testing.T, res *RouteResult) {
	t.Helper()
	geom := res.Segments[0].Geometry
	var sum float64
	for i := 0; i+1 < len(geom); i++ {
		sum += geo.Haversine(geom[i].Lat, geom[i].Lng, geom[i+1].Lat, geom[i+1].Lng)
	}
	if math.Abs(sum-res.TotalDistanceMeters) > 0.5 {
		t.Errorf("distance %.2f != polyline length %.2f", res.TotalDistanceMeters, sum)
	}
}

func chContract(t *testing.T, g *graph.Graph) *graph.CHGraph {
	t.Helper()
	return ch.Contract(g)
}

// stubParse: main road A-B-C-D (lon 103.800, ~100 m apart) plus a stub node S
// that sits ~2 m from the query but connects to the network ONLY via a far node
// F (~3.3 km east). Reaching the main road from S costs a ~6.8 km GEOMETRIC
// detour, which the geometry-based distance (Task A2) can detect.
func stubParse() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 100, ToNodeID: 200, Weight: 100}, {FromNodeID: 200, ToNodeID: 100, Weight: 100},
			{FromNodeID: 200, ToNodeID: 300, Weight: 100}, {FromNodeID: 300, ToNodeID: 200, Weight: 100},
			{FromNodeID: 300, ToNodeID: 400, Weight: 100}, {FromNodeID: 400, ToNodeID: 300, Weight: 100},
			{FromNodeID: 500, ToNodeID: 600, Weight: 3300}, {FromNodeID: 600, ToNodeID: 500, Weight: 3300},
			{FromNodeID: 600, ToNodeID: 100, Weight: 3300}, {FromNodeID: 100, ToNodeID: 600, Weight: 3300},
		},
		NodeLat: map[osm.NodeID]float64{
			100: 1.30000, 200: 1.30090, 300: 1.30180, 400: 1.30270,
			500: 1.30093, 600: 1.30090,
		},
		NodeLon: map[osm.NodeID]float64{
			100: 103.80000, 200: 103.80000, 300: 103.80000, 400: 103.80000,
			500: 103.80050, 600: 103.83000,
		},
	}
}

func TestMultiCandidateAvoidsStub(t *testing.T) {
	g := graph.Build(stubParse())
	chg := chContract(t, g)
	eng := NewEngine(chg, g)

	// Query ~2 m from stub node S (single-nearest snaps to S-F); destination is
	// node D on the main road. Via stub: S→F→A→B→C→D ≈ 6.8 km. Via main: ≈ 250 m.
	res, err := eng.Route(t.Context(),
		LatLng{Lat: 1.30093, Lng: 103.80048},
		LatLng{Lat: 1.30270, Lng: 103.80000})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.TotalDistanceMeters > 2000 {
		t.Errorf("expected main-road route (<2 km), got %.0f m (stub detour not avoided)", res.TotalDistanceMeters)
	}
}
