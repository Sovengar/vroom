package tail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// R19/S19.1: ReadNew devuelve solo los bytes nuevos desde el offset.
func TestReadNewIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("line1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, off, err := ReadNew(path, 0)
	if err != nil || data != "line1\n" || off != 6 {
		t.Fatalf("primera lectura: data=%q off=%d err=%v", data, off, err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("line2\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	data, off, err = ReadNew(path, off)
	if err != nil || data != "line2\n" || off != 12 {
		t.Fatalf("lectura incremental: data=%q off=%d err=%v", data, off, err)
	}

	data, _, err = ReadNew(path, off)
	if err != nil || data != "" {
		t.Fatalf("sin crecimiento debe devolver vacío: data=%q err=%v", data, err)
	}
}

// ReadNew con fichero inexistente: vacío sin error (servicio sin arrancar).
func TestReadNewMissingFile(t *testing.T) {
	data, _, err := ReadNew(filepath.Join(t.TempDir(), "nope.log"), 0)
	if err != nil || data != "" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

// ReadNew tras truncado/rotación: relee completo (offset reset).
func TestReadNewAfterTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	if err := os.WriteFile(path, []byte("aaaa\nbbbb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, off, err := ReadNew(path, 0)
	if err != nil || off != 10 {
		t.Fatalf("off=%d err=%v", off, err)
	}
	if err := os.WriteFile(path, []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, off2, err := ReadNew(path, off)
	if err != nil || data != "c\n" || off2 != 2 {
		t.Fatalf("tras truncado: data=%q off=%d err=%v", data, off2, err)
	}
}

// S19.5: strip de ANSI en logs con color.
func TestStripANSI(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"sin escapes", "plain log line\n", "plain log line\n"},
		{"color CSI", "\x1b[32mOK\x1b[0m started\n", "OK started\n"},
		{"CSI con parámetros", "\x1b[1;31;40mbold red\x1b[m end", "bold red end"},
		{"OSC title", "\x1b]0;window title\x07rest", "rest"},
		{"OSC con ST", "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"escape 2 bytes", "a\x1bM b", "a b"},
		{"CSI incompleto al final", "text\x1b[32", "text"},
		{"multibyte preservado", "año ñ\r\x1b[K", "año ñ\r"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.in); got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// S19.6: cap del buffer conservando el final y cortando por líneas.
func TestCapBuffer(t *testing.T) {
	if got := CapBuffer("short", 64); got != "short" {
		t.Errorf("bajo el cap no debe tocar: %q", got)
	}

	big := strings.Repeat("line\n", 30) // 150 bytes
	got := CapBuffer(big, 60)
	if len(got) > 60 {
		t.Errorf("cap excedido: %d", len(got))
	}
	if !strings.HasSuffix(big, got) {
		t.Error("cap debe conservar el final")
	}
	if !strings.HasPrefix(got, "line\n") {
		t.Errorf("corte debe ser por línea completa: %q", got)
	}

	// Línea única enorme sin \n: recorta conservando runes válidos.
	one := strings.Repeat("x", 100) + "ñ"
	got = CapBuffer(one, 50)
	if got != one[52:] {
		t.Errorf("línea única: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Error("no debe partir un rune multibyte")
	}
}
