package portless

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
)

func TestHostnameNoDuplicaElSufijoNiLoPierde(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"sin sufijo", "svc", "svc.localhost"},
		{"con sufijo", "svc.localhost", "svc.localhost"},
		{"sufijo en mayusculas", "svc.LOCALHOST", "svc.localhost"},
		{"sufijo repetido", "svc.localhost.localhost", "svc.localhost"},
		{"solo el sufijo: nada utilizable", ".localhost", ""},
		{"vacio", "", ""},
		{"solo caracteres no utilizables", "!!!", ""},
		{"con espacios alrededor", "  svc  ", "svc.localhost"},
		{"subdominio", "a.b", "a.b.localhost"},
		{
			// Stripping the suffix happens BEFORE collapsing dots, so it can rebuild it: without the HasSuffix this yields "x.localhost.localhost".
			"sufijo reconstruido por el colapso de puntos",
			"localhost..LocalHostLocalHost", "localhost.localhost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Hostname(tt.in); got != tt.want {
				t.Errorf("Hostname(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHostnameNuncaEmiteElSufijoDosVeces(t *testing.T) {
	atoms := []string{"", ".", "..", "a", "svc", "-", "_", "localhost", ".localhost", "LocalHost", "9"}
	checked := 0

	for _, a := range atoms {
		for _, b := range atoms {
			for _, c := range atoms {
				in := a + b + c
				got := Hostname(in)
				checked++

				if got == "" {
					continue
				}
				if !strings.HasSuffix(got, HostSuffix) {
					t.Errorf("Hostname(%q) = %q no termina en el sufijo", in, got)
				}
				if strings.Count(got, HostSuffix) > 1 {
					t.Errorf("Hostname(%q) = %q repite el sufijo", in, got)
				}
				if strings.HasPrefix(got, ".") || strings.HasSuffix(got, ".") {
					t.Errorf("Hostname(%q) = %q tiene un punto colgante", in, got)
				}
			}
		}
	}
	t.Logf("barridas %d combinaciones", checked)
}

func TestDeriveNameAutoSinRamaUsaElProyecto(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		want   string
	}{
		{"rama vacia", "", "tienda-api"},
		{"rama no utilizable", "!!!", "tienda-api"},
		{"rama con espacios", "   ", "tienda-api"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeriveName(manifest.URLGenByWorkspaceHostname, "", tt.branch, "tienda-api")
			if err != nil {
				t.Fatalf("DeriveName fallo: %v", err)
			}
			if got != tt.want {
				t.Errorf("DeriveName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeriveNameAutoSinRamaNiProyectoUtilizableFalla(t *testing.T) {
	got, err := DeriveName(manifest.URLGenByWorkspaceHostname, "", "", "!!!")

	if err == nil {
		t.Fatalf("DeriveName devolvio %q, want error: no hay nada de quoi derivar un nombre", got)
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("el error %q no explica que el problema es el nombre", err)
	}
}

func TestDeriveNameAutoPrefiereLaRama(t *testing.T) {
	got, err := DeriveName(manifest.URLGenByWorkspaceHostname, "", "feature/login", "tienda-api")
	if err != nil {
		t.Fatal(err)
	}
	if got != "feature-login.tienda-api" {
		t.Errorf("DeriveName = %q, want feature-login.tienda-api", got)
	}
}

func TestResolveBinaryEncuentraElDelPATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "portless")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())

	got := ResolveBinary()

	if got != bin {
		t.Errorf("ResolveBinary = %q, want %q (el del PATH)", got, bin)
	}
}

// Without the exec bit LookPath refuses it, which keeps the clear "no portless" warning instead of a later permission denied.
func TestResolveBinaryIgnoraUnFicheroNoEjecutableDelPATH(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede ejecutar un fichero sin permiso de ejecucion: la rama no se puede provocar")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "portless")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", dir)
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := ResolveBinary(); got != "" {
		t.Errorf("ResolveBinary = %q: un fichero sin permiso de ejecucion no es un binario", got)
	}
}

// A transport failure must return an error, never status 0: verify reads a bare 0 as a 404 and would stop trying schemes.
func TestHTTPProbeConElNombreDeUnProxyMuerto(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	status, err := httpProbe(ctx, "http", "svc.localhost", deadPort, "/")

	if err == nil {
		t.Fatalf("httpProbe a un puerto muerto devolvio status %d sin error: verify lo confundiria con un 404", status)
	}
	if status != 0 {
		t.Errorf("status = %d con error de transporte, want 0", status)
	}
}

func TestHTTPProbePropagaElStatusDeVerdad(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	status, err := httpProbe(ctx, "http", "svc.localhost", srvPort(t, srv), "/")
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", status)
	}
	if gotHost != "svc.localhost" {
		t.Errorf("la peticion llevo Host = %q, want svc.localhost: es lo que elige la ruta en el proxy", gotHost)
	}
}

func TestAcceptsConnectionsConUnPuertoRealYCerrado(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	livePort := l.Addr().(*net.TCPAddr).Port

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()

	c := New(WithBinary("/fake/portless"), WithTimeout(2*time.Second))

	if !c.acceptsConnections(livePort) {
		t.Error("acceptsConnections = false con un listener real")
	}
	if c.acceptsConnections(deadPort) {
		t.Error("acceptsConnections = true con el puerto cerrado")
	}
	_ = l.Close()
}

func TestDeriveNamedRechazaUnNombreNoUtilizable(t *testing.T) {
	for _, name := range []string{"", "   ", "!!!", ".localhost", "-"} {
		got, err := DeriveName(manifest.URLGenByHostnameOrWorkspace, name, "", "proyecto")
		if err == nil {
			t.Errorf("DeriveName(named, %q) = %q, want error", name, got)
		}
	}
}

// named must ignore the branch: OAuth and CORS callbacks are configured ahead of time, so a `git branch -m` would move the URL.
func TestDeriveNamedIgnoraLaRama(t *testing.T) {
	got, err := DeriveName(manifest.URLGenByHostnameOrWorkspace, "tienda", "cualquier-rama", "proyecto")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tienda" {
		t.Errorf("DeriveName(named) = %q, want tienda: no puede depender de la rama", got)
	}
}

// DeriveName is public and can get the raw string, so it must reject anything that is not a URL-publishing generation.
func TestDeriveNameRechazaUnModoDesconocido(t *testing.T) {
	for _, mode := range []string{"", "OFF", "auto ", "inventado"} {
		got, err := DeriveName(mode, "tienda", "rama", "proyecto")
		if err == nil {
			t.Errorf("DeriveName(%q) = %q, want error", mode, got)
		}
	}
}

func TestOwnershipAuthorisesSoloSuPropioPuerto(t *testing.T) {
	tests := []struct {
		name string
		held Ownership
		port int
		want bool
	}{
		{"sin propiedad, puerto correcto", Ownership{}, 8080, false},
		{"con propiedad, su puerto", Ownership{Owned: true, Port: 8080}, 8080, true},
		{"con propiedad, otro puerto", Ownership{Owned: true, Port: 8080}, 9999, false},
		{"con propiedad, puerto 0", Ownership{Owned: true, Port: 8080}, 0, false},
		{"propiedad sin puerto", Ownership{Owned: true}, 8080, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.held.Authorises(tt.port); got != tt.want {
				t.Errorf("Authorises(%d) = %v, want %v", tt.port, got, tt.want)
			}
		})
	}
}

// Empty args used to index out of range mid-startup, the worst thing this seam can do, so the error must name the binary instead.
func TestExecCommandSinSubcomandoNoRevienta(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "falla")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 4\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, code, err := execCommand(context.Background(), script)

	if err == nil {
		t.Fatal("un exit 4 deberia ser error aunque no haya subcomando")
	}
	if code != 4 {
		t.Errorf("code = %d, want 4", code)
	}
	if !strings.Contains(err.Error(), "falla") {
		t.Errorf("el error no nombra ni el subcomando ni el binario: %q", err)
	}
}

// A listener that accepts but never answers is the only path reaching ReasonRouteNotServed: the port is alive, so it cannot be ReasonProxyUnreachable.
func TestVerifyDegradaCuandoElPuertoAceptaPeroNoHablaHTTP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	f := newFake()
	f.noProxy = true
	c := newClientWithProxy(t, l.Addr().(*net.TCPAddr).Port, f.exec)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q: el puerto vive pero no sirve la ruta", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q: nadie respondio a la sonda", r.Url)
	}
	if !r.Registered {
		t.Error("Registered se perdio: el alta ocurrio antes de sondear")
	}
}

// Routed with a dead backend and ours is the only cleanup `portless prune` does not do (M5), so the orphan would survive forever.
func TestReconcileRetiraLaRutaEnrutaYMuertaCuandoEsNuestra(t *testing.T) {
	f := newFake()
	c := f.client(t)
	host := Hostname("ruta-vieja")
	f.routes[host] = 8080
	f.probeStatus = 502

	held := Ownership{Owned: true, Port: 8080}
	warnings := c.Reconcile("ruta-vieja", held, "ruta-nueva")

	if len(warnings) != 0 {
		t.Errorf("una ruta nuestra enruta y muerta deberia retirarse sin avisar, avise: %v", warnings)
	}
	if _, still := f.routes[host]; still {
		t.Errorf("la ruta %q sigue en el estado tras la reconciliacion: es la huella de un vroom muerto", host)
	}
	if len(f.removedNames) == 0 {
		t.Error("no se registro ninguna retirada: la limpieza no ocurrio")
	}
}

// Same 502 orphan with ownership revoked: a matching port is no proof, because a port is a number that does not expire and only ownership is authority.
func TestReconcileNoRetiraUnaRutaEnrutaYMuertaSinPropiedad(t *testing.T) {
	f := newFake()
	c := f.client(t)
	host := Hostname("ruta-ajena")
	f.routes[host] = 8080
	f.probeStatus = 502

	held := Ownership{Owned: false, Port: 8080}
	warnings := c.Reconcile("ruta-ajena", held, "ruta-nueva")

	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want 1 aviso: la ruta no es nuestra", warnings)
	}
	if !strings.Contains(warnings[0], "left untouched") {
		t.Errorf("el aviso no dice que la ruta quedo intacta: %q", warnings[0])
	}
	if _, still := f.routes[host]; !still {
		t.Error("se borro una ruta sin prueba de que fuera nuestra")
	}
	if len(f.removedNames) != 0 {
		t.Errorf("se Registro una retirada: %v", f.removedNames)
	}
}

func TestHTTPProbeConUnHostnameNoParseable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for _, hostname := range []string{"host con espacio", "host	con	tab", "%%"} {
		status, err := httpProbe(ctx, "http", hostname, 1, "/")
		if err == nil {
			t.Errorf("hostname %q: sin error y status %d, se confundiria con un 404", hostname, status)
		}
		if status != 0 {
			t.Errorf("hostname %q: status = %d con error de parseo, want 0", hostname, status)
		}
	}
}

// Routed but absent from list: without the early return published stays 0 and reconciliation reads "not ours" out of the M8/probe window.
func TestLiveRouteSaltaALLookupCuandoLaTablaNoLaTiene(t *testing.T) {
	f := newFake()
	c := f.client(t)
	host := Hostname("ruta-fantasma")
	f.routes[host] = 8080
	f.serve404[host] = false
	f.probeStatus = 200

	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			return "\nActive routes:\n\n", 0, nil
		}
		return f.exec(ctx, bin, args...)
	}

	published, st := c.liveRoute("ruta-fantasma")

	if st != routeUnknown {
		t.Errorf("state = %v, want %v: sin lectura de la tabla no se puede afirmar nada", st, routeUnknown)
	}
	if published != 0 {
		t.Errorf("published = %d en un routeUnknown: ese camino devuelve 0 SIEMPRE", published)
	}
}

func TestLiveRouteSaltaALLookupCuandoLaLecturaFalla(t *testing.T) {
	f := newFake()
	c := f.client(t)
	host := Hostname("ruta-ilegible")
	f.routes[host] = 8080
	f.probeStatus = 200

	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			return "", 0, errors.New("portless list: routes.json ilegible")
		}
		return f.exec(ctx, bin, args...)
	}

	published, st := c.liveRoute("ruta-ilegible")

	if st != routeUnknown {
		t.Errorf("state = %v, want %v: una lectura fallida no es evidencia", st, routeUnknown)
	}
	if published != 0 {
		t.Errorf("published = %d con la lectura fallida, want 0", published)
	}
}
