//go:build unix

package logrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A cancelled command_pre_start must not orphan its shell: RunContext puts the hook in its own process group and reaps the whole tree, so the sh child AND the background job it started are gone when RunContext returns. Without that, the shell would outlive the host process holding the service log fds.
func TestRunContextReapsTheHookProcessGroupWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.log")
	errLog := filepath.Join(dir, "err.log")
	pidPath := filepath.Join(dir, "child.pid")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Let the shell spawn and record its background job, then cancel mid-hook.
	time.AfterFunc(300*time.Millisecond, cancel)

	start := time.Now()
	_, _, err := RunContext(ctx, "pre_start", "sleep 60 & echo $! > "+pidPath+"; wait", dir, out, errLog)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a cancelled hook must return an error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("RunContext took %v after cancellation: it must not wait out the hook", elapsed)
	}
	if got := readFile(t, out); !strings.Contains(got, "── vroom ▶ pre_start:") {
		t.Errorf("stdout log = %q, want the banner of the hook that was cut", got)
	}

	pid := readPid(t, pidPath)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // ESRCH: the group was reaped
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the hook's background child %d survived the cancellation (orphaned)", pid)
}

// The nil-process guard is the Cancel path when the shell never started (an already-done context): it must be a no-op, not a nil dereference.
func TestKillGroupIsANoOpWithoutAProcess(t *testing.T) {
	if err := killGroup(&exec.Cmd{}); err != nil {
		t.Errorf("killGroup without a process = %v, want nil", err)
	}
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	var data []byte
	var err error
	for range 100 {
		data, err = os.ReadFile(path)
		if err == nil && len(data) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the hook never recorded its background pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad pid %q: %v", data, err)
	}
	return pid
}
