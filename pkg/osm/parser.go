package osm

import (
	"context"
	"fmt"
	"io"
	"log"
	"github.com/azybler/map_router/pkg/geo"
	"math"

	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
)

// RawEdge represents a directed edge parsed from OSM data.
type RawEdge struct {
	FromNodeID osm.NodeID
	ToNodeID   osm.NodeID
	Weight     uint32    // distance in millimeters
	ShapeLats  []float64 // intermediate shape node latitudes (excluding from/to)
	ShapeLons  []float64 // intermediate shape node longitudes (excluding from/to)
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

// isCarAccessible returns true if the way is drivable by car.
func isCarAccessible(tags osm.Tags) bool {
	hw := tags.Find("highway")
	if !carHighways[hw] {
		return false
	}

	// Skip area highways (pedestrian plazas).
	if tags.Find("area") == "yes" {
		return false
	}

	// Skip restricted access.
	access := tags.Find("access")
	if access == "no" || access == "private" {
		return false
	}
	if tags.Find("motor_vehicle") == "no" {
		return false
	}

	return true
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
	NodeIDs  []osm.NodeID
	Forward  bool
	Backward bool
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
	BBox BBox // if non-zero, filter edges to this bounding box
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

		if !isCarAccessible(w.Tags) {
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
			NodeIDs:  nodeIDs,
			Forward:  fwd,
			Backward: bwd,
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
			weightMM := uint32(math.Round(dist * 1000))
			if weightMM == 0 {
				weightMM = 1 // avoid zero-weight edges
			}

			if w.Forward {
				edges = append(edges, RawEdge{
					FromNodeID: fromID,
					ToNodeID:   toID,
					Weight:     weightMM,
				})
			}
			if w.Backward {
				edges = append(edges, RawEdge{
					FromNodeID: toID,
					ToNodeID:   fromID,
					Weight:     weightMM,
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
