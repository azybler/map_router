package routing

import (
	"errors"
	"math"

	"map_router/pkg/geo"
	"map_router/pkg/graph"
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

// Grid cell size in degrees. 0.01° ≈ 1.1 km at the equator.
// A 3×3 cell search covers ±1.1 km, well over the 500 m max snap distance.
const gridCellSize = 0.01

// snapEdge stores an edge index with its precomputed source node,
// avoiding an O(log N) binary search per candidate during snap queries.
type snapEdge struct {
	edgeIdx uint32
	source  uint32
}

// Snapper provides nearest-road snapping using a flat spatial grid index.
type Snapper struct {
	cells map[uint64][]snapEdge // cellKey → edges with precomputed sources
	g     *graph.Graph
}

// gridCell returns the integer cell coordinates for a lat/lon.
func gridCell(lat, lon float64) (latIdx, lonIdx int32) {
	return int32(math.Floor(lat / gridCellSize)), int32(math.Floor(lon / gridCellSize))
}

// cellKey packs two int32 cell indices into a single uint64 map key.
func cellKey(latIdx, lonIdx int32) uint64 {
	return uint64(uint32(latIdx))<<32 | uint64(uint32(lonIdx))
}

// NewSnapper builds a spatial grid index from the original graph's edges.
func NewSnapper(g *graph.Graph) *Snapper {
	cells := make(map[uint64][]snapEdge, g.NumEdges/64)

	for u := uint32(0); u < g.NumNodes; u++ {
		start, end := g.EdgesFrom(u)
		for e := start; e < end; e++ {
			v := g.Head[e]

			uLat, uLon := g.NodeLat[u], g.NodeLon[u]
			vLat, vLon := g.NodeLat[v], g.NodeLon[v]

			// Bounding box of segment endpoints.
			minLat := math.Min(uLat, vLat)
			maxLat := math.Max(uLat, vLat)
			minLon := math.Min(uLon, vLon)
			maxLon := math.Max(uLon, vLon)

			// Insert edge into every cell its bounding box overlaps.
			latLo, lonLo := gridCell(minLat, minLon)
			latHi, lonHi := gridCell(maxLat, maxLon)

			se := snapEdge{edgeIdx: e, source: u}
			for la := latLo; la <= latHi; la++ {
				for lo := lonLo; lo <= lonHi; lo++ {
					key := cellKey(la, lo)
					cells[key] = append(cells[key], se)
				}
			}
		}
	}

	return &Snapper{cells: cells, g: g}
}

// Snap finds the nearest road segment to the given lat/lng.
func (s *Snapper) Snap(lat, lng float64) (SnapResult, error) {
	centerLat, centerLon := gridCell(lat, lng)

	bestDist := math.Inf(1)
	var bestResult SnapResult

	// Search 3×3 grid of cells around the query point.
	for dLat := int32(-1); dLat <= 1; dLat++ {
		for dLon := int32(-1); dLon <= 1; dLon++ {
			key := cellKey(centerLat+dLat, centerLon+dLon)
			for _, se := range s.cells[key] {
				u := se.source
				v := s.g.Head[se.edgeIdx]

				exactDist, ratio := geo.PointToSegmentDist(
					lat, lng,
					s.g.NodeLat[u], s.g.NodeLon[u],
					s.g.NodeLat[v], s.g.NodeLon[v],
				)

				if exactDist < bestDist {
					bestDist = exactDist
					bestResult = SnapResult{
						EdgeIdx: se.edgeIdx,
						NodeU:   u,
						NodeV:   v,
						Ratio:   ratio,
						Dist:    exactDist,
					}
				}
			}
		}
	}

	if bestDist > maxSnapDistMeters {
		return SnapResult{}, ErrPointTooFar
	}

	return bestResult, nil
}

