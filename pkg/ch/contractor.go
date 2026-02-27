package ch

import (
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

	for u := range n {
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
	pq := newContractionPQ(int(n))
	for i := range n {
		pq.Push(i, computePriority(outAdj, inAdj, i, contracted, contractedNeighbors[i], level[i]))
	}

	// Pre-allocate reusable witness search state.
	ws := newWitnessState(n)

	log.Printf("Starting contraction of %d nodes...", n)

	var totalShortcuts int
	order := uint32(0)

	// Adaptive log interval: frequent near the end.
	logInterval := uint32(50000)

	for pq.Len() > 0 {
		// Pop minimum-priority node.
		entry := pq.Pop()
		node := entry.node

		if contracted[node] {
			continue
		}

		// Lazy update: recompute priority and re-insert if it changed.
		newPriority := computePriority(outAdj, inAdj, node, contracted, contractedNeighbors[node], level[node])
		if newPriority > entry.priority && pq.Len() > 0 && newPriority > pq.PeekPriority() {
			pq.Push(node, newPriority)
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
	for i := range n {
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
	// Collect active incoming and outgoing neighbors using reusable buffers.
	ws.incoming = ws.incoming[:0]
	for _, e := range inAdj[node] {
		if !contracted[e.to] {
			ws.incoming = append(ws.incoming, e)
		}
	}

	ws.outgoing = ws.outgoing[:0]
	for _, e := range outAdj[node] {
		if !contracted[e.to] {
			ws.outgoing = append(ws.outgoing, e)
		}
	}

	if len(ws.incoming) == 0 || len(ws.outgoing) == 0 {
		return nil
	}

	ws.shortcuts = ws.shortcuts[:0]

	for _, in := range ws.incoming {
		// Find max outgoing weight for upper bound of this batch search.
		var maxOut uint32
		for _, out := range ws.outgoing {
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

		for _, out := range ws.outgoing {
			if out.to == in.to {
				continue // skip self-loops
			}

			scWeight := in.weight + out.weight

			// Check if witness path exists: dist[out.to] <= scWeight means
			// there's an alternative path at least as good as the shortcut.
			if ws.dist[out.to] > scWeight {
				ws.shortcuts = append(ws.shortcuts, shortcut{
					from:   in.to,
					to:     out.to,
					weight: scWeight,
				})
			}
		}
	}

	return ws.shortcuts
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

	for u := range n {
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

// Concrete-typed priority queue for contraction ordering.
// Avoids container/heap's interface boxing, pointer per entry, and virtual dispatch.

type pqItem struct {
	node     uint32
	priority int
}

type contractionPQ struct {
	items []pqItem
}

func newContractionPQ(cap int) *contractionPQ {
	return &contractionPQ{items: make([]pqItem, 0, cap)}
}

func (pq *contractionPQ) Len() int { return len(pq.items) }

func (pq *contractionPQ) PeekPriority() int { return pq.items[0].priority }

func (pq *contractionPQ) Push(node uint32, priority int) {
	pq.items = append(pq.items, pqItem{node, priority})
	pq.siftUp(len(pq.items) - 1)
}

func (pq *contractionPQ) Pop() pqItem {
	top := pq.items[0]
	n := len(pq.items) - 1
	pq.items[0] = pq.items[n]
	pq.items = pq.items[:n]
	if n > 0 {
		pq.siftDown(0)
	}
	return top
}

func (pq *contractionPQ) siftUp(i int) {
	item := pq.items[i]
	for i > 0 {
		parent := (i - 1) / 2
		if item.priority >= pq.items[parent].priority {
			break
		}
		pq.items[i] = pq.items[parent]
		i = parent
	}
	pq.items[i] = item
}

func (pq *contractionPQ) siftDown(i int) {
	n := len(pq.items)
	item := pq.items[i]
	for {
		child := 2*i + 1
		if child >= n {
			break
		}
		if right := child + 1; right < n && pq.items[right].priority < pq.items[child].priority {
			child = right
		}
		if item.priority <= pq.items[child].priority {
			break
		}
		pq.items[i] = pq.items[child]
		i = child
	}
	pq.items[i] = item
}
