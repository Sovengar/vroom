package cli

import (
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"vroom/internal/process"
	"vroom/internal/state"
)

// Commands here run real against a real tree on a real disk with no scanner, store or manager doubles, because a double would only test the double; only the env is substituted so the developer's state is untouched.

var errWrite = errors.New("could not write")

// Returning the store is what lets a test check the on-disk effect, because a command can claim anything about itself.
func chdirTree(t *testing.T, root string) *state.Store {
	t.Helper()
	t.Chdir(root)
	store, err := state.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A missing file returns "" because the log of a service that never started is absent, which is not a test failure.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	writeFile(t, path, strings.Join(lines, "\n")+"\n")
}

func linesOf(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// Reads /proc instead of asking the manager because the interesting question is whether the PID really exists, not what a double would say.
func processAlive(t *testing.T, pid int) bool {
	t.Helper()
	if pid <= 0 {
		return false
	}
	if _, err := os.Stat(procDir(pid)); err != nil {
		return false
	}
	// A zombie still owns /proc/<pid> but its state is Z, so it is not alive.
	data, err := os.ReadFile(procDir(pid, "stat"))
	if err != nil {
		return false
	}
	return !strings.Contains(string(data), ") Z ")
}

func procDir(pid int, parts ...string) string {
	base := "/proc/" + itoa(pid)
	for _, p := range parts {
		base += "/" + p
	}
	return base
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// The timeout is not decorative: process.Stop escalates SIGTERM to SIGKILL and the kernel is slow to reap the zombie, so without the wait liveness checks flake.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(t, pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("process %d still alive after stop: waitGone timed out", pid)
}

// Tests that start something must stop it, or every sleep 30 leaves a zombie process accumulating on the runner.
func stopService(t *testing.T, store *state.Store, path string) {
	t.Helper()
	if err := stopCleanup(store, process.NewManager(), path); err != nil {
		t.Fatalf("service cleanup %s: %v", path, err)
	}
}

// Records Stop specs without touching processes, so a test can assert the Manager contract without paying for a real kill.
type aliveManager struct {
	stopped []process.StopSpec
	warned  int
	killErr error
}

func (m *aliveManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}

func (m *aliveManager) Stop(spec process.StopSpec) error {
	m.stopped = append(m.stopped, spec)
	if m.killErr != nil {
		return m.killErr
	}
	// Emits the same warning a real kill would, so the code path writing warnings into the log is exercised by whoever uses this double.
	if spec.Warn != nil {
		spec.Warn("stopped pid %d", spec.Pid)
		m.warned++
	}
	return nil
}

func (m *aliveManager) Evaluate(process.EvalSpec) process.Status {
	return process.StatusStopped
}

var _ process.Manager = (*aliveManager)(nil)

// The service the fixture starts is this same test binary re-executed as a child: nothing weaker proves the declared port is
// bound by the service's own process. A test-held listener cannot stand in, because by_port's preflight (correctly) refuses an
// occupied port, and a child that never binds (a bare sleep) makes Evaluate report unknown instead of running.
const (
	cliHelperEnv     = "VROOM_CLI_HELPER"
	cliHelperPortEnv = "VROOM_CLI_HELPER_PORT"
)

// Not a test: this is the service listeningService starts, and the -test.run guard keeps the parent process out of helper mode.
func TestCLIHelperService(t *testing.T) {
	if os.Getenv(cliHelperEnv) == "" || flag.Lookup("test.run").Value.String() != "^TestCLIHelperService$" {
		t.Skip("helper process, not a test")
	}
	port, err := strconv.Atoi(os.Getenv(cliHelperPortEnv))
	if err != nil || port == 0 {
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		os.Exit(3)
	}
	defer func() { _ = ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_ = c.Close()
	}
}

// listeningService is the only fixture whose child really binds the declared port: by_port's preflight sees it free before
// spawning, and Evaluate later confirms running against the child's listener, which is the only path to already_running.
func listeningService(t *testing.T, root, dir string, extraTOML string) int {
	t.Helper()
	port := freePort(t)
	t.Setenv(cliHelperEnv, "hold")
	body := "commands.start.run = \"" + cliHelperPortEnv + "=" + itoa(port) + " " + shellQuote(os.Args[0]) + " -test.run=^TestCLIHelperService$\"\nport = " + itoa(port) + "\nurl_generation = \"by_port\"\n" + extraTOML
	writeFile(t, filepath.Join(root, dir, ".vroom.toml"), body)
	return port
}

// The child binds asynchronously after cmdStart returns, so whoever reads the service state waits for the real listener.
func waitPortOpen(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if process.PortOpen(port) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("port %d never opened: the helper service did not bind it", port)
}

// Closed before returning: the preflight must see the port free, and only the child binds it.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Always fails, standing in for a closed stdout or a broken pipe.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

// Separate from installCLIReleaser because tests that must read what was released need the concrete double, while tests that only need a failure do not.
func installCLIFailingReleaser(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { cliReleaseStub, cliReleaseStubInstalled = nil, false })
	cliReleaseStub = (&failingReleaser{}).RemoveAbsent
	cliReleaseStubInstalled = true
}

// Used where the code expects a file: OpenFile with O_CREATE on a directory fails with EISDIR, which is reproducible without depending on the uid.
func makeDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
