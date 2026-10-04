package tail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A path under a regular file yields ENOTDIR, which is "there but unreadable" rather than NotExist; the offset comes back untouched because the caller decides whether that is terminal.
func TestReadNewDevuelveElErrorCuandoElLogEsIlegible(t *testing.T) {
	fichero := filepath.Join(t.TempDir(), "soy-un-fichero")
	if err := os.WriteFile(fichero, []byte("no soy un directorio"), 0o644); err != nil {
		t.Fatal(err)
	}

	const offset = 4096
	data, nuevo, err := ReadNew(filepath.Join(fichero, "stdout.log"), offset)
	if err == nil {
		t.Fatalf("ReadNew = %q sin error con una ruta debajo de un fichero: un log ilegible se "+
			"confunde con uno que no existe y la consola se queda vacía sin decir por qué", data)
	}
	if data != "" {
		t.Errorf("data = %q con un error de apertura, want vacío", data)
	}
	if nuevo != offset {
		t.Errorf("offset = %d, want %d (el que entró): devolver 0 haría que el siguiente tick "+
			"releyera el log entero desde el principio", nuevo, offset)
	}
}

// A short read is not a failure: the log moved, the service is fine, and an error here would blank the console for one tick.
func TestReadNewSobreUnLogQueCambiaDebajoDevuelveLoQuePudoLeer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("primera linea\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	primero, nuevo, err := ReadNew(path, 0)
	if err != nil {
		t.Fatalf("ReadNew: %v", err)
	}
	if primero != "primera linea\n" {
		t.Fatalf("primera lectura = %q, want %q", primero, "primera linea\n")
	}

	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}

	trasRotacion, nuevo2, err := ReadNew(path, nuevo)
	if err != nil {
		t.Errorf("ReadNew tras truncar el log = %v, want nil: un log que se movió no es un fallo "+
			"del servicio, y un error aquí vacía la consola un tick", err)
	}
	if trasRotacion != "" {
		t.Errorf("data = %q tras truncar a cero, want vacío: el fichero está vacío, y eso es lo "+
			"que hay", trasRotacion)
	}
	if nuevo2 != 0 {
		t.Errorf("offset = %d tras releer un log vacío, want 0", nuevo2)
	}
}

func TestReadNewNoDevuelveMasDeLoQueHay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	const contenido = "linea uno\nlinea dos\n"
	if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}

	data, nuevo, err := ReadNew(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if data != contenido {
		t.Errorf("data = %q, want %q: leer desde el principio tiene que devolver el fichero entero",
			data, contenido)
	}
	if nuevo != int64(len(contenido)) {
		t.Errorf("offset = %d, want %d", nuevo, len(contenido))
	}

	cola, nuevo2, err := ReadNew(path, 9)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(contenido, cola) {
		t.Errorf("cola = %q, want la cola del contenido a partir del offset 9", cola)
	}
	if nuevo2 != int64(len(contenido)) {
		t.Errorf("offset = %d, want %d", nuevo2, len(contenido))
	}
}

// The log becoming a directory is not hypothetical: it gets deleted and something makes a directory in its place, and the next console tick reads it. MEDIDO: on ext4 ReadAt returns EISDIR when it has bytes to ask for and nil when the buffer comes out empty, because a zero-byte read never reaches the disk; an offset at the exact size is not a failure but exactly what a tail is asked for when the log has not grown, so only "never panics, never leaves the offset above" is asserted.
func TestReadNewNoRevientaConUnLogQueEsUnDirectorio(t *testing.T) {
	asDir := filepath.Join(t.TempDir(), "log-es-un-directorio")
	if err := os.MkdirAll(filepath.Join(asDir, "con-contenido"), 0o755); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(asDir)
	if err != nil {
		t.Fatal(err)
	}
	tam := fi.Size()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ReadNew reventó con un log que es un directorio: %v. Un panic aquí se come "+
				"la TUI, y un error de lectura es algo que el tick ya sabe manejar", r)
		}
	}()

	for _, off := range []int64{0, 1, tam, tam + 4096} {
		data, nuevo, err := ReadNew(asDir, off)
		if data != "" {
			t.Errorf("ReadNew(%d) devolvió %q con un directorio, want vacío: no hay log que leer", off, data)
		}
		if nuevo > off {
			t.Errorf("ReadNew(%d) dejó el offset en %d: avanzar sobre una lectura que no ha leído "+
				"nada perdería los bytes que no se leyeron", off, nuevo)
		}
		if off < tam && err == nil {
			t.Errorf("ReadNew(%d) = nil con un directorio y %d bytes que pedir, want EISDIR: un log "+
				"que es un directorio tiene que decir algo, o el servicio parecerá callado", off, tam)
		}
	}
}

func TestReadNewPropagaElFalloDePreguntarElTamaño(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("contenido\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const offset = 3
	data, nuevo, err := readNew(path, offset, func(*os.File) (int64, error) {
		return 0, errors.New("el log se rotó justo antes de preguntar")
	})
	if err == nil {
		t.Fatal("readNew = nil cuando no se puede preguntar el tamaño: el buffer se haría con un " +
			"tamaño inventado")
	}
	if data != "" {
		t.Errorf("data = %q con un fallo al preguntar, want vacío", data)
	}
	if nuevo != offset {
		t.Errorf("offset = %d tras un fallo al preguntar, want %d", nuevo, offset)
	}
}

// Nobody produces a negative offset today, but a signed overflow in offset+n would make size-offset enormous, so the floor must exist even for a hypothetical case.
func TestReadNewConUnOffsetNegativoNoRevienta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	const contenido = "linea\n"
	if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ReadNew reventó con un offset negativo: %v", r)
		}
	}()

	data, nuevo, err := ReadNew(path, -1)
	if err != nil {
		t.Fatalf("ReadNew con offset negativo = %v, want nil: un offset imposible se corrige, no "+
			"se devuelve como error", err)
	}
	if data != contenido {
		t.Errorf("data = %q, want %q: el suelo a 0 hace que se lea el fichero entero", data, contenido)
	}
	if nuevo != int64(len(contenido)) {
		t.Errorf("offset = %d, want %d", nuevo, len(contenido))
	}
}

// MEDIDO: closing the descriptor under Stat makes it fail with EBADF, the state a descriptor is in when its file is deleted and the fd recycled, which happens on a machine with inode churn.
func TestTamanoDeFallaCuandoElDescriptorYaNoSirve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("contenido\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := tamanoDe(f); err == nil {
		t.Error("tamanoDe = nil sobre un descriptor cerrado: un tamaño inventado a partir de un " +
			"fd muerto es un buffer de tamaño arbitrario")
	}
}
