package routing

import (
	"context"
	"errors"
	"math"

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
	chg      *graph.CHGraph
	origGraph *graph.Graph // for geometry and snap
	snapper  *Snapper
}

// NewEngine creates a routing engine from a CH graph and the original graph.
func NewEngine(chg *graph.CHGraph, origGraph *graph.Graph) *Engine {
	return &Engine{
		chg:      chg,
		origGraph: origGraph,
		snapper:  NewSnapper(origGraph),
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

	// Step 2: Run bidirectional CH Dijkstra.
	qs := NewQueryState(e.chg.NumNodes)
	defer qs.Reset()

	// Seed forward PQ with start snap's endpoints.
	seedForward(qs, e.origGraph, startSnap)
	// Seed backward PQ with end snap's endpoints.
	seedBackward(qs, e.origGraph, endSnap)

	mu, meetNode := e.runCHDijkstra(ctx, qs)

	if meetNode == noNode || mu == math.MaxUint32 {
		return nil, ErrNoRoute
	}

	// Step 3: Compute total distance in meters (from millimeters).
	totalDistMeters := float64(mu) / 1000.0

	// For now, return a simplified result with total distance.
	// Full segment unpacking with geometry will be added when the
	// original graph's geometry is properly wired through.
	result := &RouteResult{
		TotalDistanceMeters: totalDistMeters,
		Segments: []Segment{
			{
				DistanceMeters: totalDistMeters,
				Geometry: []LatLng{
					{Lat: start.Lat, Lng: start.Lng},
					{Lat: end.Lat, Lng: end.Lng},
				},
			},
		},
	}

	return result, nil
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

// runCHDijkstra runs bidirectional CH Dijkstra and returns (mu, meetNode).
func (e *Engine) runCHDijkstra(ctx context.Context, qs *QueryState) (uint32, uint32) {
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
