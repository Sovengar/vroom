package portless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
)

// Every declared reason is tabulated, including ReasonInvalidName and an unknown one, because a miswritten default only shows up when tested.
func TestWarnNombraCadaMotivoYPrometeElPuerto(t *testing.T) {
	tests := []struct {
		reason string
		host   string
		// Fragments the warning MUST carry: its text is the only thing telling the user what failed, and no other layer pins it.
		mustContain []string
		// wantPortPromise is false only for port_unresolved: the service has not confirmed a port yet, so promising it would be a lie.
		wantPortPromise bool
	}{
		{
			reason:          ReasonPortlessMissing,
			mustContain:     []string{"binary", "not available"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonPortlessTimeout,
			mustContain:     []string{"did not respond in time"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonPortlessFailed,
			mustContain:     []string{"failed", "Node"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonProxyNotRunning,
			mustContain:     []string{"no proxy running"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonProxyUnreachable,
			mustContain:     []string{"proxy port", "does not accept"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonRouteConflict,
			host:            "tienda.localhost",
			mustContain:     []string{"tienda.localhost", "already taken"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonPortUnresolved,
			mustContain:     []string{"not resolved"},
			wantPortPromise: false,
		},
		{
			reason:          ReasonRouteNotServed,
			mustContain:     []string{"does not serve this route"},
			wantPortPromise: true,
		},
		{
			reason:          "algo_raro_nuevo",
			mustContain:     []string{"algo_raro_nuevo"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonInvalidName,
			mustContain:     []string{ReasonInvalidName},
			wantPortPromise: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			got := Warn(Result{Name: "svc", Host: tt.host, Reason: tt.reason})

			if got == "" {
				t.Fatalf("Warn(%q) devolvio aviso vacio", tt.reason)
			}
			for _, want := range tt.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("el aviso no nombra el fallo: falta %q\naviso: %q", want, got)
				}
			}
			if tt.wantPortPromise && !strings.Contains(got, "running on its own port") {
				t.Errorf("el aviso no dice que el servicio sigue en su puerto: %q", got)
			}
		})
	}
}

func TestWarnSilenciosoSinMotivo(t *testing.T) {
	for _, r := range []Result{
		{Name: "svc", Status: StatusRegistered, Registered: true},
		{Name: "svc", Status: StatusRegistered, Url: "https://svc.localhost"},
		{Name: "svc"},
	} {
		if got := Warn(r); got != "" {
			t.Errorf("Warn(%+v) = %q, want cadena vacia", r, got)
		}
	}
}

func TestWarnNombraElHostEnConflicto(t *testing.T) {
	got := Warn(Result{Name: "svc", Host: "tienda.localhost", Reason: ReasonRouteConflict})

	if !strings.Contains(got, "tienda.localhost") {
		t.Errorf("el aviso de conflicto no nombra la ruta: %q", got)
	}
	// It must also say the route was left untouched, or the user deletes it by hand and breaks the real owner.
	if !strings.Contains(got, "left untouched") {
		t.Errorf("el aviso de conflicto no dice que la ruta quedó intacta: %q", got)
	}
}

func TestClassifyTraduceCadaError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"timeout del contexto", context.DeadlineExceeded, ReasonPortlessTimeout},
		{"proxy parado", ErrProxyNotRunning, ReasonProxyNotRunning},
		{"binario ausente por nombre", execErrNotFound(), ReasonPortlessMissing},
		{"binario ausente por ruta", errors.New("fork/exec /opt/portless: no such file or directory"), ReasonPortlessMissing},
		{"fallo generico", errors.New("requires Node >= 24"), ReasonPortlessFailed},
		{"json corrupto", errors.New("SyntaxError: Unexpected token }"), ReasonPortlessFailed},
		{"envolvido de otro modo", wrappedProxyNotRunning(), ReasonProxyNotRunning},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.err); got != tt.want {
				t.Errorf("classify(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// A PATH that does not resolve makes exec.Command return fork/exec, not ErrNotFound, so telling the two apart is the point: missing is an install, broken is a debug session.
func TestIsMissingBinaryDistingueAusenteDeRoto(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"ErrNotFound", execErrNotFound(), true},
		{"fork/exec sin fichero", errors.New(`fork/exec /opt/x: no such file or directory`), true},
		{"envuelto", wrappedNotFound(), true},
		{"node viejo", errors.New("requires Node >= 24"), false},
		{"permisos", errors.New("EACCES: permission denied"), false},
		{"json corrupto", errors.New("SyntaxError: Unexpected token }"), false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMissingBinary(tt.err); got != tt.want {
				t.Errorf("isMissingBinary(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestApplySinBinarioNoResuelvePuertoNiBinario(t *testing.T) {
	c := New(WithBinary(""))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want %q", r.Status, StatusDegraded)
	}
	if r.Reason != ReasonPortlessMissing {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonPortlessMissing)
	}
	if r.Registered {
		t.Error("Registered=true sin binario: se afirmaria una escritura que no ocurrio")
	}
	if r.Url != "" {
		t.Errorf("Url = %q sin binario", r.Url)
	}
	if r.Name != "svc" {
		t.Errorf("Name = %q, want svc incluso degradado", r.Name)
	}
}

func TestApplyConPuertoNoResueltoNoInventaDireccion(t *testing.T) {
	f := newFake()
	c := f.client(t)

	for _, port := range []int{0, -1} {
		r := c.Apply("svc", port, Ownership{})

		if r.Reason != ReasonPortUnresolved {
			t.Errorf("port=%d: Reason = %q, want %q", port, r.Reason, ReasonPortUnresolved)
		}
		if r.Registered {
			t.Errorf("port=%d: Registered=true sin puerto confirmado", port)
		}
		if len(f.calls) != 0 {
			t.Errorf("port=%d: se invoco el binario sin puerto: %v", port, f.calls)
		}
	}
}

func TestApplyConservaElHechoRegistradoCuandoFallaLaLecturaDeVuelta(t *testing.T) {
	f := newFake()
	c := f.client(t)
	calls := 0
	realExec := f.exec
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			calls++
			if calls > 1 {
				return "", 1, errors.New("portless list: routes.json unreadable")
			}
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if !r.Registered {
		t.Error("Registered se perdio tras un fallo de lectura: la ruta se escribio y quedaria huerfana")
	}
	if r.Url != "" {
		t.Errorf("Url = %q sin haber podido verificar", r.Url)
	}
	if r.Reason != ReasonPortlessFailed {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonPortlessFailed)
	}
}

// MEASURED (M6): the binary takes exit 0 with no proxy running because alias never contacts it, so the write happens and only the URL must be withheld.
func TestApplySinProxyDegradaPeroConservaElAlta(t *testing.T) {
	f := newFake()
	dir := t.TempDir()
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(2*time.Second),
	)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if r.Reason != ReasonProxyNotRunning {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonProxyNotRunning)
	}
	if !r.Registered {
		t.Error("Registered se perdio con el proxy parado: el alta ocurrio (M1, el binario no lo contacta)")
	}
	if r.Url != "" {
		t.Errorf("Url = %q sin proxy", r.Url)
	}
	if _, ok := f.routes[Hostname("svc")]; !ok {
		t.Error("la ruta no se escribio en el binario, pero el Result dice Registered")
	}
}

func TestApplyPublicaCuandoElProxyEnruta(t *testing.T) {
	f := newFake()
	c := f.client(t)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusRegistered {
		t.Fatalf("Status = %q (motivo %q), want registrado: %v", r.Status, r.Reason, f.calls)
	}
	if !strings.HasPrefix(r.Url, "http") {
		t.Errorf("Url = %q, quiero un esquema http/https", r.Url)
	}
	if !strings.HasSuffix(r.Url, Hostname("svc")) {
		t.Errorf("Url = %q no termina en el hostname de la ruta", r.Url)
	}
	if r.Reason != "" {
		t.Errorf("Reason = %q en un alta correcta", r.Reason)
	}
	if !r.Registered {
		t.Error("Registered=false en un alta correcta")
	}
}

func TestWithReasonConservaElHechoYQuitaLaUrl(t *testing.T) {
	base := Result{
		Name: "svc", Host: "svc.localhost", Port: 8080,
		Status: StatusRegistered, Url: "https://svc.localhost", Registered: true,
	}

	got := base.withReason(ReasonProxyNotRunning)

	if got.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", got.Status)
	}
	if got.Reason != ReasonProxyNotRunning {
		t.Errorf("Reason = %q", got.Reason)
	}
	if got.Url != "" {
		t.Errorf("Url = %q: degradar tiene que quitar la URL no verificada", got.Url)
	}
	if !got.Registered {
		t.Error("Registered se perdio al degradar: la escritura ocurrio")
	}
	if got.Name != base.Name || got.Host != base.Host || got.Port != base.Port {
		t.Errorf("withReason perdio identidad: %+v", got)
	}
	// It must not mutate the receiver: callers reuse the original for both branches.
	if base.Status != StatusRegistered || base.Url == "" {
		t.Errorf("withReason muto el receptor original: %+v", base)
	}
}

func TestReleaseConNombreVacioRevocaSinTocarNada(t *testing.T) {
	called := false
	r := ReleaserFunc(func(string) error { called = true; return nil })

	if !Release(r, "") {
		t.Error("sin nombre no hay nada que retirar: debe revocar")
	}
	if called {
		t.Error("se llamo al releaser con nombre vacio")
	}
}

// ErrRouteAbsent revokes because the route is gone, which is what was wanted; a real failure does not, or the route stays orphan with nobody to clean it.
func TestReleaseRevocaConRutaAusente(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"exito", nil, true},
		{"benigno M10: no estaba", ErrRouteAbsent, true},
		{"envuelto, sigue siendo el mismo", wrappedRouteAbsent(), true},
		{"fallo real", errors.New("requires Node >= 24"), false},
		{"permisos", errors.New("EACCES"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Release(failingReleaserWith{err: tt.err}, "mi-ruta"); got != tt.want {
				t.Errorf("Release con %v = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

type failingReleaserWith struct{ err error }

func (f failingReleaserWith) RemoveAbsent(string) error { return f.err }

// Without exercising the adapter the removal seam stays theoretical: the interface would have no implementation outside tests.
func TestReleaserFuncAdaptaUnaFuncion(t *testing.T) {
	var got string
	var r Releaser = ReleaserFunc(func(name string) error { got = name; return ErrRouteAbsent })

	if err := r.RemoveAbsent("x"); !errors.Is(err, ErrRouteAbsent) {
		t.Errorf("el adaptador no devuelve el error de la funcion: %v", err)
	}
	if got != "x" {
		t.Errorf("la funcion recibio %q, want x", got)
	}
}

// InertReleaser is the default under a test binary so a test that forgets the seam cannot mutate the developer's routes.json.
func TestInertReleaserNoTocaNadaYEsEstatico(t *testing.T) {
	r := InertReleaser()
	if err := r.RemoveAbsent("cualquier-cosa"); err != nil {
		t.Errorf("el releaser inerte devolvio %v, debe ser nil siempre", err)
	}
	// Identity is not compared on purpose: ReleaserFunc is a func type, so == would not compile.
	for _, name := range []string{"", "a", "ruta.larga.localhost"} {
		if err := InertReleaser().RemoveAbsent(name); err != nil {
			t.Errorf("InertReleaser().RemoveAbsent(%q) = %v, debe ser nil siempre", name, err)
		}
	}
	assertImplementsReleaser(t, InertReleaser())
}

func TestIsTestBinaryDetectaElBinarioDeTest(t *testing.T) {
	if !IsTestBinary() {
		t.Error("IsTestBinary() es false corriendo bajo `go test`: la guarda de InertReleaser no protege nada")
	}
	orig := os.Args[0]
	t.Cleanup(func() { os.Args[0] = orig })

	os.Args[0] = "/usr/local/bin/vroom"
	if IsTestBinary() {
		t.Error("el binario de producción se identifica como test")
	}
	os.Args[0] = "/tmp/vroom.test"
	if !IsTestBinary() {
		t.Error("un binario .test deberia identificarse como test")
	}
}

// The gate must return nil BEFORE resolving anything, or vroom looks for portless in a project that never asked for routes.
func TestClientForDevuelveNilSinContratoDeRuta(t *testing.T) {
	// Both env vars point at paths that do not exist, so any resolution attempt surfaces as a client.
	t.Setenv("PORTLESS_BIN", filepath.Join(t.TempDir(), "no-existe"))
	t.Setenv("PORTLESS_STATE_DIR", t.TempDir())

	tests := []struct {
		name string
		m    *manifest.Manifest
		want bool
	}{
		{"nil", nil, false},
		{"sin route_mode (default off)", &manifest.Manifest{Name: "svc"}, false},
		{"route_mode off", &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeOff}, false},
		{"route_mode invalido", &manifest.Manifest{Name: "svc", RouteMode: "inventado"}, false},
		{"route_mode named", &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeNamed, RouteName: "svc"}, true},
		{"route_mode auto", &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeAuto}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClientFor(tt.m)
			if tt.want && got == nil {
				t.Error("ClientFor devolvio nil con contrato de ruta activo")
			}
			if !tt.want && got != nil {
				t.Error("ClientFor devolvio un cliente sin contrato de ruta: se buscaria portless en un proyecto que no lo pidio")
			}
		})
	}
}

// MEASURED on 0.15.6: PORTLESS_HOME is ignored, and the plan's order read proxy.port from one directory while the binary wrote routes.json in another.
func TestResolveStateDirSigueElOrdenMedido(t *testing.T) {
	t.Run("PORTLESS_STATE_DIR manda", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "/opt/pl-state")
		t.Setenv("XDG_STATE_HOME", "/xdg")
		t.Setenv("HOME", "/home/u")
		if got := ResolveStateDir(); got != "/opt/pl-state" {
			t.Errorf("ResolveStateDir = %q, want /opt/pl-state", got)
		}
	})

	t.Run("XDG_STATE_HOME cuando no hay PORTLESS_STATE_DIR", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "/xdg")
		t.Setenv("HOME", "/home/u")
		if got := ResolveStateDir(); got != filepath.Join("/xdg", "portless") {
			t.Errorf("ResolveStateDir = %q, want /xdg/portless", got)
		}
	})

	t.Run("HOME como ultimo recurso", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "/home/u")
		if got := ResolveStateDir(); got != "/home/u/.portless" {
			t.Errorf("ResolveStateDir = %q, want /home/u/.portless", got)
		}
	})

	t.Run("sin nada devuelve vacio y no un path inventado", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "")
		if got := ResolveStateDir(); got != "" {
			t.Errorf("ResolveStateDir = %q: un path de usuario concreto seria un modo de fallo silencioso", got)
		}
	})
}

// MEASURED (M14): `env -i PATH=/usr/bin:/bin` does not resolve portless because it lives behind the mise shims, hence the third step after $PORTLESS_BIN and LookPath.
func TestResolveBinarySigueElOrdenMedido(t *testing.T) {
	t.Run("PORTLESS_BIN manda", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "/opt/bin/portless")
		if got := ResolveBinary(); got != "/opt/bin/portless" {
			t.Errorf("ResolveBinary = %q, want /opt/bin/portless", got)
		}
	})

	t.Run("shim de mise cuando no esta en el PATH", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "")
		t.Setenv("PATH", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		shimDir := filepath.Join(home, ".local", "share", "mise", "shims")
		if err := os.MkdirAll(shimDir, 0o755); err != nil {
			t.Fatal(err)
		}
		shim := filepath.Join(shimDir, "portless")
		if err := os.WriteFile(shim, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		if got := ResolveBinary(); got != shim {
			t.Errorf("ResolveBinary = %q, want el shim %q: con PATH vacio este es el unico camino real", got, shim)
		}
	})

	t.Run("un shim que es directorio no cuenta", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "")
		t.Setenv("PATH", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		shimDir := filepath.Join(home, ".local", "bin")
		if err := os.MkdirAll(filepath.Join(shimDir, "portless"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := ResolveBinary(); got != "" {
			t.Errorf("ResolveBinary = %q, un directorio no es un binario", got)
		}
	})

	t.Run("nada resoluble devuelve vacio", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "")
		t.Setenv("PATH", "")
		t.Setenv("HOME", t.TempDir())
		if got := ResolveBinary(); got != "" {
			t.Errorf("ResolveBinary = %q, want vacio (degradacion, no error)", got)
		}
	})
}

func TestDefaultConstruyeClienteConLoResuelto(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "/opt/bin/portless")
	t.Setenv("PORTLESS_STATE_DIR", "/opt/pl-state")

	c := Default()

	if !c.HasBinary() {
		t.Fatal("HasBinary() = false con PORTLESS_BIN puesto")
	}
	if c.Binary() != "/opt/bin/portless" {
		t.Errorf("Binary() = %q", c.Binary())
	}
	if c.StateDir() != "/opt/pl-state" {
		t.Errorf("StateDir() = %q", c.StateDir())
	}
}

func TestDefaultSinBinarioDevuelveClienteDegradado(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PORTLESS_STATE_DIR", t.TempDir())

	c := Default()

	if c == nil {
		t.Fatal("Default devolvio nil: sin binario es una degradacion, no un error")
	}
	if c.HasBinary() {
		t.Error("HasBinary() = true sin binario resoluble")
	}
	if c.Binary() != "" {
		t.Errorf("Binary() = %q, want vacio", c.Binary())
	}
}

func TestDefaultRespetaLasOpcionesSobreElEntorno(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "/opt/bin/portless")
	t.Setenv("PORTLESS_STATE_DIR", "/opt/pl-state")

	c := Default(WithBinary("/forzado/portless"), WithStateDir("/forzado/state"))

	if c.Binary() != "/forzado/portless" {
		t.Errorf("Binary() = %q, la opcion explicita deberia ganar al entorno", c.Binary())
	}
	if c.StateDir() != "/forzado/state" {
		t.Errorf("StateDir() = %q", c.StateDir())
	}
}

// The real exec.ErrNotFound, not a string: classify and isMissingBinary use errors.Is, so a hand-built message would test the message.
func execErrNotFound() error { return exec.ErrNotFound }

func wrappedNotFound() error {
	return fmt.Errorf("portless alias x: %w", exec.ErrNotFound)
}

func wrappedProxyNotRunning() error {
	return fmt.Errorf("verificando la ruta: %w", ErrProxyNotRunning)
}

func wrappedRouteAbsent() error {
	return fmt.Errorf("retirando: %w", ErrRouteAbsent)
}

// A compile-time check would be `var _ Releaser = r`, but staticcheck QF1011 rejects that form; this has the same effect.
func assertImplementsReleaser(t *testing.T, r Releaser) {
	t.Helper()
	if r == nil {
		t.Fatal("InertReleaser devolvio nil")
	}
}
