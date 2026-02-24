package routing

import "map_router/pkg/graph"

const maxUnpackDepth = 100

// UnpackPath recursively unpacks shortcut edges into original edge sequences.
// Uses iterative approach with explicit stack to avoid stack overflow.
func UnpackPath(chg *graph.CHGraph, fwdPred, bwdPred map[uint32]predInfo, meetNode uint32) []uint32 {
	if meetNode == noNode {
		return nil
	}

	// Reconstruct forward path: source → meetNode.
	var fwdPath []uint32
	node := meetNode
	for {
		info, ok := fwdPred[node]
		if !ok {
			break
		}
		fwdPath = append(fwdPath, info.edgeIdx)
		node = info.prevNode
	}
	// Reverse to get source→meetNode order.
	for i, j := 0, len(fwdPath)-1; i < j; i, j = i+1, j-1 {
		fwdPath[i], fwdPath[j] = fwdPath[j], fwdPath[i]
	}

	// Reconstruct backward path: meetNode → target.
	var bwdPath []uint32
	node = meetNode
	for {
		info, ok := bwdPred[node]
		if !ok {
			break
		}
		bwdPath = append(bwdPath, info.edgeIdx)
		node = info.prevNode
	}

	// Unpack all edges.
	var result []uint32

	// Unpack forward path (uses forward graph).
	for _, edgeIdx := range fwdPath {
		unpackForwardEdge(chg, edgeIdx, &result)
	}

	// Unpack backward path (uses backward graph).
	for _, edgeIdx := range bwdPath {
		unpackBackwardEdge(chg, edgeIdx, &result)
	}

	return result
}

// predInfo tracks predecessor for path reconstruction.
type predInfo struct {
	prevNode uint32
	edgeIdx  uint32
}

const noNode = ^uint32(0) // sentinel for "no node"

// unpackForwardEdge iteratively unpacks a forward shortcut edge into original edges.
func unpackForwardEdge(chg *graph.CHGraph, edgeIdx uint32, result *[]uint32) {
	type stackItem struct {
		edgeIdx uint32
		isFwd   bool
		depth   int
	}

	stack := []stackItem{{edgeIdx, true, 0}}

	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if item.depth > maxUnpackDepth {
			continue // safety bound
		}

		var middle int32
		var head, from uint32
		if item.isFwd {
			middle = chg.FwdMiddle[item.edgeIdx]
			head = chg.FwdHead[item.edgeIdx]
			from = findCSRSource(chg.FwdFirstOut, item.edgeIdx)
		} else {
			middle = chg.BwdMiddle[item.edgeIdx]
			head = chg.BwdHead[item.edgeIdx]
			from = findCSRSource(chg.BwdFirstOut, item.edgeIdx)
		}

		if middle < 0 {
			// Original edge — add to result.
			_ = from
			_ = head
			*result = append(*result, item.edgeIdx)
			continue
		}

		// Shortcut edge from→head via middle.
		// Unpack: from→middle, then middle→head.
		mid := uint32(middle)

		// Find the edge from→mid in the forward graph.
		fromMidEdge := findEdge(chg.FwdFirstOut, chg.FwdHead, from, mid)
		// Find the edge mid→head in the forward graph.
		midHeadEdge := findEdge(chg.FwdFirstOut, chg.FwdHead, mid, head)

		if fromMidEdge != noNode && midHeadEdge != noNode {
			// Push in reverse order (mid→head first, from→mid second) so from→mid is processed first.
			stack = append(stack, stackItem{midHeadEdge, true, item.depth + 1})
			stack = append(stack, stackItem{fromMidEdge, true, item.depth + 1})
		}
	}
}

// unpackBackwardEdge iteratively unpacks a backward shortcut edge.
// Backward edges are stored reversed: edge from node u pointing to higher-rank v.
// When traversed during backward search, they represent v→u in the original graph.
func unpackBackwardEdge(chg *graph.CHGraph, edgeIdx uint32, result *[]uint32) {
	type stackItem struct {
		edgeIdx uint32
		depth   int
	}

	stack := []stackItem{{edgeIdx, 0}}

	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if item.depth > maxUnpackDepth {
			continue
		}

		middle := chg.BwdMiddle[item.edgeIdx]

		if middle < 0 {
			*result = append(*result, item.edgeIdx)
			continue
		}

		// Backward edge from u→v (stored), represents v→u in reality.
		from := findCSRSource(chg.BwdFirstOut, item.edgeIdx)
		head := chg.BwdHead[item.edgeIdx]
		mid := uint32(middle)

		// The shortcut represents head→mid→from in the original graph.
		// Unpack: head→mid, then mid→from.
		headMidEdge := findEdge(chg.BwdFirstOut, chg.BwdHead, mid, head)
		midFromEdge := findEdge(chg.BwdFirstOut, chg.BwdHead, from, mid)

		if headMidEdge != noNode && midFromEdge != noNode {
			stack = append(stack, stackItem{midFromEdge, item.depth + 1})
			stack = append(stack, stackItem{headMidEdge, item.depth + 1})
		}
	}
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

// findCSRSource finds the source node for an edge index in a CSR graph.
func findCSRSource(firstOut []uint32, edgeIdx uint32) uint32 {
	n := uint32(len(firstOut) - 1)
	lo, hi := uint32(0), n
	for lo < hi {
		mid := (lo + hi) / 2
		if firstOut[mid+1] <= edgeIdx {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
