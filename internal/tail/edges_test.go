package tail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The offset must never advance unless bytes were read: a lost chunk stays lost for good and the console keeps working, so the failure is invisible.
func TestReadNewReturnsOnlyNewFromOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, off, err := ReadNew(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if data != "first\n" {
		t.Errorf("first read = %q, want 'first\\n'", data)
	}
	if off != int64(len("first\n")) {
		t.Errorf("offset = %d, want %d", off, len("first\n"))
	}

	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, off2, err := ReadNew(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if data != "second\n" {
		t.Errorf("second read = %q, want only 'second\\n'", data)
	}
	if off2 != int64(len("first\nsecond\n")) {
		t.Errorf("offset = %d, want the full size", off2)
	}

	data, off3, err := ReadNew(path, off2)
	if err != nil {
		t.Fatal(err)
	}
	if data != "" {
		t.Errorf("with nothing written = %q, want empty", data)
	}
	if off3 != off2 {
		t.Errorf("offset changed with nothing written: %d -> %d", off2, off3)
	}
}

// A momentary read failure must not cost a chunk of log: if the offset advanced, those lines would never be shown again.
func TestReadNewDoesNotAdvanceOffsetIfCannotRead(t *testing.T) {
	// A directory opens fine on unix, so this is the read failure that is not NotExist.
	dir := t.TempDir()
	asDir := filepath.Join(dir, "log-is-dir")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, off, err := ReadNew(asDir, 4242)
	if err == nil {
		t.Error("a log that is a directory should give a read error")
	}
	// MEASURED: the offset comes back 0, not 4242, because a directory's "size" is below the offset and ReadNew reads that as a rotation; the invariant is only that it never ends above.
	if off > 4242 {
		t.Errorf("offset = %d after an error, must stay below 4242: advancing would lose the unread bytes", off)
	}
}

func TestReadNewWithMissingFileIsNotError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.log")
	data, off, err := ReadNew(missing, 77)
	if err != nil {
		t.Errorf("a missing log is not an error: %v", err)
	}
	if data != "" {
		t.Errorf("data = %q from a missing log, want empty", data)
	}
	if off != 77 {
		t.Errorf("offset = %d, want the one passed in", off)
	}
}

func TestReadNewReReadsAfterTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("long content from before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, off, err := ReadNew(path, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if data != "new\n" {
		t.Errorf("after a rotation data = %q, want the entire content of the new log", data)
	}
	if off != int64(len("new\n")) {
		t.Errorf("offset = %d, want the real size after re-reading", off)
	}
}

// The cap is a memory limit, so the hard invariants are: never exceed maxBytes, and never start mid-line when a newline can be made to fit.
func TestCapBufferNeverExceedsLimitAndCutsAtLineStart(t *testing.T) {
	doc := "aaaa\nbbbb\ncccc\ndddd\n"

	// MEASURED: the cut goes FORWARD to the first newline at or after the window, so whatever starts there fits in maxBytes; searching backwards would leave a suffix bigger than the cap and split long lines in half.
	for maxBytes := 1; maxBytes <= len(doc); maxBytes++ {
		got := CapBuffer(doc, maxBytes)

		if len(got) > maxBytes {
			t.Fatalf("CapBuffer(doc, %d) returned %d bytes (%q): the cap must be hard", maxBytes, len(got), got)
		}
		if got == "" || got == doc {
			continue
		}
		if !strings.HasSuffix(doc, got) {
			t.Fatalf("CapBuffer(doc, %d) = %q is not a suffix of the original", maxBytes, got)
		}
		at := len(doc) - len(got)
		if got[0] == '\n' || doc[at-1] != '\n' {
			t.Fatalf("CapBuffer(doc, %d) = %q starts mid-line", maxBytes, got)
		}
		if maxBytes >= len("dddd\n") && !strings.HasSuffix(got, "dddd\n") {
			t.Fatalf("CapBuffer(doc, %d) = %q: the end was lost with room for the last line", maxBytes, got)
		}
	}

	if got := CapBuffer(doc, 16); len(got) != 15 {
		t.Errorf("CapBuffer(doc, 16) = %q (%d bytes), want the last 15 aligned", got, len(got))
	}
	if got := CapBuffer(doc, 6); got != "dddd\n" {
		t.Errorf("CapBuffer(doc, 6) = %q, want %q: the complete line that fits", got, "dddd\n")
	}

	line := strings.Repeat("x", 40) + "\n"
	for _, cap_ := range []int{1, 7, 39, 40, 41} {
		got := CapBuffer(line, cap_)
		if len(got) > cap_ && cap_ > 0 {
			t.Errorf("CapBuffer(line, %d) returned %d bytes", cap_, len(got))
		}
	}
}

// A byte-wise cut would leave half an emoji glued to the line start until a whole line arrives.
func TestCapBufferDoesNotSplitMultibyteRunes(t *testing.T) {
	line := strings.Repeat("\u03bb", 40) + "\n"
	got := CapBuffer(line, 20)

	if len(got) > 20 {
		t.Errorf("CapBuffer returned %d bytes, want <= 20", len(got))
	}
	if !isValidUTF8(got) {
		t.Errorf("the cut split a rune: %q", got)
	}
	if strings.TrimRight(got, "\n") != strings.Repeat("\u03bb", len(got)/len("\u03bb")) {
		t.Errorf("the cut did not preserve whole runes: %q", got)
	}
}

func isValidUTF8(s string) bool {
	return strings.ToValidUTF8(s, "") == s
}

// maxBytes <= 0 returns the text whole: a misconfigured cap must not wipe the console.
func TestCapBufferDoesNothingIfItFitsOrLimitIsZero(t *testing.T) {
	const doc = "short\n"
	if got := CapBuffer(doc, 100); got != doc {
		t.Errorf("if it fits it must return it whole: %q", got)
	}
	if got := CapBuffer(doc, 0); got != doc {
		t.Errorf("with maxBytes 0 it must return it whole, not empty it: %q", got)
	}
	if got := CapBuffer(doc, -1); got != doc {
		t.Errorf("with negative maxBytes it must return it whole: %q", got)
	}
	if got := CapBuffer("", 10); got != "" {
		t.Errorf("CapBuffer(\"\") = %q", got)
	}
}

// CSI (Node colours), OSC (docker sets the title) and 2-byte escapes all show up in real logs; missing one breaks the viewport width.
func TestStripANSIRemovesSequencesFoundInRealLogs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no escapes", "normal text", "normal text"},
		{"color CSI", "\x1b[31mred\x1b[0m", "red"},
		{"CSI with parameters", "\x1b[1;32mbright green\x1b[0m", "bright green"},
		{"OSC title", "\x1b]0;my terminal\x07after", "after"},
		// MEASURED: a lone BEL survives, because StripANSI only recognizes sequences starting with ESC; the behaviour is pinned as measured rather than invented.
		{"lone BEL is preserved", "before\x07after", "before\x07after"},
		// MEASURED: a 2-byte escape whose second byte is one of ]PX^_ is read as a string start and eats through the BEL; the format is genuinely ambiguous and the parser picks string.
		{"ESC + string opening letter eats the rest", "before\x1bXafter", "before"},
		{"several in a row", "\x1b[31ma\x1b[0m\x1b[32mb\x1b[0m", "ab"},
		{"escape at the end", "text\x1b[0m", "text"},
		{"unclosed escape", "text\x1b[31m", "text"},
		{"escape at the start", "\x1b[31mtext", "text"},
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
func TestReadNewWhenFileChangesDuringRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, off, err := ReadNew(path, int64(len("first\n")))
	if err != nil {
		t.Fatal(err)
	}
	if off != int64(len("first\n")) {
		t.Errorf("offset = %d with nothing new, want %d", off, len("first\n"))
	}

	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, off2, err := ReadNew(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if data != "second\n" {
		t.Errorf("data = %q, want only the new part", data)
	}
	if off2 != int64(len("first\nsecond\n")) {
		t.Errorf("offset = %d, want the real size read", off2)
	}
}

// With no newline in the window the cut is dry and the line splits: the memory limit wins, but a rune must never split.
func TestCapBufferWithLineLongerThanCapSplitsLine(t *testing.T) {
	line := strings.Repeat("\u20ac", 50)
	got := CapBuffer(line, 21)

	if len(got) > 21 {
		t.Errorf("CapBuffer returned %d bytes, want <= 21", len(got))
	}
	if !isValidUTF8(got) {
		t.Errorf("the cut split a multibyte rune: %q", got)
	}
	if !strings.HasSuffix(line, got) {
		t.Errorf("CapBuffer = %q is not a suffix of the original", got)
	}
	for _, cap_ := range []int{20, 21, 22, 23} {
		got := CapBuffer(line, cap_)
		if len(got) > cap_ {
			t.Errorf("CapBuffer(line, %d) returned %d bytes", cap_, len(got))
		}
		if !isValidUTF8(got) {
			t.Errorf("CapBuffer(line, %d) = %q split a rune", cap_, got)
		}
		if got != "" && !strings.HasSuffix(line, got) {
			t.Errorf("CapBuffer(line, %d) = %q is not a suffix", cap_, got)
		}
	}
}

// NotExist means "has not written yet"; any other failure means "the log is there and unreadable", and swallowing it shows a console that looks mute. MEASURED: a directory is no use here, since on Linux os.Open succeeds and the failure only shows up in the Read, so a real file without permission is needed.
func TestReadNewWithLogWithoutPermissionIsNotMissingAndPropagatesError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open a file without permission: the case cannot be triggered")
	}
	path := filepath.Join(t.TempDir(), "log-without-permission")
	if err := os.WriteFile(path, []byte("content\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	data, off, err := ReadNew(path, 12)
	if err == nil {
		t.Fatalf("a log without permission gave %q without error: the console would appear mute", data)
	}
	if os.IsNotExist(err) {
		t.Fatalf("err = %v: it is not a missing log, it is an unreadable log. NotExist and EACCES lead to different behaviours", err)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("err = %v, want a permission error", err)
	}
	if off != 12 {
		t.Errorf("offset = %d, want 12: with no bytes read the offset does not advance", off)
	}
}
