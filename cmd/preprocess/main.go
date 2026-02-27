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
	bbox := flag.String("bbox", "", "Bounding box filter: minLat,minLng,maxLat,maxLng (e.g. 1.15,103.6,1.48,104.1)")
	singapore := flag.Bool("singapore", false, "Shortcut for --bbox 1.15,103.6,1.48,104.1 (Singapore bounding box)")
	kl := flag.Bool("kl", false, "Shortcut for --bbox 2.75,101.2,3.5,102.0 (Selangor + Kuala Lumpur bounding box)")
	flag.Parse()

	if *input == "" {
		fmt.Fprintln(os.Stderr, "Usage: preprocess --input <file.osm.pbf> [--output graph.bin] [--singapore | --kl | --bbox minLat,minLng,maxLat,maxLng]")
		os.Exit(1)
	}

	// Parse bbox option.
	var opts osmparser.ParseOptions
	if *kl {
		opts.BBox = osmparser.BBox{MinLat: 2.75, MaxLat: 3.5, MinLng: 101.2, MaxLng: 102.0}
		log.Println("Using Selangor + KL bounding box filter: lat [2.75, 3.50], lng [101.20, 102.00]")
	} else if *singapore {
		opts.BBox = osmparser.BBox{MinLat: 1.15, MaxLat: 1.48, MinLng: 103.6, MaxLng: 104.1}
		log.Println("Using Singapore bounding box filter: lat [1.15, 1.48], lng [103.6, 104.1]")
	} else if *bbox != "" {
		var minLat, minLng, maxLat, maxLng float64
		_, err := fmt.Sscanf(*bbox, "%f,%f,%f,%f", &minLat, &minLng, &maxLat, &maxLng)
		if err != nil {
			log.Fatalf("Invalid bbox format (expected minLat,minLng,maxLat,maxLng): %v", err)
		}
		opts.BBox = osmparser.BBox{MinLat: minLat, MaxLat: maxLat, MinLng: minLng, MaxLng: maxLng}
		log.Printf("Using bounding box filter: lat [%.4f, %.4f], lng [%.4f, %.4f]", minLat, maxLat, minLng, maxLng)
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
	parseResult, err := osmparser.Parse(context.Background(), f, opts)
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
