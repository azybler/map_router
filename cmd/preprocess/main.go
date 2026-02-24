package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"map_router/pkg/ch"
	"map_router/pkg/graph"
	osmparser "map_router/pkg/osm"
)

func main() {
	input := flag.String("input", "", "Path to .osm.pbf file")
	output := flag.String("output", "graph.bin", "Output binary graph file path")
	flag.Parse()

	if *input == "" {
		fmt.Fprintln(os.Stderr, "Usage: preprocess --input <file.osm.pbf> [--output graph.bin]")
		os.Exit(1)
	}

	start := time.Now()

	// Step 1: Parse OSM data.
	log.Println("Opening OSM file...")
	f, err := os.Open(*input)
	if err != nil {
		log.Fatalf("Failed to open input file: %v", err)
	}
	defer f.Close()

	log.Println("Parsing OSM data...")
	parseResult, err := osmparser.Parse(context.Background(), f)
	if err != nil {
		log.Fatalf("Failed to parse OSM data: %v", err)
	}
	log.Printf("Parsed %d edges, %d nodes", len(parseResult.Edges), len(parseResult.NodeLat))

	// Step 2: Build graph.
	log.Println("Building graph...")
	g := graph.Build(parseResult)
	log.Printf("Graph: %d nodes, %d edges", g.NumNodes, g.NumEdges)

	// Step 3: Extract largest connected component.
	log.Println("Extracting largest connected component...")
	componentNodes := graph.LargestComponent(g)
	log.Printf("Largest component: %d nodes (%.1f%%)", len(componentNodes), float64(len(componentNodes))/float64(g.NumNodes)*100)
	g = graph.FilterToComponent(g, componentNodes)
	log.Printf("Filtered graph: %d nodes, %d edges", g.NumNodes, g.NumEdges)

	// Step 4: Contract CH.
	log.Println("Running Contraction Hierarchies...")
	chResult := ch.Contract(g)
	log.Printf("CH complete: %d fwd edges, %d bwd edges", len(chResult.FwdHead), len(chResult.BwdHead))

	// Step 5: Serialize to binary.
	log.Printf("Writing binary to %s...", *output)
	if err := graph.WriteBinary(*output, chResult); err != nil {
		log.Fatalf("Failed to write binary: %v", err)
	}

	info, _ := os.Stat(*output)
	elapsed := time.Since(start)
	log.Printf("Done in %s. Output: %s (%.1f MB)", elapsed.Round(time.Second), *output, float64(info.Size())/(1024*1024))
}
