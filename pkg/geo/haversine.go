package geo

import "math"

const earthRadiusMeters = 6_371_000.0

// Haversine returns the great-circle distance in meters between two points.
func Haversine(lat1, lon1, lat2, lon2 float64) float64 {
	lat1r := lat1 * math.Pi / 180
	lat2r := lat2 * math.Pi / 180
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1r)*math.Cos(lat2r)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return earthRadiusMeters * c
}

// EquirectangularDist returns an approximate distance in meters.
// ~3x faster than Haversine; accurate to <0.1% at Singapore's latitude (~1.3°N).
// Use for candidate filtering and comparisons, not for final edge weights.
func EquirectangularDist(lat1, lon1, lat2, lon2 float64) float64 {
	x := (lon2 - lon1) * math.Cos((lat1+lat2)/2*math.Pi/180) * math.Pi / 180
	y := (lat2 - lat1) * math.Pi / 180
	return math.Sqrt(x*x+y*y) * earthRadiusMeters
}

// PointToSegmentDist computes the perpendicular distance from point P to segment AB,
// and returns the projection ratio along AB (clamped to [0,1]).
// dist is in meters, ratio is in [0.0, 1.0].
func PointToSegmentDist(pLat, pLon, aLat, aLon, bLat, bLon float64) (dist float64, ratio float64) {
	// Work in equirectangular projection (good enough at Singapore latitude).
	cosLat := math.Cos((aLat+bLat) / 2 * math.Pi / 180)

	// Convert to approximate planar coordinates (meters).
	ax := aLon * cosLat
	ay := aLat
	bx := bLon * cosLat
	by := bLat
	px := pLon * cosLat
	py := pLat

	// Check for degenerate segment using original coordinates.
	if aLat == bLat && aLon == bLon {
		return Haversine(pLat, pLon, aLat, aLon), 0
	}

	dx := bx - ax
	dy := by - ay
	lenSq := dx*dx + dy*dy

	if lenSq == 0 {
		return Haversine(pLat, pLon, aLat, aLon), 0
	}

	// Project P onto line AB, clamp to [0,1].
	t := ((px-ax)*dx + (py-ay)*dy) / lenSq
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}

	// Closest point on segment in original coordinates.
	closeLat := aLat + t*(bLat-aLat)
	closeLon := aLon + t*(bLon-aLon)

	return Haversine(pLat, pLon, closeLat, closeLon), t
}
