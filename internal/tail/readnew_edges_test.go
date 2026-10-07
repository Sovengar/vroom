package tail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A path under a regular file yields ENOTDIR, which is "there but unreadable" rather than NotExist; the offset comes back untouched because the caller decides whether that is terminal.
func TestReadNewReturnsErrorWhenLogIsUnreadable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "i-am-a-file")
	if err := os.WriteFile(file, []byte("i am not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	const offset = 4096
	data, newOff, err := ReadNew(filepath.Join(file, "stdout.log"), offset)
	if err == nil {
		t.Fatalf("ReadNew = %q without error on a path under a file: an unreadable log is "+
			"confused with a non-existent one and the console stays blank without saying why", data)
	}
	if data != "" {
		t.Errorf("data = %q with an open error, want empty", data)
	}
	if newOff != offset {
		t.Errorf("offset = %d, want %d (the one passed in): returning 0 would make the next tick "+
			"replay the entire log from the beginning", newOff, offset)
	}
}

// A short read is not a failure: the log moved, the service is fine, and an error here would blank the console for one tick.
func TestReadNewOnLogThatChangesUnderneathReturnsWhatItCouldRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("first line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, newOff, err := ReadNew(path, 0)
	if err != nil {
		t.Fatalf("ReadNew: %v", err)
	}
	if first != "first line\n" {
		t.Fatalf("first read = %q, want %q", first, "first line\n")
	}

	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}

	afterRotation, newOff2, err := ReadNew(path, newOff)
	if err != nil {
		t.Errorf("ReadNew after truncating the log = %v, want nil: a log that moved is not a "+
			"service failure, and an error here blanks the console for one tick", err)
	}
	if afterRotation != "" {
		t.Errorf("data = %q after truncating to zero, want empty: the file is empty, and that is "+
			"what there is", afterRotation)
	}
	if newOff2 != 0 {
		t.Errorf("offset = %d after re-reading an empty log, want 0", newOff2)
	}
}

func TestReadNewDoesNotReturnMoreThanWhatIsThere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	const content = "line one\nline two\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	data, newOff, err := ReadNew(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if data != content {
		t.Errorf("data = %q, want %q: reading from the start must return the entire file",
			data, content)
	}
	if newOff != int64(len(content)) {
		t.Errorf("offset = %d, want %d", newOff, len(content))
	}

	tail, newOff2, err := ReadNew(path, 9)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(content, tail) {
		t.Errorf("tail = %q, want the tail of the content starting at offset 9", tail)
	}
	if newOff2 != int64(len(content)) {
		t.Errorf("offset = %d, want %d", newOff2, len(content))
	}
}

// The log becoming a directory is not hypothetical: it gets deleted and something makes a directory in its place, and the next console tick reads it. MEASURED: on ext4 ReadAt returns EISDIR when it has bytes to ask for and nil when the buffer comes out empty, because a zero-byte read never reaches the disk; an offset at the exact size is not a failure but exactly what a tail is asked for when the log has not grown, so only "never panics, never leaves the offset above" is asserted.
func TestReadNewDoesNotPanicWithALogThatIsADirectory(t *testing.T) {
	asDir := filepath.Join(t.TempDir(), "log-is-a-directory")
	if err := os.MkdirAll(filepath.Join(asDir, "with-content"), 0o755); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(asDir)
	if err != nil {
		t.Fatal(err)
	}
	size := fi.Size()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ReadNew panicked with a log that is a directory: %v. A panic here takes "+
				"down the TUI, and a read error is something the tick already knows how to handle", r)
		}
	}()

	for _, off := range []int64{0, 1, size, size + 4096} {
		data, newOff, err := ReadNew(asDir, off)
		if data != "" {
			t.Errorf("ReadNew(%d) returned %q with a directory, want empty: there is no log to read", off, data)
		}
		if newOff > off {
			t.Errorf("ReadNew(%d) left the offset at %d: advancing over a read that read "+
				"nothing would lose the bytes that were not read", off, newOff)
		}
		if off < size && err == nil {
			t.Errorf("ReadNew(%d) = nil with a directory and %d bytes to ask for, want EISDIR: a "+
				"log that is a directory must say something, or the service will appear silent", off, size)
		}
	}
}

func TestReadNewPropagatesFailureOfAskingSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const offset = 3
	data, newOff, err := readNew(path, offset, func(*os.File) (int64, error) {
		return 0, errors.New("the log rotated right before asking")
	})
	if err == nil {
		t.Fatal("readNew = nil when the size cannot be asked: the buffer would be made with an " +
			"invented size")
	}
	if data != "" {
		t.Errorf("data = %q with a failure on asking, want empty", data)
	}
	if newOff != offset {
		t.Errorf("offset = %d after a failure on asking, want %d", newOff, offset)
	}
}

// Nobody produces a negative offset today, but a signed overflow in offset+n would make size-offset enormous, so the floor must exist even for a hypothetical case.
func TestReadNewWithNegativeOffsetDoesNotPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	const content = "line\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ReadNew panicked with a negative offset: %v", r)
		}
	}()

	data, newOff, err := ReadNew(path, -1)
	if err != nil {
		t.Fatalf("ReadNew with negative offset = %v, want nil: an impossible offset is corrected, "+
			"not returned as an error", err)
	}
	if data != content {
		t.Errorf("data = %q, want %q: the floor at 0 makes it read the entire file", data, content)
	}
	if newOff != int64(len(content)) {
		t.Errorf("offset = %d, want %d", newOff, len(content))
	}
}

// MEASURED: closing the descriptor under Stat makes it fail with EBADF, the state a descriptor is in when its file is deleted and the fd recycled, which happens on a machine with inode churn.
func TestSizeFailsWhenDescriptorIsNoLongerValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := sizeOf(f); err == nil {
		t.Error("sizeOf = nil on a closed descriptor: an invented size from a " +
			"dead fd is a buffer of arbitrary size")
	}
}
