package routing

import (
	"errors"
	"math"
	"sort"

	"github.com/azybler/map_router/pkg/geo"
	"github.com/azybler/map_router/pkg/graph"
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

// gridCell returns the integer cell coordinates for a lat/lon.
func gridCell(lat, lon float64) (latIdx, lonIdx int32) {
	return int32(math.Floor(lat / gridCellSize)), int32(math.Floor(lon / gridCellSize))
}

// cellKey packs two int32 cell indices into a single uint64 map key.
func cellKey(latIdx, lonIdx int32) uint64 {
	return uint64(uint32(latIdx))<<32 | uint64(uint32(lonIdx))
}

// cellEdge stores a cell key and edge data in a flat sortable structure.
type cellEdge struct {
	key     uint64
	edgeIdx uint32
	source  uint32
}

// Snapper provides nearest-road snapping using a flat sorted grid index.
// All edges are stored in a single sorted slice keyed by cell, eliminating
// per-cell slice allocations and map pointer overhead for reduced GC pressure.
type Snapper struct {
	edges []cellEdge // sorted by key
	g     *graph.Graph
}

// NewSnapper builds a flat spatial grid index from the original graph's edges.
func NewSnapper(g *graph.Graph) *Snapper {
	// First pass: count total entries to pre-allocate.
	totalEntries := 0
	for u := uint32(0); u < g.NumNodes; u++ {
		start, end := g.EdgesFrom(u)
		for e := start; e < end; e++ {
			v := g.Head[e]
			uLat, uLon := g.NodeLat[u], g.NodeLon[u]
			vLat, vLon := g.NodeLat[v], g.NodeLon[v]

			latLo, lonLo := gridCell(math.Min(uLat, vLat), math.Min(uLon, vLon))
			latHi, lonHi := gridCell(math.Max(uLat, vLat), math.Max(uLon, vLon))
			totalEntries += int(latHi-latLo+1) * int(lonHi-lonLo+1)
		}
	}

	edges := make([]cellEdge, 0, totalEntries)

	// Second pass: populate entries.
	for u := uint32(0); u < g.NumNodes; u++ {
		start, end := g.EdgesFrom(u)
		for e := start; e < end; e++ {
			v := g.Head[e]
			uLat, uLon := g.NodeLat[u], g.NodeLon[u]
			vLat, vLon := g.NodeLat[v], g.NodeLon[v]

			latLo, lonLo := gridCell(math.Min(uLat, vLat), math.Min(uLon, vLon))
			latHi, lonHi := gridCell(math.Max(uLat, vLat), math.Max(uLon, vLon))

			for la := latLo; la <= latHi; la++ {
				for lo := lonLo; lo <= lonHi; lo++ {
					edges = append(edges, cellEdge{
						key:     cellKey(la, lo),
						edgeIdx: e,
						source:  u,
					})
				}
			}
		}
	}

	sort.Slice(edges, func(i, j int) bool {
		return edges[i].key < edges[j].key
	})

	return &Snapper{edges: edges, g: g}
}

// cellRange returns the slice of edges for the given cell key using binary search.
func (s *Snapper) cellRange(key uint64) []cellEdge {
	// Find first entry with this key.
	lo := sort.Search(len(s.edges), func(i int) bool {
		return s.edges[i].key >= key
	})
	if lo >= len(s.edges) || s.edges[lo].key != key {
		return nil
	}
	// Find first entry past this key.
	hi := sort.Search(len(s.edges), func(i int) bool {
		return s.edges[i].key > key
	})
	return s.edges[lo:hi]
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
			for _, ce := range s.cellRange(key) {
				u := ce.source
				v := s.g.Head[ce.edgeIdx]

				exactDist, ratio := geo.PointToSegmentDist(
					lat, lng,
					s.g.NodeLat[u], s.g.NodeLon[u],
					s.g.NodeLat[v], s.g.NodeLon[v],
				)

				if exactDist < bestDist {
					bestDist = exactDist
					bestResult = SnapResult{
						EdgeIdx: ce.edgeIdx,
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
