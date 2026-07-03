package osm

import (
	"math"
	"os"
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

func TestLoadSpeedTable(t *testing.T) {
	jsonData := `{"class_kmh":{"motorway":100,"primary":50},"zone_kmh":{"MY:urban":60},"link_factor":0.6,"fallback":28}`
	tbl, err := ParseSpeedTable([]byte(jsonData))
	if err != nil {
		t.Fatal(err)
	}
	if tbl.ClassKmh["motorway"] != 100 || tbl.LinkFactor != 0.6 || tbl.Fallback != 28 {
		t.Errorf("parsed table wrong: %+v", tbl)
	}

	// Exercise the filesystem path too.
	dir := t.TempDir()
	path := dir + "/speeds.json"
	if err := os.WriteFile(path, []byte(jsonData), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSpeedTable(path)
	if err != nil {
		t.Fatalf("LoadSpeedTable: %v", err)
	}
	if loaded.ClassKmh["primary"] != 50 || loaded.Fallback != 28 {
		t.Errorf("loaded table wrong: %+v", loaded)
	}
	if _, err := LoadSpeedTable(dir + "/missing.json"); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestFloorAndCapClassKmh(t *testing.T) {
	jsonData := `{"floor_class_kmh":{"motorway":90},"cap_class_kmh":{"primary":60},"link_factor":0.5}`
	tbl, err := ParseSpeedTable([]byte(jsonData))
	if err != nil {
		t.Fatal(err)
	}
	tags := func(kv ...string) osm.Tags {
		var ts osm.Tags
		for i := 0; i+1 < len(kv); i += 2 {
			ts = append(ts, osm.Tag{Key: kv[i], Value: kv[i+1]})
		}
		return ts
	}
	// Motorway tagged maxspeed=60 floors to 90.
	if v := tbl.SpeedKmh(tags("highway", "motorway", "maxspeed", "60")); v != 90 {
		t.Errorf("floored motorway = %v, want 90", v)
	}
	// Motorway tagged above the floor keeps its tag.
	if v := tbl.SpeedKmh(tags("highway", "motorway", "maxspeed", "110")); v != 110 {
		t.Errorf("fast motorway = %v, want 110", v)
	}
	// Motorway link floors at LinkFactor x floor = 45.
	if v := tbl.SpeedKmh(tags("highway", "motorway_link", "maxspeed", "40")); v != 45 {
		t.Errorf("floored motorway_link = %v, want 45", v)
	}
	// Primary tagged maxspeed=80 caps to 60.
	if v := tbl.SpeedKmh(tags("highway", "primary", "maxspeed", "80")); v != 60 {
		t.Errorf("capped primary = %v, want 60", v)
	}
	// Primary tagged below the cap keeps its tag.
	if v := tbl.SpeedKmh(tags("highway", "primary", "maxspeed", "40")); v != 40 {
		t.Errorf("slow primary = %v, want 40", v)
	}
	// Untagged classes unaffected by floor/cap of other classes.
	if v := tbl.SpeedKmh(tags("highway", "residential")); v != DefaultSpeedTable().ClassKmh["residential"] {
		t.Errorf("residential = %v, want default", v)
	}
}
