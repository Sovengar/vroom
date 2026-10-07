package tail

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// A sweep is the cheap proof that the invariant holds for every offset, including math.MinInt64 and overflow results, where a handful of fixed offsets proves nothing; the offsets are random on purpose and the seed is fixed, because an unseeded rand would make a failure show up only sometimes in CI.
func TestReadNewSobreviveAlBarridoDeOffsets(t *testing.T) {
	dir := t.TempDir()

	casos := []string{
		filepath.Join(dir, "fichero"),
		filepath.Join(dir, "directorio"),
		filepath.Join(dir, "no-existe"),
		filepath.Join(dir, "fichero", "dentro-de-otro"),
	}
	if err := os.WriteFile(casos[0], []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(casos[1], "x"), 0o755); err != nil {
		t.Fatal(err)
	}

	offsets := []int64{0, 1, 10, 11, 1 << 20, math.MaxInt64, math.MinInt64, math.MinInt64 + 1}
	r := rand.New(rand.NewSource(1))
	for range 500 {
		offsets = append(offsets, r.Int63()-r.Int63())
	}

	for _, ruta := range casos {
		for _, off := range offsets {
			data, nuevo, err := ReadNew(ruta, off)
			// The sweep asserts only "no panic" plus the two universal invariants below; the offset contract lives in the focused tests, since a rotation deliberately lowers it and a negative offset is clamped back to 0.
			if nuevo < 0 {
				t.Errorf("ReadNew(%q, %d) returned a NEGATIVE offset (%d)", ruta, off, nuevo)
			}
			if ruta == casos[0] && len(data) > 10 {
				t.Errorf("ReadNew(%q, %d) returned %d bytes from a 10-byte file", ruta, off, len(data))
			}
			_ = err
		}
	}
}
