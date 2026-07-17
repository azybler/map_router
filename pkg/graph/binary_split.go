package graph

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// Split on-disk format.
//
// A combined graph binary (WriteBinary/ReadBinary) stores everything for one
// metric in one file. When several metrics share a road network, the base half
// — node coordinates, original edge topology, and geometry — is byte-identical
// across all of them and only the overlay half (ranks + the CH upward graph +
// per-metric original edge weights) differs.
//
// The split format writes those halves separately: one base file plus one
// overlay file per metric. The server loads the base once and stitches each
// overlay onto it, so N metrics cost base + N overlays instead of N full copies
// (and a single shared Snapper instead of N). A topology Identity hash written
// into both files binds an overlay to its base and is checked on load.
const (
	baseMagic    = "MPRBASE1"
	overlayMagic = "MPROVLY1"
	splitVersion = uint32(1)
)

// baseHeader is the header of a base file.
type baseHeader struct {
	Magic        [8]byte
	Version      uint32
	NumNodes     uint32
	NumOrigEdges uint32
	Identity     uint32 // topologyIdentity over coords + original CSR
}

// overlayHeader is the header of an overlay file.
type overlayHeader struct {
	Magic        [8]byte
	Version      uint32
	NumNodes     uint32
	NumOrigEdges uint32
	BaseIdentity uint32 // must equal the paired base's Identity
	NumShortcuts uint32
	NumFwdEdges  uint32
	NumBwdEdges  uint32
}

// topologyIdentity fingerprints the metric-independent topology so a base and an
// overlay that were built from different sources can be detected at load time.
// It deliberately covers only what makes node/edge indices meaningful (node
// count, coordinates, original CSR); geometry lives solely in the base and can
// never be addressed by an overlay, so it is excluded.
func topologyIdentity(numNodes uint32, nodeLat, nodeLon []float64, origFirstOut, origHead []uint32) uint32 {
	h := crc32.NewIEEE()
	_ = binary.Write(h, binary.LittleEndian, numNodes)
	_ = writeFloat64Slice(h, nodeLat)
	_ = writeFloat64Slice(h, nodeLon)
	_ = writeUint32Slice(h, origFirstOut)
	_ = writeUint32Slice(h, origHead)
	return h.Sum32()
}

// WriteBase serializes the metric-independent half of a CHGraph to a base file.
func WriteBase(path string, chg *CHGraph) error {
	return writeSplitFile(path, func(w io.Writer) error {
		hdr := baseHeader{
			Version:      splitVersion,
			NumNodes:     chg.NumNodes,
			NumOrigEdges: uint32(len(chg.OrigHead)),
			Identity:     topologyIdentity(chg.NumNodes, chg.NodeLat, chg.NodeLon, chg.OrigFirstOut, chg.OrigHead),
		}
		copy(hdr.Magic[:], baseMagic)
		if err := binary.Write(w, binary.LittleEndian, &hdr); err != nil {
			return fmt.Errorf("write header: %w", err)
		}

		if err := writeFloat64Slice(w, chg.NodeLat); err != nil {
			return fmt.Errorf("write NodeLat: %w", err)
		}
		if err := writeFloat64Slice(w, chg.NodeLon); err != nil {
			return fmt.Errorf("write NodeLon: %w", err)
		}
		if err := writeUint32Slice(w, chg.OrigFirstOut); err != nil {
			return fmt.Errorf("write OrigFirstOut: %w", err)
		}
		if err := writeUint32Slice(w, chg.OrigHead); err != nil {
			return fmt.Errorf("write OrigHead: %w", err)
		}
		if err := writeLenPrefixedUint32(w, chg.GeoFirstOut); err != nil {
			return fmt.Errorf("write GeoFirstOut: %w", err)
		}
		if err := writeLenPrefixedFloat64(w, chg.GeoShapeLat); err != nil {
			return fmt.Errorf("write GeoShapeLat: %w", err)
		}
		if err := writeLenPrefixedFloat64(w, chg.GeoShapeLon); err != nil {
			return fmt.Errorf("write GeoShapeLon: %w", err)
		}
		return nil
	})
}

// WriteOverlay serializes the metric-specific half of a CHGraph to an overlay
// file, stamped with the paired base's topology identity.
func WriteOverlay(path string, chg *CHGraph) error {
	return writeSplitFile(path, func(w io.Writer) error {
		var numShortcuts uint32
		for _, m := range chg.FwdMiddle {
			if m >= 0 {
				numShortcuts++
			}
		}
		for _, m := range chg.BwdMiddle {
			if m >= 0 {
				numShortcuts++
			}
		}

		hdr := overlayHeader{
			Version:      splitVersion,
			NumNodes:     chg.NumNodes,
			NumOrigEdges: uint32(len(chg.OrigHead)),
			BaseIdentity: topologyIdentity(chg.NumNodes, chg.NodeLat, chg.NodeLon, chg.OrigFirstOut, chg.OrigHead),
			NumShortcuts: numShortcuts,
			NumFwdEdges:  uint32(len(chg.FwdHead)),
			NumBwdEdges:  uint32(len(chg.BwdHead)),
		}
		copy(hdr.Magic[:], overlayMagic)
		if err := binary.Write(w, binary.LittleEndian, &hdr); err != nil {
			return fmt.Errorf("write header: %w", err)
		}

		// Per-metric weights on the original edges (used by snapping/seeding).
		if err := writeUint32Slice(w, chg.OrigWeight); err != nil {
			return fmt.Errorf("write OrigWeight: %w", err)
		}
		// Rank is intentionally not stored: it is a preprocessing-only artifact
		// that the query engine never reads, and the combined format likewise
		// discards it on load. Omitting it keeps a converted overlay (whose
		// source already dropped Rank) and a freshly-built one byte-compatible.

		if err := writeUint32Slice(w, chg.FwdFirstOut); err != nil {
			return fmt.Errorf("write FwdFirstOut: %w", err)
		}
		if err := writeUint32Slice(w, chg.FwdHead); err != nil {
			return fmt.Errorf("write FwdHead: %w", err)
		}
		if err := writeUint32Slice(w, chg.FwdWeight); err != nil {
			return fmt.Errorf("write FwdWeight: %w", err)
		}
		if err := writeInt32Slice(w, chg.FwdMiddle); err != nil {
			return fmt.Errorf("write FwdMiddle: %w", err)
		}
		if err := writeUint32Slice(w, chg.BwdFirstOut); err != nil {
			return fmt.Errorf("write BwdFirstOut: %w", err)
		}
		if err := writeUint32Slice(w, chg.BwdHead); err != nil {
			return fmt.Errorf("write BwdHead: %w", err)
		}
		if err := writeUint32Slice(w, chg.BwdWeight); err != nil {
			return fmt.Errorf("write BwdWeight: %w", err)
		}
		if err := writeInt32Slice(w, chg.BwdMiddle); err != nil {
			return fmt.Errorf("write BwdMiddle: %w", err)
		}
		return nil
	})
}

// ReadBase deserializes a base file.
func ReadBase(path string) (*BaseGraph, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	crcReader := crc32Reader{r: f, hash: crc32.NewIEEE()}
	r := &crcReader

	var hdr baseHeader
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if string(hdr.Magic[:]) != baseMagic {
		return nil, fmt.Errorf("invalid base magic bytes: %q", hdr.Magic)
	}
	if hdr.Version != splitVersion {
		return nil, fmt.Errorf("unsupported base version: %d", hdr.Version)
	}
	if hdr.NumNodes > maxNodes {
		return nil, fmt.Errorf("NumNodes %d exceeds limit %d", hdr.NumNodes, maxNodes)
	}
	if hdr.NumOrigEdges > maxEdges {
		return nil, fmt.Errorf("NumOrigEdges %d exceeds limit %d", hdr.NumOrigEdges, maxEdges)
	}

	b := &BaseGraph{NumNodes: hdr.NumNodes, Identity: hdr.Identity}
	if b.NodeLat, err = readFloat64Slice(r, int(hdr.NumNodes)); err != nil {
		return nil, fmt.Errorf("read NodeLat: %w", err)
	}
	if b.NodeLon, err = readFloat64Slice(r, int(hdr.NumNodes)); err != nil {
		return nil, fmt.Errorf("read NodeLon: %w", err)
	}
	if b.OrigFirstOut, err = readUint32Slice(r, int(hdr.NumNodes+1)); err != nil {
		return nil, fmt.Errorf("read OrigFirstOut: %w", err)
	}
	if b.OrigHead, err = readUint32Slice(r, int(hdr.NumOrigEdges)); err != nil {
		return nil, fmt.Errorf("read OrigHead: %w", err)
	}
	b.GeoFirstOut, _ = readUint32SliceOptional(r)
	b.GeoShapeLat, _ = readFloat64SliceOptional(r)
	b.GeoShapeLon, _ = readFloat64SliceOptional(r)

	if err := verifyCRC(f, &crcReader); err != nil {
		return nil, err
	}

	if err := validateCSR(b.OrigFirstOut, b.OrigHead, hdr.NumNodes); err != nil {
		return nil, fmt.Errorf("original CSR invalid: %w", err)
	}
	// Guard against a base whose stored identity does not match its own payload
	// (corruption/tampering). The overlay check below relies on this being sound.
	if got := topologyIdentity(b.NumNodes, b.NodeLat, b.NodeLon, b.OrigFirstOut, b.OrigHead); got != hdr.Identity {
		return nil, fmt.Errorf("base identity mismatch: header=%08x computed=%08x", hdr.Identity, got)
	}
	return b, nil
}

// ReadOverlay deserializes an overlay file and stitches it onto base, returning a
// full CHGraph whose base-half slices are shared with base (not copied). The
// overlay's stamped identity must match base.Identity.
func ReadOverlay(path string, base *BaseGraph) (*CHGraph, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	crcReader := crc32Reader{r: f, hash: crc32.NewIEEE()}
	r := &crcReader

	var hdr overlayHeader
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if string(hdr.Magic[:]) != overlayMagic {
		return nil, fmt.Errorf("invalid overlay magic bytes: %q", hdr.Magic)
	}
	if hdr.Version != splitVersion {
		return nil, fmt.Errorf("unsupported overlay version: %d", hdr.Version)
	}
	if hdr.NumNodes != base.NumNodes {
		return nil, fmt.Errorf("overlay NumNodes %d != base NumNodes %d", hdr.NumNodes, base.NumNodes)
	}
	if int(hdr.NumOrigEdges) != len(base.OrigHead) {
		return nil, fmt.Errorf("overlay NumOrigEdges %d != base %d", hdr.NumOrigEdges, len(base.OrigHead))
	}
	if hdr.BaseIdentity != base.Identity {
		return nil, fmt.Errorf("overlay/base mismatch: overlay expects base %08x, got %08x (built from different sources?)", hdr.BaseIdentity, base.Identity)
	}
	if hdr.NumFwdEdges > maxEdges || hdr.NumBwdEdges > maxEdges {
		return nil, fmt.Errorf("edge count exceeds limit %d", maxEdges)
	}

	// Base-half slices are shared with base — a view, not a copy.
	chg := &CHGraph{
		NumNodes:     base.NumNodes,
		NodeLat:      base.NodeLat,
		NodeLon:      base.NodeLon,
		OrigFirstOut: base.OrigFirstOut,
		OrigHead:     base.OrigHead,
		GeoFirstOut:  base.GeoFirstOut,
		GeoShapeLat:  base.GeoShapeLat,
		GeoShapeLon:  base.GeoShapeLon,
	}

	if chg.OrigWeight, err = readUint32Slice(r, int(hdr.NumOrigEdges)); err != nil {
		return nil, fmt.Errorf("read OrigWeight: %w", err)
	}
	// No Rank section — see WriteOverlay.

	if chg.FwdFirstOut, err = readUint32Slice(r, int(hdr.NumNodes+1)); err != nil {
		return nil, fmt.Errorf("read FwdFirstOut: %w", err)
	}
	if chg.FwdHead, err = readUint32Slice(r, int(hdr.NumFwdEdges)); err != nil {
		return nil, fmt.Errorf("read FwdHead: %w", err)
	}
	if chg.FwdWeight, err = readUint32Slice(r, int(hdr.NumFwdEdges)); err != nil {
		return nil, fmt.Errorf("read FwdWeight: %w", err)
	}
	if chg.FwdMiddle, err = readInt32Slice(r, int(hdr.NumFwdEdges)); err != nil {
		return nil, fmt.Errorf("read FwdMiddle: %w", err)
	}
	if chg.BwdFirstOut, err = readUint32Slice(r, int(hdr.NumNodes+1)); err != nil {
		return nil, fmt.Errorf("read BwdFirstOut: %w", err)
	}
	if chg.BwdHead, err = readUint32Slice(r, int(hdr.NumBwdEdges)); err != nil {
		return nil, fmt.Errorf("read BwdHead: %w", err)
	}
	if chg.BwdWeight, err = readUint32Slice(r, int(hdr.NumBwdEdges)); err != nil {
		return nil, fmt.Errorf("read BwdWeight: %w", err)
	}
	if chg.BwdMiddle, err = readInt32Slice(r, int(hdr.NumBwdEdges)); err != nil {
		return nil, fmt.Errorf("read BwdMiddle: %w", err)
	}

	if err := verifyCRC(f, &crcReader); err != nil {
		return nil, err
	}

	if err := validateCSR(chg.FwdFirstOut, chg.FwdHead, hdr.NumNodes); err != nil {
		return nil, fmt.Errorf("forward CSR invalid: %w", err)
	}
	if err := validateCSR(chg.BwdFirstOut, chg.BwdHead, hdr.NumNodes); err != nil {
		return nil, fmt.Errorf("backward CSR invalid: %w", err)
	}
	return chg, nil
}

// writeSplitFile runs body against a CRC-wrapping writer over a temp file, then
// appends the CRC32 trailer and atomically renames into place. Mirrors the
// durability contract of WriteBinary.
func writeSplitFile(path string, body func(w io.Writer) error) error {
	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		f.Close()
		os.Remove(tmpPath) // clean up on error (no-op after a successful rename)
	}()

	crcWriter := crc32Writer{w: f, hash: crc32.NewIEEE()}
	if err := body(&crcWriter); err != nil {
		return err
	}

	if err := binary.Write(f, binary.LittleEndian, crcWriter.hash.Sum32()); err != nil {
		return fmt.Errorf("write CRC32: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// verifyCRC reads the trailing CRC32 from f and compares it to what the reader
// accumulated over the payload.
func verifyCRC(f *os.File, cr *crc32Reader) error {
	expected := cr.hash.Sum32()
	var stored uint32
	if err := binary.Read(f, binary.LittleEndian, &stored); err != nil {
		return fmt.Errorf("read CRC32: %w", err)
	}
	if stored != expected {
		return fmt.Errorf("CRC32 mismatch: stored=%08x computed=%08x", stored, expected)
	}
	return nil
}
