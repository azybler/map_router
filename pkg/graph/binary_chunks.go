package graph

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// Optional trailing chunks.
//
// The combined and overlay formats each end with a CRC32 over everything before
// it, and their readers stop reading there. Data added to a format later is
// written AFTER that trailer, as self-describing chunks:
//
//	[8]byte  tag
//	uint64   payload length in bytes
//	payload
//	uint32   CRC32 of tag, length and payload
//
// A reader that predates a chunk stops at the core CRC and never sees it, so a
// new chunk keeps every older reader working: graph files and the binaries that
// load them can be deployed in either order. A reader skips chunks whose tag it
// does not know.

// penaltiesTag marks the chunk holding a graph's EdgePenalties. Payload:
//
//	uint32          count
//	[count]uint32   Edges (strictly ascending)
//	[count]uint32   Unpenalized
//
// It is written whenever the graph has Penalties, even with count 0, so a
// reader can tell "nothing penalized" from "written before penalties were
// recorded".
const penaltiesTag = "MPRPENAL"

// maxChunkBytes bounds a chunk's declared length, guarding against a corrupt one.
const maxChunkBytes = 1 << 31

// chunk is one optional trailing chunk to write.
type chunk struct {
	tag     string // exactly 8 bytes
	payload []byte
}

// graphChunks returns the trailing chunks a CHGraph's file should carry.
func graphChunks(chg *CHGraph) []chunk {
	if chg.Penalties == nil {
		return nil
	}
	return []chunk{{tag: penaltiesTag, payload: encodePenalties(chg.Penalties)}}
}

// writeChunks writes chunks after a file's core CRC trailer.
func writeChunks(w io.Writer, chunks []chunk) error {
	for _, c := range chunks {
		if len(c.tag) != 8 {
			return fmt.Errorf("chunk tag %q is not 8 bytes", c.tag)
		}
		h := crc32.NewIEEE()
		cw := crc32Writer{w: w, hash: h}
		if _, err := io.WriteString(&cw, c.tag); err != nil {
			return fmt.Errorf("write chunk %s: %w", c.tag, err)
		}
		if err := binary.Write(&cw, binary.LittleEndian, uint64(len(c.payload))); err != nil {
			return fmt.Errorf("write chunk %s: %w", c.tag, err)
		}
		if _, err := cw.Write(c.payload); err != nil {
			return fmt.Errorf("write chunk %s: %w", c.tag, err)
		}
		if err := binary.Write(w, binary.LittleEndian, h.Sum32()); err != nil {
			return fmt.Errorf("write chunk %s CRC32: %w", c.tag, err)
		}
	}
	return nil
}

// readChunks reads the chunks after a file's core CRC trailer up to EOF and
// applies the known ones to chg. A chunk that is truncated, fails its CRC or
// does not validate is an error, like any other corruption.
func readChunks(r io.Reader, chg *CHGraph) error {
	for {
		var tag [8]byte
		if _, err := io.ReadFull(r, tag[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil // no (more) chunks
			}
			return fmt.Errorf("read chunk tag: %w", err)
		}
		h := crc32.NewIEEE()
		h.Write(tag[:])
		cr := crc32Reader{r: r, hash: h}

		var n uint64
		if err := binary.Read(&cr, binary.LittleEndian, &n); err != nil {
			return fmt.Errorf("read chunk %q length: %w", tag, err)
		}
		if n > maxChunkBytes {
			return fmt.Errorf("chunk %q length %d exceeds limit %d", tag, n, maxChunkBytes)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(&cr, payload); err != nil {
			return fmt.Errorf("read chunk %q: %w", tag, err)
		}
		var stored uint32
		if err := binary.Read(r, binary.LittleEndian, &stored); err != nil {
			return fmt.Errorf("read chunk %q CRC32: %w", tag, err)
		}
		if got := h.Sum32(); stored != got {
			return fmt.Errorf("chunk %q CRC32 mismatch: stored=%08x computed=%08x", tag, stored, got)
		}

		switch string(tag[:]) {
		case penaltiesTag:
			p, err := decodePenalties(payload, chg.OrigWeight)
			if err != nil {
				return fmt.Errorf("chunk %q: %w", tag, err)
			}
			chg.Penalties = p
		default:
			// Written by a newer version: not ours to interpret.
		}
	}
}

func encodePenalties(p *EdgePenalties) []byte {
	n := len(p.Edges)
	b := make([]byte, 0, 4+8*n)
	b = binary.LittleEndian.AppendUint32(b, uint32(n))
	for _, e := range p.Edges {
		b = binary.LittleEndian.AppendUint32(b, e)
	}
	for _, w := range p.Unpenalized {
		b = binary.LittleEndian.AppendUint32(b, w)
	}
	return b
}

// decodePenalties parses a penalties payload and checks it against the graph's
// original edge weights: every listed edge must exist, be listed once in
// ascending order, and weigh no more without its penalty than with it.
func decodePenalties(b []byte, origWeight []uint32) (*EdgePenalties, error) {
	if len(b) < 4 {
		return nil, fmt.Errorf("penalties payload too short (%d bytes)", len(b))
	}
	n := int(binary.LittleEndian.Uint32(b))
	if len(b) != 4+8*n {
		return nil, fmt.Errorf("penalties payload is %d bytes, want %d for %d edges", len(b), 4+8*n, n)
	}
	p := &EdgePenalties{Edges: make([]uint32, n), Unpenalized: make([]uint32, n)}
	for i := range n {
		p.Edges[i] = binary.LittleEndian.Uint32(b[4+4*i:])
		p.Unpenalized[i] = binary.LittleEndian.Uint32(b[4+4*n+4*i:])
	}
	for i, e := range p.Edges {
		if int(e) >= len(origWeight) {
			return nil, fmt.Errorf("penalized edge %d out of range (%d edges)", e, len(origWeight))
		}
		if i > 0 && e <= p.Edges[i-1] {
			return nil, fmt.Errorf("penalized edges not strictly ascending at %d", i)
		}
		if p.Unpenalized[i] > origWeight[e] {
			return nil, fmt.Errorf("edge %d: unpenalized weight %d exceeds its weight %d", e, p.Unpenalized[i], origWeight[e])
		}
	}
	return p, nil
}
