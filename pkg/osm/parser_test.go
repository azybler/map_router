package osm

import (
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
			name: "private access",
			tags: osm.Tags{
				{Key: "highway", Value: "residential"},
				{Key: "access", Value: "private"},
			},
			want: false,
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
