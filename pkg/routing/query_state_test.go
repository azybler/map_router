package routing

import (
	"context"
	"runtime"
	"sync"
	"testing"
)

// A QueryState is as large as the graph, so an idle one must outlive garbage
// collections: a sync.Pool alone is emptied by the second collection, and the
// query after it built a new state.
func TestQueryStateOutlivesGarbageCollection(t *testing.T) {
	g, chg := buildTestGraphAndCH(t)
	eng := NewEngine(chg, g)
	ctx := context.Background()
	start, end := LatLng{Lat: 1.300, Lng: 103.800}, LatLng{Lat: 1.301, Lng: 103.802}

	first, err := eng.Route(ctx, start, end)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got := eng.qsBuilt.Load(); got != 1 {
		t.Fatalf("the first query built %d QueryStates, want 1", got)
	}

	// The first collection moves a pool's contents to its victim cache and the
	// second drops them; a third for good measure.
	for range 3 {
		runtime.GC()
	}

	again, err := eng.Route(ctx, start, end)
	if err != nil {
		t.Fatalf("Route after GC: %v", err)
	}
	if got := eng.qsBuilt.Load(); got != 1 {
		t.Errorf("a query after garbage collection built a new QueryState: %d built, want 1", got)
	}
	// Exactly equal, not within a tolerance: it is the same query on the same
	// engine in the same process, so every floating-point step is the same.
	if again.Cost != first.Cost || again.TotalDistanceMeters != first.TotalDistanceMeters {
		t.Errorf("the reused QueryState gave a different route: cost %d, %.3f m; want %d, %.3f m",
			again.Cost, again.TotalDistanceMeters, first.Cost, first.TotalDistanceMeters)
	}

	// RouteBetweenSnaps draws from the same place.
	a := eng.SnapCandidates(start.Lat, start.Lng, 1, 500)
	b := eng.SnapCandidates(end.Lat, end.Lng, 1, 500)
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("no snap candidates on the test graph")
	}
	for range 3 {
		runtime.GC()
	}
	if _, err := eng.RouteBetweenSnaps(ctx, a[0], b[0]); err != nil {
		t.Fatalf("RouteBetweenSnaps: %v", err)
	}
	if got := eng.qsBuilt.Load(); got != 1 {
		t.Errorf("RouteBetweenSnaps after garbage collection built a new QueryState: %d built, want 1", got)
	}
}

// Queries that overlap must each have a QueryState of their own, and the kept
// one must be back in place once they are done.
func TestQueryStatesUnderConcurrentQueries(t *testing.T) {
	g, chg := buildTestGraphAndCH(t)
	eng := NewEngine(chg, g)
	ctx := context.Background()

	// Every ordered pair of the test graph's six nodes.
	points := []LatLng{
		{Lat: 1.300, Lng: 103.800}, {Lat: 1.300, Lng: 103.801}, {Lat: 1.300, Lng: 103.802},
		{Lat: 1.301, Lng: 103.800}, {Lat: 1.301, Lng: 103.801}, {Lat: 1.301, Lng: 103.802},
	}
	type query struct{ from, to LatLng }
	type answer struct {
		cost uint32
		dist float64
	}
	var queries []query
	var want []answer
	for _, from := range points {
		for _, to := range points {
			if from == to {
				continue
			}
			res, err := eng.Route(ctx, from, to)
			if err != nil {
				t.Fatalf("Route %v -> %v: %v", from, to, err)
			}
			queries = append(queries, query{from, to})
			want = append(want, answer{res.Cost, res.TotalDistanceMeters})
		}
	}

	const workers, rounds = 8, 50
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range rounds {
				i := (w*7 + r) % len(queries)
				res, err := eng.Route(ctx, queries[i].from, queries[i].to)
				if err != nil {
					errs <- "Route: " + err.Error()
					return
				}
				// Exactly equal, for the same reason as in the test above.
				if res.Cost != want[i].cost || res.TotalDistanceMeters != want[i].dist {
					errs <- "a query that overlapped others got a different route than it does alone"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}

	if eng.keptQS.Load() == nil {
		t.Error("no QueryState is kept once every query has finished")
	}
	// The burst is over: whatever it built beyond the kept state may go, and
	// the next query still builds nothing.
	built := eng.qsBuilt.Load()
	for range 3 {
		runtime.GC()
	}
	if _, err := eng.Route(ctx, queries[0].from, queries[0].to); err != nil {
		t.Fatalf("Route after the burst: %v", err)
	}
	if got := eng.qsBuilt.Load(); got != built {
		t.Errorf("the query after the burst built a new QueryState: %d built, want %d", got, built)
	}
}
