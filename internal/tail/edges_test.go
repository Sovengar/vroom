package tail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The offset must never advance unless bytes were read: a lost chunk stays lost for good and the console keeps working, so the failure is invisible.
func TestReadNewDevuelveSoloLoNuevoDesdeElOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("primera\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, off, err := ReadNew(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if data != "primera\n" {
		t.Errorf("primera lectura = %q, want 'primera\\n'", data)
	}
	if off != int64(len("primera\n")) {
		t.Errorf("offset = %d, want %d", off, len("primera\n"))
	}

	if err := os.WriteFile(path, []byte("primera\nsegunda\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, off2, err := ReadNew(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if data != "segunda\n" {
		t.Errorf("segunda lectura = %q, want sólo 'segunda\\n'", data)
	}
	if off2 != int64(len("primera\nsegunda\n")) {
		t.Errorf("offset = %d, want el tamaño completo", off2)
	}

	data, off3, err := ReadNew(path, off2)
	if err != nil {
		t.Fatal(err)
	}
	if data != "" {
		t.Errorf("sin escribir nada = %q, want vacío", data)
	}
	if off3 != off2 {
		t.Errorf("el offset cambió sin escribir nada: %d -> %d", off2, off3)
	}
}

// A momentary read failure must not cost a chunk of log: if the offset advanced, those lines would never be shown again.
func TestReadNewNoAvanzaElOffsetSiNoPuedeLeer(t *testing.T) {
	// A directory opens fine on unix, so this is the read failure that is not NotExist.
	dir := t.TempDir()
	asDir := filepath.Join(dir, "log-es-dir")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, off, err := ReadNew(asDir, 4242)
	if err == nil {
		t.Error("un log que es un directorio debería dar error de lectura")
	}
	// MEDIDO: the offset comes back 0, not 4242, because a directory's "size" is below the offset and ReadNew reads that as a rotation; the invariant is only that it never ends above.
	if off > 4242 {
		t.Errorf("offset = %d tras un error, debe quedar por debajo de 4242: avanzar perdería los bytes no leídos", off)
	}
}

func TestReadNewConFicheroAusenteNoEsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-existe.log")
	data, off, err := ReadNew(missing, 77)
	if err != nil {
		t.Errorf("un log ausente no es un error: %v", err)
	}
	if data != "" {
		t.Errorf("data = %q de un log ausente, want vacío", data)
	}
	if off != 77 {
		t.Errorf("offset = %d, want el que se pasó", off)
	}
}

func TestReadNewReleeTrasUnTruncado(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("contenido largo de antes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("nuevo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, off, err := ReadNew(path, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if data != "nuevo\n" {
		t.Errorf("tras una rotación data = %q, want el contenido entero del log nuevo", data)
	}
	if off != int64(len("nuevo\n")) {
		t.Errorf("offset = %d, want el tamaño real tras releer", off)
	}
}

// The cap is a memory limit, so the hard invariants are: never exceed maxBytes, and never start mid-line when a newline can be made to fit.
func TestCapBufferNuncaSuperaElLimiteYCortaEnUnPrincipioDeLinea(t *testing.T) {
	doc := "aaaa\nbbbb\ncccc\ndddd\n"

	// MEDIDO: the cut goes FORWARD to the first newline at or after the window, so whatever starts there fits in maxBytes; searching backwards would leave a suffix bigger than the cap and split long lines in half.
	for maxBytes := 1; maxBytes <= len(doc); maxBytes++ {
		got := CapBuffer(doc, maxBytes)

		if len(got) > maxBytes {
			t.Fatalf("CapBuffer(doc, %d) devolvió %d bytes (%q): el cap tiene que ser duro", maxBytes, len(got), got)
		}
		if got == "" || got == doc {
			continue
		}
		if !strings.HasSuffix(doc, got) {
			t.Fatalf("CapBuffer(doc, %d) = %q no es un sufijo del original", maxBytes, got)
		}
		at := len(doc) - len(got)
		if got[0] == '\n' || doc[at-1] != '\n' {
			t.Fatalf("CapBuffer(doc, %d) = %q empieza a mitad de línea", maxBytes, got)
		}
		if maxBytes >= len("dddd\n") && !strings.HasSuffix(got, "dddd\n") {
			t.Fatalf("CapBuffer(doc, %d) = %q: se perdió el final con sitio para la última línea", maxBytes, got)
		}
	}

	if got := CapBuffer(doc, 16); len(got) != 15 {
		t.Errorf("CapBuffer(doc, 16) = %q (%d bytes), want los últimos 15 alineados", got, len(got))
	}
	if got := CapBuffer(doc, 6); got != "dddd\n" {
		t.Errorf("CapBuffer(doc, 6) = %q, want %q: la línea completa que cabe", got, "dddd\n")
	}

	line := strings.Repeat("x", 40) + "\n"
	for _, cap_ := range []int{1, 7, 39, 40, 41} {
		got := CapBuffer(line, cap_)
		if len(got) > cap_ && cap_ > 0 {
			t.Errorf("CapBuffer(linea, %d) devolvió %d bytes", cap_, len(got))
		}
	}
}

// A byte-wise cut would leave half an emoji glued to the line start until a whole line arrives.
func TestCapBufferNoParteRunesMultibyte(t *testing.T) {
	line := strings.Repeat("é", 40) + "\n"
	got := CapBuffer(line, 20)

	if len(got) > 20 {
		t.Errorf("CapBuffer devolvió %d bytes, want <= 20", len(got))
	}
	if !isValidUTF8(got) {
		t.Errorf("el recorte partió un rune: %q", got)
	}
	if strings.TrimRight(got, "\n") != strings.Repeat("é", len(got)/len("é")) {
		t.Errorf("el recorte no conservó runes enteros: %q", got)
	}
}

func isValidUTF8(s string) bool {
	return strings.ToValidUTF8(s, "") == s
}

// maxBytes <= 0 returns the text whole: a misconfigured cap must not wipe the console.
func TestCapBufferNoHaceNadaSiCabeOElLimiteEsCero(t *testing.T) {
	const doc = "corto\n"
	if got := CapBuffer(doc, 100); got != doc {
		t.Errorf("si cabe tiene que devolverlo entero: %q", got)
	}
	if got := CapBuffer(doc, 0); got != doc {
		t.Errorf("con maxBytes 0 tiene que devolverlo entero, no vaciarlo: %q", got)
	}
	if got := CapBuffer(doc, -1); got != doc {
		t.Errorf("con maxBytes negativo tiene que devolverlo entero: %q", got)
	}
	if got := CapBuffer("", 10); got != "" {
		t.Errorf("CapBuffer(\"\") = %q", got)
	}
}

// CSI (Node colours), OSC (docker sets the title) and 2-byte escapes all show up in real logs; missing one breaks the viewport width.
func TestStripANSIQuitaLasSecuenciasQueAparecenEnLogsReales(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"sin escapes", "texto normal", "texto normal"},
		{"CSI de color", "\x1b[31mrojo\x1b[0m", "rojo"},
		{"CSI con parámetros", "\x1b[1;32mverde brillo\x1b[0m", "verde brillo"},
		{"OSC de titulo", "\x1b]0;mi terminal\x07despues", "despues"},
		// MEDIDO: a lone BEL survives, because StripANSI only recognizes sequences starting with ESC; the behaviour is pinned as measured rather than invented.
		{"BEL suelto se conserva", "antes\x07despues", "antes\x07despues"},
		// MEDIDO: a 2-byte escape whose second byte is one of ]PX^_ is read as a string start and eats through the BEL; the format is genuinely ambiguous and the parser picks string.
		{"ESC + letra de apertura de cadena se come el resto", "antes\x1bXdespues", "antes"},
		{"varios seguidos", "\x1b[31ma\x1b[0m\x1b[32mb\x1b[0m", "ab"},
		{"escape al final", "texto\x1b[0m", "texto"},
		{"escape sin cerrar", "texto\x1b[31m", "texto"},
		{"escape al principio", "\x1b[31mtexto", "texto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.in); got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The returned offset is what was read, not the size at open time: with busy logs the latter would skip forever everything written in between.
func TestReadNewCuandoElFicheroCambiaDuranteLaLectura(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("primera\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, off, err := ReadNew(path, int64(len("primera\n")))
	if err != nil {
		t.Fatal(err)
	}
	if off != int64(len("primera\n")) {
		t.Errorf("offset = %d sin nada nuevo, want %d", off, len("primera\n"))
	}

	if err := os.WriteFile(path, []byte("primera\nsegunda\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, off2, err := ReadNew(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if data != "segunda\n" {
		t.Errorf("data = %q, want sólo lo nuevo", data)
	}
	if off2 != int64(len("primera\nsegunda\n")) {
		t.Errorf("offset = %d, want el tamaño real leído", off2)
	}
}

// With no newline in the window the cut is dry and the line splits: the memory limit wins, but a rune must never split.
func TestCapBufferConUnaLineaMasLargaQueElCapParteLaLinea(t *testing.T) {
	line := strings.Repeat("€", 50)
	got := CapBuffer(line, 21)

	if len(got) > 21 {
		t.Errorf("CapBuffer devolvió %d bytes, want <= 21", len(got))
	}
	if !isValidUTF8(got) {
		t.Errorf("el recorte partió un rune multibyte: %q", got)
	}
	if !strings.HasSuffix(line, got) {
		t.Errorf("CapBuffer = %q no es un sufijo del original", got)
	}
	for _, cap_ := range []int{20, 21, 22, 23} {
		got := CapBuffer(line, cap_)
		if len(got) > cap_ {
			t.Errorf("CapBuffer(linea, %d) devolvió %d bytes", cap_, len(got))
		}
		if !isValidUTF8(got) {
			t.Errorf("CapBuffer(linea, %d) = %q partió un rune", cap_, got)
		}
		if got != "" && !strings.HasSuffix(line, got) {
			t.Errorf("CapBuffer(linea, %d) = %q no es un sufijo", cap_, got)
		}
	}
}

// NotExist means "has not written yet"; any other failure means "the log is there and unreadable", and swallowing it shows a console that looks mute. MEDIDO: a directory is no use here, since on Linux os.Open succeeds and the failure only shows up in the Read, so a real file without permission is needed.
func TestReadNewConUnLogSinPermisoNoEsAusenteYPropagaElError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede abrir un fichero sin permiso: el caso no se puede provocar")
	}
	path := filepath.Join(t.TempDir(), "log-sin-permiso")
	if err := os.WriteFile(path, []byte("contenido\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	data, off, err := ReadNew(path, 12)
	if err == nil {
		t.Fatalf("un log sin permiso dio %q sin error: la consola parecería muda", data)
	}
	if os.IsNotExist(err) {
		t.Fatalf("err = %v: no es un log ausente, es un log ilegible. NotExist y EACCES conducen a comportamientos distintos", err)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("err = %v, want un error de permiso", err)
	}
	if off != 12 {
		t.Errorf("offset = %d, want 12: sin bytes leídos el offset no avanza", off)
	}
}
