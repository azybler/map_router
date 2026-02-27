package routing

import "map_router/pkg/graph"

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

// findMiddle looks up the middle (contracted) node for an edge from→to in the
// CH overlay. Returns -1 if the edge is original (not a shortcut).
//
// The edge might be stored as:
//   - Forward overlay edge from→to (if rank[from] < rank[to])
//   - Backward overlay edge to→from (if rank[to] < rank[from]), representing
//     original direction from→to
func findMiddle(chg *graph.CHGraph, from, to uint32) int32 {
	// Try forward overlay: edge from→to.
	if edge := findEdge(chg.FwdFirstOut, chg.FwdHead, from, to); edge != noNode {
		return chg.FwdMiddle[edge]
	}
	// Try backward overlay: edge to→from (represents original from→to).
	if edge := findEdge(chg.BwdFirstOut, chg.BwdHead, to, from); edge != noNode {
		return chg.BwdMiddle[edge]
	}
	return -1
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
