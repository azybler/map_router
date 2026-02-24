package geo

import (
	"math"
	"testing"
)

func TestHaversine(t *testing.T) {
	tests := []struct {
		name              string
		lat1, lon1        float64
		lat2, lon2        float64
		wantMeters        float64
		tolerancePercent  float64
	}{
		{
			name:     "Singapore CBD to Changi Airport",
			lat1:     1.2830, lon1: 103.8513, // Raffles Place
			lat2:     1.3644, lon2: 103.9915, // Changi Airport
			wantMeters:       18_023, // ~18 km great-circle
			tolerancePercent: 1,
		},
		{
			name:     "Same point",
			lat1:     1.3521, lon1: 103.8198,
			lat2:     1.3521, lon2: 103.8198,
			wantMeters:       0,
			tolerancePercent: 0,
		},
		{
			name:     "London to Paris",
			lat1:     51.5074, lon1: -0.1278,
			lat2:     48.8566, lon2: 2.3522,
			wantMeters:       343_500, // ~343.5 km
			tolerancePercent: 1,
		},
		{
			name:     "Short distance (~100m)",
			lat1:     1.3521, lon1: 103.8198,
			lat2:     1.3530, lon2: 103.8198,
			wantMeters:       100,
			tolerancePercent: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Haversine(tt.lat1, tt.lon1, tt.lat2, tt.lon2)
			if tt.wantMeters == 0 {
				if got != 0 {
					t.Errorf("expected 0, got %f", got)
				}
				return
			}
			diff := math.Abs(got-tt.wantMeters) / tt.wantMeters * 100
			if diff > tt.tolerancePercent {
				t.Errorf("Haversine = %f m, want ~%f m (diff %.1f%%)", got, tt.wantMeters, diff)
			}
		})
	}
}

func TestEquirectangularDist(t *testing.T) {
	// At Singapore latitude, equirectangular should be very close to Haversine.
	lat1, lon1 := 1.3521, 103.8198
	lat2, lon2 := 1.3600, 103.8300

	h := Haversine(lat1, lon1, lat2, lon2)
	e := EquirectangularDist(lat1, lon1, lat2, lon2)

	diffPercent := math.Abs(h-e) / h * 100
	if diffPercent > 0.5 {
		t.Errorf("EquirectangularDist differs from Haversine by %.2f%% (haversine=%f, equirect=%f)", diffPercent, h, e)
	}
}

func TestPointToSegmentDist(t *testing.T) {
	tests := []struct {
		name      string
		pLat, pLon float64
		aLat, aLon float64
		bLat, bLon float64
		wantRatio  float64
		maxDistM   float64 // max expected distance
	}{
		{
			name: "Point at start of segment",
			pLat: 1.3500, pLon: 103.8200,
			aLat: 1.3500, aLon: 103.8200,
			bLat: 1.3600, bLon: 103.8200,
			wantRatio: 0.0,
			maxDistM:  1,
		},
		{
			name: "Point at end of segment",
			pLat: 1.3600, pLon: 103.8200,
			aLat: 1.3500, aLon: 103.8200,
			bLat: 1.3600, bLon: 103.8200,
			wantRatio: 1.0,
			maxDistM:  1,
		},
		{
			name: "Point at midpoint perpendicular",
			pLat: 1.3550, pLon: 103.8210,
			aLat: 1.3500, aLon: 103.8200,
			bLat: 1.3600, bLon: 103.8200,
			wantRatio: 0.5,
			maxDistM:  200, // roughly 111m perpendicular
		},
		{
			name: "Degenerate segment (A == B)",
			pLat: 1.3500, pLon: 103.8210,
			aLat: 1.3500, aLon: 103.8200,
			bLat: 1.3500, bLon: 103.8200,
			wantRatio: 0.0,
			maxDistM:  200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dist, ratio := PointToSegmentDist(tt.pLat, tt.pLon, tt.aLat, tt.aLon, tt.bLat, tt.bLon)
			if dist > tt.maxDistM {
				t.Errorf("dist = %f m, want <= %f m", dist, tt.maxDistM)
			}
			if math.Abs(ratio-tt.wantRatio) > 0.05 {
				t.Errorf("ratio = %f, want ~%f", ratio, tt.wantRatio)
			}
		})
	}
}

func BenchmarkHaversine(b *testing.B) {
	for b.Loop() {
		Haversine(1.3521, 103.8198, 1.2905, 103.8520)
	}
}

func BenchmarkEquirectangularDist(b *testing.B) {
	for b.Loop() {
		EquirectangularDist(1.3521, 103.8198, 1.2905, 103.8520)
	}
}
