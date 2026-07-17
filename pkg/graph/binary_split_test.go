package graph_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/paulmach/osm"

	"github.com/azybler/map_router/pkg/ch"
	"github.com/azybler/map_router/pkg/graph"
	osmparser "github.com/azybler/map_router/pkg/osm"
)

// buildTestCHDistinct builds a second CH graph with different topology, for
// verifying that overlays are rejected against a non-matching base.
func buildTestCHDistinct(t *testing.T) *graph.CHGraph {
	t.Helper()
	result := &osmparser.ParseResult{
		Edges: []osmparser.RawEdge{
			{FromNodeID: 10, ToNodeID: 20, Weight: 100},
			{FromNodeID: 20, ToNodeID: 10, Weight: 100},
			{FromNodeID: 20, ToNodeID: 30, Weight: 200},
			{FromNodeID: 30, ToNodeID: 20, Weight: 200},
		},
		NodeLat: map[osm.NodeID]float64{10: 1.0, 20: 1.1, 30: 1.2},
		NodeLon: map[osm.NodeID]float64{10: 103.0, 20: 103.1, 30: 103.2},
	}
	g := graph.Build(result)
	return ch.Contract(g)
}

// TestSplitRoundTrip writes a CH graph as base + overlay, stitches it back, and
// asserts the result is field-for-field identical to the combined-format load —
// with the base-half slices actually shared, not copied.
func TestSplitRoundTrip(t *testing.T) {
	original := buildTestCH(t)

	dir := t.TempDir()
	basePath := filepath.Join(dir, "test.base.bin")
	overlayPath := filepath.Join(dir, "test.overlay.bin")

	if err := graph.WriteBase(basePath, original); err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	if err := graph.WriteOverlay(overlayPath, original); err != nil {
		t.Fatalf("WriteOverlay: %v", err)
	}

	base, err := graph.ReadBase(basePath)
	if err != nil {
		t.Fatalf("ReadBase: %v", err)
	}
	if base.Identity == 0 {
		t.Error("base Identity should be non-zero")
	}
	if base.NumNodes != original.NumNodes {
		t.Errorf("base NumNodes: got %d, want %d", base.NumNodes, original.NumNodes)
	}

	chg, err := graph.ReadOverlay(overlayPath, base)
	if err != nil {
		t.Fatalf("ReadOverlay: %v", err)
	}

	// The stitched graph must equal a combined-format load of the same source.
	combinedPath := filepath.Join(dir, "test.combined.bin")
	if err := graph.WriteBinary(combinedPath, original); err != nil {
		t.Fatalf("WriteBinary: %v", err)
	}
	want, err := graph.ReadBinary(combinedPath)
	if err != nil {
		t.Fatalf("ReadBinary: %v", err)
	}

	assertU32Eq(t, "NodeLatLen", uint32(len(chg.NodeLat)), uint32(len(want.NodeLat)))
	for i := range want.NodeLat {
		if chg.NodeLat[i] != want.NodeLat[i] || chg.NodeLon[i] != want.NodeLon[i] {
			t.Errorf("node coord[%d] mismatch", i)
		}
	}
	assertSliceU32Eq(t, "OrigFirstOut", chg.OrigFirstOut, want.OrigFirstOut)
	assertSliceU32Eq(t, "OrigHead", chg.OrigHead, want.OrigHead)
	assertSliceU32Eq(t, "OrigWeight", chg.OrigWeight, want.OrigWeight)
	assertSliceU32Eq(t, "FwdFirstOut", chg.FwdFirstOut, want.FwdFirstOut)
	assertSliceU32Eq(t, "FwdHead", chg.FwdHead, want.FwdHead)
	assertSliceU32Eq(t, "FwdWeight", chg.FwdWeight, want.FwdWeight)
	assertSliceU32Eq(t, "BwdFirstOut", chg.BwdFirstOut, want.BwdFirstOut)
	assertSliceU32Eq(t, "BwdHead", chg.BwdHead, want.BwdHead)
	assertSliceU32Eq(t, "BwdWeight", chg.BwdWeight, want.BwdWeight)
	if len(chg.FwdMiddle) != len(want.FwdMiddle) {
		t.Fatalf("FwdMiddle length: got %d, want %d", len(chg.FwdMiddle), len(want.FwdMiddle))
	}
	for i := range want.FwdMiddle {
		if chg.FwdMiddle[i] != want.FwdMiddle[i] {
			t.Errorf("FwdMiddle[%d]: got %d, want %d", i, chg.FwdMiddle[i], want.FwdMiddle[i])
		}
	}
	assertSliceU32Eq(t, "GeoFirstOut", chg.GeoFirstOut, want.GeoFirstOut)

	// Rank is skipped on load, exactly like the combined format.
	if chg.Rank != nil {
		t.Errorf("Rank should be nil after ReadOverlay, got len=%d", len(chg.Rank))
	}

	// Base-half slices must be SHARED with the base, not copied — that sharing is
	// the whole point (one base in RAM across metrics).
	if len(chg.NodeLat) > 0 && &chg.NodeLat[0] != &base.NodeLat[0] {
		t.Error("chg.NodeLat should alias base.NodeLat (shared, not copied)")
	}
	if len(chg.OrigHead) > 0 && &chg.OrigHead[0] != &base.OrigHead[0] {
		t.Error("chg.OrigHead should alias base.OrigHead (shared, not copied)")
	}
}

// TestSplitOverlayRejectsWrongBase ensures a mismatched base/overlay pairing is
// rejected rather than silently addressing the wrong roads.
func TestSplitOverlayRejectsWrongBase(t *testing.T) {
	dir := t.TempDir()

	overlayPath := filepath.Join(dir, "a.overlay.bin")
	if err := graph.WriteOverlay(overlayPath, buildTestCH(t)); err != nil {
		t.Fatalf("WriteOverlay: %v", err)
	}

	// A base built from a different (smaller) network.
	otherBasePath := filepath.Join(dir, "b.base.bin")
	if err := graph.WriteBase(otherBasePath, buildTestCHDistinct(t)); err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	otherBase, err := graph.ReadBase(otherBasePath)
	if err != nil {
		t.Fatalf("ReadBase: %v", err)
	}

	if _, err := graph.ReadOverlay(overlayPath, otherBase); err == nil {
		t.Fatal("expected ReadOverlay to reject an overlay paired with the wrong base")
	}

	// Same-shape base but corrupted identity is also rejected.
	basePath := filepath.Join(dir, "a.base.bin")
	if err := graph.WriteBase(basePath, buildTestCH(t)); err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	base, err := graph.ReadBase(basePath)
	if err != nil {
		t.Fatalf("ReadBase: %v", err)
	}
	base.Identity ^= 0xFFFFFFFF
	if _, err := graph.ReadOverlay(overlayPath, base); err == nil {
		t.Fatal("expected ReadOverlay to reject an identity-mismatched base")
	}
}

func TestSplitInvalidMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.base.bin")
	os.WriteFile(path, []byte("NOT_A_BASE_HEADER_AT_ALL_XXXXXXXXXXXX"), 0644)
	if _, err := graph.ReadBase(path); err == nil {
		t.Fatal("expected error for invalid base magic bytes")
	}
}

func assertU32Eq(t *testing.T, name string, got, want uint32) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %d, want %d", name, got, want)
	}
}

func assertSliceU32Eq(t *testing.T, name string, got, want []uint32) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s length: got %d, want %d", name, len(got), len(want))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d]: got %d, want %d", name, i, got[i], want[i])
			return
		}
	}
}
