package main

import (
	"flag"
	"fmt"
	"log"
	"os"
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

	// Build original graph for snapping (we need node coords and edge geometry).
	// The CHGraph carries the original graph's geometry and coords.
	origGraph := &graph.Graph{
		NumNodes:    chg.NumNodes,
		NumEdges:    uint32(len(chg.FwdHead)), // approximate, used for snapping
		FirstOut:    chg.FwdFirstOut,
		Head:        chg.FwdHead,
		Weight:      chg.FwdWeight,
		NodeLat:     chg.NodeLat,
		NodeLon:     chg.NodeLon,
		GeoFirstOut: chg.GeoFirstOut,
		GeoShapeLat: chg.GeoShapeLat,
		GeoShapeLon: chg.GeoShapeLon,
	}

	// Build routing engine.
	log.Println("Building R-tree spatial index...")
	engine := routing.NewEngine(chg, origGraph)

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
