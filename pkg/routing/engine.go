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
	accessPenaltyMult = 4.0              // off-road distance penalty multiplier
)

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

// NewEngine creates a routing engine from a CH graph and the original graph.
func NewEngine(chg *graph.CHGraph, origGraph *graph.Graph) *Engine {
	e := &Engine{
		chg:       chg,
		origGraph: origGraph,
		snapper:   NewSnapper(origGraph),
	}
	e.qsPool.New = func() any {
		return NewQueryState(chg.NumNodes)
	}
	return e
}

// Route computes the shortest path between two points.
func (e *Engine) Route(ctx context.Context, start, end LatLng) (*RouteResult, error) {
	// Step 1: Snap points to nearest road segments (multi-candidate).
	startCands := e.snapper.SnapCandidates(start.Lat, start.Lng, snapK, snapRadiusMeters)
	if len(startCands) == 0 {
		return nil, ErrPointTooFar
	}
	endCands := e.snapper.SnapCandidates(end.Lat, end.Lng, snapK, snapRadiusMeters)
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
		Segments: []Segment{
			{
				DistanceMeters: totalDistMeters,
				Geometry:       geometry,
			},
		},
	}, nil
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
	c := cands[best]
	lat = g.NodeLat[c.NodeU] + c.Ratio*(g.NodeLat[c.NodeV]-g.NodeLat[c.NodeU])
	lng = g.NodeLon[c.NodeU] + c.Ratio*(g.NodeLon[c.NodeV]-g.NodeLon[c.NodeU])
	return lat, lng, true
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
	u, v := snap.NodeU, snap.NodeV
	weight := g.Weight[snap.EdgeIdx]
	pen := accessPenalty(g, snap)

	qs.seedFwdMin(v, uint32(math.Round(float64(weight)*(1-snap.Ratio)))+pen)
	if findEdge(g.FirstOut, g.Head, v, u) != noNode {
		qs.seedFwdMin(u, uint32(math.Round(float64(weight)*snap.Ratio))+pen)
	}
}

// seedBackward seeds the backward PQ from the end snap point. Arriving from u
// (travel u→v, stop at the point) is always legal; arriving from v requires the
// reverse edge v→u to exist.
func seedBackward(qs *QueryState, g *graph.Graph, snap SnapResult) {
	u, v := snap.NodeU, snap.NodeV
	weight := g.Weight[snap.EdgeIdx]
	pen := accessPenalty(g, snap)

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
