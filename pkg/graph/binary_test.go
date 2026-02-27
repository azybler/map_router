package graph_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/paulmach/osm"

	"map_router/pkg/ch"
	"map_router/pkg/graph"
	osmparser "map_router/pkg/osm"
)

func buildTestCH(t *testing.T) *graph.CHGraph {
	t.Helper()
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 20, ToNodeID: 10, Weight: 100},
			{FromNodeID: 20, ToNodeID: 30, Weight: 200},
			{FromNodeID: 30, ToNodeID: 20, Weight: 200},
			{FromNodeID: 10, ToNodeID: 40, Weight: 300},
			{FromNodeID: 40, ToNodeID: 10, Weight: 300},
		},
		NodeLat: map[osm.NodeID]float64{10: 1.0, 20: 1.1, 30: 1.2, 40: 1.3},
		NodeLon: map[osm.NodeID]float64{10: 103.0, 20: 103.1, 30: 103.2, 40: 103.3},
	}
	g := graph.Build(result)
	return ch.Contract(g)
}

func TestBinaryRoundTrip(t *testing.T) {
	original := buildTestCH(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.graph.bin")

	if err := graph.WriteBinary(path, original); err != nil {
		t.Fatalf("WriteBinary: %v", err)
	}

	loaded, err := graph.ReadBinary(path)
	if err != nil {
		t.Fatalf("ReadBinary: %v", err)
	}

	if loaded.NumNodes != original.NumNodes {
		t.Errorf("NumNodes: got %d, want %d", loaded.NumNodes, original.NumNodes)
	}

	for i := uint32(0); i < original.NumNodes; i++ {
		if loaded.NodeLat[i] != original.NodeLat[i] {
			t.Errorf("NodeLat[%d]: got %f, want %f", i, loaded.NodeLat[i], original.NodeLat[i])
		}
	}

	// Rank is skipped during ReadBinary (only needed for preprocessing).
	if loaded.Rank != nil {
		t.Errorf("Rank should be nil after ReadBinary, got len=%d", len(loaded.Rank))
	}

	if len(loaded.FwdHead) != len(original.FwdHead) {
		t.Fatalf("FwdHead length: got %d, want %d", len(loaded.FwdHead), len(original.FwdHead))
	}
	for i := range original.FwdHead {
		if loaded.FwdHead[i] != original.FwdHead[i] {
			t.Errorf("FwdHead[%d]: got %d, want %d", i, loaded.FwdHead[i], original.FwdHead[i])
		}
		if loaded.FwdWeight[i] != original.FwdWeight[i] {
			t.Errorf("FwdWeight[%d]: got %d, want %d", i, loaded.FwdWeight[i], original.FwdWeight[i])
		}
		if loaded.FwdMiddle[i] != original.FwdMiddle[i] {
			t.Errorf("FwdMiddle[%d]: got %d, want %d", i, loaded.FwdMiddle[i], original.FwdMiddle[i])
		}
	}

	if len(loaded.BwdHead) != len(original.BwdHead) {
		t.Fatalf("BwdHead length: got %d, want %d", len(loaded.BwdHead), len(original.BwdHead))
	}
}

func TestBinaryInvalidMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.graph.bin")
	os.WriteFile(path, []byte("NOT_MPROUTER_HEADER_BLAH_BLAH_BLAH_MORE_DATA"), 0644)

	_, err := graph.ReadBinary(path)
	if err == nil {
		t.Fatal("expected error for invalid magic bytes")
	}
}

func TestBinaryTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.graph.bin")
	os.WriteFile(path, []byte("MPROUTER"), 0644)

	_, err := graph.ReadBinary(path)
	if err == nil {
		t.Fatal("expected error for truncated file")
	}
}
