package ch

const (
	maxSettled = 500 // max nodes settled during witness search
	maxHops    = 5   // max hops from source
)

// witnessSearch checks if there exists a path from source to target not going
// through the excluded node, with total weight <= maxWeight.
// Returns true if a witness (alternative path) exists.
func witnessSearch(outAdj [][]adjEntry, source, target, excluded uint32, maxWeight uint32, contracted []bool) bool {
	if source == target {
		return true
	}

	type item struct {
		node uint32
		dist uint32
		hops int
	}

	// Simple priority queue using a slice (good enough for small local search).
	var pq []item
	pq = append(pq, item{node: source, dist: 0, hops: 0})

	dist := make(map[uint32]uint32)
	dist[source] = 0

	settled := 0

	for len(pq) > 0 {
		// Find minimum.
		minIdx := 0
		for i := 1; i < len(pq); i++ {
			if pq[i].dist < pq[minIdx].dist {
				minIdx = i
			}
		}
		cur := pq[minIdx]
		pq[minIdx] = pq[len(pq)-1]
		pq = pq[:len(pq)-1]

		// Skip stale entries.
		if d, ok := dist[cur.node]; ok && cur.dist > d {
			continue
		}

		// Early target termination.
		if cur.node == target {
			return true
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

			if d, ok := dist[e.to]; !ok || newDist < d {
				dist[e.to] = newDist
				pq = append(pq, item{node: e.to, dist: newDist, hops: cur.hops + 1})
			}
		}
	}

	return false
}
