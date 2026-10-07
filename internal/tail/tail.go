// Package tail streams only the new bytes of a log file for the live console, stripping ANSI so the viewport keeps its real width.
package tail

import (
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// A missing log is not an error: the service has not started yet, so the console simply gets nothing.
func ReadNew(path string, offset int64) (data string, newOffset int64, err error) {
	return readNew(path, offset, sizeOf)
}

// Injected because its error is reachable: between Open and Stat the log can rotate away, so the failure needs to be testable instead of an inline Stat.
func sizeOf(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// MEASURED (bug): Seek(0, io.SeekEnd) on a DIRECTORY descriptor reports ~2^63 bytes on ext4 and panicked inside make, while Stat reports a small size and lets ReadAt fail with EISDIR.
func readNew(path string, offset int64, sizeFn func(*os.File) (int64, error)) (string, int64, error) {
	// The negative clamp runs before Open because every exit returns the caller's offset untouched, so clamping only the read path would break that contract.
	if offset < 0 {
		offset = 0
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", offset, nil
		}
		return "", offset, err
	}
	defer func() { _ = f.Close() }()

	size, err := sizeFn(f)
	if err != nil {
		return "", offset, err
	}
	// A file smaller than the offset means rotation or truncation, so reread it whole.
	if size < offset {
		offset = 0
	}
	buf := make([]byte, max(size-offset, 0))

	// ReadAt, not Read, so the descriptor position never matters; io.EOF is the normal end (including a rotation met mid-read), while EISDIR on a log that is a directory is a real error.
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", offset, err
	}
	return string(buf[:n]), offset + int64(n), nil
}

// Service logs carry color that the viewport does not interpret, so unstripped escapes break the width.
func StripANSI(s string) string {
	if !strings.ContainsRune(s, '\x1b') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\x1b' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7E) {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		if i+1 < len(s) && strings.IndexByte("]PX^_", s[i+1]) >= 0 {
			i += 2
			for i < len(s) {
				if s[i] == '\x07' {
					i++
					break
				}
				if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
			continue
		}
		i += 2
	}
	return b.String()
}

func CapBuffer(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	// The cut moves FORWARD on purpose: any line starting at or after cut fits in maxBytes, whereas searching backwards can only split a long line in half.
	cut := len(s) - maxBytes
	if nl := strings.IndexByte(s[cut:], '\n'); nl >= 0 {
		cut = cut + nl + 1
		return s[cut:]
	}
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}
