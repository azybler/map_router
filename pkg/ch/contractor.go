package ch

import (
	"container/heap"
	"log"
	"map_router/pkg/graph"
)

// maxShortcutsPerNode is the limit on shortcuts a single contraction can create.
// Nodes exceeding this form an uncontracted "core" at the top of the hierarchy.
const maxShortcutsPerNode = 1000

// adjEntry represents an edge in the mutable adjacency list.
type adjEntry struct {
	to     uint32
	weight uint32
	middle int32 // -1 for original edges, else the contracted node ID
}

// Contract performs Contraction Hierarchies preprocessing on the given graph.
func Contract(g *graph.Graph) *graph.CHGraph {
	n := g.NumNodes
	if n == 0 {
		return &graph.CHGraph{}
	}

	// Build mutable forward and reverse adjacency lists from the CSR graph.
	outAdj := make([][]adjEntry, n)
	inAdj := make([][]adjEntry, n)

	for u := uint32(0); u < n; u++ {
		start, end := g.EdgesFrom(u)
		for e := start; e < end; e++ {
			v := g.Head[e]
			w := g.Weight[e]
			outAdj[u] = append(outAdj[u], adjEntry{to: v, weight: w, middle: -1})
			inAdj[v] = append(inAdj[v], adjEntry{to: u, weight: w, middle: -1})
		}
	}

	contracted := make([]bool, n)
	rank := make([]uint32, n)
	contractedNeighbors := make([]int, n)
	level := make([]int, n)

	// Initialize priority queue with all nodes.
	pq := make(priorityQueue, n)
	for i := uint32(0); i < n; i++ {
		pq[i] = &pqEntry{
			node:     i,
			priority: computePriority(outAdj, inAdj, i, contracted, contractedNeighbors[i], level[i]),
			index:    int(i),
		}
	}
	heap.Init(&pq)

	// Pre-allocate reusable witness search state.
	ws := newWitnessState(n)

	log.Printf("Starting contraction of %d nodes...", n)

	var totalShortcuts int
	order := uint32(0)

	// Adaptive log interval: frequent near the end.
	logInterval := uint32(50000)

	for pq.Len() > 0 {
		// Pop minimum-priority node.
		entry := heap.Pop(&pq).(*pqEntry)
		node := entry.node

		if contracted[node] {
			continue
		}

		// Lazy update: recompute priority and re-insert if it changed.
		newPriority := computePriority(outAdj, inAdj, node, contracted, contractedNeighbors[node], level[node])
		if newPriority > entry.priority && pq.Len() > 0 && newPriority > pq[0].priority {
			entry.priority = newPriority
			heap.Push(&pq, entry)
			continue
		}

		// Find shortcuts needed using batch witness search.
		shortcuts := findShortcuts(ws, outAdj, inAdj, node, contracted)

		// If contracting this node would produce too many shortcuts,
		// stop contraction entirely. Remaining nodes form a "core"
		// at the top of the hierarchy with original edges preserved.
		if len(shortcuts) > maxShortcutsPerNode {
			log.Printf("Stopping contraction: node %d would create %d shortcuts (limit %d). %d nodes remain in core.",
				node, len(shortcuts), maxShortcutsPerNode, n-order)
			break
		}

		// Contract this node.
		contracted[node] = true
		rank[node] = order
		order++
		totalShortcuts += len(shortcuts)

		// Add shortcuts to adjacency lists.
		for _, sc := range shortcuts {
			outAdj[sc.from] = append(outAdj[sc.from], adjEntry{to: sc.to, weight: sc.weight, middle: int32(node)})
			inAdj[sc.to] = append(inAdj[sc.to], adjEntry{to: sc.from, weight: sc.weight, middle: int32(node)})
		}

		// Update neighbors' contracted neighbor count and level.
		for _, e := range outAdj[node] {
			if !contracted[e.to] {
				contractedNeighbors[e.to]++
				if level[node]+1 > level[e.to] {
					level[e.to] = level[node] + 1
				}
			}
		}
		for _, e := range inAdj[node] {
			if !contracted[e.to] {
				contractedNeighbors[e.to]++
				if level[node]+1 > level[e.to] {
					level[e.to] = level[node] + 1
				}
			}
		}

		// Adaptive logging: more frequent as we approach the end.
		remaining := n - order
		if remaining < 1000 {
			logInterval = 100
		} else if remaining < 10000 {
			logInterval = 1000
		} else if remaining < 100000 {
			logInterval = 10000
		} else {
			logInterval = 50000
		}

		if order%logInterval == 0 {
			log.Printf("Contracted %d/%d nodes, %d shortcuts so far", order, n, totalShortcuts)
		}
	}

	// Assign ranks to remaining uncontracted core nodes.
	coreSize := uint32(0)
	for i := uint32(0); i < n; i++ {
		if !contracted[i] {
			contracted[i] = true
			rank[i] = order
			order++
			coreSize++
		}
	}

	log.Printf("Contraction complete: %d shortcuts created (%.1fx original edges), %d core nodes",
		totalShortcuts, float64(totalShortcuts)/float64(g.NumEdges), coreSize)

	// Build forward and backward upward CSR overlay.
	return buildOverlay(g, outAdj, inAdj, rank)
}

// shortcut represents a shortcut edge to be added.
type shortcut struct {
	from, to uint32
	weight   uint32
}

// findShortcuts determines which shortcuts are needed when contracting a node.
// Uses batch witness search: one Dijkstra per incoming neighbor instead of one
// per (incoming, outgoing) pair. This reduces search count from O(|in|*|out|)
// to O(|in|).
func findShortcuts(ws *witnessState, outAdj, inAdj [][]adjEntry, node uint32, contracted []bool) []shortcut {
	// Collect active incoming and outgoing neighbors.
	var incoming []adjEntry
	for _, e := range inAdj[node] {
		if !contracted[e.to] {
			incoming = append(incoming, e)
		}
	}

	var outgoing []adjEntry
	for _, e := range outAdj[node] {
		if !contracted[e.to] {
			outgoing = append(outgoing, e)
		}
	}

	if len(incoming) == 0 || len(outgoing) == 0 {
		return nil
	}

	var shortcuts []shortcut

	for _, in := range incoming {
		// Find max outgoing weight for upper bound of this batch search.
		var maxOut uint32
		for _, out := range outgoing {
			if out.to != in.to && out.weight > maxOut {
				maxOut = out.weight
			}
		}
		if maxOut == 0 {
			continue // all outgoing go back to in.to
		}

		maxWeight := in.weight + maxOut

		// Run ONE Dijkstra from in.to, then check all outgoing targets.
		batchWitnessSearch(ws, outAdj, in.to, node, maxWeight, contracted)

		for _, out := range outgoing {
			if out.to == in.to {
				continue // skip self-loops
			}

			scWeight := in.weight + out.weight

			// Check if witness path exists: dist[out.to] <= scWeight means
			// there's an alternative path at least as good as the shortcut.
			if ws.dist[out.to] > scWeight {
				shortcuts = append(shortcuts, shortcut{
					from:   in.to,
					to:     out.to,
					weight: scWeight,
				})
			}
		}
	}

	return shortcuts
}

// computePriority returns the priority for a node (lower = contract first).
func computePriority(outAdj, inAdj [][]adjEntry, node uint32, contracted []bool, contractedNeighbors, level int) int {
	// Count active incoming/outgoing edges.
	activeIn := 0
	for _, e := range inAdj[node] {
		if !contracted[e.to] {
			activeIn++
		}
	}
	activeOut := 0
	for _, e := range outAdj[node] {
		if !contracted[e.to] {
			activeOut++
		}
	}

	// Count shortcuts that would be needed (simplified: worst case = in * out).
	// For accurate count we'd run witness search, but for ordering a simpler
	// heuristic is faster and good enough.
	edgeDifference := activeIn*activeOut - (activeIn + activeOut)

	return edgeDifference + 2*contractedNeighbors + level
}

// buildOverlay creates forward and backward upward CSR graphs from the
// contracted adjacency lists and node ranks.
func buildOverlay(orig *graph.Graph, outAdj, inAdj [][]adjEntry, rank []uint32) *graph.CHGraph {
	n := orig.NumNodes

	// Collect forward upward edges: edge u→v where rank[u] < rank[v].
	type csrEdge struct {
		from, to uint32
		weight   uint32
		middle   int32
	}

	var fwdEdges, bwdEdges []csrEdge

	for u := uint32(0); u < n; u++ {
		for _, e := range outAdj[u] {
			if rank[u] < rank[e.to] {
				fwdEdges = append(fwdEdges, csrEdge{from: u, to: e.to, weight: e.weight, middle: e.middle})
			}
		}
		// Backward upward: for edges v→u where rank[u] < rank[v],
		// store as u→v in the backward graph (for backward search from target).
		for _, e := range inAdj[u] {
			if rank[u] < rank[e.to] {
				bwdEdges = append(bwdEdges, csrEdge{from: u, to: e.to, weight: e.weight, middle: e.middle})
			}
		}
	}

	log.Printf("Overlay: %d forward upward edges, %d backward upward edges", len(fwdEdges), len(bwdEdges))

	buildCSR := func(edges []csrEdge) (firstOut, head []uint32, weight []uint32, middle []int32) {
		numEdges := uint32(len(edges))
		firstOut = make([]uint32, n+1)
		head = make([]uint32, numEdges)
		weight = make([]uint32, numEdges)
		middle = make([]int32, numEdges)

		// Count edges per source.
		for _, e := range edges {
			firstOut[e.from+1]++
		}
		for i := uint32(1); i <= n; i++ {
			firstOut[i] += firstOut[i-1]
		}

		// Place edges.
		pos := make([]uint32, n)
		copy(pos, firstOut[:n])
		for _, e := range edges {
			idx := pos[e.from]
			head[idx] = e.to
			weight[idx] = e.weight
			middle[idx] = e.middle
			pos[e.from]++
		}

		return
	}

	fwdFirstOut, fwdHead, fwdWeight, fwdMiddle := buildCSR(fwdEdges)
	bwdFirstOut, bwdHead, bwdWeight, bwdMiddle := buildCSR(bwdEdges)

	return &graph.CHGraph{
		NumNodes:     n,
		NodeLat:      orig.NodeLat,
		NodeLon:      orig.NodeLon,
		Rank:         rank,
		FwdFirstOut:  fwdFirstOut,
		FwdHead:      fwdHead,
		FwdWeight:    fwdWeight,
		FwdMiddle:    fwdMiddle,
		BwdFirstOut:  bwdFirstOut,
		BwdHead:      bwdHead,
		BwdWeight:    bwdWeight,
		BwdMiddle:    bwdMiddle,
		OrigFirstOut: orig.FirstOut,
		OrigHead:     orig.Head,
		OrigWeight:   orig.Weight,
		GeoFirstOut:  orig.GeoFirstOut,
		GeoShapeLat:  orig.GeoShapeLat,
		GeoShapeLon:  orig.GeoShapeLon,
	}
}

// Priority queue implementation for contraction ordering.

type pqEntry struct {
	node     uint32
	priority int
	index    int
}

type priorityQueue []*pqEntry

func (pq priorityQueue) Len() int           { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool { return pq[i].priority < pq[j].priority }
func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue) Push(x any) {
	entry := x.(*pqEntry)
	entry.index = len(*pq)
	*pq = append(*pq, entry)
}

func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil
	entry.index = -1
	*pq = old[:n-1]
	return entry
}
