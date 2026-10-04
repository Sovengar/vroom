package tail

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// TestReadNewSobreviveAlBarridoDeOffsets: que no haya panic, para NINGÚN offset.
//
// El panic que había en CI venía de `make([]byte, size-offset)` con un `size` que no
// era un tamaño. Los tests de al lado lo fijan con cuatro offsets, y cuatro son los que
// se me ocurren mientras escribo un test; un barrido es la forma barata de comprobar
// que la invariante se sostiene para todos, incluidos `math.MinInt64` y los que salen
// de un desbordamiento.
//
// Los offsets son aleatorios A PROPÓSITO y la semilla es fija: un `rand` sin semilla
// haría que un fallo sólo apareciera a veces en CI, que es la peor forma de test que
// existe.
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
			// El barrido comprueba UNA cosa: que no hay panic. El contrato del offset
			// lo fija `TestReadNewNoAvanzaElOffsetSiNoPuedeLeer` y el del directorio
			// el test de al lado, y aquí ninguna de las dos cosas podría expresarse sin
			// volverse confuso: tras una rotación el offset baja a propósito, y con un
			// offset negativo el suelo a 0 lo mueve otra vez.
			//
			// Lo único que sí es invariante aquí es que el offset nunca sea negativo y
			// que no se devuelva más de lo que el fichero tiene, porque eso lo decide
			// el `size` que preguntamos por `Stat`.
			if nuevo < 0 {
				t.Errorf("ReadNew(%q, %d) devolvió un offset NEGATIVO (%d)", ruta, off, nuevo)
			}
			if ruta == casos[0] && len(data) > 10 {
				t.Errorf("ReadNew(%q, %d) devolvió %d bytes de un fichero de 10", ruta, off, len(data))
			}
			_ = err
		}
	}
}
