package routing

import (
	"context"
	"errors"
	"math"

	"map_router/pkg/geo"
	"map_router/pkg/graph"
)

// ErrNoRoute is returned when no route exists between the two points.
var ErrNoRoute = errors.New("no route found")

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
}

// NewEngine creates a routing engine from a CH graph and the original graph.
func NewEngine(chg *graph.CHGraph, origGraph *graph.Graph) *Engine {
	return &Engine{
		chg:       chg,
		origGraph: origGraph,
		snapper:   NewSnapper(origGraph),
	}
}

// Route computes the shortest path between two points.
func (e *Engine) Route(ctx context.Context, start, end LatLng) (*RouteResult, error) {
	// Step 1: Snap points to nearest road segments.
	startSnap, err := e.snapper.Snap(start.Lat, start.Lng)
	if err != nil {
		return nil, err
	}
	endSnap, err := e.snapper.Snap(end.Lat, end.Lng)
	if err != nil {
		return nil, err
	}

	// Step 2: Run bidirectional CH Dijkstra with predecessor tracking.
	qs := NewQueryState(e.chg.NumNodes)
	defer qs.Reset()

	fwdPred := make(map[uint32]uint32)
	bwdPred := make(map[uint32]uint32)

	// Seed forward PQ with start snap's endpoints.
	seedForward(qs, e.origGraph, startSnap)
	// Seed backward PQ with end snap's endpoints.
	seedBackward(qs, e.origGraph, endSnap)

	mu, meetNode := e.runCHDijkstra(ctx, qs, fwdPred, bwdPred)

	if meetNode == noNode || mu == math.MaxUint32 {
		return nil, ErrNoRoute
	}

	// Step 3: Reconstruct overlay node path.
	overlayNodes := e.reconstructOverlayPath(meetNode, fwdPred, bwdPred)

	// Step 4: Unpack shortcuts into original node sequence.
	origNodes := unpackOverlayPath(e.chg, overlayNodes)

	// Step 5: Build geometry from original node sequence.
	totalDistMeters := float64(mu) / 1000.0
	geometry := e.buildGeometry(origNodes)

	result := &RouteResult{
		TotalDistanceMeters: totalDistMeters,
		Segments: []Segment{
			{
				DistanceMeters: totalDistMeters,
				Geometry:       geometry,
			},
		},
	}

	return result, nil
}

// reconstructOverlayPath builds the full overlay node path from
// source seed → meetNode → target seed.
func (e *Engine) reconstructOverlayPath(meetNode uint32, fwdPred, bwdPred map[uint32]uint32) []uint32 {
	// Forward path: meetNode ← ... ← source seed (trace backwards, then reverse).
	var fwdPath []uint32
	node := meetNode
	for {
		fwdPath = append(fwdPath, node)
		pred, ok := fwdPred[node]
		if !ok {
			break
		}
		node = pred
	}
	// Reverse to get source → meetNode.
	for i, j := 0, len(fwdPath)-1; i < j; i, j = i+1, j-1 {
		fwdPath[i], fwdPath[j] = fwdPath[j], fwdPath[i]
	}

	// Backward path: meetNode → ... → target seed.
	// bwdPred[v] = u means original direction v → u (toward target).
	node = meetNode
	for {
		pred, ok := bwdPred[node]
		if !ok {
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
	var geom []LatLng

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

// seedForward seeds the forward PQ with the start snap point's reachable nodes.
func seedForward(qs *QueryState, g *graph.Graph, snap SnapResult) {
	u := snap.NodeU
	v := snap.NodeV
	weight := g.Weight[snap.EdgeIdx]

	// Distance from snap point to v (forward along edge u→v).
	dv := uint32(math.Round(float64(weight) * (1 - snap.Ratio)))
	if dv < math.MaxUint32 {
		qs.touchFwd(v, dv)
		qs.FwdPQ.Push(v, dv)
	}

	// Distance from snap point to u (backward along edge u→v).
	du := uint32(math.Round(float64(weight) * snap.Ratio))
	if du < math.MaxUint32 {
		qs.touchFwd(u, du)
		qs.FwdPQ.Push(u, du)
	}
}

// seedBackward seeds the backward PQ with the end snap point's reachable nodes.
func seedBackward(qs *QueryState, g *graph.Graph, snap SnapResult) {
	u := snap.NodeU
	v := snap.NodeV
	weight := g.Weight[snap.EdgeIdx]

	// Distance from u to snap point (forward direction).
	du := uint32(math.Round(float64(weight) * snap.Ratio))
	if du < math.MaxUint32 {
		qs.touchBwd(u, du)
		qs.BwdPQ.Push(u, du)
	}

	// Distance from v to snap point (backward direction).
	dv := uint32(math.Round(float64(weight) * (1 - snap.Ratio)))
	if dv < math.MaxUint32 {
		qs.touchBwd(v, dv)
		qs.BwdPQ.Push(v, dv)
	}
}

// runCHDijkstra runs bidirectional CH Dijkstra with predecessor tracking.
func (e *Engine) runCHDijkstra(ctx context.Context, qs *QueryState, fwdPred, bwdPred map[uint32]uint32) (uint32, uint32) {
	mu := uint32(math.MaxUint32)
	meetNode := noNode

	iterations := 0

	for qs.FwdPQ.Len() > 0 || qs.BwdPQ.Len() > 0 {
		// Check context cancellation periodically.
		iterations++
		if iterations%100 == 0 {
			if ctx.Err() != nil {
				return mu, meetNode
			}
		}

		// Forward step.
		if qs.FwdPQ.Len() > 0 && qs.FwdPQ.PeekDist() < mu {
			item := qs.FwdPQ.Pop()
			u := item.Node
			d := item.Dist

			if d > qs.DistFwd[u] {
				goto backward // stale entry
			}

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
					fwdPred[v] = u
				}
			}
		}

	backward:
		// Backward step.
		if qs.BwdPQ.Len() > 0 && qs.BwdPQ.PeekDist() < mu {
			item := qs.BwdPQ.Pop()
			u := item.Node
			d := item.Dist

			if d > qs.DistBwd[u] {
				continue // stale entry
			}

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
					bwdPred[v] = u
				}
			}
		}

		// Termination check.
		if qs.FwdPQ.PeekDist() >= mu && qs.BwdPQ.PeekDist() >= mu {
			break
		}
	}

	return mu, meetNode
}

// distBetween computes the distance in meters between two LatLng points.
func distBetween(a, b LatLng) float64 {
	return geo.Haversine(a.Lat, a.Lng, b.Lat, b.Lng)
}
