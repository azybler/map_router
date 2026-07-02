package osm

import (
	"context"
	"fmt"
	"github.com/azybler/map_router/pkg/geo"
	"io"
	"log"
	"math"

	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
)

// RawEdge represents a directed edge parsed from OSM data.
type RawEdge struct {
	FromNodeID osm.NodeID
	ToNodeID   osm.NodeID
	Weight     uint32    // travel time in ms, or physical distance in cm when ParseOptions.Distance is set
	ShapeLats  []float64 // intermediate shape node latitudes (excluding from/to)
	ShapeLons  []float64 // intermediate shape node longitudes (excluding from/to)
	Restricted bool      // gated/private (access=private/permit/residents); last-mile only
}

// computeWeightMs converts a segment length (m) and speed (km/h) to travel time
// in milliseconds, clamped to >= 1.
func computeWeightMs(lengthMeters, speedKmh float64) uint32 {
	if speedKmh <= 0 {
		speedKmh = 1
	}
	ms := lengthMeters / (speedKmh / 3.6) * 1000
	w := uint32(math.Round(ms))
	if w == 0 {
		w = 1
	}
	return w
}

// computeWeightDistanceCm converts a segment length (m) to a speed-independent
// distance weight in centimeters, clamped to >= 1. Used for shortest-distance
// routing (ParseOptions.Distance): the edge weight IS the physical road length,
// so the router minimizes total distance instead of travel time. Centimeters
// keep continent-scale path sums comfortably inside uint32 (max ~4.29e9 cm ≈
// 42,900 km — far above any real-world route) while preserving sub-meter detail.
func computeWeightDistanceCm(lengthMeters float64) uint32 {
	w := uint32(math.Round(lengthMeters * 100))
	if w == 0 {
		w = 1
	}
	return w
}

// ParseResult holds the output of parsing an OSM PBF file.
type ParseResult struct {
	Edges   []RawEdge
	NodeLat map[osm.NodeID]float64
	NodeLon map[osm.NodeID]float64
}

// carHighways lists highway tag values accessible by car.
var carHighways = map[string]bool{
	"motorway":       true,
	"motorway_link":  true,
	"trunk":          true,
	"trunk_link":     true,
	"primary":        true,
	"primary_link":   true,
	"secondary":      true,
	"secondary_link": true,
	"tertiary":       true,
	"tertiary_link":  true,
	"unclassified":   true,
	"residential":    true,
	"living_street":  true,
	"service":        true,
}

// classifyAccess decides whether a car-class way is kept, and if kept whether it
// is "restricted" (gated/private — usable for last-mile access, inlined later only
// if it forms a cul-de-sac). access governs over motor_vehicle. access=destination
// and access=customers stay PUBLIC (they route normally today).
func classifyAccess(tags osm.Tags) (keep, restricted bool) {
	hw := tags.Find("highway")
	if !carHighways[hw] || tags.Find("area") == "yes" {
		return false, false
	}
	switch tags.Find("access") {
	case "no":
		return false, false
	case "private", "permit", "residents":
		return true, true
	}
	switch tags.Find("motor_vehicle") {
	case "no":
		return false, false
	case "private", "destination", "customers":
		return true, true
	}
	return true, false
}

// isCarAccessible reports whether a way is kept for car routing (ignoring the
// restricted distinction). Thin wrapper over classifyAccess.
func isCarAccessible(tags osm.Tags) bool {
	keep, _ := classifyAccess(tags)
	return keep
}

// directionFlags returns (forward, backward) based on highway type and oneway tags.
func directionFlags(tags osm.Tags) (forward, backward bool) {
	// Default: bidirectional.
	forward = true
	backward = true

	hw := tags.Find("highway")

	// Implied oneway for motorways and roundabouts.
	if hw == "motorway" || hw == "motorway_link" || tags.Find("junction") == "roundabout" {
		backward = false
	}

	// Explicit oneway tag overrides.
	oneway := tags.Find("oneway")
	switch oneway {
	case "yes", "true", "1":
		forward = true
		backward = false
	case "-1", "reverse":
		forward = false
		backward = true
	case "no":
		forward = true
		backward = true
	case "reversible":
		// Time-dependent — skip entirely.
		forward = false
		backward = false
	}

	return forward, backward
}

// wayInfo holds parsed way data collected during Pass 1.
type wayInfo struct {
	NodeIDs    []osm.NodeID
	Forward    bool
	Backward   bool
	SpeedKmh   float64
	Restricted bool
}

// BBox defines a geographic bounding box for filtering.
// If non-zero, only edges with both endpoints inside the box are kept.
type BBox struct {
	MinLat, MaxLat float64
	MinLng, MaxLng float64
}

// IsZero returns true if the bbox is unset.
func (b BBox) IsZero() bool {
	return b.MinLat == 0 && b.MaxLat == 0 && b.MinLng == 0 && b.MaxLng == 0
}

// Contains returns true if the point is inside the bounding box.
func (b BBox) Contains(lat, lng float64) bool {
	return lat >= b.MinLat && lat <= b.MaxLat && lng >= b.MinLng && lng <= b.MaxLng
}

// ParseOptions configures the OSM parser.
type ParseOptions struct {
	BBox     BBox       // if non-zero, filter edges to this bounding box
	Speeds   SpeedTable // free-flow speed model; zero value → DefaultSpeedTable()
	Distance bool       // if true, weight edges by physical road length (cm) for
	// shortest-distance routing; Speeds is ignored.
}

// Parse reads an OSM PBF file and returns directed edges for car routing.
// The reader is consumed twice (seeks back to start for the second pass),
// so it must implement io.ReadSeeker.
func Parse(ctx context.Context, rs io.ReadSeeker, opts ...ParseOptions) (*ParseResult, error) {
	var opt ParseOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	useBBox := !opt.BBox.IsZero()
	if opt.Speeds.ClassKmh == nil {
		opt.Speeds = DefaultSpeedTable()
	}
	// Pass 1: Scan ways to collect referenced node IDs and way info.
	referencedNodes := make(map[osm.NodeID]struct{})
	var ways []wayInfo

	scanner := osmpbf.New(ctx, rs, 1)
	scanner.SkipNodes = true
	scanner.SkipRelations = true

	for scanner.Scan() {
		obj := scanner.Object()
		w, ok := obj.(*osm.Way)
		if !ok {
			continue
		}

		keep, restricted := classifyAccess(w.Tags)
		if !keep {
			continue
		}

		if len(w.Nodes) < 2 {
			continue
		}

		fwd, bwd := directionFlags(w.Tags)
		if !fwd && !bwd {
			continue
		}

		nodeIDs := make([]osm.NodeID, len(w.Nodes))
		for i, wn := range w.Nodes {
			nodeIDs[i] = wn.ID
			referencedNodes[wn.ID] = struct{}{}
		}

		ways = append(ways, wayInfo{
			NodeIDs:    nodeIDs,
			Forward:    fwd,
			Backward:   bwd,
			SpeedKmh:   opt.Speeds.SpeedKmh(w.Tags),
			Restricted: restricted,
		})
	}
	if err := scanner.Err(); err != nil {
		scanner.Close()
		return nil, fmt.Errorf("pass 1 (ways): %w", err)
	}
	scanner.Close()

	log.Printf("Pass 1 complete: %d ways, %d referenced nodes", len(ways), len(referencedNodes))

	// Pass 2: Scan nodes to collect coordinates for referenced nodes only.
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek for pass 2: %w", err)
	}

	nodeLat := make(map[osm.NodeID]float64, len(referencedNodes))
	nodeLon := make(map[osm.NodeID]float64, len(referencedNodes))

	scanner = osmpbf.New(ctx, rs, 1)
	scanner.SkipWays = true
	scanner.SkipRelations = true

	for scanner.Scan() {
		obj := scanner.Object()
		n, ok := obj.(*osm.Node)
		if !ok {
			continue
		}

		if _, needed := referencedNodes[n.ID]; !needed {
			continue
		}

		nodeLat[n.ID] = n.Lat
		nodeLon[n.ID] = n.Lon
	}
	if err := scanner.Err(); err != nil {
		scanner.Close()
		return nil, fmt.Errorf("pass 2 (nodes): %w", err)
	}
	scanner.Close()

	log.Printf("Pass 2 complete: %d node coordinates collected", len(nodeLat))

	// Build edges from ways.
	var edges []RawEdge
	var skippedEdges int
	var bboxFiltered int

	for _, w := range ways {
		for i := 0; i < len(w.NodeIDs)-1; i++ {
			fromID := w.NodeIDs[i]
			toID := w.NodeIDs[i+1]

			fromLat, fromOk := nodeLat[fromID]
			fromLon := nodeLon[fromID]
			toLat, toOk := nodeLat[toID]
			toLon := nodeLon[toID]

			if !fromOk || !toOk {
				skippedEdges++
				continue
			}

			// Bounding box filter: skip edges with any endpoint outside.
			if useBBox && (!opt.BBox.Contains(fromLat, fromLon) || !opt.BBox.Contains(toLat, toLon)) {
				bboxFiltered++
				continue
			}

			dist := geo.Haversine(fromLat, fromLon, toLat, toLon)
			var weight uint32
			if opt.Distance {
				weight = computeWeightDistanceCm(dist)
			} else {
				weight = computeWeightMs(dist, w.SpeedKmh)
			}

			if w.Forward {
				edges = append(edges, RawEdge{
					FromNodeID: fromID,
					ToNodeID:   toID,
					Weight:     weight,
					Restricted: w.Restricted,
				})
			}
			if w.Backward {
				edges = append(edges, RawEdge{
					FromNodeID: toID,
					ToNodeID:   fromID,
					Weight:     weight,
					Restricted: w.Restricted,
				})
			}
		}
	}

	if skippedEdges > 0 {
		log.Printf("Warning: skipped %d edges due to missing node coordinates", skippedEdges)
	}
	if bboxFiltered > 0 {
		log.Printf("Filtered %d edges outside bounding box", bboxFiltered)
	}
	log.Printf("Built %d directed edges", len(edges))

	return &ParseResult{
		Edges:   edges,
		NodeLat: nodeLat,
		NodeLon: nodeLon,
	}, nil
}
