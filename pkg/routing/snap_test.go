package routing

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/geo"
	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

// snapTestGraph: road A (0<->1) and a separate road B (2<->3) ~30 m north.
func snapTestGraph() *graph.Graph {
	return graph.Build(&osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 20, ToNodeID: 10, Weight: 100},
			{FromNodeID: 30, ToNodeID: 40, Weight: 100},
			{FromNodeID: 40, ToNodeID: 30, Weight: 100},
		},
		NodeLat: map[osm.NodeID]float64{10: 1.30000, 20: 1.30000, 30: 1.30027, 40: 1.30027},
		NodeLon: map[osm.NodeID]float64{10: 103.800, 20: 103.801, 30: 103.800, 40: 103.801},
	})
}

func TestSnapCandidatesDistinctAndSorted(t *testing.T) {
	s := NewSnapper(snapTestGraph())
	cands := s.SnapCandidates(1.30005, 103.8005, 4, 500.0)
	if len(cands) < 2 {
		t.Fatalf("expected at least 2 distinct candidates, got %d", len(cands))
	}
	for i := 1; i < len(cands); i++ {
		if cands[i].Dist < cands[i-1].Dist {
			t.Errorf("candidates not sorted by distance: %v", cands)
		}
	}
	seen := map[[2]uint32]bool{}
	for _, c := range cands {
		a, b := c.NodeU, c.NodeV
		if a > b {
			a, b = b, a
		}
		if seen[[2]uint32{a, b}] {
			t.Errorf("duplicate node-pair candidate %d-%d", a, b)
		}
		seen[[2]uint32{a, b}] = true
	}
}

func TestSnapCandidatesRespectsRadius(t *testing.T) {
	s := NewSnapper(snapTestGraph())
	cands := s.SnapCandidates(1.4, 103.9, 4, 50.0)
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates far from roads, got %d", len(cands))
	}
}

func TestSnapCandidatesRespectsK(t *testing.T) {
	s := NewSnapper(snapTestGraph())
	// Two distinct roads are nearby, but k=1 must cap the result to one.
	cands := s.SnapCandidates(1.30005, 103.8005, 1, 500.0)
	if len(cands) != 1 {
		t.Errorf("expected exactly 1 candidate with k=1, got %d", len(cands))
	}
	// k <= 0 must not panic (negative make cap) and returns nothing.
	if got := s.SnapCandidates(1.30005, 103.8005, 0, 500.0); len(got) != 0 {
		t.Errorf("expected 0 candidates with k=0, got %d", len(got))
	}
}

// snapCandidatesReference is SnapCandidates as it was before the bounding-box
// test, the reused scratch buffer and the scan for repeats: the exact distance
// to every edge in the cells, a fresh slice, a map. SnapCandidates must return
// exactly what this returns.
func snapCandidatesReference(s *Snapper, lat, lng float64, k int, radiusMeters float64) []SnapResult {
	if k <= 0 {
		return nil
	}
	centerLat, centerLon := gridCell(lat, lng)
	span := int32(radiusMeters/(gridCellSize*111000)) + 1

	var all []SnapResult
	for dLat := -span; dLat <= span; dLat++ {
		for dLon := -span; dLon <= span; dLon++ {
			key := cellKey(centerLat+dLat, centerLon+dLon)
			for _, ce := range s.cellRange(key) {
				u := ce.source
				v := s.g.Head[ce.edgeIdx]
				exactDist, ratio := geo.PointToSegmentDist(
					lat, lng,
					s.g.NodeLat[u], s.g.NodeLon[u],
					s.g.NodeLat[v], s.g.NodeLon[v],
				)
				if exactDist <= radiusMeters {
					all = append(all, SnapResult{
						EdgeIdx: ce.edgeIdx, NodeU: u, NodeV: v, Ratio: ratio, Dist: exactDist,
					})
				}
			}
		}
	}

	sort.Slice(all, func(i, j int) bool { return all[i].Dist < all[j].Dist })

	seen := make(map[uint64]struct{}, len(all))
	out := make([]SnapResult, 0, k)
	for _, r := range all {
		a, b := r.NodeU, r.NodeV
		if a > b {
			a, b = b, a
		}
		pairKey := uint64(a)<<32 | uint64(b)
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

// randomStreets builds a few hundred streets within about 3 km of (lat0,
// lon0), a few hundred metres long each: one-way and two-way, some drawn twice
// between the same two junctions, one of no length, and a few running to
// places hundreds of kilometres away (so that their ends are far outside any
// query's reach).
func randomStreets(rng *rand.Rand, lat0, lon0 float64) *graph.Graph {
	const side = 20 // junctions per side, about 275 m apart
	const step = 0.05 / side
	res := &osmparser.ParseResult{
		NodeLat: map[osm.NodeID]float64{},
		NodeLon: map[osm.NodeID]float64{},
	}
	id := func(r, c int) osm.NodeID { return osm.NodeID(r*side + c + 1) }
	for r := range side {
		for c := range side {
			res.NodeLat[id(r, c)] = lat0 + (float64(r)-side/2+rng.Float64()*0.8)*step
			res.NodeLon[id(r, c)] = lon0 + (float64(c)-side/2+rng.Float64()*0.8)*step
		}
	}
	edge := func(a, b osm.NodeID) {
		res.Edges = append(res.Edges, osmparser.RawEdge{FromNodeID: a, ToNodeID: b, Weight: 100})
	}
	for r := range side {
		for c := range side {
			// To the junction east, north, or north-east, each most of the time.
			for _, d := range [][2]int{{0, 1}, {1, 0}, {1, 1}} {
				nr, nc := r+d[0], c+d[1]
				if nr >= side || nc >= side || rng.IntN(4) == 0 {
					continue
				}
				a, b := id(r, c), id(nr, nc)
				edge(a, b)
				if rng.IntN(2) == 0 {
					edge(b, a) // two-way: the reverse half is the same road
				}
				if rng.IntN(10) == 0 {
					edge(a, b) // the same road drawn twice
				}
			}
		}
	}
	next := osm.NodeID(side*side + 1)
	// A segment of no length.
	res.NodeLat[next], res.NodeLon[next] = res.NodeLat[id(3, 3)], res.NodeLon[id(3, 3)]
	edge(id(3, 3), next)
	next++
	// Long segments: the far end, in latitude or longitude or both, well
	// beyond one degree.
	for _, d := range [][2]float64{{0.4, 0.3}, {1.6, -0.2}, {-2.5, 3.0}, {0.01, 4.0}, {-3.0, 0.02}} {
		res.NodeLat[next], res.NodeLon[next] = lat0+d[0], lon0+d[1]
		edge(id(rng.IntN(side), rng.IntN(side)), next)
		edge(next, id(rng.IntN(side), rng.IntN(side)))
		next++
	}
	return graph.Build(res)
}

// The places the equality tests run at: near the equator (where the routing
// data is), across the equator and the prime meridian, mid and high
// latitudes, and close enough to the pole that longitude tells nothing.
var snapTestPlaces = []struct {
	name     string
	lat, lon float64
}{
	{"Kuala Lumpur", 3.139, 101.687},
	{"equator and prime meridian", 0.004, -0.006},
	{"Sydney", -33.87, 151.21},
	{"Reykjavik", 64.14, -21.94},
	{"near the pole", 89.6, 15.0},
}

// SnapCandidates must return exactly what it returned before it was made
// faster: the same edges, in the same order, with the same ratios and
// distances, for any k and any radius.
func TestSnapCandidatesMatchesReference(t *testing.T) {
	for i, place := range snapTestPlaces {
		t.Run(place.name, func(t *testing.T) {
			rng := rand.New(rand.NewPCG(1, uint64(i)))
			g := randomStreets(rng, place.lat, place.lon)
			s := NewSnapper(g)

			type point struct{ lat, lng float64 }
			var points []point
			for range 120 {
				points = append(points, point{place.lat + (rng.Float64()-0.5)*0.07, place.lon + (rng.Float64()-0.5)*0.07})
			}
			// Exactly on a node, and exactly on the edge of a grid cell.
			points = append(points, point{g.NodeLat[0], g.NodeLon[0]})
			points = append(points, point{math.Floor(place.lat/gridCellSize) * gridCellSize, math.Floor(place.lon/gridCellSize) * gridCellSize})

			found := 0
			for n, p := range points {
				radii := []float64{0, 30, 500, 1500}
				if n%12 == 0 {
					// The fallback radius and beyond: thousands of cells each,
					// so on a few of the points only.
					radii = append(radii, 5000, 30000)
				}
				for _, k := range []int{1, 2, 4, 8, scanDedupeMaxK, scanDedupeMaxK + 1, 50} {
					for _, radius := range radii {
						want := snapCandidatesReference(s, p.lat, p.lng, k, radius)
						got := s.SnapCandidates(p.lat, p.lng, k, radius)
						if len(got) != len(want) {
							t.Fatalf("(%v, %v) k=%d radius=%v: %d candidates, want %d", p.lat, p.lng, k, radius, len(got), len(want))
						}
						for j := range want {
							if got[j] != want[j] {
								t.Fatalf("(%v, %v) k=%d radius=%v: candidate %d is %+v, want %+v", p.lat, p.lng, k, radius, j, got[j], want[j])
							}
						}
						found += len(want)
					}
				}
			}
			if found == 0 {
				t.Fatal("no query found a candidate: the test compared nothing")
			}
		})
	}
}

// The bounding-box test may only rule out a segment the exact distance also
// puts outside the radius. Checked on segments and points drawn to sit on both
// sides of the radius, at every latitude.
func TestBoxTestNeverRulesOutASegmentInRange(t *testing.T) {
	rng := rand.New(rand.NewPCG(2, 3))
	radii := []float64{0, 1, 40, 500, 1500, 5000}
	ruledOut, inRange := 0, 0
	for n := range 400000 {
		lat := (rng.Float64() - 0.5) * 179.8
		if n%7 == 0 {
			lat = (rng.Float64() - 0.5) * 12 // most of the data is near the equator
		}
		lng := (rng.Float64() - 0.5) * 359
		radius := radii[rng.IntN(len(radii))]

		// A segment near the point: its near end within a radius or two, its
		// length anything from a metre to hundreds of kilometres.
		reachDeg := (radius + 5) / geo.MetersPerDegree * 1.5
		lonScale := 1 / math.Max(0.01, math.Cos(lat*math.Pi/180))
		length := math.Pow(10, -5+rng.Float64()*5.5) // degrees: 1e-5 to about 3
		uLat := lat + (rng.Float64()-0.5)*2*reachDeg
		uLon := lng + (rng.Float64()-0.5)*2*reachDeg*lonScale
		vLat := uLat + (rng.Float64()-0.5)*2*length
		vLon := uLon + (rng.Float64()-0.5)*2*length*lonScale
		if n%50 == 0 {
			vLat, vLon = uLat, uLon // no length
		}
		if n%5 == 0 && radius > 0 {
			// A long segment running north-south beside the point, in range
			// by the exact distance. That distance scales longitude by the
			// cosine at the segment's mid-latitude, which is degrees away
			// from the point's: the hardest case for a test that takes one
			// cosine per query.
			uLat = lat - rng.Float64()*0.5
			vLat = lat + 2 + rng.Float64()*40
			if lat > 0 {
				uLat, vLat = lat+rng.Float64()*0.5, lat-2-rng.Float64()*40
			}
			midCos := math.Cos((uLat + vLat) / 2 * math.Pi / 180)
			gap := (0.2 + rng.Float64()*0.799) * radius / (geo.MetersPerDegree * midCos)
			if rng.IntN(2) == 0 {
				gap = -gap
			}
			uLon, vLon = lng+gap, lng+gap
		}
		if math.Abs(uLat) > 90 || math.Abs(vLat) > 90 {
			continue
		}

		box := newBoxTest(lat, lng, radius)
		dist, _ := geo.PointToSegmentDist(lat, lng, uLat, uLon, vLat, vLon)
		if dist <= radius {
			inRange++
		}
		if !box.rulesOut(uLat, uLon, vLat, vLon) {
			continue
		}
		ruledOut++
		if dist <= radius {
			t.Fatalf("ruled out a segment %.6f m away with radius %v: point (%v, %v), segment (%v, %v)-(%v, %v)",
				dist, radius, lat, lng, uLat, uLon, vLat, vLon)
		}
	}
	// Both outcomes have to be common, or the cases above tested little.
	if ruledOut < 40000 || inRange < 40000 {
		t.Fatalf("%d segments ruled out and %d in range, of 400000: want at least 40000 of each", ruledOut, inRange)
	}
}

// The test is only worth having if it rules out what is plainly out of range.
func TestBoxTestRulesOutDistantSegments(t *testing.T) {
	box := newBoxTest(3.139, 101.687, 500)
	for _, tc := range []struct {
		name                   string
		uLat, uLon, vLat, vLon float64
		want                   bool
	}{
		{"through the point", 3.138, 101.686, 3.140, 101.688, false},
		{"300 m north", 3.1417, 101.686, 3.1417, 101.688, false},
		{"2 km north", 3.157, 101.686, 3.157, 101.688, true},
		{"2 km south", 3.121, 101.686, 3.121, 101.688, true},
		{"2 km east", 3.138, 101.705, 3.140, 101.706, true},
		{"2 km west", 3.138, 101.668, 3.140, 101.669, true},
		// Its ends are far to either side, but it passes over the point.
		{"long, crossing overhead", 3.139, 100.0, 3.139, 103.0, false},
		// East of the point, but one end is too far north to be judged on
		// longitude: left to the exact distance.
		{"2 km east, 300 km long", 3.138, 101.705, 6.0, 101.706, false},
	} {
		if got := box.rulesOut(tc.uLat, tc.uLon, tc.vLat, tc.vLon); got != tc.want {
			t.Errorf("%s: rulesOut = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// denseStreets is a square street grid, about 100 m between junctions, every
// street two-way: the density of a city centre.
func denseStreets(lat0, lon0 float64, side int) *graph.Graph {
	res := &osmparser.ParseResult{
		NodeLat: map[osm.NodeID]float64{},
		NodeLon: map[osm.NodeID]float64{},
	}
	id := func(r, c int) osm.NodeID { return osm.NodeID(r*side + c + 1) }
	for r := range side {
		for c := range side {
			res.NodeLat[id(r, c)] = lat0 + float64(r)*0.0009
			res.NodeLon[id(r, c)] = lon0 + float64(c)*0.0009
			if c > 0 {
				res.Edges = append(res.Edges,
					osmparser.RawEdge{FromNodeID: id(r, c-1), ToNodeID: id(r, c), Weight: 100},
					osmparser.RawEdge{FromNodeID: id(r, c), ToNodeID: id(r, c-1), Weight: 100})
			}
			if r > 0 {
				res.Edges = append(res.Edges,
					osmparser.RawEdge{FromNodeID: id(r-1, c), ToNodeID: id(r, c), Weight: 100},
					osmparser.RawEdge{FromNodeID: id(r, c), ToNodeID: id(r-1, c), Weight: 100})
			}
		}
	}
	return graph.Build(res)
}

// A call used to grow a slice of every edge in range and build a map of them:
// about 144 KB on city streets. It now allocates its result and little else.
func TestSnapCandidatesAllocatesLittle(t *testing.T) {
	const side = 40 // 4 km across
	s := NewSnapper(denseStreets(3.10, 101.60, side))
	lat, lng := 3.10+0.0009*side/2+0.0002, 101.60+0.0009*side/2+0.0003
	if got := len(s.SnapCandidates(lat, lng, 8, 500)); got != 8 {
		t.Fatalf("%d candidates on a dense grid, want 8", got)
	}
	var total int
	allocs := testing.AllocsPerRun(200, func() {
		total += len(s.SnapCandidates(lat, lng, 8, 500))
	})
	// The result, and what sort.Slice needs. A collection during the run can
	// empty the scratch pool once; averaged over 200 calls that is nothing.
	if allocs > 6 {
		t.Errorf("SnapCandidates made %.1f allocations per call, want at most 6", allocs)
	}
	t.Logf("%.1f allocations per call", allocs)
	if total == 0 {
		t.Fatal("no candidates")
	}
}

// One Snapper serves every query at once, so its scratch buffers must not be
// shared between calls in flight. Run with -race.
func TestSnapCandidatesConcurrent(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 5))
	s := NewSnapper(randomStreets(rng, 3.139, 101.687))
	type query struct {
		lat, lng float64
		k        int
		radius   float64
		want     []SnapResult
	}
	var queries []query
	for range 200 {
		q := query{
			lat:    3.139 + (rng.Float64()-0.5)*0.06,
			lng:    101.687 + (rng.Float64()-0.5)*0.06,
			k:      1 + rng.IntN(20),
			radius: []float64{40, 500, 1500, 5000}[rng.IntN(4)],
		}
		q.want = snapCandidatesReference(s, q.lat, q.lng, q.k, q.radius)
		queries = append(queries, q)
	}

	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queries {
				q := queries[(i+w*31)%len(queries)]
				got := s.SnapCandidates(q.lat, q.lng, q.k, q.radius)
				if len(got) != len(q.want) {
					errs <- fmt.Sprintf("%d candidates, want %d", len(got), len(q.want))
					return
				}
				for j := range got {
					if got[j] != q.want[j] {
						errs <- fmt.Sprintf("candidate %d is %+v, want %+v", j, got[j], q.want[j])
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

// City-centre density: about 3,000 edges in the nine cells searched.
func BenchmarkSnapCandidates(b *testing.B) {
	const side = 40
	s := NewSnapper(denseStreets(3.10, 101.60, side))
	lat, lng := 3.10+0.0009*side/2+0.0002, 101.60+0.0009*side/2+0.0003
	b.ReportAllocs()
	for b.Loop() {
		s.SnapCandidates(lat, lng, 8, 500)
	}
}

// The same search as it was, for comparison.
func BenchmarkSnapCandidatesReference(b *testing.B) {
	const side = 40
	s := NewSnapper(denseStreets(3.10, 101.60, side))
	lat, lng := 3.10+0.0009*side/2+0.0002, 101.60+0.0009*side/2+0.0003
	b.ReportAllocs()
	for b.Loop() {
		snapCandidatesReference(s, lat, lng, 8, 500)
	}
}
