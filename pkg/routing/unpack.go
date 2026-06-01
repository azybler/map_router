package routing

import "github.com/azybler/map_router/pkg/graph"

const maxUnpackDepth = 200

const noNode = ^uint32(0) // sentinel for "no node"

// unpackOverlayPath takes a sequence of overlay-level nodes and unpacks all
// shortcut hops into original-graph node sequences.
// Uses a single pre-allocated stack across all hops to avoid per-hop allocations.
func unpackOverlayPath(chg *graph.CHGraph, overlayNodes []uint32) []uint32 {
	if len(overlayNodes) < 2 {
		return overlayNodes
	}

	type stackItem struct {
		from, to uint32
		depth    int
	}

	result := make([]uint32, 1, len(overlayNodes)*8)
	result[0] = overlayNodes[0]
	stack := make([]stackItem, 0, 32)

	for i := 0; i < len(overlayNodes)-1; i++ {
		stack = append(stack[:0], stackItem{overlayNodes[i], overlayNodes[i+1], 0})

		for len(stack) > 0 {
			it := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if it.depth > maxUnpackDepth {
				continue // safety bound
			}

			middle := findMiddle(chg, it.from, it.to)
			if middle < 0 {
				// Original edge — append nodes, avoiding duplication.
				if result[len(result)-1] != it.from {
					result = append(result, it.from)
				}
				result = append(result, it.to)
				continue
			}

			m := uint32(middle)
			// Push right half first (m→to), then left half (from→m),
			// so left is processed first (LIFO).
			stack = append(stack, stackItem{m, it.to, it.depth + 1})
			stack = append(stack, stackItem{it.from, m, it.depth + 1})
		}
	}

	return result
}

// findMiddle looks up the middle (contracted) node for the edge from→to in the
// CH overlay. Among PARALLEL overlay edges for the pair, it selects the one with
// minimum weight — the edge the bidirectional search actually relaxed — so the
// unpacked path matches the shortest path. Returns -1 if the pair has no overlay
// edge (a plain original edge) OR if the cheapest overlay edge is itself original.
//
// The edge may be stored as a forward overlay edge from→to (rank[from] <
// rank[to]) or a backward overlay edge to→from (rank[to] < rank[from],
// representing original direction from→to).
func findMiddle(chg *graph.CHGraph, from, to uint32) int32 {
	bestWeight := ^uint32(0)
	bestMiddle := int32(-1)
	found := false

	for i := chg.FwdFirstOut[from]; i < chg.FwdFirstOut[from+1]; i++ {
		if chg.FwdHead[i] == to && (!found || chg.FwdWeight[i] < bestWeight) {
			bestWeight = chg.FwdWeight[i]
			bestMiddle = chg.FwdMiddle[i]
			found = true
		}
	}
	for i := chg.BwdFirstOut[to]; i < chg.BwdFirstOut[to+1]; i++ {
		if chg.BwdHead[i] == from && (!found || chg.BwdWeight[i] < bestWeight) {
			bestWeight = chg.BwdWeight[i]
			bestMiddle = chg.BwdMiddle[i]
			found = true
		}
	}

	if !found {
		return -1
	}
	return bestMiddle
}

// findEdge finds an edge from source to target in a CSR graph using linear scan.
func findEdge(firstOut, head []uint32, source, target uint32) uint32 {
	for i := firstOut[source]; i < firstOut[source+1]; i++ {
		if head[i] == target {
			return i
		}
	}
	return noNode
}
