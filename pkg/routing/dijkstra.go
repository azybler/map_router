package routing

import "math"

// MinHeap is a concrete-typed min-heap for Dijkstra priority queue.
// Avoids interface boxing overhead of container/heap.
type MinHeap struct {
	items []PQItem
}

// PQItem is a priority queue entry.
type PQItem struct {
	Node uint32
	Dist uint32
}

func (h *MinHeap) Len() int { return len(h.items) }

func (h *MinHeap) Push(node, dist uint32) {
	h.items = append(h.items, PQItem{node, dist})
	h.siftUp(len(h.items) - 1)
}

func (h *MinHeap) Pop() PQItem {
	n := len(h.items)
	item := h.items[0]
	h.items[0] = h.items[n-1]
	h.items = h.items[:n-1]
	if len(h.items) > 0 {
		h.siftDown(0)
	}
	return item
}

func (h *MinHeap) PeekDist() uint32 {
	if len(h.items) == 0 {
		return math.MaxUint32
	}
	return h.items[0].Dist
}

func (h *MinHeap) Reset() {
	h.items = h.items[:0]
}

func (h *MinHeap) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if h.items[i].Dist >= h.items[parent].Dist {
			break
		}
		h.items[i], h.items[parent] = h.items[parent], h.items[i]
		i = parent
	}
}

func (h *MinHeap) siftDown(i int) {
	n := len(h.items)
	for {
		smallest := i
		left := 2*i + 1
		right := 2*i + 2
		if left < n && h.items[left].Dist < h.items[smallest].Dist {
			smallest = left
		}
		if right < n && h.items[right].Dist < h.items[smallest].Dist {
			smallest = right
		}
		if smallest == i {
			break
		}
		h.items[i], h.items[smallest] = h.items[smallest], h.items[i]
		i = smallest
	}
}

// QueryState holds per-query state for bidirectional CH Dijkstra.
type QueryState struct {
	DistFwd []uint32
	DistBwd []uint32
	PredFwd []uint32 // predecessor in forward search (noNode = no predecessor)
	PredBwd []uint32 // predecessor in backward search (noNode = no predecessor)
	Touched []uint32 // nodes touched during this query (for fast reset)
	FwdPQ   MinHeap
	BwdPQ   MinHeap
}

// NewQueryState creates a new QueryState for a graph with n nodes.
func NewQueryState(n uint32) *QueryState {
	distFwd := make([]uint32, n)
	distBwd := make([]uint32, n)
	predFwd := make([]uint32, n)
	predBwd := make([]uint32, n)
	for i := range distFwd {
		distFwd[i] = math.MaxUint32
		distBwd[i] = math.MaxUint32
		predFwd[i] = noNode
		predBwd[i] = noNode
	}
	return &QueryState{
		DistFwd: distFwd,
		DistBwd: distBwd,
		PredFwd: predFwd,
		PredBwd: predBwd,
		Touched: make([]uint32, 0, 1024),
		FwdPQ:   MinHeap{items: make([]PQItem, 0, 256)},
		BwdPQ:   MinHeap{items: make([]PQItem, 0, 256)},
	}
}

// Reset clears only the touched entries for fast reuse.
func (qs *QueryState) Reset() {
	for _, node := range qs.Touched {
		qs.DistFwd[node] = math.MaxUint32
		qs.DistBwd[node] = math.MaxUint32
		qs.PredFwd[node] = noNode
		qs.PredBwd[node] = noNode
	}
	qs.Touched = qs.Touched[:0]
	qs.FwdPQ.Reset()
	qs.BwdPQ.Reset()
}

func (qs *QueryState) touchFwd(node uint32, dist uint32) {
	if qs.DistFwd[node] == math.MaxUint32 && qs.DistBwd[node] == math.MaxUint32 {
		qs.Touched = append(qs.Touched, node)
	}
	qs.DistFwd[node] = dist
}

func (qs *QueryState) touchBwd(node uint32, dist uint32) {
	if qs.DistFwd[node] == math.MaxUint32 && qs.DistBwd[node] == math.MaxUint32 {
		qs.Touched = append(qs.Touched, node)
	}
	qs.DistBwd[node] = dist
}
