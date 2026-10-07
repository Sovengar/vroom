package tail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadNewIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("line1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, off, err := ReadNew(path, 0)
	if err != nil || data != "line1\n" || off != 6 {
		t.Fatalf("first read: data=%q off=%d err=%v", data, off, err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("line2\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	data, off, err = ReadNew(path, off)
	if err != nil || data != "line2\n" || off != 12 {
		t.Fatalf("incremental read: data=%q off=%d err=%v", data, off, err)
	}

	data, _, err = ReadNew(path, off)
	if err != nil || data != "" {
		t.Fatalf("no growth must return empty: data=%q err=%v", data, err)
	}
}

func TestReadNewMissingFile(t *testing.T) {
	data, _, err := ReadNew(filepath.Join(t.TempDir(), "nope.log"), 0)
	if err != nil || data != "" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

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
		t.Fatalf("after truncate: data=%q off=%d err=%v", data, off2, err)
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"no escapes", "plain log line\n", "plain log line\n"},
		{"color CSI", "\x1b[32mOK\x1b[0m started\n", "OK started\n"},
		{"CSI with parameters", "\x1b[1;31;40mbold red\x1b[m end", "bold red end"},
		{"OSC title", "\x1b]0;window title\x07rest", "rest"},
		{"OSC with ST", "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"2-byte escape", "a\x1bM b", "a b"},
		{"incomplete CSI at the end", "text\x1b[32", "text"},
		{"multibyte preserved", "cafe \u03bb\r\x1b[K", "cafe \u03bb\r"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.in); got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCapBuffer(t *testing.T) {
	if got := CapBuffer("short", 64); got != "short" {
		t.Errorf("below the cap it must not touch: %q", got)
	}

	big := strings.Repeat("line\n", 30) // 150 bytes
	got := CapBuffer(big, 60)
	if len(got) > 60 {
		t.Errorf("cap exceeded: %d", len(got))
	}
	if !strings.HasSuffix(big, got) {
		t.Error("cap must preserve the end")
	}
	if !strings.HasPrefix(got, "line\n") {
		t.Errorf("cut must be by complete line: %q", got)
	}

	one := strings.Repeat("x", 100) + "\u03bb"
	got = CapBuffer(one, 50)
	if got != one[52:] {
		t.Errorf("single line: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Error("must not split a multibyte rune")
	}
}
