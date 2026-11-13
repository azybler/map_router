package routing

import (
	"errors"
	"math"
	"sort"
	"sync"

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

	// Reusable snapScratch for SnapCandidates. Small and cheap to rebuild, so
	// it does not matter that the garbage collector empties a pool.
	scratch sync.Pool
}

// snapScratch is the working memory of one SnapCandidates call: every edge
// within the radius, before the nearest are picked. On dense streets that is
// about a thousand edges, a slice that used to be grown from nothing on every
// call.
type snapScratch struct {
	all []SnapResult
}

// maxKeptScratch is the largest scratch buffer, in candidates, handed back for
// reuse. A search out to the last fallback radius in a city fills megabytes;
// that is not worth holding on to.
const maxKeptScratch = 8192

func (s *Snapper) getScratch() *snapScratch {
	if sc, ok := s.scratch.Get().(*snapScratch); ok {
		return sc
	}
	return &snapScratch{}
}

func (s *Snapper) putScratch(sc *snapScratch, all []SnapResult) {
	if cap(all) > maxKeptScratch {
		all = nil
	}
	sc.all = all[:0]
	s.scratch.Put(sc)
}

// boxTest rules out segments that cannot be within a radius of a point, by
// their bounding boxes. A cell is about 1.1 km across and a search covers at
// least nine of them, so most of the segments it looks at are far outside a
// 500 m radius; this tells with a few comparisons, where the exact distance
// costs a cosine and a square root each.
//
// It only ever rules out a segment that PointToSegmentDist would also put
// outside the radius, so SnapCandidates returns exactly what it would without
// it. PointToSegmentDist measures in a projection where a degree of latitude
// is geo.MetersPerDegree and a degree of longitude is that times the cosine of
// the segment's mid-latitude. The nearest point of a segment lies within the
// segment's latitude range and within its longitude range, so the point is at
// least as far from the segment as it is from either range:
//
//   - latitude: a segment wholly north of latMax or south of latMin is more
//     than the radius away;
//   - longitude: so is one wholly east of lngMax or west of lngMin. Those two
//     are set with cosFloor, the cosine of a latitude further from the equator
//     than any segment judged this way, so no cosine is taken per segment.
//     Only segments with both ends within boxTestLatReachDeg of the point's
//     latitude are judged on longitude: that is all but the rare segment
//     hundreds of kilometres long, which goes to the exact test.
//
// boxTestSlackMeters is added to the radius. Rounding in PointToSegmentDist is
// many orders of magnitude smaller, so it cannot bring a ruled-out segment
// back inside the radius.
type boxTest struct {
	latMin, latMax     float64 // the radius, plus slack, either side of the point
	lngMin, lngMax     float64
	reachMin, reachMax float64 // latitudes a segment must stay within to be judged on longitude
}

const (
	boxTestSlackMeters = 1.0
	boxTestLatReachDeg = 1.0
)

func newBoxTest(lat, lng, radiusMeters float64) boxTest {
	latLim := (radiusMeters + boxTestSlackMeters) / geo.MetersPerDegree
	// Past the pole the cosine is negative: floored at 0, the longitude limits
	// below are infinite and rule nothing out.
	cosFloor := math.Max(0, math.Cos((math.Abs(lat)+boxTestLatReachDeg)*math.Pi/180))
	lngLim := latLim / cosFloor
	return boxTest{
		latMin: lat - latLim, latMax: lat + latLim,
		lngMin: lng - lngLim, lngMax: lng + lngLim,
		reachMin: lat - boxTestLatReachDeg, reachMax: lat + boxTestLatReachDeg,
	}
}

// rulesOut reports whether the segment from (uLat, uLon) to (vLat, vLon) is
// certainly further from the point than the radius.
func (b *boxTest) rulesOut(uLat, uLon, vLat, vLon float64) bool {
	if (uLat < b.latMin && vLat < b.latMin) || (uLat > b.latMax && vLat > b.latMax) {
		return true
	}
	if (uLon < b.lngMin && vLon < b.lngMin) || (uLon > b.lngMax && vLon > b.lngMax) {
		return uLat > b.reachMin && uLat < b.reachMax && vLat > b.reachMin && vLat < b.reachMax
	}
	return false
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

// SnapCandidates returns up to k nearest DISTINCT road edges within radiusMeters
// of the query point, sorted ascending by off-road distance. Distinct = unique
// undirected node-pair, so the two directed halves of a two-way road and
// duplicate geometry collapse to one candidate. The grid ring span is derived
// from radiusMeters, so radii beyond the historical ~1.1 km 3×3 window are
// searched correctly (used by the escalating-radius fallback in Route).
func (s *Snapper) SnapCandidates(lat, lng float64, k int, radiusMeters float64) []SnapResult {
	if k <= 0 {
		return nil
	}
	centerLat, centerLon := gridCell(lat, lng)

	// Ring span: each cell is ~1.1 km of latitude; cover radiusMeters fully
	// (min 1 ring keeps the historical 3×3 behavior for radii ≤ 500 m).
	span := int32(radiusMeters/(gridCellSize*111000)) + 1

	box := newBoxTest(lat, lng, radiusMeters)
	sc := s.getScratch()
	all := sc.all[:0]
	for dLat := -span; dLat <= span; dLat++ {
		for dLon := -span; dLon <= span; dLon++ {
			key := cellKey(centerLat+dLat, centerLon+dLon)
			for _, ce := range s.cellRange(key) {
				u := ce.source
				v := s.g.Head[ce.edgeIdx]
				uLat, uLon := s.g.NodeLat[u], s.g.NodeLon[u]
				vLat, vLon := s.g.NodeLat[v], s.g.NodeLon[v]
				if box.rulesOut(uLat, uLon, vLat, vLon) {
					continue
				}
				exactDist, ratio := geo.PointToSegmentDist(lat, lng, uLat, uLon, vLat, vLon)
				if exactDist <= radiusMeters {
					all = append(all, SnapResult{
						EdgeIdx: ce.edgeIdx, NodeU: u, NodeV: v, Ratio: ratio, Dist: exactDist,
					})
				}
			}
		}
	}

	sort.Slice(all, func(i, j int) bool { return all[i].Dist < all[j].Dist })

	out := firstDistinct(all, k)
	s.putScratch(sc, all)
	return out
}

// scanDedupeMaxK is the largest k for which firstDistinct looks for a repeat
// among the candidates it has already picked. Callers ask for a handful, and
// comparing against a handful beats building a map of everything in range.
const scanDedupeMaxK = 16

// nodePairKey identifies the road a candidate is on, whichever way its edge
// runs.
func nodePairKey(r SnapResult) uint64 {
	a, b := r.NodeU, r.NodeV
	if a > b {
		a, b = b, a
	}
	return uint64(a)<<32 | uint64(b)
}

// firstDistinct returns the first k entries of sorted that are on different
// roads: the first candidate on a road stands for it, and later ones on the
// same pair of nodes are dropped.
func firstDistinct(sorted []SnapResult, k int) []SnapResult {
	out := make([]SnapResult, 0, k)
	if k > scanDedupeMaxK {
		seen := make(map[uint64]struct{}, len(sorted))
		for _, r := range sorted {
			pairKey := nodePairKey(r)
			if _, dup := seen[pairKey]; dup {
				continue
			}
			seen[pairKey] = struct{}{}
			out = append(out, r)
			if len(out) >= k {
				break
			}
		}
		return out
	}
next:
	for i := range sorted {
		pairKey := nodePairKey(sorted[i])
		for j := range out {
			if nodePairKey(out[j]) == pairKey {
				continue next
			}
		}
		out = append(out, sorted[i])
		if len(out) >= k {
			break
		}
	}
	return out
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
