package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/azybler/map_router/pkg/api"
	"github.com/azybler/map_router/pkg/graph"
	"github.com/azybler/map_router/pkg/routing"
)

func main() {
	graphPath := flag.String("graph", "graph.bin", "Path to the time-metric graph: a combined binary, or a time overlay when --graph-base is set")
	graphDistance := flag.String("graph-distance", "", "Optional distance graph: a combined binary, or a distance overlay when --graph-base is set; enables metric=\"distance\" routing")
	graphBase := flag.String("graph-base", "", "Optional shared base file (coords, topology, geometry). When set, --graph and --graph-distance are overlay files stitched onto this one base, so the base and its Snapper are held once in RAM instead of per metric")
	port := flag.Int("port", 8080, "HTTP port")
	corsOrigin := flag.String("cors-origin", "", "CORS allowed origin (empty = same-origin)")
	flag.Parse()

	start := time.Now()

	// loadTime/loadDist resolve to either the combined path (each graph
	// self-contained, its own Snapper) or the split path (one shared base +
	// Snapper, per-metric overlays), depending on whether --graph-base is set.
	loadTime := func() (*routing.Engine, *graph.CHGraph, error) { return loadEngine(*graphPath) }
	loadDist := func() (*routing.Engine, *graph.CHGraph, error) { return loadEngine(*graphDistance) }

	if *graphBase != "" {
		log.Printf("Loading shared base from %s...", *graphBase)
		base, err := graph.ReadBase(*graphBase)
		if err != nil {
			log.Fatalf("Failed to load base graph: %v", err)
		}
		// One Snapper over the base, shared by every metric engine (the grid
		// index is metric-independent).
		sharedSnapper := routing.NewSnapper(base.Graph(nil))
		log.Printf("Loaded base: %d nodes, %d orig edges (shared Snapper built)", base.NumNodes, len(base.OrigHead))
		loadTime = func() (*routing.Engine, *graph.CHGraph, error) {
			return loadOverlayEngine(base, sharedSnapper, *graphPath)
		}
		loadDist = func() (*routing.Engine, *graph.CHGraph, error) {
			return loadOverlayEngine(base, sharedSnapper, *graphDistance)
		}
	}

	// Load the time graph (required).
	log.Printf("Loading time graph from %s...", *graphPath)
	timeEngine, timeCHG, err := loadTime()
	if err != nil {
		log.Fatalf("Failed to load time graph: %v", err)
	}
	log.Printf("Loaded time graph: %d nodes, %d fwd edges, %d bwd edges",
		timeCHG.NumNodes, len(timeCHG.FwdHead), len(timeCHG.BwdHead))

	// routers and availableMetrics are kept in lockstep: every metric registered
	// in the map is also appended to availableMetrics (in a stable order), so the
	// /stats advertisement can never drift from what the server can actually route.
	routers := map[string]routing.Router{api.MetricTime: timeEngine}
	availableMetrics := []string{api.MetricTime}

	// Load the distance graph (optional).
	if *graphDistance != "" {
		log.Printf("Loading distance graph from %s...", *graphDistance)
		distEngine, distCHG, err := loadDist()
		if err != nil {
			log.Fatalf("Failed to load distance graph: %v", err)
		}
		log.Printf("Loaded distance graph: %d nodes, %d fwd edges, %d bwd edges",
			distCHG.NumNodes, len(distCHG.FwdHead), len(distCHG.BwdHead))
		routers[api.MetricDistance] = distEngine
		availableMetrics = append(availableMetrics, api.MetricDistance)
	}

	// Reclaim memory from init-time temporaries (R-tree construction doubles the
	// heap each GC cycle). Return unused pages to the OS.
	runtime.GC()
	debug.FreeOSMemory()

	log.Printf("Ready in %s (metrics: %v)", time.Since(start).Round(time.Millisecond), availableMetrics)

	// Setup HTTP server.
	addr := fmt.Sprintf(":%d", *port)
	cfg := api.DefaultConfig(addr)
	cfg.CORSOrigin = *corsOrigin

	stats := api.StatsResponse{
		NumNodes:         timeCHG.NumNodes,
		NumFwdEdges:      len(timeCHG.FwdHead),
		NumBwdEdges:      len(timeCHG.BwdHead),
		AvailableMetrics: availableMetrics,
	}

	handlers := api.NewHandlersMulti(routers, stats)
	srv := api.NewServer(cfg, handlers)

	if err := api.ListenAndServe(srv); err != nil {
		log.Printf("Server stopped: %v", err)
		os.Exit(1)
	}
}

// loadEngine reads a CH graph binary and builds a routing engine over it,
// reconstructing the original graph needed for snapping and geometry.
func loadEngine(path string) (*routing.Engine, *graph.CHGraph, error) {
	chg, err := graph.ReadBinary(path)
	if err != nil {
		return nil, nil, err
	}
	origGraph := &graph.Graph{
		NumNodes:    chg.NumNodes,
		NumEdges:    uint32(len(chg.OrigHead)),
		FirstOut:    chg.OrigFirstOut,
		Head:        chg.OrigHead,
		Weight:      chg.OrigWeight,
		NodeLat:     chg.NodeLat,
		NodeLon:     chg.NodeLon,
		GeoFirstOut: chg.GeoFirstOut,
		GeoShapeLat: chg.GeoShapeLat,
		GeoShapeLon: chg.GeoShapeLon,
	}
	return routing.NewEngine(chg, origGraph), chg, nil
}

// loadOverlayEngine stitches a metric overlay onto the shared base and builds an
// engine over the shared Snapper. The base's coords/topology/geometry slices are
// shared (not copied) across every metric; only the overlay and the metric's
// original-edge weights are per-engine.
func loadOverlayEngine(base *graph.BaseGraph, snapper *routing.Snapper, overlayPath string) (*routing.Engine, *graph.CHGraph, error) {
	chg, err := graph.ReadOverlay(overlayPath, base)
	if err != nil {
		return nil, nil, err
	}
	origGraph := base.Graph(chg.OrigWeight)
	return routing.NewEngineWithSnapper(chg, origGraph, snapper), chg, nil
}
