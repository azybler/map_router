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
	graphPath := flag.String("graph", "graph.bin", "Path to preprocessed graph binary (time metric)")
	graphDistance := flag.String("graph-distance", "", "Optional distance-weighted graph binary; enables metric=\"distance\" routing")
	port := flag.Int("port", 8080, "HTTP port")
	corsOrigin := flag.String("cors-origin", "", "CORS allowed origin (empty = same-origin)")
	flag.Parse()

	start := time.Now()

	// Load the time graph (required).
	log.Printf("Loading time graph from %s...", *graphPath)
	timeEngine, timeCHG, err := loadEngine(*graphPath)
	if err != nil {
		log.Fatalf("Failed to load time graph: %v", err)
	}
	log.Printf("Loaded time graph: %d nodes, %d fwd edges, %d bwd edges",
		timeCHG.NumNodes, len(timeCHG.FwdHead), len(timeCHG.BwdHead))

	routers := map[string]routing.Router{api.MetricTime: timeEngine}
	availableMetrics := []string{api.MetricTime}

	// Load the distance graph (optional).
	if *graphDistance != "" {
		log.Printf("Loading distance graph from %s...", *graphDistance)
		distEngine, distCHG, err := loadEngine(*graphDistance)
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
