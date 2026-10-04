package portless

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A real server, not a double: what these tests check is whether verify tells 404 from 502, which needs a real status.
func newHTTPServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeFileIn(t *testing.T, dir, name, content string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// Declares proxyPort in a real state dir and keeps the real httpProbe: the behaviour of verify, not a double the test decides.
func newClientWithProxy(t *testing.T, proxyPort int, exec func(context.Context, string, ...string) (string, int, error)) *Client {
	t.Helper()
	dir := t.TempDir()
	if proxyPort > 0 {
		if err := writeFileIn(t, dir, proxyPortFile, itoa(proxyPort)); err != nil {
			t.Fatal(err)
		}
	}
	return New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(exec),
		WithTimeout(2*time.Second),
	)
}

func okExec(t *testing.T) func(context.Context, string, ...string) (string, int, error) {
	t.Helper()
	f := newFake()
	return f.exec
}

func srvPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("puerto de %q no numerico: %v", srv.URL, err)
	}
	return n
}

func TestVerifyPublicaCuandoElProxyEnrutaDeVerdad(t *testing.T) {
	srv := newHTTPServer(t, http.StatusOK)
	c := newClientWithProxy(t, srvPort(t, srv), okExec(t))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusRegistered {
		t.Fatalf("Status = %q (motivo %q): con el proxy enrutando deberia publicar. %s", r.Status, r.Reason, srvCalls(r))
	}
	if !strings.HasPrefix(r.Url, "http://") {
		t.Errorf("Url = %q: sin TLS el esquema publicado tiene que ser http", r.Url)
	}
	if !strings.Contains(r.Url, Hostname("svc")) {
		t.Errorf("Url = %q no contiene el hostname de la ruta", r.Url)
	}
}

// verify must degrade BEFORE probing (M6): probing without a proxy means a request to an assumed port, the very constant this seam must not introduce.
func TestVerifyNoSondeaConProxyParado(t *testing.T) {
	f := newFake()
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(t.TempDir()),
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(2*time.Second),
	)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Reason != ReasonProxyNotRunning {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonProxyNotRunning)
	}
	if r.Url != "" {
		t.Errorf("Url = %q sin proxy en marcha", r.Url)
	}
	if !r.Registered {
		t.Error("Registered se perdio: el alta ocurrio igual (M1)")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "probe:") {
			t.Errorf("se sondeo sin saber que hay proxy: %v", f.calls)
			break
		}
	}
}

// MEASURED: 404 means "I do not know this host", the opposite of routed, so publishing there asserts an address the proxy does not serve.
func TestVerifyDegradaSinPublicarURLCuandoElProxyNoSirveLaRuta(t *testing.T) {
	srv := newHTTPServer(t, http.StatusNotFound)
	c := newClientWithProxy(t, srvPort(t, srv), okExec(t))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado con un proxy que responde 404", r.Status)
	}
	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q: 404 prueba que el proxy NO enruta la ruta", r.Url)
	}
}

func TestVerifyAceptaEl502ComoPruebaDeEnrutado(t *testing.T) {
	srv := newHTTPServer(t, http.StatusBadGateway)
	c := newClientWithProxy(t, srvPort(t, srv), okExec(t))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusRegistered {
		t.Errorf("Status = %q (motivo %q): un 502 prueba que el proxy enruta. %s", r.Status, r.Reason, srvCalls(r))
	}
	if r.Url == "" {
		t.Error("un 502 no debe impedir publicar la URL: la ruta esta enrutada")
	}
}

// A declared port nobody listens on is a different failure from "the proxy answers and does not know the route", so they degrade with different reasons.
func TestVerifyDegradaCuandoElPuertoDeclaradoNoAceptaConexiones(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	f := newFake()
	f.noProxy = true
	c := newClientWithProxy(t, deadPort, f.exec)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if r.Reason != ReasonProxyUnreachable {
		t.Errorf("Reason = %q, want %q: el puerto declarado no acepta conexiones", r.Reason, ReasonProxyUnreachable)
	}
	if !r.Registered {
		t.Error("Registered se perdio: la escritura ocurrio antes de sondear")
	}
}

// Both cases end in "no scheme answered" and are separated only by acceptsConnections; without it the user cannot tell a stopped portless from a missing route.
func TestVerifyDistingueProxyVivoQueNoSirveDePuertoMuerto(t *testing.T) {
	srv := newHTTPServer(t, http.StatusNotFound)

	f := newFake()
	f.serve404[Hostname("svc")] = true
	c := newClientWithProxy(t, srvPort(t, srv), f.exec)

	r := c.Apply("svc", 8080, Ownership{})

	// acceptsConnections is only consulted when no scheme answered, so a 404 never reaches it.
	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q sin enrutado probado", r.Url)
	}
}

// prev not authorising that port is what makes M8 observable: if it did authorise it, the route is ours and rewriting it is legitimate.
func TestApplyReportaConflictoCuandoElNombreLoTieneOtroPuerto(t *testing.T) {
	t.Run("sin autorizacion previa: conflicto y no se escribe", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		if err := c.Register("svc", 9999); err != nil {
			t.Fatal(err)
		}
		before := f.routes[Hostname("svc")]

		r := c.Apply("svc", 8080, Ownership{})

		if r.Reason != ReasonRouteConflict {
			t.Errorf("Reason = %q, want %q", r.Reason, ReasonRouteConflict)
		}
		if r.Registered {
			t.Error("Registered=true en un conflicto: se afirmaria una escritura que no ocurrio")
		}
		if got := f.routes[Hostname("svc")]; got != before {
			t.Errorf("el puerto ajeno cambio de %d a %d: no se debe tocar la ruta de otro", before, got)
		}
	})

	t.Run("con autorizacion previa del mismo puerto: se reescribe", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		if err := c.Register("svc", 8080); err != nil {
			t.Fatal(err)
		}

		r := c.Apply("svc", 8080, Ownership{Owned: true, Port: 8080})

		if r.Reason == ReasonRouteConflict {
			t.Error("una ruta que ya es nuestra no es un conflicto consigo misma")
		}
		if !r.Registered {
			t.Errorf("Reason = %q y no se publico: %v", r.Reason, srvCalls(r))
		}
	})
}

// The prior lookup decides whether the write is legitimate: writing without it is exactly the harm M8 allows, taking another owner's name unknowingly.
func TestApplyNoEscribeSiNoSePuedeLeerElEstadoPrevio(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if err := c.Register("svc", 9999); err != nil {
		t.Fatal(err)
	}
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			return "", 0, errors.New("portless list: routes.json unreadable")
		}
		return f.exec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Registered {
		t.Error("Registered=true: se escribio sin poder leer contra que")
	}
	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if got := f.routes[Hostname("svc")]; got != 9999 {
		t.Errorf("el puerto ajeno quedo en %d, want 9999 sin tocar: no se debia escribir", got)
	}
}

// The faithful fake cannot express this, its list sees what its alias wrote, so the exec writes but lists something else: the real failure to catch.
func TestApplyDegradaSiLaLecturaDeVueltaNoLaVe(t *testing.T) {
	f := newFake()
	c := f.client(t)
	realExec := f.exec
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			return "\nActive routes:\n\n", 0, nil
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q: el alta salio bien pero la ruta no aparece", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q: no se puede publicar una ruta que no se ve", r.Url)
	}
	if !r.Registered {
		t.Error("Registered se perdio: el alias salio 0, luego la escritura ocurrio")
	}
}

// M8 live: the prior lookup sees a free name, another actor takes it in between, and reading back after writing would be a tautology.
func TestApplyDetectaElCambioDeDueñoEntreLaConsultaYElAlta(t *testing.T) {
	f := newFake()
	c := f.client(t)
	realExec := f.exec
	calls := 0
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			calls++
			if calls == 1 {
				// Prior lookup: the name is free.
				return "\nActive routes:\n\n", 0, nil
			}
			// Read-back: another actor took it.
			host := Hostname("svc")
			return "  http://" + host + ":1355  ->  localhost:9999  (alias)\n", 0, nil
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Reason != ReasonRouteConflict {
		t.Errorf("Reason = %q, want %q: el nombre cambio de dueño entre medias", r.Reason, ReasonRouteConflict)
	}
	if r.Url != "" {
		t.Errorf("Url = %q con un conflicto sin resolver", r.Url)
	}
	// Degrading the status cannot undo the write: without Registered the route stays orphan and reconciliation cannot clean it.
	if !r.Registered {
		t.Error("Registered se perdio: el alta ocurrio (upsert incondicional, M8)")
	}
}

func TestApplyPropagaElFalloDelAltaSinAfirmarNada(t *testing.T) {
	f := newFake()
	c := f.client(t)
	realExec := f.exec
	// Only alias fails: if exec failed everywhere the failure would belong to the prior lookup and the test would never reach the write.
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "alias" {
			return "", 1, errors.New("Error: requires Node >= 24")
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if r.Registered {
		t.Error("Registered=true con el alta fallida: se afirmaria una escritura que no ocurrio")
	}
	if r.Reason == "" {
		t.Error("Reason vacio tras un fallo del alta: el usuario no sabria que paso")
	}
	if r.Url != "" {
		t.Errorf("Url = %q con el alta fallida", r.Url)
	}
}

// The r-nil branch builds the real client, which production stop paths use and cannot be exercised against a real portless, so it is checked with no resolvable binary.
func TestReleaseConReleaserNilConstruyeElClienteReal(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PORTLESS_STATE_DIR", t.TempDir())

	if !Release(nil, "") {
		t.Error("nombre vacio debe revocar sin construir cliente")
	}

	// With a name but no portless, all that matters is that the real client does not blow up.
	got := Release(nil, "mi-ruta")
	if !got {
		t.Log("sin portless no se puede afirmar nada sobre la ruta: no revoca, y el handle se conserva para reconciliar")
	}
}

// The Result as text for the failure message: tests in this file must fail with the reason, not with a bare "want registered".
func srvCalls(r Result) string {
	return "motivo=" + r.Reason + " url=" + r.Url
}
