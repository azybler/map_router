package routing

import (
	"errors"
	"math"

	"map_router/pkg/geo"
	"map_router/pkg/graph"

	"github.com/tidwall/rtree"
)

const maxSnapDistMeters = 500.0

// ErrPointTooFar is returned when the query point is too far from any road.
var ErrPointTooFar = errors.New("point too far from road")

// SnapResult represents a point snapped to a road segment.
type SnapResult struct {
	EdgeIdx uint32  // index into original edge arrays
	NodeU   uint32  // source node of the edge
	NodeV   uint32  // target node of the edge
	Ratio   float64 // 0.0 = at NodeU, 1.0 = at NodeV
	Dist    float64 // distance in meters from query point to snapped point
}

// Snapper provides nearest-road snapping using an R-tree spatial index.
type Snapper struct {
	tree *rtree.RTreeG[uint32]
	g    *graph.Graph
}

// NewSnapper builds an R-tree from the original graph's edges.
func NewSnapper(g *graph.Graph) *Snapper {
	var tree rtree.RTreeG[uint32]

	for u := uint32(0); u < g.NumNodes; u++ {
		start, end := g.EdgesFrom(u)
		for e := start; e < end; e++ {
			v := g.Head[e]
			// Bounding box of segment (u, v).
			minLat := math.Min(g.NodeLat[u], g.NodeLat[v])
			maxLat := math.Max(g.NodeLat[u], g.NodeLat[v])
			minLon := math.Min(g.NodeLon[u], g.NodeLon[v])
			maxLon := math.Max(g.NodeLon[u], g.NodeLon[v])
			tree.Insert([2]float64{minLon, minLat}, [2]float64{maxLon, maxLat}, e)
		}
	}

	return &Snapper{tree: &tree, g: g}
}

// Snap finds the nearest road segment to the given lat/lng.
func (s *Snapper) Snap(lat, lng float64) (SnapResult, error) {
	bestDist := math.Inf(1)
	var bestResult SnapResult
	found := false

	// Use Nearby to get candidates in approximate distance order.
	s.tree.Nearby(
		rtree.BoxDist[float64, uint32]([2]float64{lng, lat}, [2]float64{lng, lat}, nil),
		func(min, max [2]float64, edgeIdx uint32, dist float64) bool {
			// Find the edge's source node by scanning (reverse lookup).
			u := s.findSource(edgeIdx)
			v := s.g.Head[edgeIdx]

			exactDist, ratio := geo.PointToSegmentDist(
				lat, lng,
				s.g.NodeLat[u], s.g.NodeLon[u],
				s.g.NodeLat[v], s.g.NodeLon[v],
			)

			if exactDist < bestDist {
				bestDist = exactDist
				bestResult = SnapResult{
					EdgeIdx: edgeIdx,
					NodeU:   u,
					NodeV:   v,
					Ratio:   ratio,
					Dist:    exactDist,
				}
				found = true
			}

			// Stop if the R-tree box distance exceeds our best + margin.
			// Once we have a good candidate within maxSnapDist, stop expanding.
			if found && bestDist <= maxSnapDistMeters {
				// Continue a bit to ensure we have the true nearest.
				// The R-tree returns in box-distance order; once box distance
				// exceeds our best exact distance by a margin, we can stop.
				return dist <= bestDist*1.5
			}

			// Stop if we've gone way beyond snap distance.
			return !found || bestDist <= maxSnapDistMeters*2
		},
	)

	if !found || bestDist > maxSnapDistMeters {
		return SnapResult{}, ErrPointTooFar
	}

	return bestResult, nil
}

// findSource finds the source node for edge at index edgeIdx.
func (s *Snapper) findSource(edgeIdx uint32) uint32 {
	// Binary search in FirstOut to find which node owns this edge.
	lo, hi := uint32(0), s.g.NumNodes
	for lo < hi {
		mid := (lo + hi) / 2
		if s.g.FirstOut[mid+1] <= edgeIdx {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
