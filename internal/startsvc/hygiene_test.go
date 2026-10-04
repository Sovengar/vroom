package startsvc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// The helpers of this package ARE this same binary, so their TestMain would run this guard, find the process that launched them and kill it: helpers declare themselves with this variable and the guard never touches its own ancestors.
const helperEnvVar = "VROOM_START_HELPER"

// Matched by executable, never by command line: a pgrep -f is machine-global and would blame or kill another run.
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

// A helper that outlives the suite holds a reserved port slot for good: every leak costs one and only a TUI restart recovers it, which is what a TestHelperService surviving 4h08m once cost.
func TestMain(m *testing.M) {
	code := m.Run()

	if os.Getenv(helperEnvVar) != "" {
		os.Exit(code)
	}

	leaked := leakedTestBinaries()
	if len(leaked) == 0 {
		os.Exit(code)
	}
	for _, pid := range leaked {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	fmt.Fprintf(os.Stderr,
		"\nHYGIENE: %d proceso(s) de este binario sobrevivieron a la suite: %v\n"+
			"Algo que este paquete arranca no se está parando.\n",
		len(leaked), leaked)
	os.Exit(1)
}

// The guard's only real logic, factored out so a test can exercise it against a process it owns, with no spawn and no shell.
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

// Fails instead of returning an empty string: a guard that cannot resolve its binary finds nothing and looks like a passing guard, which is worse than no guard.
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

// pid 1 is the negative control that keeps an always-true isTestBinary from passing the positive assertion.
func TestHygieneGuardMatchesOwnBinary(t *testing.T) {
	self := testBinaryPath(t)

	if !isTestBinary("/proc", self, os.Getpid()) {
		t.Errorf("el guard tiene que reconocer su propio binario (pid %d)", os.Getpid())
	}
	if isTestBinary("/proc", self, 1) {
		t.Error("pid 1 no ejecuta este binario: el emparejamiento no discrimina")
	}
	if isTestBinary("/proc", self, 999999999) {
		t.Error("un pid inexistente no puede ser este binario")
	}
	if containsInt(leakedTestBinaries(), os.Getpid()) {
		t.Error("el guard no puede listarse a sí mismo: se mataría al terminar")
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
