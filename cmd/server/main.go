package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"map_router/pkg/api"
	"map_router/pkg/graph"
	"map_router/pkg/routing"
)

func main() {
	graphPath := flag.String("graph", "graph.bin", "Path to preprocessed graph binary")
	port := flag.Int("port", 8080, "HTTP port")
	corsOrigin := flag.String("cors-origin", "", "CORS allowed origin (empty = same-origin)")
	flag.Parse()

	start := time.Now()

	// Load graph.
	log.Printf("Loading graph from %s...", *graphPath)
	chg, err := graph.ReadBinary(*graphPath)
	if err != nil {
		log.Fatalf("Failed to load graph: %v", err)
	}
	log.Printf("Loaded: %d nodes, %d fwd edges, %d bwd edges",
		chg.NumNodes, len(chg.FwdHead), len(chg.BwdHead))

	// Reconstruct original graph for snapping (R-tree needs real road edges).
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

	// Build routing engine.
	log.Println("Building spatial index...")
	engine := routing.NewEngine(chg, origGraph)

	// Reclaim memory from init-time temporaries. Without this, Go's heap
	// retains peak RSS from index construction (GC doubles heap each cycle:
	// 120→240→480→960→1920 MB). This returns unused pages to the OS.
	runtime.GC()
	debug.FreeOSMemory()

	loadTime := time.Since(start)
	log.Printf("Ready in %s", loadTime.Round(time.Millisecond))

	// Setup HTTP server.
	addr := fmt.Sprintf(":%d", *port)
	cfg := api.DefaultConfig(addr)
	cfg.CORSOrigin = *corsOrigin

	stats := api.StatsResponse{
		NumNodes:    chg.NumNodes,
		NumFwdEdges: len(chg.FwdHead),
		NumBwdEdges: len(chg.BwdHead),
	}

	handlers := api.NewHandlers(engine, stats)
	srv := api.NewServer(cfg, handlers)

	if err := api.ListenAndServe(srv); err != nil {
		log.Printf("Server stopped: %v", err)
		os.Exit(1)
	}
}
