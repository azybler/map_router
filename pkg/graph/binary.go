package graph

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"unsafe"
)

const (
	magicBytes = "MPROUTER"
	version    = uint32(2) // v2: added original graph edges for snapping
	maxNodes   = 10_000_000
	maxEdges   = 50_000_000
)

// fileHeader is the binary header.
type fileHeader struct {
	Magic        [8]byte
	Version      uint32
	NumNodes     uint32
	NumOrigEdges uint32 // original graph edge count (for snapping R-tree)
	NumShortcuts uint32
	NumFwdEdges  uint32
	NumBwdEdges  uint32
}

// WriteBinary serializes a CHResult to a binary file.
// Uses unsafe.Slice for fast zero-copy I/O.
func WriteBinary(path string, chg *CHGraph) error {
	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		f.Close()
		os.Remove(tmpPath) // clean up on error
	}()

	crcWriter := crc32Writer{w: f, hash: crc32.NewIEEE()}
	w := &crcWriter

	numFwdEdges := uint32(len(chg.FwdHead))
	numBwdEdges := uint32(len(chg.BwdHead))
	numOrigEdges := uint32(len(chg.OrigHead))

	// Count shortcut edges in overlay.
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

	// Write header.
	hdr := fileHeader{
		Version:      version,
		NumNodes:     chg.NumNodes,
		NumOrigEdges: numOrigEdges,
		NumShortcuts: numShortcuts,
		NumFwdEdges:  numFwdEdges,
		NumBwdEdges:  numBwdEdges,
	}
	copy(hdr.Magic[:], magicBytes)
	if err := binary.Write(w, binary.LittleEndian, &hdr); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	// Node data.
	if err := writeFloat64Slice(w, chg.NodeLat); err != nil {
		return fmt.Errorf("write NodeLat: %w", err)
	}
	if err := writeFloat64Slice(w, chg.NodeLon); err != nil {
		return fmt.Errorf("write NodeLon: %w", err)
	}
	if err := writeUint32Slice(w, chg.Rank); err != nil {
		return fmt.Errorf("write Rank: %w", err)
	}

	// Forward upward graph.
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

	// Backward upward graph.
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

	// Original graph edges (for snapping R-tree).
	if err := writeUint32Slice(w, chg.OrigFirstOut); err != nil {
		return fmt.Errorf("write OrigFirstOut: %w", err)
	}
	if err := writeUint32Slice(w, chg.OrigHead); err != nil {
		return fmt.Errorf("write OrigHead: %w", err)
	}
	if err := writeUint32Slice(w, chg.OrigWeight); err != nil {
		return fmt.Errorf("write OrigWeight: %w", err)
	}

	// Geometry (length-prefixed for variable-size arrays).
	if err := writeLenPrefixedUint32(w, chg.GeoFirstOut); err != nil {
		return fmt.Errorf("write GeoFirstOut: %w", err)
	}
	if err := writeLenPrefixedFloat64(w, chg.GeoShapeLat); err != nil {
		return fmt.Errorf("write GeoShapeLat: %w", err)
	}
	if err := writeLenPrefixedFloat64(w, chg.GeoShapeLon); err != nil {
		return fmt.Errorf("write GeoShapeLon: %w", err)
	}

	// Write CRC32 trailer.
	checksum := crcWriter.hash.Sum32()
	if err := binary.Write(f, binary.LittleEndian, checksum); err != nil {
		return fmt.Errorf("write CRC32: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// ReadBinary deserializes a CHResult from a binary file.
func ReadBinary(path string) (*CHGraph, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	crcReader := crc32Reader{r: f, hash: crc32.NewIEEE()}
	r := &crcReader

	// Read and validate header.
	var hdr fileHeader
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	if string(hdr.Magic[:]) != magicBytes {
		return nil, fmt.Errorf("invalid magic bytes: %q", hdr.Magic)
	}
	if hdr.Version != version {
		return nil, fmt.Errorf("unsupported version: %d", hdr.Version)
	}
	if hdr.NumNodes > maxNodes {
		return nil, fmt.Errorf("NumNodes %d exceeds limit %d", hdr.NumNodes, maxNodes)
	}
	if hdr.NumFwdEdges > maxEdges || hdr.NumBwdEdges > maxEdges {
		return nil, fmt.Errorf("edge count exceeds limit %d", maxEdges)
	}

	result := &CHGraph{NumNodes: hdr.NumNodes}

	// Node data.
	if result.NodeLat, err = readFloat64Slice(r, int(hdr.NumNodes)); err != nil {
		return nil, fmt.Errorf("read NodeLat: %w", err)
	}
	if result.NodeLon, err = readFloat64Slice(r, int(hdr.NumNodes)); err != nil {
		return nil, fmt.Errorf("read NodeLon: %w", err)
	}
	// Skip Rank (only used during preprocessing, not at query time).
	if err := skipBytes(r, int(hdr.NumNodes)*4); err != nil {
		return nil, fmt.Errorf("skip Rank: %w", err)
	}

	// Forward upward graph.
	if result.FwdFirstOut, err = readUint32Slice(r, int(hdr.NumNodes+1)); err != nil {
		return nil, fmt.Errorf("read FwdFirstOut: %w", err)
	}
	if result.FwdHead, err = readUint32Slice(r, int(hdr.NumFwdEdges)); err != nil {
		return nil, fmt.Errorf("read FwdHead: %w", err)
	}
	if result.FwdWeight, err = readUint32Slice(r, int(hdr.NumFwdEdges)); err != nil {
		return nil, fmt.Errorf("read FwdWeight: %w", err)
	}
	if result.FwdMiddle, err = readInt32Slice(r, int(hdr.NumFwdEdges)); err != nil {
		return nil, fmt.Errorf("read FwdMiddle: %w", err)
	}

	// Backward upward graph.
	if result.BwdFirstOut, err = readUint32Slice(r, int(hdr.NumNodes+1)); err != nil {
		return nil, fmt.Errorf("read BwdFirstOut: %w", err)
	}
	if result.BwdHead, err = readUint32Slice(r, int(hdr.NumBwdEdges)); err != nil {
		return nil, fmt.Errorf("read BwdHead: %w", err)
	}
	if result.BwdWeight, err = readUint32Slice(r, int(hdr.NumBwdEdges)); err != nil {
		return nil, fmt.Errorf("read BwdWeight: %w", err)
	}
	if result.BwdMiddle, err = readInt32Slice(r, int(hdr.NumBwdEdges)); err != nil {
		return nil, fmt.Errorf("read BwdMiddle: %w", err)
	}

	// Original graph edges (for snapping R-tree).
	if result.OrigFirstOut, err = readUint32Slice(r, int(hdr.NumNodes+1)); err != nil {
		return nil, fmt.Errorf("read OrigFirstOut: %w", err)
	}
	if result.OrigHead, err = readUint32Slice(r, int(hdr.NumOrigEdges)); err != nil {
		return nil, fmt.Errorf("read OrigHead: %w", err)
	}
	if result.OrigWeight, err = readUint32Slice(r, int(hdr.NumOrigEdges)); err != nil {
		return nil, fmt.Errorf("read OrigWeight: %w", err)
	}

	// Geometry (length-prefixed, optional for small test graphs).
	result.GeoFirstOut, _ = readUint32SliceOptional(r)
	result.GeoShapeLat, _ = readFloat64SliceOptional(r)
	result.GeoShapeLon, _ = readFloat64SliceOptional(r)

	// Read and validate CRC32.
	expectedCRC := crcReader.hash.Sum32()
	var storedCRC uint32
	if err := binary.Read(f, binary.LittleEndian, &storedCRC); err != nil {
		return nil, fmt.Errorf("read CRC32: %w", err)
	}
	if storedCRC != expectedCRC {
		return nil, fmt.Errorf("CRC32 mismatch: stored=%08x computed=%08x", storedCRC, expectedCRC)
	}

	// Validate CSR invariants.
	if err := validateCSR(result.FwdFirstOut, result.FwdHead, hdr.NumNodes); err != nil {
		return nil, fmt.Errorf("forward CSR invalid: %w", err)
	}
	if err := validateCSR(result.BwdFirstOut, result.BwdHead, hdr.NumNodes); err != nil {
		return nil, fmt.Errorf("backward CSR invalid: %w", err)
	}

	return result, nil
}

// validateCSR checks CSR invariants.
func validateCSR(firstOut, head []uint32, numNodes uint32) error {
	if uint32(len(firstOut)) != numNodes+1 {
		return fmt.Errorf("FirstOut length %d != NumNodes+1 %d", len(firstOut), numNodes+1)
	}
	numEdges := firstOut[numNodes]
	if uint32(len(head)) != numEdges {
		return fmt.Errorf("Head length %d != FirstOut[NumNodes] %d", len(head), numEdges)
	}
	for i := uint32(1); i <= numNodes; i++ {
		if firstOut[i] < firstOut[i-1] {
			return fmt.Errorf("FirstOut not monotonic at %d: %d < %d", i, firstOut[i], firstOut[i-1])
		}
	}
	for i, h := range head {
		if h >= numNodes {
			return fmt.Errorf("Head[%d]=%d >= NumNodes=%d", i, h, numNodes)
		}
	}
	return nil
}

// skipBytes reads and discards n bytes from r.
// Used to skip fields that are written for format compatibility but not needed at runtime.
func skipBytes(r io.Reader, n int) error {
	var buf [32 * 1024]byte
	for n > 0 {
		toRead := min(n, len(buf))
		if _, err := io.ReadFull(r, buf[:toRead]); err != nil {
			return err
		}
		n -= toRead
	}
	return nil
}

// Zero-copy I/O helpers using unsafe.Slice.

func writeUint32Slice(w io.Writer, s []uint32) error {
	if len(s) == 0 {
		return nil
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*4)
	_, err := w.Write(b)
	return err
}

func writeInt32Slice(w io.Writer, s []int32) error {
	if len(s) == 0 {
		return nil
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*4)
	_, err := w.Write(b)
	return err
}

func writeFloat64Slice(w io.Writer, s []float64) error {
	if len(s) == 0 {
		return nil
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*8)
	_, err := w.Write(b)
	return err
}

func readUint32Slice(r io.Reader, n int) ([]uint32, error) {
	if n == 0 {
		return nil, nil
	}
	s := make([]uint32, n)
	b := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), n*4)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return s, nil
}

func readInt32Slice(r io.Reader, n int) ([]int32, error) {
	if n == 0 {
		return nil, nil
	}
	s := make([]int32, n)
	b := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), n*4)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return s, nil
}

func readFloat64Slice(r io.Reader, n int) ([]float64, error) {
	if n == 0 {
		return nil, nil
	}
	s := make([]float64, n)
	b := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), n*8)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return s, nil
}

func writeLenPrefixedUint32(w io.Writer, s []uint32) error {
	n := uint32(len(s))
	if err := binary.Write(w, binary.LittleEndian, n); err != nil {
		return err
	}
	return writeUint32Slice(w, s)
}

func writeLenPrefixedFloat64(w io.Writer, s []float64) error {
	n := uint32(len(s))
	if err := binary.Write(w, binary.LittleEndian, n); err != nil {
		return err
	}
	return writeFloat64Slice(w, s)
}

// readUint32SliceOptional reads a uint32 length prefix then the slice data.
// Returns nil, nil if at EOF or data unavailable.
func readUint32SliceOptional(r io.Reader) ([]uint32, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, nil // EOF or error — geometry is optional
	}
	if n == 0 || n > math.MaxUint32/4 {
		return nil, nil
	}
	return readUint32Slice(r, int(n))
}

func readFloat64SliceOptional(r io.Reader) ([]float64, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, nil
	}
	if n == 0 || n > math.MaxUint32/8 {
		return nil, nil
	}
	return readFloat64Slice(r, int(n))
}

// CRC32 wrapping writers/readers.

type crc32Writer struct {
	w    io.Writer
	hash crc32Hash
}

type crc32Hash interface {
	Write([]byte) (int, error)
	Sum32() uint32
}

func (cw *crc32Writer) Write(p []byte) (int, error) {
	cw.hash.Write(p)
	return cw.w.Write(p)
}

type crc32Reader struct {
	r    io.Reader
	hash crc32Hash
}

func (cr *crc32Reader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.hash.Write(p[:n])
	}
	return n, err
}
