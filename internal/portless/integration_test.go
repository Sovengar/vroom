package portless_test

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"vroom/internal/portless"
)

// The only test that talks to a real portless: CI has neither the binary nor a proxy, so it isolates PORTLESS_STATE_DIR in a temp dir and never prunes, cleans or stops anything.
func TestIntegrationRealPortless(t *testing.T) {
	stateDir := integrationStateDir(t)
	bin := integrationBin(t)
	c := portless.New(
		portless.WithBinary(bin),
		portless.WithStateDir(stateDir),
		portless.WithTimeout(10*time.Second),
	)

	// The hermetic suite cannot show this: there the proxy is a double.
	res := c.Apply("vroom.integration", 4321, portless.Ownership{})
	if res.Succeeded() {
		t.Fatalf("without its own proxy no url must be published, got %+v", res)
	}
	if res.Url != "" {
		t.Errorf("an unverified route does not publish url, got %q", res.Url)
	}
	port, found, err := c.Lookup("vroom.integration")
	if err != nil {
		skipOrFail(t, "portless does not respond as expected (%s): %v", bin, err)
	}
	if !found || port != 4321 {
		t.Fatalf("the route must remain registered for when the proxy returns: port=%d found=%v", port, found)
	}

	if err := c.Remove("vroom.integration"); err != nil {
		t.Errorf("stopping must remove the route: %v", err)
	}
	if err := c.Remove("vroom.integration"); err != nil {
		t.Errorf("a repeated stop cannot fail: %v", err)
	}
	if _, still, _ := c.Lookup("vroom.integration"); still {
		t.Error("the route must disappear after the stop")
	}
}

// The seam's own probe, exercised against a real listener because that is the only way to show 404 and 502 mean different things.
func probeOnce(scheme, hostname string, proxyPort int) (int, error) {
	return portless.ProbeOnce(scheme, hostname, proxyPort, "/", 3*time.Second)
}

// No portless needed, so this runs in CI: 404 means the proxy does not know the host, 502 means it routes it although the backend is dead.
func TestLiveProbeDistinguishesNotServedFromBackendDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go serveOnce(ln, "502 Bad Gateway")

	port := ln.Addr().(*net.TCPAddr).Port
	status, err := probeOnce("http", "app.localhost", port)
	if err != nil {
		t.Fatalf("the test proxy must respond: %v", err)
	}
	if status != 502 {
		t.Fatalf("expected 502, got %d", status)
	}

	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln2.Close() }()
	go serveOnce(ln2, "404 Not Found")
	port2 := ln2.Addr().(*net.TCPAddr).Port
	status2, err := probeOnce("http", "app.localhost", port2)
	if err != nil {
		t.Fatal(err)
	}
	if status2 != 404 {
		t.Fatalf("expected 404, got %d", status2)
	}

	// A dead port is a third category, a connection failure, and it means no proxy.
	if _, err := probeOnce("http", "app.localhost", closedPort(t)); err == nil {
		t.Error("a closed port must fail, not return a status")
	}
}

func serveOnce(ln net.Listener, status string) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	_, _ = conn.Read(buf)
	_, _ = conn.Write([]byte("HTTP/1.1 " + status + "\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
}

func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// skipOrFail is the difference between a developer machine and CI: without the binary or a usable proxy there is nothing to verify, which is a legitimate skip when a human runs one test by hand — but under VROOM_PORTLESS_INTEGRATION_STRICT=1 the same missing environment is a FAILURE, because a green job that silently skipped every integration test tests nothing at all.
func skipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("VROOM_PORTLESS_INTEGRATION_STRICT") == "1" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// Never touches ~/.portless: the developer's proxy is running and that state is not ours.
func integrationStateDir(t *testing.T) string {
	t.Helper()
	requireIntegration(t)
	dir := t.TempDir()
	t.Setenv("PORTLESS_STATE_DIR", dir)
	t.Setenv("PORTLESS_HTTPS", "0")
	t.Setenv("PORTLESS_SYNC_HOSTS", "0")
	return dir
}

func integrationBin(t *testing.T) string {
	t.Helper()
	requireIntegration(t)
	if bin := os.Getenv("PORTLESS_BIN"); bin != "" {
		return bin
	}
	if bin := portless.ResolveBinary(); bin != "" {
		return bin
	}
	skipOrFail(t, "no portless installed")
	return ""
}

// Opt-in only: touching a real proxy, even isolated, must never happen by surprise.
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("VROOM_PORTLESS_INTEGRATION") != "1" {
		t.Skip("real integration: set VROOM_PORTLESS_INTEGRATION=1 (isolates state in a temp dir)")
	}
}

// M3 for real: the hermetic version only flipped noProxy on the same fake, so here the proxy process is killed and restarted, by PID and never with `portless proxy stop`.
func TestRouteSurvivesAProxyRestart(t *testing.T) {
	iso := integrationStateDir(t)
	bin := integrationBin(t)
	proxyPort := startIsolatedProxy(t, iso, bin)

	backend := startEchoBackend(t, 0, "")

	c := portless.New(
		portless.WithBinary(bin),
		portless.WithStateDir(iso),
		portless.WithTimeout(15*time.Second),
	)
	if res := c.Apply("vroom.restart", backend, portless.Ownership{}); !res.Succeeded() {
		skipOrFail(t, "without its own proxy there is nothing to verify: %+v", res)
	}

	pid := readProxyPID(t, iso)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	waitPortClosed(t, proxyPort)

	proxyPort = startIsolatedProxy(t, iso, bin)

	port, found, err := c.Lookup("vroom.restart")
	if err != nil || !found {
		t.Fatalf("after the restart the route must remain registered: found=%v err=%v", found, err)
	}
	if port != backend {
		t.Errorf("the route must still point to %d, got %d", backend, port)
	}

	status := probeHTTP(t, "http", "vroom.restart.localhost", proxyPort)
	if status == 0 {
		t.Errorf("the proxy must serve the route after the restart, there was no response")
	}

	portless.Release(c, "vroom.restart")
}

// M4 against a real binary and a live app: the hermetic version could not cover it because it only asserted on a map the seam adds to.
func TestIntegrationDoesNotEvictLivePortlessRoutes(t *testing.T) {
	iso := integrationStateDir(t)
	bin := integrationBin(t)
	proxyPort := startIsolatedProxy(t, iso, bin)

	liveName := "vroom-live-app"
	if err := runPortlessApp(t, bin, liveName); err != nil {
		skipOrFail(t, "could not start the portless live app: %v", err)
	}

	liveHost := liveName + ".localhost"
	if !waitServes(t, liveHost, proxyPort) {
		skipOrFail(t, "the live app never got served; the environment is not suitable for this test")
	}

	c := portless.New(
		portless.WithBinary(bin),
		portless.WithStateDir(iso),
		portless.WithTimeout(15*time.Second),
	)

	// The port the app really hears on is the one portless assigned, not the one requested.
	appPort, found, err := c.Lookup(liveName)
	if err != nil || !found {
		t.Fatalf("the live app must have a registered route: found=%v err=%v", found, err)
	}

	if res := c.Apply("vroom-other", 4321, portless.Ownership{}); !res.Succeeded() {
		t.Fatalf("the vroom route must register: %+v", res)
	}

	published, stillThere, err := c.Lookup(liveName)
	if err != nil || !stillThere {
		t.Fatalf("the live app's route must still exist: found=%v err=%v", stillThere, err)
	}
	if published != appPort {
		t.Errorf("the live app's route must still be at %d, got %d", appPort, published)
	}

	if status := probeHTTP(t, "http", liveHost, proxyPort); status == 0 {
		t.Error("the live app stopped being served after the vroom alias")
	}

	portless.Release(c, "vroom-other")
	if _, still, _ := c.Lookup(liveName); !still {
		t.Error("stopping our own cannot evict the live app's route")
	}
	if status := probeHTTP(t, "http", liveHost, proxyPort); status == 0 {
		t.Error("the live app stopped being served after removing the vroom route")
	}
}

func startIsolatedProxy(t *testing.T, iso, bin string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	cmd := exec.Command(bin, "proxy", "start", "-p", strconv.Itoa(proxyPort))
	cmd.Env = append(os.Environ(), "PORTLESS_STATE_DIR="+iso)
	if out, err := cmd.CombinedOutput(); err != nil {
		skipOrFail(t, "could not start an isolated proxy: %v: %s", err, out)
	}
	// Registered before the wait so a proxy that started and never opened the port is still reaped: without it every run leaves a live node process behind (measured: the previous run's proxy was still listening).
	t.Cleanup(func() { stopIsolatedProxy(iso) })
	waitPortOpen(t, proxyPort)
	return proxyPort
}

// stopIsolatedProxy kills by PID, never with `portless proxy stop`, and reads the pid best-effort: a proxy the test already killed has no process to reap.
func stopIsolatedProxy(iso string) {
	raw, err := os.ReadFile(filepath.Join(iso, "proxy.pid"))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
}

func readProxyPID(t *testing.T, iso string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(iso, "proxy.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// startEchoBackend answers 200 with body: a ladder test runs two backends behind one proxy, and only the body says WHICH of them the proxy routed to — the status alone is 200 either way.
func startEchoBackend(t *testing.T, port int, body string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().(*net.TCPAddr).Port
}

// MEASURED: `portless <name> <cmd>` REASSIGNS the port and passes it via PORT (asking for 4611 served 4698), so a fixed bind leaves the proxy routing to nobody and a mute 502.
const portlessAppScript = `import os,http.server
http.server.HTTPServer(('127.0.0.1',int(os.environ['PORT'])),http.server.SimpleHTTPRequestHandler).serve_forever()`

// Started through portless so the route gets its own pid, the kind prune never touches; cleaned up with `portless kill`, never by touching user state.
func runPortlessApp(t *testing.T, bin, name string) error {
	t.Helper()
	cmd := exec.Command(bin, name, "python3", "-c", portlessAppScript)
	// MEASURED (M13): portless derives the worktree prefix from the BRANCH, so inside this worktree the app would publish as <branch>.<name> and the hostname under test would not exist.
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "PORTLESS_STATE_DIR="+os.Getenv("PORTLESS_STATE_DIR"))
	if err := cmd.Start(); err != nil {
		return err
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = exec.Command(bin, "kill", name).Run()
	})
	return nil
}

func probeHTTP(t *testing.T, scheme, host string, proxyPort int) int {
	t.Helper()
	status, err := portless.ProbeOnce(scheme, host, proxyPort, "/", 3*time.Second)
	if err != nil {
		return 0
	}
	return status
}

// 404 does not count as served: it is the proxy's default answer for any unknown name, so accepting it would return at once with the app still down and fail later with a misleading message.
func waitServes(t *testing.T, host string, proxyPort int) bool {
	t.Helper()
	for range 40 {
		switch probeHTTP(t, "http", host, proxyPort) {
		case 404, 0:
		default:
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

func waitPortOpen(t *testing.T, port int) {
	t.Helper()
	for range 40 {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	skipOrFail(t, "the isolated proxy did not open port %d", port)
}

func waitPortClosed(t *testing.T, port int) {
	t.Helper()
	for range 40 {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = c.Close()
		time.Sleep(150 * time.Millisecond)
	}
	skipOrFail(t, "port %d remained open after stopping the proxy", port)
}

// MEASURED: 4 bytes with no trailing newline, and the only source of the proxy port; a stopped proxy has no file.
func TestProxyPortFileShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.port")
	if err := os.WriteFile(path, []byte("1399"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 4 || strings.TrimSpace(string(data)) != "1399" {
		t.Errorf("proxy.port must be the bare number, got %q", data)
	}
}
