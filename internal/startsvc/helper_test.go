package startsvc

import (
	"flag"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/state"
)

// The service under test is this same test binary re-executed as a child: no mock can prove PORT injection or that discovery walks /proc across the real lineage.

const helperEnv = "VROOM_START_HELPER"

// Not a test: this is the service vroom starts, and the -test.run guard keeps the parent process out of helper mode even with the env set.
func TestHelperService(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" || flag.Lookup("test.run").Value.String() != "^TestHelperService$" {
		t.Skip("proceso helper, no un test")
	}

	report("PORT_SEEN", os.Getenv("PORT"))
	report("HOST_SEEN", os.Getenv("HOST"))
	report("PATH_SEEN", os.Getenv("PATH"))
	report("HOME_SEEN", os.Getenv("HOME"))

	// SH_RESOLVED means the child still resolves sh through its PATH, i.e. injecting PORT did not truncate the environment.
	report("SH_RESOLVED", map[bool]string{true: "ok", false: "unavailable"}[shResolvable()])

	if delay, err := time.ParseDuration(os.Getenv("VROOM_HELPER_DELAY")); err == nil && delay > 0 {
		time.Sleep(delay)
	}

	switch mode {
	case "fixed-port":
		hold(mustAtoi(os.Getenv("VROOM_HELPER_PORT")))
	case "udp-only":
		time.Sleep(60 * time.Second)
	case "two-http-ports":
		startTwoHTTPListeners()
	case "two-raw-ports":
		startTwoRawListeners()
	case "churn":
		startChurningListeners()
	case "die":
		os.Exit(1)
	default:
		port, err := strconv.Atoi(os.Getenv("PORT"))
		if err != nil || port == 0 {
			os.Exit(2)
		}
		hold(port)
	}
}

// The listener set never stabilises, so discovery cannot pick a primary: a true "port unresolved" case, distinct from "has no ports".
func startChurningListeners() {
	var prev net.Listener
	defer func() {
		if prev != nil {
			_ = prev.Close()
		}
	}()
	for range 200 {
		ln := mustListen(0)
		if prev != nil {
			_ = prev.Close()
		}
		prev = ln
		time.Sleep(100 * time.Millisecond)
	}
}

// The child can report back only through this file, so the assertions have to read it.
func report(k, v string) {
	f, err := os.OpenFile(os.Getenv("VROOM_HELPER_OUT"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(k + "=" + v + "\n")
}

func hold(port int) {
	ln, err := listen(port)
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

// Metrics (404 everywhere) binds first and main (200 on /health) 150ms later, so discovery sees a half-built port set.
func startTwoHTTPListeners() {
	metrics := mustListen(mustAtoi(os.Getenv("VROOM_HELPER_PORT_A")))
	go serve(metrics, func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	time.Sleep(150 * time.Millisecond)

	main := mustListen(mustAtoi(os.Getenv("VROOM_HELPER_GOOD")))
	report("GOOD_PORT", strconv.Itoa(main.Addr().(*net.TCPAddr).Port))
	go serve(main, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		http.NotFound(w, r)
	})
	time.Sleep(120 * time.Second)
}

// Both listeners accept but never answer, so no health_path can get a response: the non-HTTP case.
func startTwoRawListeners() {
	a := mustListen(mustAtoi(os.Getenv("VROOM_HELPER_PORT_A")))
	report("PORT_A", strconv.Itoa(a.Addr().(*net.TCPAddr).Port))
	b := mustListen(mustAtoi(os.Getenv("VROOM_HELPER_PORT_B")))
	report("PORT_B", strconv.Itoa(b.Addr().(*net.TCPAddr).Port))
	time.Sleep(120 * time.Second)
}

func serve(ln net.Listener, h func(http.ResponseWriter, *http.Request)) {
	_ = http.Serve(ln, http.HandlerFunc(h))
}

func mustListen(port int) net.Listener {
	ln, err := listen(port)
	if err != nil {
		os.Exit(5)
	}
	return ln
}

func shResolvable() bool {
	_, err := os.Stat("/bin/sh")
	return err == nil
}

func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		os.Exit(4)
	}
	return n
}

type fixture struct {
	store    *state.Store
	dir      string
	outFile  string
	manifest *manifest.Manifest
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	return &fixture{
		store:    state.NewStoreAt(t.TempDir()),
		dir:      dir,
		outFile:  dir + "/helper.env",
		manifest: &manifest.Manifest{Name: "svc", Port: 8080, PortMode: manifest.PortModeDynamic},
	}
}

// Variables travel through the parent environment on purpose, so a broken env merge stops the helper from starting and the test notices.
func (f *fixture) command(t *testing.T, mode string, extraEnv ...string) {
	t.Helper()
	t.Setenv(helperEnv, mode)
	t.Setenv("VROOM_HELPER_OUT", f.outFile)
	for _, kv := range extraEnv {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	f.manifest.Command = shellQuote(os.Args[0]) + " -test.run=^TestHelperService$"
}

func (f *fixture) start(t *testing.T, timeout time.Duration) (Result, error) {
	t.Helper()
	return Start(Request{
		Manifest:         f.manifest,
		Path:             f.dir,
		Store:            f.store,
		Manager:          process.NewManager(),
		StdoutPath:       f.store.StdoutLog(f.dir),
		StderrPath:       f.store.StderrLog(f.dir),
		DiscoveryTimeout: timeout,
	})
}

// A nil routes means no seam at all, which is exactly what route_mode = "off" produces.
func (f *fixture) startWithRoutes(t *testing.T, timeout time.Duration, routes RouteRegistrar) (Result, error) {
	t.Helper()
	return f.startWithRoutesBranch(t, timeout, routes, "")
}

func (f *fixture) startWithRoutesBranch(t *testing.T, timeout time.Duration, routes RouteRegistrar, branch string) (Result, error) {
	t.Helper()
	return Start(Request{
		Manifest:         f.manifest,
		Path:             f.dir,
		Store:            f.store,
		Manager:          process.NewManager(),
		StdoutPath:       f.store.StdoutLog(f.dir),
		StderrPath:       f.store.StderrLog(f.dir),
		DiscoveryTimeout: timeout,
		Routes:           routes,
		Branch:           branch,
	})
}

func (f *fixture) cleanup(t *testing.T, out Result) {
	t.Helper()
	t.Cleanup(func() {
		_ = process.NewManager().Stop(process.StopSpec{
			Pid: out.Pid, Pgid: out.Meta.Pgid, Timeout: 2 * time.Second,
		})
	})
}

// Start does not wait for the helper, so this polls the report file until the child has written.
func (f *fixture) helperEnv(t *testing.T) map[string]string {
	t.Helper()
	var data []byte
	var err error
	for range 100 {
		data, err = os.ReadFile(f.outFile)
		if err == nil && len(data) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("el helper no escribió su entorno: %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		out[k] = v
	}
	return out
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := listen(0)
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func mustAtoiT(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("el helper no reportó %q: %v", s, err)
	}
	return n
}

func minPort(a, b int) int {
	if a < b {
		return a
	}
	return b
}
