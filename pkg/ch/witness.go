package ch

const (
	maxSettled = 500 // max nodes settled during witness search
	maxHops    = 5   // max hops from source
)

// witnessHeapItem is an entry in the witness search min-heap.
type witnessHeapItem struct {
	node uint32
	dist uint32
	hops int
}

// witnessHeap is a concrete-typed binary min-heap for witness search.
type witnessHeap struct {
	items []witnessHeapItem
}

func (h *witnessHeap) Len() int { return len(h.items) }

func (h *witnessHeap) Push(node uint32, dist uint32, hops int) {
	h.items = append(h.items, witnessHeapItem{node, dist, hops})
	h.siftUp(len(h.items) - 1)
}

func (h *witnessHeap) Pop() witnessHeapItem {
	top := h.items[0]
	n := len(h.items) - 1
	h.items[0] = h.items[n]
	h.items = h.items[:n]
	if n > 0 {
		h.siftDown(0)
	}
	return top
}

func (h *witnessHeap) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if h.items[i].dist >= h.items[parent].dist {
			break
		}
		h.items[i], h.items[parent] = h.items[parent], h.items[i]
		i = parent
	}
}

func (h *witnessHeap) siftDown(i int) {
	n := len(h.items)
	for {
		smallest := i
		left := 2*i + 1
		right := 2*i + 2
		if left < n && h.items[left].dist < h.items[smallest].dist {
			smallest = left
		}
		if right < n && h.items[right].dist < h.items[smallest].dist {
			smallest = right
		}
		if smallest == i {
			break
		}
		h.items[i], h.items[smallest] = h.items[smallest], h.items[i]
		i = smallest
	}
}

func (h *witnessHeap) Reset() {
	h.items = h.items[:0]
}

// witnessState holds reusable state for batch witness searches.
// Avoids per-call map allocation by using a touched-list pattern.
type witnessState struct {
	dist    []uint32 // distance array indexed by node ID
	touched []uint32 // list of nodes touched (for fast reset)
	heap    witnessHeap
}

func newWitnessState(numNodes uint32) *witnessState {
	dist := make([]uint32, numNodes)
	for i := range dist {
		dist[i] = maxUint32
	}
	return &witnessState{
		dist: dist,
		heap: witnessHeap{items: make([]witnessHeapItem, 0, 256)},
	}
}

func (ws *witnessState) reset() {
	for _, n := range ws.touched {
		ws.dist[n] = maxUint32
	}
	ws.touched = ws.touched[:0]
	ws.heap.Reset()
}

const maxUint32 = ^uint32(0)

// batchWitnessSearch runs a single Dijkstra from source (excluding the
// contracted node) and returns the distances to all reachable nodes.
// The caller checks which outgoing targets need shortcuts.
//
// This replaces the per-(in,out)-pair witness search with a single search
// per incoming neighbor, reducing the number of searches from O(|in|*|out|)
// to O(|in|).
func batchWitnessSearch(ws *witnessState, outAdj [][]adjEntry, source, excluded uint32, maxWeight uint32, contracted []bool) {
	ws.reset()

	ws.dist[source] = 0
	ws.touched = append(ws.touched, source)
	ws.heap.Push(source, 0, 0)

	settled := 0

	for ws.heap.Len() > 0 {
		cur := ws.heap.Pop()

		// Skip stale entries.
		if cur.dist > ws.dist[cur.node] {
			continue
		}

		settled++
		if settled >= maxSettled {
			break
		}

		if cur.dist > maxWeight {
			continue
		}

		if cur.hops >= maxHops {
			continue
		}

		// Relax outgoing neighbors.
		for _, e := range outAdj[cur.node] {
			if e.to == excluded || contracted[e.to] {
				continue
			}

			newDist := cur.dist + e.weight
			if newDist > maxWeight {
				continue
			}

			if newDist < ws.dist[e.to] {
				if ws.dist[e.to] == maxUint32 {
					ws.touched = append(ws.touched, e.to)
				}
				ws.dist[e.to] = newDist
				ws.heap.Push(e.to, newDist, cur.hops+1)
			}
		}
	}
}
