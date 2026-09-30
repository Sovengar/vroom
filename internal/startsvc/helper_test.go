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

// ---- helper process ----
//
// El servicio bajo prueba es el propio binario de test lanzado como
// descendiente: ningún mock puede probar ni la inyección de PORT ni que el
// discovery cruce /proc con el linaje real.

const helperEnv = "VROOM_START_HELPER"

// TestHelperService no es un test: es el servicio que vroom arranca. El
// guard exige que -test.run lo haya seleccionado, así que el proceso padre
// nunca se cuela en el modo helper aunque las variables estén puestas.
func TestHelperService(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" || flag.Lookup("test.run").Value.String() != "^TestHelperService$" {
		t.Skip("proceso helper, no un test")
	}

	report("PORT_SEEN", os.Getenv("PORT"))
	report("HOST_SEEN", os.Getenv("HOST"))
	report("PATH_SEEN", os.Getenv("PATH"))
	report("HOME_SEEN", os.Getenv("HOME"))

	// Si el hijo puede resolver sh por su PATH es que el entorno no se
	// truncó al inyectar PORT.
	report("SH_RESOLVED", map[bool]string{true: "ok", false: "unavailable"}[shResolvable()])

	if delay, err := time.ParseDuration(os.Getenv("VROOM_HELPER_DELAY")); err == nil && delay > 0 {
		time.Sleep(delay)
	}

	switch mode {
	case "fixed-port": // ignora PORT y hace bind a su propio número
		hold(mustAtoi(os.Getenv("VROOM_HELPER_PORT")))
	case "udp-only": // no abre ningún puerto TCP
		time.Sleep(60 * time.Second)
	case "two-http-ports": // ignora PORT: metrics (404) y main (200 en health_path)
		startTwoHTTPListeners()
	case "two-raw-ports": // sockets crudos: ningún health_path responde
		startTwoRawListeners()
	case "churn": // abre listeners sin parar: el conjunto nunca se estabiliza
		startChurningListeners()
	case "die": // muere antes de hacer bind
		os.Exit(1)
	default: // honra PORT
		port, err := strconv.Atoi(os.Getenv("PORT"))
		if err != nil || port == 0 {
			os.Exit(2)
		}
		hold(port)
	}
}

// startChurningListeners abre un listener nuevo cada 100ms y cierra el
// anterior. El conjunto de listeners del linaje no para de cambiar, así que
// nunca se estabiliza y el discovery no puede decidir cuál es el principal:
// es el caso "unresolved" de verdad, distinto de "no tiene puertos".
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

// report deja en el fichero de salida lo que el hijo vio. Lo leen los
// asserts; el hijo no puede volver por otro canal.
func report(k, v string) {
	f, err := os.OpenFile(os.Getenv("VROOM_HELPER_OUT"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(k + "=" + v + "\n")
}

// hold escucha en 127.0.0.1:port hasta que le maten.
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

// startTwoHTTPListeners abre el listener de métricas primero (404 en
// cualquier ruta) y el principal después (200 en /health). Es la app que
// ignora PORT y expone dos endpoints.
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

// startTwoRawListeners abre dos sockets que aceptan y no responden: ningún
// health_path obtiene nada, que es el caso no-HTTP.
func startTwoRawListeners() {
	a := mustListen(mustAtoi(os.Getenv("VROOM_HELPER_PORT_A")))
	report("PORT_A", strconv.Itoa(a.Addr().(*net.TCPAddr).Port))
	b := mustListen(mustAtoi(os.Getenv("VROOM_HELPER_PORT_B")))
	report("PORT_B", strconv.Itoa(b.Addr().(*net.TCPAddr).Port))
	time.Sleep(120 * time.Second)
}

// serve corre un handler HTTP sin que su error de cierre moleste al linter.
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

// ---- harness ----

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

// command apunta el manifiesto al binario de test en modo helper. Las
// variables viajan por el entorno del padre a propósito: si el merge del
// entorno se rompe, el helper ni siquiera arranca y el test lo nota.
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

// cleanup mata el proceso vivo de un arranque.
func (f *fixture) cleanup(t *testing.T, out Result) {
	t.Helper()
	t.Cleanup(func() {
		_ = process.NewManager().Stop(process.StopSpec{
			Pid: out.Pid, Pgid: out.Meta.Pgid, Timeout: 2 * time.Second,
		})
	})
}

// helperEnv lee lo que el hijo reportó de su entorno. El arranque no
// espera al helper (en fixed y none no hay discovery que esperar), así que
// hay que darle un margen a que arranque.
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

// freePort pide un puerto efímero libre.
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

// mustAtoiT es mustAtoi con el salto de test que da el helper real.
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
