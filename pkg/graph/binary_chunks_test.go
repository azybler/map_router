package graph_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/azybler/map_router/pkg/graph"
)

// penalizedTestCH is buildTestCH with two of its original edges penalized.
func penalizedTestCH(t *testing.T) *graph.CHGraph {
	t.Helper()
	chg := buildTestCH(t)
	chg.Penalties = &graph.EdgePenalties{
		Edges:       []uint32{1, 4},
		Unpenalized: []uint32{chg.OrigWeight[1] / 2, chg.OrigWeight[4] / 50},
	}
	return chg
}

// graphFormats writes and reads back a CHGraph in each on-disk format.
var graphFormats = map[string]struct {
	write func(t *testing.T, dir string, chg *graph.CHGraph) string // returns the file holding the penalties
	read  func(t *testing.T, dir string) (*graph.CHGraph, error)
}{
	"combined": {
		write: func(t *testing.T, dir string, chg *graph.CHGraph) string {
			path := filepath.Join(dir, "g.bin")
			if err := graph.WriteBinary(path, chg); err != nil {
				t.Fatalf("WriteBinary: %v", err)
			}
			return path
		},
		read: func(t *testing.T, dir string) (*graph.CHGraph, error) {
			return graph.ReadBinary(filepath.Join(dir, "g.bin"))
		},
	},
	"split": {
		write: func(t *testing.T, dir string, chg *graph.CHGraph) string {
			if err := graph.WriteBase(filepath.Join(dir, "g.base.bin"), chg); err != nil {
				t.Fatalf("WriteBase: %v", err)
			}
			path := filepath.Join(dir, "g.overlay.bin")
			if err := graph.WriteOverlay(path, chg); err != nil {
				t.Fatalf("WriteOverlay: %v", err)
			}
			return path
		},
		read: func(t *testing.T, dir string) (*graph.CHGraph, error) {
			base, err := graph.ReadBase(filepath.Join(dir, "g.base.bin"))
			if err != nil {
				t.Fatalf("ReadBase: %v", err)
			}
			return graph.ReadOverlay(filepath.Join(dir, "g.overlay.bin"), base)
		},
	},
}

func TestPenaltiesRoundTrip(t *testing.T) {
	cases := map[string]*graph.EdgePenalties{
		"not recorded": nil,
		"none":         {},
		"two edges":    penalizedTestCH(t).Penalties,
	}
	for format, f := range graphFormats {
		for name, p := range cases {
			t.Run(format+"/"+name, func(t *testing.T) {
				chg := buildTestCH(t)
				chg.Penalties = p
				dir := t.TempDir()
				f.write(t, dir, chg)
				got, err := f.read(t, dir)
				if err != nil {
					t.Fatalf("read: %v", err)
				}
				switch {
				case p == nil:
					if got.Penalties != nil {
						t.Errorf("Penalties = %+v, want nil (none were recorded)", got.Penalties)
					}
				case got.Penalties == nil:
					t.Fatal("Penalties = nil, want the recorded set")
				case len(p.Edges) == 0:
					if len(got.Penalties.Edges) != 0 {
						t.Errorf("Penalties = %+v, want empty", got.Penalties)
					}
				case !reflect.DeepEqual(got.Penalties, p):
					t.Errorf("Penalties = %+v, want %+v", got.Penalties, p)
				}
			})
		}
	}
}

// TestPenaltiesInvisibleToOlderReaders: a file with penalties is byte-for-byte
// the file without them plus trailing data. Readers from before chunks stop at
// the core CRC, so they load it exactly as before — which is what lets new
// graph files go out ahead of the binaries that understand them.
func TestPenaltiesInvisibleToOlderReaders(t *testing.T) {
	for format, f := range graphFormats {
		t.Run(format, func(t *testing.T) {
			plain, err := os.ReadFile(f.write(t, t.TempDir(), buildTestCH(t)))
			if err != nil {
				t.Fatal(err)
			}
			withPen, err := os.ReadFile(f.write(t, t.TempDir(), penalizedTestCH(t)))
			if err != nil {
				t.Fatal(err)
			}
			if len(withPen) <= len(plain) || !bytes.HasPrefix(withPen, plain) {
				t.Errorf("file with penalties (%d bytes) does not extend the plain file (%d bytes)", len(withPen), len(plain))
			}
		})
	}
}

// appendChunk appends a well-formed trailing chunk to the file at path.
func appendChunk(t *testing.T, path, tag string, payload []byte) {
	t.Helper()
	var b bytes.Buffer
	b.WriteString(tag)
	binary.Write(&b, binary.LittleEndian, uint64(len(payload)))
	b.Write(payload)
	binary.Write(&b, binary.LittleEndian, crc32.ChecksumIEEE(b.Bytes()))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(b.Bytes()); err != nil {
		t.Fatal(err)
	}
}

// TestUnknownChunkSkipped: a chunk from a newer writer must not stop this reader.
func TestUnknownChunkSkipped(t *testing.T) {
	for format, f := range graphFormats {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			want := penalizedTestCH(t)
			path := f.write(t, dir, want)
			appendChunk(t, path, "MPRFUTUR", []byte("whatever a later version stores"))
			got, err := f.read(t, dir)
			if err != nil {
				t.Fatalf("read with an unknown chunk: %v", err)
			}
			if !reflect.DeepEqual(got.Penalties, want.Penalties) {
				t.Errorf("Penalties = %+v, want %+v", got.Penalties, want.Penalties)
			}
		})
	}
}

// TestBadChunkRejected: a damaged or inconsistent chunk fails the load like any
// other corruption, rather than silently reporting penalized travel times.
func TestBadChunkRejected(t *testing.T) {
	damage := map[string]func(t *testing.T, path string){
		"flipped byte": func(t *testing.T, path string) {
			b, _ := os.ReadFile(path)
			b[len(b)-6] ^= 0xFF // inside the penalties payload
			os.WriteFile(path, b, 0o644)
		},
		"truncated": func(t *testing.T, path string) {
			b, _ := os.ReadFile(path)
			os.WriteFile(path, b[:len(b)-3], 0o644)
		},
		"stray bytes": func(t *testing.T, path string) {
			appendChunk(t, path, "MPRFUTUR", nil)
			b, _ := os.ReadFile(path)
			os.WriteFile(path, append(b, 'M', 'P', 'R'), 0o644)
		},
	}
	for format, f := range graphFormats {
		for name, fn := range damage {
			t.Run(format+"/"+name, func(t *testing.T) {
				dir := t.TempDir()
				fn(t, f.write(t, dir, penalizedTestCH(t)))
				if _, err := f.read(t, dir); err == nil {
					t.Error("expected the damaged chunk to fail the load")
				}
			})
		}
	}
}

func TestInconsistentPenaltiesRejected(t *testing.T) {
	cases := map[string]func(p *graph.EdgePenalties, origWeight []uint32){
		"heavier without penalty": func(p *graph.EdgePenalties, w []uint32) { p.Unpenalized[0] = w[p.Edges[0]] + 1 },
		"edge out of range":       func(p *graph.EdgePenalties, w []uint32) { p.Edges[1] = uint32(len(w)) },
		"not ascending":           func(p *graph.EdgePenalties, w []uint32) { p.Edges[0], p.Edges[1] = p.Edges[1], p.Edges[0] },
	}
	for format, f := range graphFormats {
		for name, corrupt := range cases {
			t.Run(format+"/"+name, func(t *testing.T) {
				chg := penalizedTestCH(t)
				corrupt(chg.Penalties, chg.OrigWeight)
				dir := t.TempDir()
				f.write(t, dir, chg)
				if _, err := f.read(t, dir); err == nil {
					t.Error("expected inconsistent penalties to fail the load")
				}
			})
		}
	}
}

func TestUnpenalizedWeight(t *testing.T) {
	p := &graph.EdgePenalties{Edges: []uint32{3, 9}, Unpenalized: []uint32{30, 90}}
	for _, c := range []struct{ e, w, want uint32 }{{3, 1500, 30}, {9, 4500, 90}, {4, 77, 77}, {0, 5, 5}} {
		if got := p.UnpenalizedWeight(c.e, c.w); got != c.want {
			t.Errorf("UnpenalizedWeight(%d, %d) = %d, want %d", c.e, c.w, got, c.want)
		}
	}
	var none *graph.EdgePenalties
	if got := none.UnpenalizedWeight(3, 1500); got != 1500 {
		t.Errorf("nil penalties: UnpenalizedWeight = %d, want the weight itself", got)
	}
}
