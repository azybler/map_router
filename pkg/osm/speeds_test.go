package osm

import (
	"math"
	"testing"

	"github.com/paulmach/osm"
)

func tags(kv ...string) osm.Tags {
	var t osm.Tags
	for i := 0; i+1 < len(kv); i += 2 {
		t = append(t, osm.Tag{Key: kv[i], Value: kv[i+1]})
	}
	return t
}

func TestSpeedKmh(t *testing.T) {
	tbl := DefaultSpeedTable()
	cases := []struct {
		name string
		tags osm.Tags
		want float64
	}{
		{"motorway default", tags("highway", "motorway"), 90},
		{"residential default", tags("highway", "residential"), 25},
		{"service default", tags("highway", "service"), 15},
		{"motorway_link derived", tags("highway", "motorway_link"), 0.7 * 90},
		{"numeric maxspeed", tags("highway", "primary", "maxspeed", "80"), 80},
		{"mph maxspeed", tags("highway", "primary", "maxspeed", "30 mph"), 30 * 1.609344},
		{"MY:urban zone", tags("highway", "primary", "maxspeed", "MY:urban"), 60},
		{"none falls back to class", tags("highway", "secondary", "maxspeed", "none"), 45},
		{"garbage falls back", tags("highway", "tertiary", "maxspeed", "fast"), 38},
		{"unknown class falls back", tags("highway", "track"), tbl.Fallback},
		{"link maxspeed wins over derivation", tags("highway", "motorway_link", "maxspeed", "80"), 80},
		{"kmh unit", tags("highway", "primary", "maxspeed", "50 km/h"), 50},
		{"unknown unit falls back", tags("highway", "primary", "maxspeed", "50 knots"), 55},
	}
	for _, c := range cases {
		got := tbl.SpeedKmh(c.tags)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("%s: SpeedKmh = %.3f, want %.3f", c.name, got, c.want)
		}
	}
}
