package osm

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/paulmach/osm"
)

// SpeedTable maps highway classes to free-flow speeds (km/h). Link classes are
// derived as LinkFactor × parent. Fallback is used for unlisted drivable classes.
type SpeedTable struct {
	ClassKmh   map[string]float64
	ZoneKmh    map[string]float64 // maxspeed zone codes, e.g. "MY:urban"
	LinkFactor float64
	Fallback   float64
}

// DefaultSpeedTable returns the Malaysian-urban free-flow priors.
func DefaultSpeedTable() SpeedTable {
	return SpeedTable{
		ClassKmh: map[string]float64{
			"motorway": 90, "trunk": 70, "primary": 55, "secondary": 45,
			"tertiary": 38, "unclassified": 35, "residential": 25,
			"living_street": 12, "service": 15,
		},
		ZoneKmh: map[string]float64{
			"MY:urban": 60, "MY:rural": 90, "MY:expressway": 110,
			"RM:urban": 60, "RM:rural": 90,
		},
		LinkFactor: 0.7,
		Fallback:   30,
	}
}

// ParseSpeedTable parses a JSON speed table, overlaying it on DefaultSpeedTable.
// Omitted top-level fields keep their defaults. NOTE: class_kmh and zone_kmh,
// when present, REPLACE the entire default map (not a per-key merge) — so a
// provided class_kmh must list every class you rely on. link_factor/fallback
// override only when > 0.
func ParseSpeedTable(data []byte) (SpeedTable, error) {
	def := DefaultSpeedTable()
	var raw struct {
		ClassKmh   map[string]float64 `json:"class_kmh"`
		ZoneKmh    map[string]float64 `json:"zone_kmh"`
		LinkFactor float64            `json:"link_factor"`
		Fallback   float64            `json:"fallback"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return SpeedTable{}, err
	}
	if raw.ClassKmh != nil {
		def.ClassKmh = raw.ClassKmh
	}
	if raw.ZoneKmh != nil {
		def.ZoneKmh = raw.ZoneKmh
	}
	if raw.LinkFactor > 0 {
		def.LinkFactor = raw.LinkFactor
	}
	if raw.Fallback > 0 {
		def.Fallback = raw.Fallback
	}
	return def, nil
}

// LoadSpeedTable reads a JSON speed table from path.
func LoadSpeedTable(path string) (SpeedTable, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SpeedTable{}, err
	}
	return ParseSpeedTable(data)
}

// classSpeed returns the base (non-link) speed for a highway class.
func (s SpeedTable) classSpeed(hw string) float64 {
	if v, ok := s.ClassKmh[hw]; ok {
		return v
	}
	return s.Fallback
}

// SpeedKmh resolves a way's free-flow speed: maxspeed when parseable, else the
// class default (links = LinkFactor × parent class).
func (s SpeedTable) SpeedKmh(t osm.Tags) float64 {
	hw := t.Find("highway")

	if ms := strings.TrimSpace(t.Find("maxspeed")); ms != "" {
		if v, ok := s.parseMaxspeed(ms); ok {
			return v
		}
	}

	if strings.HasSuffix(hw, "_link") {
		return s.LinkFactor * s.classSpeed(strings.TrimSuffix(hw, "_link"))
	}
	return s.classSpeed(hw)
}

// parseMaxspeed handles "60", "30 mph", and zone codes; returns ok=false for
// "none"/"walk"/conditional/per-direction/garbage so the caller falls back.
func (s SpeedTable) parseMaxspeed(ms string) (float64, bool) {
	if v, ok := s.ZoneKmh[ms]; ok {
		return v, true
	}
	fields := strings.Fields(ms)
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	if len(fields) > 1 {
		switch strings.ToLower(fields[1]) {
		case "mph":
			return n * 1.609344, true
		case "km/h", "kmh", "kph":
			return n, true
		default:
			return 0, false // unknown unit → fall back to class default
		}
	}
	return n, true // bare number = km/h
}
