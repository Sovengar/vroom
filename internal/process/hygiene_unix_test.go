//go:build unix

package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A leaked helper is not cosmetic: it holds a reserve-range port permanently (measured: one TestHelperService lived 4h08m holding 41999), which is why the match is by EXECUTABLE and not by command line - a pgrep -f would be machine-global and could blame another run.

// Fails instead of returning an empty string, because a guard that finds nothing looks like a guard that passes.
func testBinaryPath(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", self, err)
	}
	return resolved
}

// Split out so it can be tested against a process the test owns and controls, with no spawn and no shell.
func isTestBinary(root, self string, pid int) bool {
	exe, err := os.Readlink(filepath.Join(root, strconv.Itoa(pid), "exe"))
	if err != nil {
		return false // no longer exists, or is not ours
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return false
	}
	return resolved == self
}

func leakedTestBinaries() []int {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return nil
	}

	// The test binary matches the criterion by definition, so exclude ourselves or the guard kills itself on exit.
	me := os.Getpid()
	myGroup, _ := syscall.Getpgid(0)

	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var leaked []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() || pid == me {
			continue
		}
		// Nor the rest of our process group: that is the runner and any test child, not a leak.
		if grp, err := syscall.Getpgid(pid); err == nil && grp == myGroup {
			continue
		}
		// Nor our ancestors: a helper is this same executable, so without this the guard can kill whoever launched it - which is exactly what happened the first time.
		if isAncestor(pid, me) {
			continue
		}
		if isTestBinary(procRoot, self, pid) {
			leaked = append(leaked, pid)
		}
	}
	return leaked
}

func isAncestor(candidate, pid int) bool {
	for range 64 {
		ppid := syscall.Getppid()
		if ppid <= 1 {
			return false
		}
		if ppid == candidate {
			return true
		}
		if ppid == pid {
			return false
		}
	}
	return false
}

const helperEnvVar = "VROOM_TEST_HELPER"

func TestMain(m *testing.M) {
	code := m.Run()

	if os.Getenv(helperEnvVar) != "" {
		os.Exit(code) // I am a helper, not the guard
	}

	leaked := leakedTestBinaries()
	if len(leaked) == 0 {
		os.Exit(code)
	}
	// Cleaned up anyway: a guard that also leaves the leak in place is worse than no guard.
	for _, pid := range leaked {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	fmt.Fprintf(os.Stderr,
		"\nHYGIENE: %d process(es) of this test binary survived the suite: %v\n"+
			"Someone is not stopping what they start, and those processes hold ports from the range.\n",
		len(leaked), leaked)
	os.Exit(1)
}

// Regression: the previous version raced a sh -c helper and used the wrapper's pid, which only works if the shell execs - on CI /bin/sh is dash and it timed out with no other trace.
func TestHygieneGuardMatchesOwnBinary(t *testing.T) {
	self := testBinaryPath(t)

	if !isTestBinary(procRoot, self, os.Getpid()) {
		t.Errorf("the guard has to recognize its own binary (pid %d)", os.Getpid())
	}

	// Negative control: without it an isTestBinary that always returned true would pass the assertion above.
	if isTestBinary(procRoot, self, 1) {
		t.Error("pid 1 does not run this binary: the matching does not discriminate")
	}

	if isTestBinary(procRoot, self, 999999999) {
		t.Error("a non-existent pid cannot be this binary")
	}

	if containsInt(leakedTestBinaries(), os.Getpid()) {
		t.Error("the guard cannot list itself: it would kill itself on exit")
	}
}

// Launched WITHOUT a shell so the pid is unambiguously ours and the assertion does not depend on sh exec-ing.
func TestHygieneGuardFindsSpawnedHelper(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}

	before := len(leakedTestBinaries())

	pid := spawnTestHelperDirectly(t)
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})

	waitFor(t, 10*time.Second, "helper visible to the guard", func() bool {
		return containsInt(leakedTestBinaries(), pid)
	})

	if got := len(leakedTestBinaries()); got != before+1 {
		t.Errorf("the guard saw %d leaks, expected %d: the discovery does not work", got, before+1)
	}
	if !strings.Contains(os.Args[0], "test") {
		t.Errorf("unexpected test path: %q", os.Args[0])
	}
}

func spawnTestHelperDirectly(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperListener$")
	cmd.Env = append(os.Environ(), helperEnvVar+"=listener", "VROOM_TEST_PORT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("direct helper: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
