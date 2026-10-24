package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"github.com/azybler/map_router/pkg/ch"
	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

func main() {
	input := flag.String("input", "", "Path to .osm.pbf file")
	output := flag.String("output", "graph.bin", "Output combined binary graph file path (base + overlay in one file)")
	outputBase := flag.String("output-base", "", "Write the metric-independent base (coords, topology, geometry) to this path instead of a combined --output")
	outputOverlay := flag.String("output-overlay", "", "Write the metric-specific overlay (ranks, CH upward graph, edge weights) to this path; requires --output-base")
	splitFrom := flag.String("split-from", "", "Convert an existing combined graph .bin into --output-base + --output-overlay without re-parsing OSM (ignores --input and all build options)")
	bbox := flag.String("bbox", "", "Bounding box filter: minLat,minLng,maxLat,maxLng (e.g. 1.15,103.6,1.48,104.1)")
	singapore := flag.Bool("singapore", false, "Shortcut for --bbox 1.15,103.6,1.48,104.1 (Singapore bounding box)")
	kl := flag.Bool("kl", false, "Shortcut for --bbox 2.75,101.2,3.5,102.0 (Selangor + Kuala Lumpur bounding box)")
	wmy := flag.Bool("wmy", false, "Shortcut for --bbox 1.25,99.6,6.75,104.35 (West / Peninsular Malaysia incl. Penang and Langkawi; Singapore falls inside the rectangle too)")
	speeds := flag.String("speeds", "", "Path to a JSON speed table (default: built-in Malaysian priors)")
	distance := flag.Bool("distance", false, "Weight edges by physical road length (shortest-distance routing) instead of travel time; ignores --speeds")
	minComponent := flag.Int("min-component", 0, "Keep every strongly-connected road network with >= N nodes (0: keep only the largest, default). Use a small value like 2 to retain disconnected networks such as islands, e.g. Tasmania for all-of-Australia coverage")
	closeCore := flag.String("close-core", "auto", "Give the uncontracted CH core exact all-pairs shortcuts so queries through it find the optimum: yes, no, or auto (yes for the time metric, no for --distance, whose routes are kept as they were)")
	flag.Parse()

	// --output-base and --output-overlay are a pair: either both name the two
	// halves of the split format, or neither does (combined --output).
	split := *outputBase != "" || *outputOverlay != ""
	if split && (*outputBase == "" || *outputOverlay == "") {
		log.Fatal("--output-base and --output-overlay must be used together")
	}

	// Conversion mode: split an existing combined graph without touching OSM.
	if *splitFrom != "" {
		if !split {
			log.Fatal("--split-from requires both --output-base and --output-overlay")
		}
		if err := splitCombined(*splitFrom, *outputBase, *outputOverlay); err != nil {
			log.Fatalf("Failed to split %s: %v", *splitFrom, err)
		}
		return
	}

	if *input == "" {
		fmt.Fprintln(os.Stderr, "Usage: preprocess --input <file.osm.pbf> [--output graph.bin | --output-base base.bin --output-overlay overlay.bin] [--singapore | --kl | --wmy | --bbox minLat,minLng,maxLat,maxLng] [--speeds <table.json> | --distance]")
		fmt.Fprintln(os.Stderr, "       preprocess --split-from combined.bin --output-base base.bin --output-overlay overlay.bin")
		os.Exit(1)
	}

	// Parse bbox option.
	var opts osmparser.ParseOptions
	if *kl {
		opts.BBox = osmparser.BBox{MinLat: 2.75, MaxLat: 3.5, MinLng: 101.2, MaxLng: 102.0}
		log.Println("Using Selangor + KL bounding box filter: lat [2.75, 3.50], lng [101.20, 102.00]")
	} else if *wmy {
		opts.BBox = osmparser.BBox{MinLat: 1.25, MaxLat: 6.75, MinLng: 99.6, MaxLng: 104.35}
		log.Println("Using West Malaysia bounding box filter: lat [1.25, 6.75], lng [99.60, 104.35]")
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

	if *distance {
		opts.Distance = true
		log.Println("Distance metric: weighting edges by physical road length (cm); --speeds ignored")
	} else if *speeds != "" {
		tbl, err := osmparser.LoadSpeedTable(*speeds)
		if err != nil {
			log.Fatalf("Failed to load speed table: %v", err)
		}
		opts.Speeds = tbl
		log.Printf("Using speed table from %s", *speeds)
	} else {
		opts.Speeds = osmparser.DefaultSpeedTable()
		log.Println("Using built-in default speed table")
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

	// Time lost at junctions, signals and toll booths is travel time: add it
	// to the time metric's weights (before the private-road filter, which
	// drops the way/class/node data it needs).
	if !opts.Distance {
		ds := opts.Speeds.DelayS
		ms := func(s float64) uint32 { return uint32(math.Round(s * 1000)) }
		d := graph.NodeDelays{JunctionMajor: ms(ds.JunctionMajor), JunctionEqual: ms(ds.JunctionEqual),
			JunctionMinor: ms(ds.JunctionMinor), Signal: ms(ds.Signal), TollBooth: ms(ds.TollBooth)}
		if d != (graph.NodeDelays{}) {
			n := graph.AddNodeDelays(g, d)
			log.Printf("Node delays (junction %g/%g/%gs major/equal/minor, signal %gs, toll booth %gs) added to %d edges",
				ds.JunctionMajor, ds.JunctionEqual, ds.JunctionMinor, ds.Signal, ds.TollBooth, n)
		}
	}

	// Keep private/gated roads (access=private/permit/residents) so gated
	// delivery endpoints are reachable, penalizing the clusters that would
	// otherwise be through-shortcuts; their unpenalized weights are recorded so
	// reported travel times exclude the penalty. Must run before component
	// extraction + contraction.
	g = graph.FilterBridgingRestricted(g)
	log.Printf("Private-road filter: %d edges, %d penalized as restricted cut-throughs",
		g.NumEdges, penalizedEdges(g.Penalties))

	// Step 3: Extract connected road network(s).
	beforeComponent := g.NumNodes
	var componentNodes []uint32
	if *minComponent > 0 {
		log.Printf("Extracting all strongly-connected components with >= %d nodes...", *minComponent)
		componentNodes = graph.LargeComponents(g, uint32(*minComponent))
	} else {
		log.Println("Extracting largest connected component...")
		componentNodes = graph.LargestComponent(g)
	}
	log.Printf("Kept %d nodes (%.1f%%); dropped %d disconnected/fragment nodes",
		len(componentNodes), float64(len(componentNodes))/float64(beforeComponent)*100,
		int(beforeComponent)-len(componentNodes))
	g = graph.FilterToComponent(g, componentNodes)
	log.Printf("Filtered graph: %d nodes, %d edges (%d penalized)", g.NumNodes, g.NumEdges, penalizedEdges(g.Penalties))

	// Step 4: Contract CH.
	var chOpt ch.Options
	switch *closeCore {
	case "yes":
		chOpt.CloseCore = true
	case "no":
	case "auto":
		chOpt.CloseCore = !*distance
	default:
		log.Fatalf("--close-core must be yes, no or auto, not %q", *closeCore)
	}
	log.Printf("Running Contraction Hierarchies (close core: %v)...", chOpt.CloseCore)
	chResult := ch.ContractWith(g, chOpt)
	log.Printf("CH complete: %d fwd edges, %d bwd edges", len(chResult.FwdHead), len(chResult.BwdHead))

	// Step 5: Serialize to binary — either one combined file or a split
	// base + overlay pair.
	if split {
		log.Printf("Writing base to %s and overlay to %s...", *outputBase, *outputOverlay)
		if err := graph.WriteBase(*outputBase, chResult); err != nil {
			log.Fatalf("Failed to write base: %v", err)
		}
		if err := graph.WriteOverlay(*outputOverlay, chResult); err != nil {
			log.Fatalf("Failed to write overlay: %v", err)
		}
		logSize("base", *outputBase)
		logSize("overlay", *outputOverlay)
	} else {
		log.Printf("Writing binary to %s...", *output)
		if err := graph.WriteBinary(*output, chResult); err != nil {
			log.Fatalf("Failed to write binary: %v", err)
		}
		logSize("output", *output)
	}
	log.Printf("Done in %s.", time.Since(start).Round(time.Second))
}

// splitCombined reads an existing combined graph binary and re-serializes it as a
// base + overlay pair, so already-built graphs migrate to the split format in
// seconds without re-parsing OSM.
func splitCombined(combinedPath, basePath, overlayPath string) error {
	log.Printf("Reading combined graph from %s...", combinedPath)
	chg, err := graph.ReadBinary(combinedPath)
	if err != nil {
		return err
	}
	log.Printf("Loaded %d nodes, %d orig edges, %d fwd / %d bwd overlay edges",
		chg.NumNodes, len(chg.OrigHead), len(chg.FwdHead), len(chg.BwdHead))
	if chg.Penalties == nil {
		log.Printf("WARNING: %s predates penalty recording; its travel times will include restricted-road penalties until it is rebuilt from OSM", combinedPath)
	} else {
		log.Printf("Penalized edges: %d", len(chg.Penalties.Edges))
	}

	log.Printf("Writing base to %s...", basePath)
	if err := graph.WriteBase(basePath, chg); err != nil {
		return fmt.Errorf("write base: %w", err)
	}
	log.Printf("Writing overlay to %s...", overlayPath)
	if err := graph.WriteOverlay(overlayPath, chg); err != nil {
		return fmt.Errorf("write overlay: %w", err)
	}
	logSize("base", basePath)
	logSize("overlay", overlayPath)
	return nil
}

// penalizedEdges counts the edges p lists (0 when none were recorded).
func penalizedEdges(p *graph.EdgePenalties) int {
	if p == nil {
		return 0
	}
	return len(p.Edges)
}

// logSize prints the on-disk size of a just-written file.
func logSize(label, path string) {
	if info, err := os.Stat(path); err == nil {
		log.Printf("  %s: %s (%.1f MB)", label, path, float64(info.Size())/(1024*1024))
	}
}
