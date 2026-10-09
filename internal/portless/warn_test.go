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
				t.Fatalf("Warn(%q) returned empty warning", tt.reason)
			}
			for _, want := range tt.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("the warning does not name the failure: missing %q\nwarning: %q", want, got)
				}
			}
			if tt.wantPortPromise && !strings.Contains(got, "running on its own port") {
				t.Errorf("the warning does not say the service is still on its port: %q", got)
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
			t.Errorf("Warn(%+v) = %q, want empty string", r, got)
		}
	}
}

func TestWarnNombraElHostEnConflicto(t *testing.T) {
	got := Warn(Result{Name: "svc", Host: "tienda.localhost", Reason: ReasonRouteConflict})

	if !strings.Contains(got, "tienda.localhost") {
		t.Errorf("the conflict warning does not name the route: %q", got)
	}
	// It must also say the route was left untouched, or the user deletes it by hand and breaks the real owner.
	if !strings.Contains(got, "left untouched") {
		t.Errorf("the conflict warning does not say the route was left intact: %q", got)
	}
}

func TestClassifyTraduceCadaError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"context timeout", context.DeadlineExceeded, ReasonPortlessTimeout},
		{"proxy stopped", ErrProxyNotRunning, ReasonProxyNotRunning},
		{"binary missing by name", execErrNotFound(), ReasonPortlessMissing},
		{"binary missing by path", errors.New("fork/exec /opt/portless: no such file or directory"), ReasonPortlessMissing},
		{"generic failure", errors.New("requires Node >= 24"), ReasonPortlessFailed},
		{"corrupt json", errors.New("SyntaxError: Unexpected token }"), ReasonPortlessFailed},
		{"wrapped differently", wrappedProxyNotRunning(), ReasonProxyNotRunning},
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
		{"fork/exec without file", errors.New(`fork/exec /opt/x: no such file or directory`), true},
		{"wrapped", wrappedNotFound(), true},
		{"old node", errors.New("requires Node >= 24"), false},
		{"permissions", errors.New("EACCES: permission denied"), false},
		{"corrupt json", errors.New("SyntaxError: Unexpected token }"), false},
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
		t.Error("Registered=true without binary: it would claim a write that did not happen")
	}
	if r.Url != "" {
		t.Errorf("Url = %q without binary", r.Url)
	}
	if r.Name != "svc" {
		t.Errorf("Name = %q, want svc even degraded", r.Name)
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
			t.Errorf("port=%d: Registered=true without confirmed port", port)
		}
		if len(f.calls) != 0 {
			t.Errorf("port=%d: the binary was invoked without a port: %v", port, f.calls)
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
		t.Errorf("Status = %q, want degraded", r.Status)
	}
	if !r.Registered {
		t.Error("Registered was lost after a read failure: the route was written and would remain orphaned")
	}
	if r.Url != "" {
		t.Errorf("Url = %q without being able to verify", r.Url)
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
		t.Errorf("Status = %q, want degraded", r.Status)
	}
	if r.Reason != ReasonProxyNotRunning {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonProxyNotRunning)
	}
	if !r.Registered {
		t.Error("Registered was lost with the proxy stopped: the registration happened (M1, the binary does not contact it)")
	}
	if r.Url != "" {
		t.Errorf("Url = %q without proxy", r.Url)
	}
	if _, ok := f.routes[Hostname("svc")]; !ok {
		t.Error("the route was not written in the binary, but the Result says Registered")
	}
}

func TestApplyPublicaCuandoElProxyEnruta(t *testing.T) {
	f := newFake()
	c := f.client(t)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusRegistered {
		t.Fatalf("Status = %q (reason %q), want registered: %v", r.Status, r.Reason, f.calls)
	}
	if !strings.HasPrefix(r.Url, "http") {
		t.Errorf("Url = %q, want an http/https scheme", r.Url)
	}
	if !strings.HasSuffix(r.Url, Hostname("svc")) {
		t.Errorf("Url = %q does not end with the route hostname", r.Url)
	}
	if r.Reason != "" {
		t.Errorf("Reason = %q on a correct registration", r.Reason)
	}
	if !r.Registered {
		t.Error("Registered=false on a correct registration")
	}
}

func TestWithReasonConservaElHechoYQuitaLaUrl(t *testing.T) {
	base := Result{
		Name: "svc", Host: "svc.localhost", Port: 8080,
		Status: StatusRegistered, Url: "https://svc.localhost", Registered: true,
	}

	got := base.withReason(ReasonProxyNotRunning)

	if got.Status != StatusDegraded {
		t.Errorf("Status = %q, want degraded", got.Status)
	}
	if got.Reason != ReasonProxyNotRunning {
		t.Errorf("Reason = %q", got.Reason)
	}
	if got.Url != "" {
		t.Errorf("Url = %q: degrading must remove the unverified URL", got.Url)
	}
	if !got.Registered {
		t.Error("Registered was lost when degrading: the write happened")
	}
	if got.Name != base.Name || got.Host != base.Host || got.Port != base.Port {
		t.Errorf("withReason lost identity: %+v", got)
	}
	// It must not mutate the receiver: callers reuse the original for both branches.
	if base.Status != StatusRegistered || base.Url == "" {
		t.Errorf("withReason mutated the original receiver: %+v", base)
	}
}

func TestReleaseConNombreVacioRevocaSinTocarNada(t *testing.T) {
	called := false
	r := ReleaserFunc(func(string) error { called = true; return nil })

	if !Release(r, "") {
		t.Error("without a name there is nothing to remove: it must revoke")
	}
	if called {
		t.Error("the releaser was called with an empty name")
	}
}

// ErrRouteAbsent revokes because the route is gone, which is what was wanted; a real failure does not, or the route stays orphan with nobody to clean it.
func TestReleaseRevocaConRutaAusente(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"success", nil, true},
		{"benign M10: did not exist", ErrRouteAbsent, true},
		{"wrapped, still the same", wrappedRouteAbsent(), true},
		{"real failure", errors.New("requires Node >= 24"), false},
		{"permissions", errors.New("EACCES"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Release(failingReleaserWith{err: tt.err}, "mi-ruta"); got != tt.want {
				t.Errorf("Release with %v = %v, want %v", tt.err, got, tt.want)
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
		t.Errorf("the adapter does not return the function's error: %v", err)
	}
	if got != "x" {
		t.Errorf("the function received %q, want x", got)
	}
}

// InertReleaser is the default under a test binary so a test that forgets the seam cannot mutate the developer's routes.json.
func TestInertReleaserNoTocaNadaYEsEstatico(t *testing.T) {
	r := InertReleaser()
	if err := r.RemoveAbsent("cualquier-cosa"); err != nil {
		t.Errorf("the inert releaser returned %v, must always be nil", err)
	}
	// Identity is not compared on purpose: ReleaserFunc is a func type, so == would not compile.
	for _, name := range []string{"", "a", "ruta.larga.localhost"} {
		if err := InertReleaser().RemoveAbsent(name); err != nil {
			t.Errorf("InertReleaser().RemoveAbsent(%q) = %v, must always be nil", name, err)
		}
	}
	assertImplementsReleaser(t, InertReleaser())
}

func TestIsTestBinaryDetectaElBinarioDeTest(t *testing.T) {
	if !IsTestBinary() {
		t.Error("IsTestBinary() is false when running under `go test`: the InertReleaser guard protects nothing")
	}
	orig := os.Args[0]
	t.Cleanup(func() { os.Args[0] = orig })

	os.Args[0] = "/usr/local/bin/vroom"
	if IsTestBinary() {
		t.Error("the production binary is identified as test")
	}
	os.Args[0] = "/tmp/vroom.test"
	if !IsTestBinary() {
		t.Error("a .test binary should be identified as test")
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
		{"without route_mode (default off)", &manifest.Manifest{Name: "svc"}, false},
		{"route_mode off", &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeOff}, false},
		{"invalid route_mode", &manifest.Manifest{Name: "svc", RouteMode: "invented"}, false},
		{"route_mode named", &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeNamedWithAutoFallback, RouteName: "svc"}, true},
		{"route_mode auto", &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeAuto}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClientFor(tt.m)
			if tt.want && got == nil {
				t.Error("ClientFor returned nil with an active route contract")
			}
			if !tt.want && got != nil {
				t.Error("ClientFor returned a client without a route contract: portless would be searched in a project that did not ask for it")
			}
		})
	}
}

// MEASURED (2026-10-07): before this gate, internal/tui's start test came through the register gateway and exec'd the
// real portless binary, rewriting the developer's ~/.portless/routes.json on every `go test` run.
func TestClientForNeverResolvesTheRealBinaryInATestBinary(t *testing.T) {
	if !IsTestBinary() {
		t.Skip("not a test binary: the case does not apply")
	}
	t.Setenv("PORTLESS_BIN", "/usr/bin/portless")
	t.Setenv("PORTLESS_STATE_DIR", t.TempDir())

	c := ClientFor(&manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeAuto})
	if c == nil {
		t.Fatal("an active route contract must return a client, not nil")
	}
	if c.HasBinary() {
		t.Error("in a test binary ClientFor must not resolve a binary: a test would exec the developer's real portless")
	}
	if res := c.Apply("svc", 4321, Ownership{}); res.Status != StatusDegraded || res.Reason != ReasonPortlessMissing {
		t.Errorf("the bin-less client must degrade with %s, got %+v", ReasonPortlessMissing, res)
	}

	// The installed binary keeps resolving: argv[0] is the same switch the release-path guards use.
	orig := os.Args[0]
	t.Cleanup(func() { os.Args[0] = orig })
	os.Args[0] = "/usr/local/bin/vroom"

	prod := ClientFor(&manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeAuto})
	if prod == nil || !prod.HasBinary() || prod.Binary() != "/usr/bin/portless" {
		t.Errorf("outside a test binary ClientFor must resolve the binary, got %+v", prod)
	}
}

// MEASURED on 0.15.6: PORTLESS_HOME and XDG_STATE_HOME are ignored; honouring XDG read proxy.port from a directory the
// binary never writes, degrading every route to proxy_not_running.
func TestResolveStateDirSigueElOrdenMedido(t *testing.T) {
	t.Run("PORTLESS_STATE_DIR takes precedence", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "/opt/pl-state")
		t.Setenv("XDG_STATE_HOME", "/xdg")
		t.Setenv("HOME", "/home/u")
		if got := ResolveStateDir(); got != "/opt/pl-state" {
			t.Errorf("ResolveStateDir = %q, want /opt/pl-state", got)
		}
	})

	t.Run("XDG_STATE_HOME is ignored by portless", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "/xdg")
		t.Setenv("HOME", "/home/u")
		if got := ResolveStateDir(); got != "/home/u/.portless" {
			t.Errorf("ResolveStateDir = %q, want /home/u/.portless: honouring XDG pointed vroom at a directory the CLI never writes, so every route degraded to %s", got, ReasonProxyNotRunning)
		}
	})

	t.Run("HOME as last resort", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "/home/u")
		if got := ResolveStateDir(); got != "/home/u/.portless" {
			t.Errorf("ResolveStateDir = %q, want /home/u/.portless", got)
		}
	})

	t.Run("with nothing returns empty and not an invented path", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "")
		if got := ResolveStateDir(); got != "" {
			t.Errorf("ResolveStateDir = %q: a concrete user path would be a silent failure mode", got)
		}
	})
}

// MEASURED (M14): `env -i PATH=/usr/bin:/bin` does not resolve portless because it lives behind the mise shims, hence the third step after $PORTLESS_BIN and LookPath.
func TestResolveBinarySigueElOrdenMedido(t *testing.T) {
	t.Run("PORTLESS_BIN takes precedence", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "/opt/bin/portless")
		if got := ResolveBinary(); got != "/opt/bin/portless" {
			t.Errorf("ResolveBinary = %q, want /opt/bin/portless", got)
		}
	})

	t.Run("mise shim when it is not in PATH", func(t *testing.T) {
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
			t.Errorf("ResolveBinary = %q, want the shim %q: with empty PATH this is the only real path", got, shim)
		}
	})

	t.Run("a shim that is a directory does not count", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "")
		t.Setenv("PATH", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		shimDir := filepath.Join(home, ".local", "bin")
		if err := os.MkdirAll(filepath.Join(shimDir, "portless"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := ResolveBinary(); got != "" {
			t.Errorf("ResolveBinary = %q, a directory is not a binary", got)
		}
	})

	t.Run("nothing resolvable returns empty", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "")
		t.Setenv("PATH", "")
		t.Setenv("HOME", t.TempDir())
		if got := ResolveBinary(); got != "" {
			t.Errorf("ResolveBinary = %q, want empty (degradation, not error)", got)
		}
	})
}

func TestDefaultConstruyeClienteConLoResuelto(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "/opt/bin/portless")
	t.Setenv("PORTLESS_STATE_DIR", "/opt/pl-state")

	c := Default()

	if !c.HasBinary() {
		t.Fatal("HasBinary() = false with PORTLESS_BIN set")
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
		t.Fatal("Default returned nil: without binary is a degradation, not an error")
	}
	if c.HasBinary() {
		t.Error("HasBinary() = true without a resolvable binary")
	}
	if c.Binary() != "" {
		t.Errorf("Binary() = %q, want empty", c.Binary())
	}
}

func TestDefaultRespetaLasOpcionesSobreElEntorno(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "/opt/bin/portless")
	t.Setenv("PORTLESS_STATE_DIR", "/opt/pl-state")

	c := Default(WithBinary("/forzado/portless"), WithStateDir("/forzado/state"))

	if c.Binary() != "/forzado/portless" {
		t.Errorf("Binary() = %q, the explicit option should win over the environment", c.Binary())
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
	return fmt.Errorf("verifying the route: %w", ErrProxyNotRunning)
}

func wrappedRouteAbsent() error {
	return fmt.Errorf("removing: %w", ErrRouteAbsent)
}

// A compile-time check would be `var _ Releaser = r`, but staticcheck QF1011 rejects that form; this has the same effect.
func assertImplementsReleaser(t *testing.T, r Releaser) {
	t.Helper()
	if r == nil {
		t.Fatal("InertReleaser returned nil")
	}
}
