package osm

import (
	"math"
	"testing"

	"github.com/paulmach/osm"
)

func TestIsCarAccessible(t *testing.T) {
	tests := []struct {
		name string
		tags osm.Tags
		want bool
	}{
		{
			name: "residential road",
			tags: osm.Tags{{Key: "highway", Value: "residential"}},
			want: true,
		},
		{
			name: "motorway",
			tags: osm.Tags{{Key: "highway", Value: "motorway"}},
			want: true,
		},
		{
			name: "footway (not car accessible)",
			tags: osm.Tags{{Key: "highway", Value: "footway"}},
			want: false,
		},
		{
			name: "cycleway",
			tags: osm.Tags{{Key: "highway", Value: "cycleway"}},
			want: false,
		},
		{
			name: "private access (now kept as restricted)",
			tags: osm.Tags{
				{Key: "highway", Value: "residential"},
				{Key: "access", Value: "private"},
			},
			want: true,
		},
		{
			name: "no access",
			tags: osm.Tags{
				{Key: "highway", Value: "residential"},
				{Key: "access", Value: "no"},
			},
			want: false,
		},
		{
			name: "motor_vehicle=no",
			tags: osm.Tags{
				{Key: "highway", Value: "residential"},
				{Key: "motor_vehicle", Value: "no"},
			},
			want: false,
		},
		{
			name: "area=yes (pedestrian plaza)",
			tags: osm.Tags{
				{Key: "highway", Value: "service"},
				{Key: "area", Value: "yes"},
			},
			want: false,
		},
		{
			name: "service road",
			tags: osm.Tags{{Key: "highway", Value: "service"}},
			want: true,
		},
		{
			name: "living_street",
			tags: osm.Tags{{Key: "highway", Value: "living_street"}},
			want: true,
		},
		{
			name: "no highway tag",
			tags: osm.Tags{{Key: "name", Value: "Some Street"}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCarAccessible(tt.tags)
			if got != tt.want {
				t.Errorf("isCarAccessible() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDirectionFlags(t *testing.T) {
	tests := []struct {
		name        string
		tags        osm.Tags
		wantForward bool
		wantBackward bool
	}{
		{
			name:        "default bidirectional",
			tags:        osm.Tags{{Key: "highway", Value: "residential"}},
			wantForward: true,
			wantBackward: true,
		},
		{
			name:        "motorway implied oneway",
			tags:        osm.Tags{{Key: "highway", Value: "motorway"}},
			wantForward: true,
			wantBackward: false,
		},
		{
			name:        "motorway_link implied oneway",
			tags:        osm.Tags{{Key: "highway", Value: "motorway_link"}},
			wantForward: true,
			wantBackward: false,
		},
		{
			name:        "roundabout implied oneway",
			tags:        osm.Tags{
				{Key: "highway", Value: "residential"},
				{Key: "junction", Value: "roundabout"},
			},
			wantForward: true,
			wantBackward: false,
		},
		{
			name:        "explicit oneway=yes",
			tags:        osm.Tags{
				{Key: "highway", Value: "primary"},
				{Key: "oneway", Value: "yes"},
			},
			wantForward: true,
			wantBackward: false,
		},
		{
			name:        "explicit oneway=true",
			tags:        osm.Tags{
				{Key: "highway", Value: "primary"},
				{Key: "oneway", Value: "true"},
			},
			wantForward: true,
			wantBackward: false,
		},
		{
			name:        "explicit oneway=1",
			tags:        osm.Tags{
				{Key: "highway", Value: "primary"},
				{Key: "oneway", Value: "1"},
			},
			wantForward: true,
			wantBackward: false,
		},
		{
			name:        "explicit oneway=-1 (reverse)",
			tags:        osm.Tags{
				{Key: "highway", Value: "primary"},
				{Key: "oneway", Value: "-1"},
			},
			wantForward: false,
			wantBackward: true,
		},
		{
			name:        "explicit oneway=reverse",
			tags:        osm.Tags{
				{Key: "highway", Value: "primary"},
				{Key: "oneway", Value: "reverse"},
			},
			wantForward: false,
			wantBackward: true,
		},
		{
			name:        "explicit oneway=no overrides implied",
			tags:        osm.Tags{
				{Key: "highway", Value: "motorway"},
				{Key: "oneway", Value: "no"},
			},
			wantForward: true,
			wantBackward: true,
		},
		{
			name:        "oneway=reversible skips entirely",
			tags:        osm.Tags{
				{Key: "highway", Value: "primary"},
				{Key: "oneway", Value: "reversible"},
			},
			wantForward: false,
			wantBackward: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fwd, bwd := directionFlags(tt.tags)
			if fwd != tt.wantForward || bwd != tt.wantBackward {
				t.Errorf("directionFlags() = (%v, %v), want (%v, %v)", fwd, bwd, tt.wantForward, tt.wantBackward)
			}
		})
	}
}

func TestEdgeWeightIsTravelTime(t *testing.T) {
	tbl := DefaultSpeedTable()
	speed := tbl.classSpeed("primary") // 55 km/h
	lengthM := 1000.0
	wantMs := lengthM / (speed / 3.6) * 1000
	got := computeWeightMs(lengthM, speed)
	if math.Abs(float64(got)-wantMs) > 1 {
		t.Errorf("computeWeightMs = %d, want ~%.0f", got, wantMs)
	}
	if got == 0 {
		t.Error("weight must be >= 1")
	}
}

func TestClassifyAccess(t *testing.T) {
	cases := []struct {
		name           string
		tags           osm.Tags
		wantKeep       bool
		wantRestricted bool
	}{
		{"plain residential", osm.Tags{{Key: "highway", Value: "residential"}}, true, false},
		{"access=private gated", osm.Tags{{Key: "highway", Value: "residential"}, {Key: "access", Value: "private"}}, true, true},
		{"access=permit gated", osm.Tags{{Key: "highway", Value: "residential"}, {Key: "access", Value: "permit"}}, true, true},
		{"access=residents gated", osm.Tags{{Key: "highway", Value: "service"}, {Key: "access", Value: "residents"}}, true, true},
		{"access=private + motor_vehicle=no (access governs)", osm.Tags{{Key: "highway", Value: "residential"}, {Key: "access", Value: "private"}, {Key: "motor_vehicle", Value: "no"}}, true, true},
		{"access=destination stays public", osm.Tags{{Key: "highway", Value: "tertiary"}, {Key: "access", Value: "destination"}}, true, false},
		{"access=customers stays public", osm.Tags{{Key: "highway", Value: "service"}, {Key: "access", Value: "customers"}}, true, false},
		{"access=no dropped", osm.Tags{{Key: "highway", Value: "residential"}, {Key: "access", Value: "no"}}, false, false},
		{"plain motor_vehicle=no dropped", osm.Tags{{Key: "highway", Value: "residential"}, {Key: "motor_vehicle", Value: "no"}}, false, false},
		{"motor_vehicle=private restricted", osm.Tags{{Key: "highway", Value: "service"}, {Key: "motor_vehicle", Value: "private"}}, true, true},
		{"footway dropped", osm.Tags{{Key: "highway", Value: "footway"}}, false, false},
		{"area=yes dropped", osm.Tags{{Key: "highway", Value: "service"}, {Key: "area", Value: "yes"}}, false, false},
		{"no highway dropped", osm.Tags{{Key: "name", Value: "X"}}, false, false},
	}
	for _, c := range cases {
		keep, restricted := classifyAccess(c.tags)
		if keep != c.wantKeep || restricted != c.wantRestricted {
			t.Errorf("%s: classifyAccess = (%v,%v), want (%v,%v)", c.name, keep, restricted, c.wantKeep, c.wantRestricted)
		}
	}
}
