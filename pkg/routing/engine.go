package routing

import (
	"context"
	"errors"
	"math"
	"sync"

	"github.com/azybler/map_router/pkg/geo"
	"github.com/azybler/map_router/pkg/graph"
)

// ErrNoRoute is returned when no route exists between the two points.
var ErrNoRoute = errors.New("no route found")

const (
	snapK             = 8
	snapRadiusMeters  = maxSnapDistMeters // 500 m: never reject what single-nearest accepted
	accessPenaltyMult = 4.0               // off-road distance penalty multiplier
)

// snapRadiiMeters is the escalating snap search schedule: the standard 500 m
// first (identical behavior for normal queries), then progressively wider so
// endpoints inside dropped private/gated estates or other road-sparse spots
// still produce a route instead of ErrPointTooFar.
var snapRadiiMeters = []float64{snapRadiusMeters, 1500, 5000}

// snapWithFallback returns snap candidates at the smallest radius in the
// schedule that yields any.
func (e *Engine) snapWithFallback(lat, lng float64) []SnapResult {
	for _, r := range snapRadiiMeters {
		if cands := e.snapper.SnapCandidates(lat, lng, snapK, r); len(cands) > 0 {
			return cands
		}
	}
	return nil
}

// accessPenalty converts the off-road snap distance into the active metric's
// units using the candidate edge's own weight/length ratio, so it auto-scales
// whether the metric is distance (mm) or time (ms).
func accessPenalty(g *graph.Graph, snap SnapResult) uint32 {
	u, v := snap.NodeU, snap.NodeV
	lenM := geo.Haversine(g.NodeLat[u], g.NodeLon[u], g.NodeLat[v], g.NodeLon[v])
	if lenM <= 0 {
		return 0
	}
	metricPerMeter := float64(g.Weight[snap.EdgeIdx]) / lenM
	return uint32(math.Round(accessPenaltyMult * snap.Dist * metricPerMeter))
}

// LatLng represents a geographic coordinate.
type LatLng struct {
	Lat float64
	Lng float64
}

// Segment represents a road segment in the route result.
type Segment struct {
	DistanceMeters float64
	Geometry       []LatLng
}

// RouteResult is the output of a route query.
type RouteResult struct {
	TotalDistanceMeters float64
	DurationSeconds     float64 // internal: mu/1000; may include access-penalty time; NOT exposed via API in Phase 1
	Segments            []Segment
}

// Router is the interface for route queries.
type Router interface {
	Route(ctx context.Context, start, end LatLng) (*RouteResult, error)
}

// Engine implements Router using a CH graph.
type Engine struct {
	chg       *graph.CHGraph
	origGraph *graph.Graph // for geometry and snap
	snapper   *Snapper
	qsPool    sync.Pool
}

// NewEngine creates a routing engine from a CH graph and the original graph,
// building a Snapper over origGraph.
func NewEngine(chg *graph.CHGraph, origGraph *graph.Graph) *Engine {
	return NewEngineWithSnapper(chg, origGraph, NewSnapper(origGraph))
}

// NewEngineWithSnapper creates a routing engine over a pre-built Snapper. This
// lets several metric engines built from one shared base share a single Snapper
// (the grid index is metric-independent — it reads only topology and coords, no
// weights), so the server holds one index instead of one per metric.
//
// The snapper MUST have been built from a graph whose node and edge numbering
// matches origGraph — i.e. the same shared base. Snapping produces raw
// EdgeIdx/NodeU/NodeV indices, and resolving them against a differently-numbered
// graph would silently address the wrong roads (see SnapCandidates).
func NewEngineWithSnapper(chg *graph.CHGraph, origGraph *graph.Graph, snapper *Snapper) *Engine {
	e := &Engine{
		chg:       chg,
		origGraph: origGraph,
		snapper:   snapper,
	}
	e.qsPool.New = func() any {
		return NewQueryState(chg.NumNodes)
	}
	return e
}

// SnapCandidates returns up to k distinct road candidates within radiusMeters of
// the given point, nearest first, snapped against this engine's own graph.
//
// Callers that later pass those candidates to RouteBetweenSnaps must obtain them
// here rather than from a separately-constructed Snapper: SnapResult carries raw
// EdgeIdx/NodeU/NodeV indices, which are only meaningful against the graph they
// were produced from. Two graphs built from the same source (say a time-weighted
// and a distance-weighted overlay) are not guaranteed to number nodes or edges
// identically, and mixing them would silently address the wrong roads.
func (e *Engine) SnapCandidates(lat, lng float64, k int, radiusMeters float64) []SnapResult {
	return e.snapper.SnapCandidates(lat, lng, k, radiusMeters)
}

// SnapPoint returns the geographic position of a snap result produced by this
// engine's SnapCandidates. Resolving a SnapResult against any other graph risks
// reading a different road's coordinates — see SnapCandidates.
func (e *Engine) SnapPoint(s SnapResult) (lat, lng float64) {
	return snapLatLng(e.origGraph, s)
}

// Route computes the shortest path between two points.
func (e *Engine) Route(ctx context.Context, start, end LatLng) (*RouteResult, error) {
	// Step 1: Snap points to nearest road segments (multi-candidate, with an
	// escalating radius fallback so road-sparse endpoints still route).
	startCands := e.snapWithFallback(start.Lat, start.Lng)
	if len(startCands) == 0 {
		return nil, ErrPointTooFar
	}
	endCands := e.snapWithFallback(end.Lat, end.Lng)
	if len(endCands) == 0 {
		return nil, ErrPointTooFar
	}

	// Step 2: Run bidirectional CH Dijkstra with predecessor tracking.
	qs := e.qsPool.Get().(*QueryState)
	defer func() {
		qs.Reset()
		e.qsPool.Put(qs)
	}()

	for _, c := range startCands {
		seedForward(qs, e.origGraph, c)
	}
	for _, c := range endCands {
		seedBackward(qs, e.origGraph, c)
	}

	mu, meetNode := e.runCHDijkstra(ctx, qs)

	if meetNode == noNode || mu == math.MaxUint32 {
		return nil, ErrNoRoute
	}

	// Step 3: Reconstruct overlay node path.
	overlayNodes := e.reconstructOverlayPath(meetNode, qs.PredFwd, qs.PredBwd)

	// Step 4: Unpack shortcuts into original node sequence.
	origNodes := unpackOverlayPath(e.chg, overlayNodes)

	// Step 5: Build geometry, anchored at the actual snapped points so the
	// partial first/last edges are included. Distance is measured from the
	// geometry (NOT from mu), which decouples it from the routing metric.
	geometry := e.buildGeometry(origNodes)
	if len(origNodes) > 0 {
		if lat, lng, ok := snapPointForCandidates(e.origGraph, startCands, origNodes[0]); ok {
			geometry = append([]LatLng{{Lat: lat, Lng: lng}}, geometry...)
		}
		if lat, lng, ok := snapPointForCandidates(e.origGraph, endCands, origNodes[len(origNodes)-1]); ok {
			geometry = append(geometry, LatLng{Lat: lat, Lng: lng})
		}
	}
	totalDistMeters := polylineLengthMeters(geometry)

	return &RouteResult{
		TotalDistanceMeters: totalDistMeters,
		DurationSeconds:     float64(mu) / 1000.0,
		Segments: []Segment{
			{
				DistanceMeters: totalDistMeters,
				Geometry:       geometry,
			},
		},
	}, nil
}

// RouteBetweenSnaps computes the shortest path between two positions that are
// already on the network, routing strictly between the two given snaps.
//
// Route is the wrong tool for this. Route re-snaps the coordinates it is handed
// and seeds every candidate within 500 m at both ends, so the path it returns
// may run between different roads than the ones asked about; and because it
// measures distance from the geometry rather than from mu, the lateral hop it
// silently took is absent from the result. For A-to-B navigation that is
// desirable — any nearby road will do, and the user wants the best one. For map
// matching it is fatal: the whole signal is the network distance between two
// *specific* candidate positions, so substituting a nearer road erases the
// quantity being measured and leaves parallel carriageways indistinguishable.
//
// No access penalty is applied. Both endpoints lie on the network by
// construction, so there is no off-road access to price; a map matcher already
// accounts for snap distance separately (as emission probability), and charging
// it again here would double-count it.
func (e *Engine) RouteBetweenSnaps(ctx context.Context, start, end SnapResult) (*RouteResult, error) {
	g := e.origGraph
	if int(start.EdgeIdx) >= len(g.Weight) || int(end.EdgeIdx) >= len(g.Weight) {
		return nil, ErrPointTooFar
	}

	// Both positions on one segment: travel is a straight run along the chord,
	// and a graph search cannot express it. The search can only leave an edge via
	// an endpoint, so it would route out to a node and back — or, on a one-way,
	// all the way around the block — reporting hundreds of metres for a few
	// metres of travel.
	if endRatio, ok := sameSegment(start, end); ok {
		if res, ok := e.routeAlongEdge(start, end, endRatio); ok {
			return res, nil
		}
		// Travelling backwards along a one-way edge: fall through to the search,
		// which finds the legal way round.
	}

	qs := e.qsPool.Get().(*QueryState)
	defer func() {
		qs.Reset()
		e.qsPool.Put(qs)
	}()

	seedForwardPenalty(qs, g, start, 0)
	seedBackwardPenalty(qs, g, end, 0)

	mu, meetNode := e.runCHDijkstra(ctx, qs)
	if meetNode == noNode || mu == math.MaxUint32 {
		return nil, ErrNoRoute
	}

	origNodes := unpackOverlayPath(e.chg, e.reconstructOverlayPath(meetNode, qs.PredFwd, qs.PredBwd))

	// Anchor the geometry at exactly the positions asked about, so the reported
	// distance covers the partial first and last edges and nothing else. Unlike
	// Route, there is no candidate set to choose an anchor from — the caller
	// named both endpoints, so they are used verbatim.
	geometry := e.buildGeometry(origNodes)
	sLat, sLng := snapLatLng(g, start)
	eLat, eLng := snapLatLng(g, end)
	if len(geometry) == 0 || geometry[0].Lat != sLat || geometry[0].Lng != sLng {
		geometry = append([]LatLng{{Lat: sLat, Lng: sLng}}, geometry...)
	}
	if last := geometry[len(geometry)-1]; last.Lat != eLat || last.Lng != eLng {
		geometry = append(geometry, LatLng{Lat: eLat, Lng: eLng})
	}
	totalDistMeters := polylineLengthMeters(geometry)

	return &RouteResult{
		TotalDistanceMeters: totalDistMeters,
		DurationSeconds:     float64(mu) / 1000.0,
		Segments: []Segment{
			{
				DistanceMeters: totalDistMeters,
				Geometry:       geometry,
			},
		},
	}, nil
}

// sameSegment reports whether two snaps lie on the same physical road segment,
// returning end's position as a ratio along start's edge.
//
// Matching EdgeIdx is not sufficient. A two-way road is stored as two directed
// edges over one pair of nodes, so two snaps on it can arrive either as the same
// EdgeIdx or as opposite twins — and SnapCandidates collapses each twin pair
// with a non-stable sort over identical distances, so which half a given query
// point receives is not guaranteed to be consistent between nearby points.
// Missing the twin case sends a few metres of travel into the graph search,
// which can only leave via an endpoint and so reports the whole way round.
func sameSegment(start, end SnapResult) (endRatio float64, ok bool) {
	switch {
	case start.EdgeIdx == end.EdgeIdx:
		return end.Ratio, true
	case start.NodeU == end.NodeV && start.NodeV == end.NodeU:
		// Opposite twin: the same chord measured from the other end.
		return 1 - end.Ratio, true
	}
	return 0, false
}

// routeAlongEdge handles two snaps sharing one segment, with endRatio expressed
// along start's edge (see sameSegment). Returns ok=false when travel would run
// against a one-way, leaving the caller to search for a legal route around.
func (e *Engine) routeAlongEdge(start, end SnapResult, endRatio float64) (*RouteResult, bool) {
	g := e.origGraph
	if endRatio < start.Ratio && findEdge(g.FirstOut, g.Head, start.NodeV, start.NodeU) == noNode {
		return nil, false
	}

	sLat, sLng := snapLatLng(g, start)
	eLat, eLng := snapLatLng(g, end)
	geometry := []LatLng{{Lat: sLat, Lng: sLng}, {Lat: eLat, Lng: eLng}}
	totalDistMeters := polylineLengthMeters(geometry)
	mu := uint32(math.Round(float64(g.Weight[start.EdgeIdx]) * math.Abs(endRatio-start.Ratio)))

	return &RouteResult{
		TotalDistanceMeters: totalDistMeters,
		DurationSeconds:     float64(mu) / 1000.0,
		Segments: []Segment{
			{
				DistanceMeters: totalDistMeters,
				Geometry:       geometry,
			},
		},
	}, true
}

// reconstructOverlayPath builds the full overlay node path from
// source seed → meetNode → target seed.
func (e *Engine) reconstructOverlayPath(meetNode uint32, predFwd, predBwd []uint32) []uint32 {
	// Forward path: meetNode ← ... ← source seed (trace backwards, then reverse).
	fwdPath := make([]uint32, 0, 16)
	node := meetNode
	for {
		fwdPath = append(fwdPath, node)
		pred := predFwd[node]
		if pred == noNode {
			break
		}
		node = pred
	}
	// Reverse to get source → meetNode.
	for i, j := 0, len(fwdPath)-1; i < j; i, j = i+1, j-1 {
		fwdPath[i], fwdPath[j] = fwdPath[j], fwdPath[i]
	}

	// Backward path: meetNode → ... → target seed.
	// predBwd[v] = u means original direction v → u (toward target).
	node = meetNode
	for {
		pred := predBwd[node]
		if pred == noNode {
			break
		}
		fwdPath = append(fwdPath, pred)
		node = pred
	}

	return fwdPath
}

// buildGeometry converts a sequence of original graph node IDs into lat/lng
// coordinates, including intermediate shape points from edge geometry.
func (e *Engine) buildGeometry(nodes []uint32) []LatLng {
	if len(nodes) == 0 {
		return nil
	}

	g := e.origGraph
	// Estimate ~2 geometry points per node (node + avg shape points).
	geom := make([]LatLng, 0, len(nodes)*2)

	// Add first node.
	geom = append(geom, LatLng{Lat: g.NodeLat[nodes[0]], Lng: g.NodeLon[nodes[0]]})

	for i := 0; i < len(nodes)-1; i++ {
		u := nodes[i]
		v := nodes[i+1]

		// Look up edge u→v in original graph for intermediate shape points.
		if g.GeoFirstOut != nil {
			edgeIdx := findEdge(g.FirstOut, g.Head, u, v)
			if edgeIdx != noNode && edgeIdx < uint32(len(g.GeoFirstOut)-1) {
				geoStart := g.GeoFirstOut[edgeIdx]
				geoEnd := g.GeoFirstOut[edgeIdx+1]
				for k := geoStart; k < geoEnd; k++ {
					geom = append(geom, LatLng{
						Lat: g.GeoShapeLat[k],
						Lng: g.GeoShapeLon[k],
					})
				}
			}
		}

		// Add target node coordinates.
		geom = append(geom, LatLng{Lat: g.NodeLat[v], Lng: g.NodeLon[v]})
	}

	return geom
}

// snapPointForCandidates returns the snap point of the nearest candidate that
// has `node` as an endpoint (i.e. the candidate that could have seeded it).
//
// When several candidates share `node`, we anchor to the one with the smallest
// off-road distance — the closest road to the requested point, which is the
// correct visual start. (Seed cost = partial-edge + access penalty, and the
// penalty is proportional to off-road distance, so min-distance ≈ min-seed-cost;
// any residual difference is bounded because all such candidates meet at `node`.)
func snapPointForCandidates(g *graph.Graph, cands []SnapResult, node uint32) (lat, lng float64, ok bool) {
	best := -1
	for i := range cands {
		if cands[i].NodeU == node || cands[i].NodeV == node {
			if best < 0 || cands[i].Dist < cands[best].Dist {
				best = i
			}
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	lat, lng = snapLatLng(g, cands[best])
	return lat, lng, true
}

// snapLatLng returns the position of a snap result, interpolated along its
// edge's chord.
//
// Linear interpolation is exact here: the OSM parser emits one edge per
// consecutive node pair and never populates intermediate shape points, and
// SnapCandidates measures Ratio against that same u→v chord.
func snapLatLng(g *graph.Graph, s SnapResult) (lat, lng float64) {
	lat = g.NodeLat[s.NodeU] + s.Ratio*(g.NodeLat[s.NodeV]-g.NodeLat[s.NodeU])
	lng = g.NodeLon[s.NodeU] + s.Ratio*(g.NodeLon[s.NodeV]-g.NodeLon[s.NodeU])
	return lat, lng
}

// polylineLengthMeters sums the great-circle length of a lat/lng polyline.
func polylineLengthMeters(geom []LatLng) float64 {
	var total float64
	for i := 0; i+1 < len(geom); i++ {
		total += geo.Haversine(geom[i].Lat, geom[i].Lng, geom[i+1].Lat, geom[i+1].Lng)
	}
	return total
}

// seedForward seeds the forward PQ from the start snap point, respecting edge
// direction: travel forward to v is always legal (edge u→v exists); travel
// backward to u is legal only if the reverse edge v→u exists.
func seedForward(qs *QueryState, g *graph.Graph, snap SnapResult) {
	seedForwardPenalty(qs, g, snap, accessPenalty(g, snap))
}

// seedForwardPenalty is seedForward with an explicit access penalty, so callers
// routing between positions already on the network can pass 0.
func seedForwardPenalty(qs *QueryState, g *graph.Graph, snap SnapResult, pen uint32) {
	u, v := snap.NodeU, snap.NodeV
	weight := g.Weight[snap.EdgeIdx]

	qs.seedFwdMin(v, uint32(math.Round(float64(weight)*(1-snap.Ratio)))+pen)
	if findEdge(g.FirstOut, g.Head, v, u) != noNode {
		qs.seedFwdMin(u, uint32(math.Round(float64(weight)*snap.Ratio))+pen)
	}
}

// seedBackward seeds the backward PQ from the end snap point. Arriving from u
// (travel u→v, stop at the point) is always legal; arriving from v requires the
// reverse edge v→u to exist.
func seedBackward(qs *QueryState, g *graph.Graph, snap SnapResult) {
	seedBackwardPenalty(qs, g, snap, accessPenalty(g, snap))
}

// seedBackwardPenalty is seedBackward with an explicit access penalty.
func seedBackwardPenalty(qs *QueryState, g *graph.Graph, snap SnapResult, pen uint32) {
	u, v := snap.NodeU, snap.NodeV
	weight := g.Weight[snap.EdgeIdx]

	qs.seedBwdMin(u, uint32(math.Round(float64(weight)*snap.Ratio))+pen)
	if findEdge(g.FirstOut, g.Head, v, u) != noNode {
		qs.seedBwdMin(v, uint32(math.Round(float64(weight)*(1-snap.Ratio)))+pen)
	}
}

// runCHDijkstra runs bidirectional CH Dijkstra with predecessor tracking.
func (e *Engine) runCHDijkstra(ctx context.Context, qs *QueryState) (uint32, uint32) {
	mu := uint32(math.MaxUint32)
	meetNode := noNode

	iterations := uint32(0)

	for {
		// PeekDist returns MaxUint32 for empty PQ, so this also handles
		// the empty-queue case without separate Len() checks.
		fwdMin := qs.FwdPQ.PeekDist()
		bwdMin := qs.BwdPQ.PeekDist()
		if fwdMin >= mu && bwdMin >= mu {
			break
		}

		// Check context cancellation periodically (bitmask avoids modulo).
		iterations++
		if iterations&255 == 0 {
			if ctx.Err() != nil {
				return mu, meetNode
			}
		}

		// Forward step.
		if fwdMin < mu {
			item := qs.FwdPQ.Pop()
			u := item.Node
			d := item.Dist

			if d <= qs.DistFwd[u] {
				// Check meet condition.
				if qs.DistBwd[u] < math.MaxUint32 {
					candidate := d + qs.DistBwd[u]
					if candidate < mu {
						mu = candidate
						meetNode = u
					}
				}

				// Relax forward upward edges.
				fStart := e.chg.FwdFirstOut[u]
				fEnd := e.chg.FwdFirstOut[u+1]
				for ei := fStart; ei < fEnd; ei++ {
					v := e.chg.FwdHead[ei]
					newDist := d + e.chg.FwdWeight[ei]
					if newDist < qs.DistFwd[v] {
						qs.touchFwd(v, newDist)
						qs.FwdPQ.Push(v, newDist)
						qs.PredFwd[v] = u
					}
				}
			}
		}

		// Re-check backward min against (potentially updated) mu.
		if qs.BwdPQ.PeekDist() < mu {
			item := qs.BwdPQ.Pop()
			u := item.Node
			d := item.Dist

			if d <= qs.DistBwd[u] {
				// Check meet condition.
				if qs.DistFwd[u] < math.MaxUint32 {
					candidate := qs.DistFwd[u] + d
					if candidate < mu {
						mu = candidate
						meetNode = u
					}
				}

				// Relax backward upward edges.
				bStart := e.chg.BwdFirstOut[u]
				bEnd := e.chg.BwdFirstOut[u+1]
				for ei := bStart; ei < bEnd; ei++ {
					v := e.chg.BwdHead[ei]
					newDist := d + e.chg.BwdWeight[ei]
					if newDist < qs.DistBwd[v] {
						qs.touchBwd(v, newDist)
						qs.BwdPQ.Push(v, newDist)
						qs.PredBwd[v] = u
					}
				}
			}
		}
	}

	return mu, meetNode
}
