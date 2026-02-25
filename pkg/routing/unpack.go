package routing

import "map_router/pkg/graph"

const maxUnpackDepth = 200

const noNode = ^uint32(0) // sentinel for "no node"

// unpackOverlayPath takes a sequence of overlay-level nodes and unpacks all
// shortcut hops into original-graph node sequences.
func unpackOverlayPath(chg *graph.CHGraph, overlayNodes []uint32) []uint32 {
	if len(overlayNodes) < 2 {
		return overlayNodes
	}

	var result []uint32
	result = append(result, overlayNodes[0])

	for i := 0; i < len(overlayNodes)-1; i++ {
		unpacked := unpackHop(chg, overlayNodes[i], overlayNodes[i+1])
		// Skip first node (already in result) to avoid duplication.
		if len(unpacked) > 1 {
			result = append(result, unpacked[1:]...)
		}
	}

	return result
}

// unpackHop iteratively unpacks a single overlay hop from→to into a sequence
// of original-graph nodes. Uses an explicit stack to avoid recursion.
func unpackHop(chg *graph.CHGraph, from, to uint32) []uint32 {
	type item struct {
		from, to uint32
		depth    int
	}

	stack := []item{{from, to, 0}}
	var result []uint32

	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if it.depth > maxUnpackDepth {
			continue // safety bound
		}

		middle := findMiddle(chg, it.from, it.to)
		if middle < 0 {
			// Original edge — append nodes.
			if len(result) == 0 || result[len(result)-1] != it.from {
				result = append(result, it.from)
			}
			result = append(result, it.to)
			continue
		}

		m := uint32(middle)
		// Push right half first (m→to), then left half (from→m),
		// so left is processed first (LIFO).
		stack = append(stack, item{m, it.to, it.depth + 1})
		stack = append(stack, item{it.from, m, it.depth + 1})
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

// findEdge finds an edge from source to target in a CSR graph.
func findEdge(firstOut, head []uint32, source, target uint32) uint32 {
	start := firstOut[source]
	end := firstOut[source+1]
	for e := start; e < end; e++ {
		if head[e] == target {
			return e
		}
	}
	return noNode
}
