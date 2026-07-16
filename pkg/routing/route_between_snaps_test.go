package routing

import (
	"context"
	"math"
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/ch"
	"github.com/azybler/map_router/pkg/geo"
	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

// dividedHighway builds a divided carriageway: two parallel one-way roads about
// 11 m apart running in opposite directions, joined only by a U-turn link at the
// far east end.
//
//	       (northbound, westward travel)
//	N:  13 <---- 12 <---- 11 <---- 10
//	                                |  U-turn link
//	S:  20 ----> 21 ----> 22 ----> 23
//	       (southbound, eastward travel)
//
// Getting from a point on S to the point directly opposite on N costs a long
// detour east to the link. That detour is exactly the signal a map matcher needs
// in order to reject the wrong carriageway — and exactly what Route discards.
func dividedHighway() *osmparser.ParseResult {
	const (
		latN = 3.00010 // ~11 m north of latS
		latS = 3.00000
	)
	lon := func(i int) float64 { return 101.60000 + float64(i)*0.00090 } // ~100 m spacing

	return &osmparser.ParseResult{
		// Northbound carriageway: one-way westward, 10 -> 11 -> 12 -> 13.
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 11, Weight: 10000},
			{FromNodeID: 11, ToNodeID: 12, Weight: 10000},
			{FromNodeID: 12, ToNodeID: 13, Weight: 10000},
			// Southbound carriageway: one-way eastward, 20 -> 21 -> 22 -> 23.
			{FromNodeID: 20, ToNodeID: 21, Weight: 10000},
			{FromNodeID: 21, ToNodeID: 22, Weight: 10000},
			{FromNodeID: 22, ToNodeID: 23, Weight: 10000},
			// U-turn link at the east end: 23 -> 10 only.
			{FromNodeID: 23, ToNodeID: 10, Weight: 1100},
		},
		NodeLat: map[osm.NodeID]float64{
			10: latN, 11: latN, 12: latN, 13: latN,
			20: latS, 21: latS, 22: latS, 23: latS,
		},
		NodeLon: map[osm.NodeID]float64{
			10: lon(3), 11: lon(2), 12: lon(1), 13: lon(0),
			20: lon(0), 21: lon(1), 22: lon(2), 23: lon(3),
		},
	}
}

// findDirectedEdge returns the edge index of the directed edge u→v.
func findDirectedEdge(t *testing.T, g *graph.Graph, u, v uint32) uint32 {
	t.Helper()
	idx := findEdge(g.FirstOut, g.Head, u, v)
	if idx == noNode {
		t.Fatalf("directed edge %d->%d not found", u, v)
	}
	return idx
}

func snapOnEdge(t *testing.T, g *graph.Graph, u, v uint32, ratio float64) SnapResult {
	t.Helper()
	return SnapResult{EdgeIdx: findDirectedEdge(t, g, u, v), NodeU: u, NodeV: v, Ratio: ratio}
}

// TestRouteBetweenSnaps_PricesCarriagewaySwap is the reason this API exists.
//
// Two positions sit directly opposite each other on a divided highway, ~11 m
// apart. Travelling between them legally requires a ~500 m detour to the U-turn.
// RouteBetweenSnaps must report that detour. Route, by contrast, re-snaps and
// seeds both carriageways at both ends, so it can hop the median for free.
func TestRouteBetweenSnaps_PricesCarriagewaySwap(t *testing.T) {
	g := graph.Build(dividedHighway())
	chg := ch.Contract(g)
	e := NewEngine(chg, g)
	ctx := context.Background()

	n21 := nodeIndex(g, 3.00000, 101.60090)
	n22 := nodeIndex(g, 3.00000, 101.60180)
	n11 := nodeIndex(g, 3.00010, 101.60180)
	n12 := nodeIndex(g, 3.00010, 101.60090)

	// Mid-way along the southbound 21->22 chord, and the point directly opposite
	// on the northbound 11->12 chord.
	south := snapOnEdge(t, g, n21, n22, 0.5)
	north := snapOnEdge(t, g, n11, n12, 0.5)

	sLat, sLng := snapLatLng(g, south)
	nLat, nLng := snapLatLng(g, north)
	straight := geo.Haversine(sLat, sLng, nLat, nLng)
	if straight > 20 {
		t.Fatalf("fixture: carriageways should be ~11 m apart, got %.1f m", straight)
	}

	res, err := e.RouteBetweenSnaps(ctx, south, north)
	if err != nil {
		t.Fatalf("RouteBetweenSnaps: %v", err)
	}

	// Legal path: east to node 23, over the U-turn link, west along the
	// northbound carriageway. That is an order of magnitude beyond the 11 m
	// straight-line gap.
	if res.TotalDistanceMeters < 10*straight {
		t.Errorf("carriageway swap reported as %.1f m for an %.1f m straight gap: "+
			"the detour has not been priced, so a map matcher cannot tell the two "+
			"carriageways apart", res.TotalDistanceMeters, straight)
	}

	// And the geometry must start and end exactly where we asked, with no
	// silently-substituted endpoint.
	geom := res.Segments[0].Geometry
	if d := geo.Haversine(geom[0].Lat, geom[0].Lng, sLat, sLng); d > 0.01 {
		t.Errorf("geometry starts %.2f m from the requested start snap; endpoint was substituted", d)
	}
	if last := geom[len(geom)-1]; geo.Haversine(last.Lat, last.Lng, nLat, nLng) > 0.01 {
		t.Errorf("geometry ends away from the requested end snap; endpoint was substituted")
	}
}

// TestRouteBetweenSnaps_SameEdgeIsDirect covers the case a graph search cannot
// express: both positions on one edge. A search can only leave via an endpoint,
// so it would route out to a node and back — on this one-way, all the way around
// the loop — turning ~45 m of travel into hundreds.
func TestRouteBetweenSnaps_SameEdgeIsDirect(t *testing.T) {
	g := graph.Build(dividedHighway())
	chg := ch.Contract(g)
	e := NewEngine(chg, g)
	ctx := context.Background()

	n21 := nodeIndex(g, 3.00000, 101.60090)
	n22 := nodeIndex(g, 3.00000, 101.60180)

	from := snapOnEdge(t, g, n21, n22, 0.25)
	to := snapOnEdge(t, g, n21, n22, 0.75)

	fLat, fLng := snapLatLng(g, from)
	tLat, tLng := snapLatLng(g, to)
	want := geo.Haversine(fLat, fLng, tLat, tLng)

	res, err := e.RouteBetweenSnaps(ctx, from, to)
	if err != nil {
		t.Fatalf("RouteBetweenSnaps: %v", err)
	}
	if math.Abs(res.TotalDistanceMeters-want) > 0.5 {
		t.Errorf("same-edge distance = %.2f m, want ~%.2f m (a graph search would "+
			"route out to a node and back)", res.TotalDistanceMeters, want)
	}
}

// TestRouteBetweenSnaps_SameEdgeAgainstOneWayGoesAround asserts direction is
// respected even within a single edge: travelling from ratio 0.75 back to 0.25
// on a one-way is illegal, so the result must be the long way round, not a
// negative or short hop.
func TestRouteBetweenSnaps_SameEdgeAgainstOneWayGoesAround(t *testing.T) {
	g := graph.Build(dividedHighway())
	chg := ch.Contract(g)
	e := NewEngine(chg, g)
	ctx := context.Background()

	n21 := nodeIndex(g, 3.00000, 101.60090)
	n22 := nodeIndex(g, 3.00000, 101.60180)

	from := snapOnEdge(t, g, n21, n22, 0.75)
	to := snapOnEdge(t, g, n21, n22, 0.25)

	fLat, fLng := snapLatLng(g, from)
	tLat, tLng := snapLatLng(g, to)
	straight := geo.Haversine(fLat, fLng, tLat, tLng)

	res, err := e.RouteBetweenSnaps(ctx, from, to)
	// The southbound carriageway is one-way east with no return path onto
	// itself, so there is legitimately no route. Either outcome is acceptable —
	// what must never happen is a cheap backwards hop along the one-way.
	if err != nil {
		if err != ErrNoRoute {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if res.TotalDistanceMeters < 5*straight {
		t.Errorf("travelling backwards along a one-way reported as %.1f m for a "+
			"%.1f m gap: direction was not respected", res.TotalDistanceMeters, straight)
	}
}

// twoWayStreet is a single two-way road: 30 -> 31 -> 32 with both directions
// present, so every segment exists as a pair of opposite directed edges.
func twoWayStreet() *osmparser.ParseResult {
	return &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 30, ToNodeID: 31, Weight: 10000},
			{FromNodeID: 31, ToNodeID: 30, Weight: 10000},
			{FromNodeID: 31, ToNodeID: 32, Weight: 10000},
			{FromNodeID: 32, ToNodeID: 31, Weight: 10000},
		},
		NodeLat: map[osm.NodeID]float64{30: 3.00000, 31: 3.00000, 32: 3.00000},
		NodeLon: map[osm.NodeID]float64{30: 101.60000, 31: 101.60090, 32: 101.60180},
	}
}

// TestRouteBetweenSnaps_ReverseTwinIsDirect covers two snaps on the same
// physical segment that arrive as OPPOSITE directed edges.
//
// This is not hypothetical. SnapCandidates collapses the two directed halves of
// a two-way road using a non-stable sort over identical distances, so adjacent
// observations along one street routinely receive different halves. Keying the
// same-segment shortcut on EdgeIdx alone misses every such pair and sends a few
// metres of travel through the graph search, which can only leave via an
// endpoint — measured at up to 217 m of overstatement on real KL geometry.
func TestRouteBetweenSnaps_ReverseTwinIsDirect(t *testing.T) {
	g := graph.Build(twoWayStreet())
	chg := ch.Contract(g)
	e := NewEngine(chg, g)
	ctx := context.Background()

	n30 := nodeIndex(g, 3.00000, 101.60000)
	n31 := nodeIndex(g, 3.00000, 101.60090)

	// Same chord, opposite directed edges. Ratio 0.4 along 30->31 and ratio 0.4
	// along 31->30 describe points 0.4 and 0.6 of the way from node 30.
	from := snapOnEdge(t, g, n30, n31, 0.4)
	to := snapOnEdge(t, g, n31, n30, 0.4)

	fLat, fLng := snapLatLng(g, from)
	tLat, tLng := snapLatLng(g, to)
	want := geo.Haversine(fLat, fLng, tLat, tLng)
	if want < 1 {
		t.Fatalf("fixture: the two points should be ~20 m apart, got %.2f m", want)
	}

	res, err := e.RouteBetweenSnaps(ctx, from, to)
	if err != nil {
		t.Fatalf("RouteBetweenSnaps: %v", err)
	}
	if math.Abs(res.TotalDistanceMeters-want) > 0.5 {
		t.Errorf("reverse-twin distance = %.2f m, want ~%.2f m — the twin was not "+
			"recognised as the same segment, so the search routed out to a node and back",
			res.TotalDistanceMeters, want)
	}
}

// TestRouteBetweenSnaps_NoAccessPenalty pins the decision that snap distance is
// not charged here. The endpoints are on the network by construction; a map
// matcher prices off-road distance separately via emission probability, and
// double-charging it would bias the matcher toward whichever candidate the
// router happens to prefer.
func TestRouteBetweenSnaps_NoAccessPenalty(t *testing.T) {
	g := graph.Build(dividedHighway())
	chg := ch.Contract(g)
	e := NewEngine(chg, g)
	ctx := context.Background()

	n20 := nodeIndex(g, 3.00000, 101.60000)
	n21 := nodeIndex(g, 3.00000, 101.60090)
	n22 := nodeIndex(g, 3.00000, 101.60180)

	// Two snaps on consecutive edges of the same one-way carriageway. Dist is
	// deliberately large: a caller's GPS fix was far off-road, but the position
	// handed to us is still exactly on the network.
	from := snapOnEdge(t, g, n20, n21, 0.5)
	from.Dist = 40
	to := snapOnEdge(t, g, n21, n22, 0.5)
	to.Dist = 40

	res, err := e.RouteBetweenSnaps(ctx, from, to)
	if err != nil {
		t.Fatalf("RouteBetweenSnaps: %v", err)
	}

	fLat, fLng := snapLatLng(g, from)
	tLat, tLng := snapLatLng(g, to)
	want := geo.Haversine(fLat, fLng, tLat, tLng)
	if math.Abs(res.TotalDistanceMeters-want) > 1 {
		t.Errorf("distance = %.2f m, want ~%.2f m — Dist must not influence the result",
			res.TotalDistanceMeters, want)
	}
}
