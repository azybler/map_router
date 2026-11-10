package osm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	// RefFactor scales the resolved speed of a way whose ref tag lists one of
	// its keys (refs are ";"-separated, e.g. "E11" for the LDP): for the roads
	// Google prices well away from their class, such as a congested urban
	// expressway. It applies after the floor and the cap.
	RefFactor map[string]float64
	// WayFactor scales the resolved speed of individual OSM ways (by way id),
	// after RefFactor. It holds per-road corrections learned from the routes
	// Google picks (datasets/elevete_route_cache, lab learn.go): a road Google
	// favours over its class gets a factor above 1, one it avoids below 1.
	// speeds.json can list them inline ("way_factor") or point at a separate
	// file ("way_factor_file", relative to speeds.json; LoadSpeedTable only).
	WayFactor map[int64]float64
	// DelayS is the time lost passing through junctions, traffic signals and
	// toll booths; preprocess adds it to time-metric edge weights
	// (graph.AddNodeDelays). The zero value adds nothing.
	DelayS DelayS
}

// DelayS is graph.NodeDelays in seconds, as speeds.json gives it.
type DelayS struct {
	JunctionMajor float64 `json:"junction_major"` // the arriving road outranks every other road there
	JunctionEqual float64 `json:"junction_equal"` // the biggest other road there ranks the same
	JunctionMinor float64 `json:"junction_minor"` // a bigger road crosses or joins it
	Signal        float64 `json:"traffic_signal"` // entering a node tagged highway=traffic_signals
	TollBooth     float64 `json:"toll_booth"`     // entering a node tagged barrier=toll_booth
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
		RefFactor      map[string]float64 `json:"ref_factor"`
		WayFactor      map[string]float64 `json:"way_factor"`
		DelayS         *DelayS            `json:"delay_s"`
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
	if raw.RefFactor != nil {
		def.RefFactor = raw.RefFactor
	}
	if raw.DelayS != nil {
		def.DelayS = *raw.DelayS
	}
	if len(raw.WayFactor) > 0 {
		wf, err := parseWayFactors(raw.WayFactor)
		if err != nil {
			return SpeedTable{}, err
		}
		def.WayFactor = wf
	}
	return def, nil
}

// parseWayFactors converts a JSON {"<osm way id>": factor} object.
func parseWayFactors(m map[string]float64) (map[int64]float64, error) {
	out := make(map[int64]float64, len(m))
	for k, f := range m {
		id, err := strconv.ParseInt(strings.TrimSpace(k), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("way_factor: bad way id %q", k)
		}
		if f <= 0 {
			return nil, fmt.Errorf("way_factor: way %d has factor %v; must be > 0", id, f)
		}
		out[id] = f
	}
	return out, nil
}

// LoadSpeedTable reads a JSON speed table from path. A "way_factor_file" key
// names a JSON {"<osm way id>": factor} file, resolved relative to path; its
// entries are loaded first, so inline "way_factor" entries win on overlap.
func LoadSpeedTable(path string) (SpeedTable, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SpeedTable{}, err
	}
	t, err := ParseSpeedTable(data)
	if err != nil {
		return SpeedTable{}, err
	}
	var ref struct {
		WayFactorFile string `json:"way_factor_file"`
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		return SpeedTable{}, err
	}
	if ref.WayFactorFile == "" {
		return t, nil
	}
	fp := ref.WayFactorFile
	if !filepath.IsAbs(fp) {
		fp = filepath.Join(filepath.Dir(path), fp)
	}
	fdata, err := os.ReadFile(fp)
	if err != nil {
		return SpeedTable{}, fmt.Errorf("way_factor_file: %w", err)
	}
	var m map[string]float64
	if err := json.Unmarshal(fdata, &m); err != nil {
		return SpeedTable{}, fmt.Errorf("way_factor_file %s: %w", fp, err)
	}
	wf, err := parseWayFactors(m)
	if err != nil {
		return SpeedTable{}, fmt.Errorf("way_factor_file %s: %w", fp, err)
	}
	for id, f := range t.WayFactor {
		wf[id] = f
	}
	t.WayFactor = wf
	return t, nil
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
	if len(s.RefFactor) > 0 {
		for ref := range strings.SplitSeq(t.Find("ref"), ";") {
			if f, ok := s.RefFactor[strings.TrimSpace(ref)]; ok {
				v *= f
				break
			}
		}
	}
	return v
}

// WaySpeedKmh is SpeedKmh for the OSM way with the given id: SpeedKmh(t)
// scaled by the way's WayFactor, if it has one.
func (s SpeedTable) WaySpeedKmh(id int64, t osm.Tags) float64 {
	v := s.SpeedKmh(t)
	if f, ok := s.WayFactor[id]; ok {
		v *= f
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
