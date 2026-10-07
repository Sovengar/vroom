package orchestrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// A helper outliving the suite is not cosmetic: this package starts real services, so a live helper holds its process and its port for hours (it happened twice here, both times a cleanup called StopStack with a stage-less stack, which stops nothing); matching is by EXECUTABLE through /proc/<pid>/exe rather than a machine-global `pgrep -f` that could kill another run; and these helpers ARE this binary, so without the env-var and ancestor guards their TestMain would run the guard, find the process that launched them and kill it.

const helperEnvVar = "VROOM_NOPORT_HELPER"

func leakedTestBinaries() []int {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return nil
	}

	me := os.Getpid()
	myGroup, _ := syscall.Getpgid(0)

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var leaked []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() || pid == me {
			continue
		}
		if grp, err := syscall.Getpgid(pid); err == nil && grp == myGroup {
			continue
		}
		if isAncestor(pid, me) {
			continue
		}
		if isTestBinary("/proc", self, pid) {
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

func TestMain(m *testing.M) {
	code := m.Run()

	if os.Getenv(helperEnvVar) != "" {
		os.Exit(code) // a helper, not the guard
	}

	leaked := leakedTestBinaries()
	if len(leaked) == 0 {
		os.Exit(code)
	}
	for _, pid := range leaked {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	fmt.Fprintf(os.Stderr,
		"\nHYGIENE: %d process(es) of this binary survived the suite: %v\n"+
			"Check the cleanups: StopStack with a stage-less stack stops nothing.\n",
		len(leaked), leaked)
	os.Exit(1)
}

// Split out so it can be tested against a process the test owns and controls, with no spawn and no shell.
func isTestBinary(root, self string, pid int) bool {
	exe, err := os.Readlink(filepath.Join(root, strconv.Itoa(pid), "exe"))
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return false
	}
	return resolved == self
}

// It fails instead of returning an empty string: a guard that finds nothing looks like a guard that passes, which is worse than not having one.
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

// Checked against the test's own process: deterministic, no spawn, no shell, no dependency on the machine's /bin/sh; the pid 1 negative control is what stops an always-true matcher from passing the positive assertion.
func TestHygieneGuardMatchesOwnBinary(t *testing.T) {
	self := testBinaryPath(t)

	if !isTestBinary("/proc", self, os.Getpid()) {
		t.Errorf("the guard must recognize its own binary (pid %d)", os.Getpid())
	}
	if isTestBinary("/proc", self, 1) {
		t.Error("pid 1 does not run this binary: the matcher does not discriminate")
	}
	if isTestBinary("/proc", self, 999999999) {
		t.Error("a nonexistent pid cannot be this binary")
	}
	if containsInt(leakedTestBinaries(), os.Getpid()) {
		t.Error("the guard cannot list itself: it would kill itself at exit")
	}
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
