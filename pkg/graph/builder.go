package graph

import (
	"sort"

	"github.com/paulmach/osm"

	osmparser "map_router/pkg/osm"
)

// Build creates a CSR Graph from parsed OSM edges.
func Build(result *osmparser.ParseResult) *Graph {
	edges := result.Edges
	if len(edges) == 0 {
		return &Graph{}
	}

	// Step 1: Collect all unique node IDs and build a compact mapping.
	nodeSet := make(map[osm.NodeID]uint32)
	var nodeIDs []osm.NodeID

	addNode := func(id osm.NodeID) uint32 {
		if idx, ok := nodeSet[id]; ok {
			return idx
		}
		idx := uint32(len(nodeIDs))
		nodeSet[id] = idx
		nodeIDs = append(nodeIDs, id)
		return idx
	}

	// Pre-collect all nodes referenced by edges.
	for i := range edges {
		addNode(edges[i].FromNodeID)
		addNode(edges[i].ToNodeID)
	}

	numNodes := uint32(len(nodeIDs))

	// Step 2: Build compact edge list with remapped indices.
	type compactEdge struct {
		from      uint32
		to        uint32
		weight    uint32
		shapeLats []float64
		shapeLons []float64
	}

	compact := make([]compactEdge, len(edges))
	for i, e := range edges {
		compact[i] = compactEdge{
			from:      nodeSet[e.FromNodeID],
			to:        nodeSet[e.ToNodeID],
			weight:    e.Weight,
			shapeLats: e.ShapeLats,
			shapeLons: e.ShapeLons,
		}
	}

	// Step 3: Sort edges by source node.
	sort.Slice(compact, func(i, j int) bool {
		if compact[i].from != compact[j].from {
			return compact[i].from < compact[j].from
		}
		return compact[i].to < compact[j].to
	})

	// Step 4: Build CSR arrays.
	numEdges := uint32(len(compact))
	firstOut := make([]uint32, numNodes+1)
	head := make([]uint32, numEdges)
	weight := make([]uint32, numEdges)

	// Geometry arrays.
	geoFirstOut := make([]uint32, numEdges+1)
	var geoShapeLat, geoShapeLon []float64

	for i, e := range compact {
		head[i] = e.to
		weight[i] = e.weight
		geoFirstOut[i] = uint32(len(geoShapeLat))
		geoShapeLat = append(geoShapeLat, e.shapeLats...)
		geoShapeLon = append(geoShapeLon, e.shapeLons...)
	}
	geoFirstOut[numEdges] = uint32(len(geoShapeLat))

	// Build FirstOut via counting.
	for _, e := range compact {
		firstOut[e.from+1]++
	}
	// Prefix sum.
	for i := uint32(1); i <= numNodes; i++ {
		firstOut[i] += firstOut[i-1]
	}

	// Step 5: Populate node coordinates.
	nodeLat := make([]float64, numNodes)
	nodeLon := make([]float64, numNodes)
	for id, idx := range nodeSet {
		nodeLat[idx] = result.NodeLat[id]
		nodeLon[idx] = result.NodeLon[id]
	}

	return &Graph{
		NumNodes:    numNodes,
		NumEdges:    numEdges,
		FirstOut:    firstOut,
		Head:        head,
		Weight:      weight,
		NodeLat:     nodeLat,
		NodeLon:     nodeLon,
		GeoFirstOut: geoFirstOut,
		GeoShapeLat: geoShapeLat,
		GeoShapeLon: geoShapeLon,
	}
}
