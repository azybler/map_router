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
// MaxspeedFactor scales numeric maxspeed tag values (posted limits) down to
// typical driven speeds; 1.0 = take the tag at face value.
// FloorClassKmh sets a per-class minimum effective speed that wins over lower
// maxspeed tags (links floor at LinkFactor × the parent's floor). Motivation:
// urban expressways like the LDP are highway=motorway with maxspeed=60, which
// otherwise erases the grade-separated advantage Google clearly models.
// CapClassKmh is the mirror: a per-class maximum effective speed that wins over
// higher maxspeed tags (links cap at LinkFactor × the parent's cap). Motivation:
// at-grade arterials tagged maxspeed=80 are not really faster than parallel
// grade-separated expressways once signals/junctions are accounted for.
type SpeedTable struct {
	ClassKmh       map[string]float64
	ZoneKmh        map[string]float64 // maxspeed zone codes, e.g. "MY:urban"
	LinkFactor     float64
	Fallback       float64
	MaxspeedFactor float64
	FloorClassKmh  map[string]float64
	CapClassKmh    map[string]float64
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
		LinkFactor:     0.7,
		Fallback:       30,
		MaxspeedFactor: 1.0,
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
		ClassKmh       map[string]float64 `json:"class_kmh"`
		ZoneKmh        map[string]float64 `json:"zone_kmh"`
		LinkFactor     float64            `json:"link_factor"`
		Fallback       float64            `json:"fallback"`
		MaxspeedFactor float64            `json:"maxspeed_factor"`
		FloorClassKmh  map[string]float64 `json:"floor_class_kmh"`
		CapClassKmh    map[string]float64 `json:"cap_class_kmh"`
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
	if raw.MaxspeedFactor > 0 {
		def.MaxspeedFactor = raw.MaxspeedFactor
	}
	if raw.FloorClassKmh != nil {
		def.FloorClassKmh = raw.FloorClassKmh
	}
	if raw.CapClassKmh != nil {
		def.CapClassKmh = raw.CapClassKmh
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
// class default (links = LinkFactor × parent class). A FloorClassKmh entry for
// the way's base class sets a minimum effective speed (links floor at
// LinkFactor × the parent's floor).
func (s SpeedTable) SpeedKmh(t osm.Tags) float64 {
	hw := t.Find("highway")
	isLink := strings.HasSuffix(hw, "_link")
	base := strings.TrimSuffix(hw, "_link")

	v := -1.0
	if ms := strings.TrimSpace(t.Find("maxspeed")); ms != "" {
		if p, ok := s.parseMaxspeed(ms); ok {
			v = p
		}
	}
	if v < 0 {
		if isLink {
			v = s.LinkFactor * s.classSpeed(base)
		} else {
			v = s.classSpeed(base)
		}
	}
	if f, ok := s.FloorClassKmh[base]; ok {
		floor := f
		if isLink {
			floor = f * s.LinkFactor
		}
		if v < floor {
			v = floor
		}
	}
	if c, ok := s.CapClassKmh[base]; ok {
		cap := c
		if isLink {
			cap = c * s.LinkFactor
		}
		if v > cap {
			v = cap
		}
	}
	return v
}

// parseMaxspeed handles "60", "30 mph", and zone codes; returns ok=false for
// "none"/"walk"/conditional/per-direction/garbage so the caller falls back.
// Numeric values (posted limits) are scaled by MaxspeedFactor to approximate
// typical driven speeds; zone codes are already "typical" values and pass
// through unscaled.
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
	f := s.MaxspeedFactor
	if f <= 0 {
		f = 1.0
	}
	if len(fields) > 1 {
		switch strings.ToLower(fields[1]) {
		case "mph":
			return n * 1.609344 * f, true
		case "km/h", "kmh", "kph":
			return n * f, true
		default:
			return 0, false // unknown unit → fall back to class default
		}
	}
	return n * f, true // bare number = km/h
}
