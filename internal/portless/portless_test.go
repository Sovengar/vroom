package portless

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePortless mimics portless 0.15.6 as MEASURED because CI has neither the binary nor a proxy, yet the suite must still demand the verification.
type fakePortless struct {
	routes map[string]int

	// binCode is the `alias` exit code; the measured case is 0 with the proxy stopped, because alias never contacts it (M1).
	binCode int
	binErr  error
	// hang blocks the call until the context expires: a hung portless.
	hang bool

	// probeStatus is what the proxy answers for a host it routes: 200, or 502 for routed-but-dead-backend; no proxy is noProxy, and -1 is never set.
	probeStatus int
	// tls lets the fake answer https; off by default because a proxy with HTTPS=0 refuses the handshake (measured, curl exits 35).
	tls bool
	// noProxy makes every probe get connection refused.
	noProxy bool

	calls []string
	// serve404 hosts are in the state file but not routed, so a live proxy that skips them stays distinguishable from a healthy one.
	serve404 map[string]bool
	// removedNames records removals, which reconciliation must observe because with the proxy stopped no probe reaches the fake.
	removedNames []string
}

func newFake() *fakePortless {
	return &fakePortless{
		routes:      map[string]int{},
		binCode:     0,
		probeStatus: 200,
		serve404:    map[string]bool{},
	}
}

func (f *fakePortless) exec(ctx context.Context, bin string, args ...string) (string, int, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.hang {
		<-ctx.Done()
		// The real seam wraps the context error so errors.Is works; breaking that would test the fake, not the seam.
		return "", -1, ctx.Err()
	}
	if f.binErr != nil {
		return "", 1, f.binErr
	}
	switch args[0] {
	case "alias":
		if len(args) >= 2 && args[1] == "--remove" {
			name := Hostname(args[2])
			if _, ok := f.routes[name]; !ok {
				// MEASURED (M10): exit 1 and BENIGN but not a failure: the route is gone, and Remove must tell it from a real one.
				return "", 1, errors.New("Error: No alias found for \"" + name + "\".")
			}
			delete(f.routes, name)
			f.removedNames = append(f.removedNames, name)
			return "Removed alias: " + name, 0, nil
		}
		// Unconditional upsert with no conflict detection (M8), so exit 0 proves nothing.
		var port int
		for _, c := range args[2:] {
			if n := atoiOr(c, -1); n > 0 {
				port = n
			}
		}
		if port <= 0 {
			return "Error: Invalid port", 1, errors.New("invalid port")
		}
		f.routes[Hostname(args[1])] = port
		return "Alias registered", f.binCode, nil
	case "list":
		var b strings.Builder
		b.WriteString("\nActive routes:\n\n")
		for host, port := range f.routes {
			b.WriteString("  http://" + host + ":1355  ->  localhost:" + itoa(port) + "  (alias)\n")
		}
		return b.String(), 0, nil
	}
	return "", 1, errors.New("unknown subcommand")
}

func (f *fakePortless) probe(ctx context.Context, scheme, host string, proxyPort int, path string) (int, error) {
	f.calls = append(f.calls, "probe:"+scheme+":"+host)
	if scheme == "https" && !f.tls {
		return 0, errors.New("tls: handshake failure")
	}
	if f.noProxy {
		return 0, errors.New("connection refused")
	}
	if _, ok := f.routes[host]; !ok || f.serve404[host] {
		// MEASURED: 404 for a host it does not know is the opposite of proof of routing; serve404 models a written but abandoned route.
		return 404, nil
	}
	if f.probeStatus == 502 {
		return 502, nil
	}
	return 200, nil
}

func (f *fakePortless) client(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	f.writeProxyPort(t, dir, 1399)
	return New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(2*time.Second),
	)
}

func (f *fakePortless) writeProxyPort(t *testing.T, dir string, port int) {
	t.Helper()
	// MEASURED: 4 bytes, no trailing newline.
	if err := os.WriteFile(filepath.Join(dir, "proxy.port"), []byte(itoa(port)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func atoiOr(s string, def int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// REGRESSION (M5): an alias route has pid 0 and `portless prune` never touches it, so startup reconciliation is the only cleanup vroom has.
func TestPruneDoesNotDestroyAliasRoutes(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("foreign.vroom")] = 9999

	if pid := aliasRoutePid; pid != 0 {
		t.Fatalf("una ruta de alias debe tener pid 0 para que prune no la toque, got %d", pid)
	}

	warns := c.Reconcile("foreign.vroom", Ownership{Owned: true, Port: 4321}, "mine.vroom")
	if len(warns) == 0 {
		t.Error("una ruta viva en otro puerto debe avisar, no retirarse en silencio")
	}
	if _, still := f.routes[Hostname("foreign.vroom")]; !still {
		t.Error("una ruta que responde y no es nuestra NUNCA se retira")
	}
}

// aliasRoutePid is the pid portless writes for an alias route (measured, M5/M4): 0 is what keeps prune away from it.
const aliasRoutePid = 0

func TestRouteWrittenWithProxyDownIsNotReportedAvailable(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.noProxy = true

	res := c.Apply("down.vroom", 4321, Ownership{})

	if res.Status != StatusDegraded {
		t.Fatalf("una ruta escrita con el proxy parado NO puede reportarse disponible, got %q", res.Status)
	}
	if res.Url != "" {
		t.Errorf("una ruta no verificada no publica url, got %q", res.Url)
	}
	if res.Reason != ReasonProxyNotRunning && res.Reason != ReasonProxyUnreachable {
		t.Errorf("el motivo debe explicar que el proxy no responde, got %q", res.Reason)
	}

	// The binary still exits 0, so this test must assert the Result and not the exit code.
	if _, code, _ := f.exec(context.Background(), "portless", "alias", "down.vroom", "4321"); code != 0 {
		t.Fatalf("alias debe salir 0 con el proxy parado (M1), got %d", code)
	}
}

// MEASURED (M2): a routes.json entry is not verification: the proxy answers 404 to a host it does not know, and publishing that lies.
func TestRoutesFileAloneIsNotVerification(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("unknown.vroom")] = 4321

	f.noProxy = false
	c2 := New(
		WithBinary("/fake/portless"),
		WithStateDir(c.stateDir),
		WithExec(f.exec),
		WithProbe(func(ctx context.Context, scheme, host string, port int, path string) (int, error) {
			return 404, nil
		}),
	)
	res := c2.Apply("written-but-not-served.vroom", 4321, Ownership{})
	if res.Succeeded() {
		t.Error("un 404 significa que el proxy no enruta: no puede reportarse registered")
	}
	if res.Url != "" {
		t.Error("un 404 no publica url")
	}
}

// REGRESSION (M6): proxy.port exists only while the proxy runs, so its absence IS the no-proxy signal; 1355 would also hardcode a movable port.
func TestMissingProxyPortDegradesWithoutAssuming1355(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if err := os.Remove(filepath.Join(c.stateDir, "proxy.port")); err != nil {
		t.Fatal(err)
	}

	res := c.Apply("noproxy.vroom", 4321, Ownership{})
	if res.Reason != ReasonProxyNotRunning {
		t.Fatalf("sin proxy.port el motivo debe ser proxy_not_running, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("sin proxy no se publica url")
	}
	for _, call := range f.calls {
		if strings.Contains(call, ":1355") && strings.HasPrefix(call, "probe") {
			t.Errorf("no se puede suponer el puerto del proxy, pero se sondeó %q", call)
		}
	}
}

func TestCorruptProxyPortDegrades(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if err := os.WriteFile(filepath.Join(c.stateDir, "proxy.port"), []byte("no-es-un-puerto"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := c.Apply("corrupt.vroom", 4321, Ownership{})
	if res.Reason != ReasonProxyNotRunning {
		t.Errorf("un proxy.port ilegible debe degradar como proxy parado, got %q", res.Reason)
	}
}

func TestReconcileRemovesRenamedOrphanAndKeepsForeignLiveRoute(t *testing.T) {
	t.Run("retira la huérfana que ya no responde", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("old-name")] = 39999
		f.probeStatus = 502
		f.routes[Hostname("new-name")] = 4321

		f.routes[Hostname("new-name")] = 4321
		c.probe = func(ctx context.Context, scheme, host string, port int, path string) (int, error) {
			if host == Hostname("old-name") {
				return 404, nil
			}
			return 200, nil
		}

		warns := c.Reconcile("old-name", Ownership{Owned: true, Port: 39999}, "new-name")
		if len(warns) != 0 {
			t.Errorf("retirar una huérfana propia no avisa: %v", warns)
		}
		if _, still := f.routes[Hostname("old-name")]; still {
			t.Error("la ruta renombrada debe retirarse: es de este servicio y no responde")
		}
	})

	t.Run("NO retira una ruta viva ajena", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("other-app")] = 5555
		f.routes[Hostname("mine")] = 4321

		// Ownership.Port is the persisted one, so a live route on another port is not ours.
		warns := c.Reconcile("other-app", Ownership{Owned: true, Port: 4321}, "mine")
		if len(warns) == 0 {
			t.Error("una ruta viva en otro puerto debe avisar del conflicto")
		}
		if _, still := f.routes[Hostname("other-app")]; !still {
			t.Error("el fallo cerrado también aplica a la limpieza: no se borra algo ajeno")
		}
	})

	t.Run("retira la huérfana enruta pero sin backend", func(t *testing.T) {
		// A real smoke found this: the proxy routes and answers 502 with nothing behind, the fingerprint of a vroom that died without stopping.
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("orphan")] = 39997
		f.probeStatus = 502

		if warns := c.Reconcile("orphan", Ownership{Owned: true, Port: 39997}, "current"); len(warns) != 0 {
			t.Errorf("retirar una huérfana propia no avisa: %v", warns)
		}
		if _, still := f.routes[Hostname("orphan")]; still {
			t.Error("una ruta enruta sin backend es una huérfana y debe retirarse")
		}
	})

	t.Run("NO retira una ruta viva y propia", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("mine")] = 4321
		f.probeStatus = 200

		if warns := c.Reconcile("mine", Ownership{Owned: true, Port: 4321}, "other"); len(warns) != 0 {
			t.Errorf("una ruta viva y propia no genera avisos: %v", warns)
		}
		if _, still := f.routes[Hostname("mine")]; !still {
			t.Error("una ruta viva y propia no se toca")
		}
	})

	t.Run("es idempotente con la ruta persistida", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("same")] = 4321
		for range 3 {
			if warns := c.Reconcile("same", Ownership{Owned: true, Port: 4321}, "same"); len(warns) != 0 {
				t.Errorf("la reconciliación debe ser silenciosa e idempotente: %v", warns)
			}
		}
		if len(f.routes) != 1 {
			t.Errorf("no deben acumularse rutas duplicadas: %v", f.routes)
		}
	})
}

// M8: `alias` overwrites silently, so the other port must appear AFTER our write for the conflict to be observable.
func TestReadBackDetectsNameTakenByAnotherPort(t *testing.T) {
	f := newFake()
	c := f.client(t)

	var stolen bool
	readBack := func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if len(args) > 0 && args[0] == "list" && !stolen {
			stolen = true
			f.routes[Hostname("taken")] = 9999
		}
		return f.exec(ctx, bin, args...)
	}
	c.exec = readBack

	res := c.Apply("taken", 4321, Ownership{})
	if res.Succeeded() {
		t.Fatal("una ruta cuyo nombre tiene otro puerto NO puede reportarse registrada")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("el motivo debe ser route_conflict, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("en conflicto no se publica url")
	}
}

func TestReadBackConfirmsOurs(t *testing.T) {
	f := newFake()
	c := f.client(t)
	res := c.Apply("mine.vroom", 4321, Ownership{})
	if !res.Succeeded() {
		t.Fatalf("la lectura de vuelta debía confirmar la ruta: %+v", res)
	}
	if res.Port != 4321 {
		t.Errorf("la ruta debe apuntar al puerto real, got %d", res.Port)
	}
	if !strings.HasPrefix(res.Url, "http://") && !strings.HasPrefix(res.Url, "https://") {
		t.Errorf("la url debe llevar el esquema comprobado, got %q", res.Url)
	}
}

func TestBackendErrorStillCountsAsRouted(t *testing.T) {
	f := newFake()
	f.probeStatus = 502
	c := f.client(t)

	res := c.Apply("502.vroom", 4321, Ownership{})
	if !res.Succeeded() {
		t.Fatalf("un 502 prueba que el proxy enruta la ruta: %+v", res)
	}
	if res.Url == "" {
		t.Error("una ruta que enruta publica su url")
	}
}

func TestSchemeIsProbedNotAssumed(t *testing.T) {
	t.Run("http responde primero", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		res := c.Apply("plain.vroom", 4321, Ownership{})
		if !strings.HasPrefix(res.Url, "http://") {
			t.Errorf("un proxy sin TLS debe publicar http, got %q", res.Url)
		}
	})

	t.Run("https responde y se publica https", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		only := New(
			WithBinary("/fake/portless"),
			WithStateDir(c.stateDir),
			WithExec(f.exec),
			WithProbe(func(ctx context.Context, scheme, host string, port int, path string) (int, error) {
				if scheme == "https" {
					return 200, nil
				}
				return 0, errors.New("connection refused")
			}),
		)
		res := only.Apply("tls.vroom", 4321, Ownership{})
		if !strings.HasPrefix(res.Url, "https://") {
			t.Errorf("si https responde se publica https, got %q", res.Url)
		}
	})
}

func TestMissingBinaryDegradesWithoutInvokingPortless(t *testing.T) {
	called := false
	c := New(WithBinary(""), WithExec(func(context.Context, string, ...string) (string, int, error) {
		called = true
		return "", 0, nil
	}), WithStateDir(t.TempDir()))

	res := c.Apply("nobin.vroom", 4321, Ownership{})
	if res.Reason != ReasonPortlessMissing {
		t.Errorf("sin binario el motivo debe ser portless_not_found, got %q", res.Reason)
	}
	if called {
		t.Error("sin binario no se invoca a portless en absoluto")
	}
}

func TestFailingBinaryDegrades(t *testing.T) {
	f := newFake()
	f.binErr = errors.New("Error: requires Node >= 24")
	c := f.client(t)

	res := c.Apply("nodeold.vroom", 4321, Ownership{})
	if res.Reason != ReasonPortlessFailed {
		t.Errorf("un binario que falla debe degradar, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("un binario que falla no publica url")
	}
	if !strings.Contains(Warn(res), "Node") {
		t.Errorf("el aviso debe nombrar el fallo concreto, got %q", Warn(res))
	}
}

func TestHangingBinaryIsBounded(t *testing.T) {
	f := newFake()
	f.hang = true
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(t.TempDir()),
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(150*time.Millisecond),
	)

	done := make(chan Result, 1)
	go func() { done <- c.Apply("hang.vroom", 4321, Ownership{}) }()

	select {
	case res := <-done:
		if res.Reason != ReasonPortlessTimeout {
			t.Errorf("un binario colgado debe degradar por timeout, got %q", res.Reason)
		}
		if res.Succeeded() {
			t.Error("un binario colgado no puede publicar una ruta")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Apply NO puede quedar bloqueado esperando al binario")
	}
}

func TestMissingBinaryIsDistinguishedFromFailingBinary(t *testing.T) {
	t.Run("binario inexistente", func(t *testing.T) {
		c := New(
			WithBinary(filepath.Join(t.TempDir(), "no-existe")),
			WithStateDir(t.TempDir()),
			WithTimeout(2*time.Second),
		)
		res := c.Apply("nada.vroom", 4321, Ownership{})
		if res.Reason != ReasonPortlessMissing {
			t.Errorf("un binario que no está debe ser portless_not_found, got %q", res.Reason)
		}
	})

	t.Run("binario que falla", func(t *testing.T) {
		f := newFake()
		f.binErr = errors.New("Error: requires Node >= 24")
		res := f.client(t).Apply("roto.vroom", 4321, Ownership{})
		if res.Reason != ReasonPortlessFailed {
			t.Errorf("un binario que sale con error debe ser portless_failed, got %q", res.Reason)
		}
	})
}

func TestRemoveMissingIsBenign(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if err := c.Remove("nunca-existio"); err != nil {
		t.Fatalf("quitar una ruta inexistente no puede ser un error: %v", err)
	}
}

func TestRemoveOnlyTouchesItsOwn(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("mine")] = 4321
	f.routes[Hostname("sibling")] = 5555

	if err := c.Remove("mine"); err != nil {
		t.Fatal(err)
	}
	if _, still := f.routes[Hostname("mine")]; still {
		t.Error("la ruta propia debe desaparecer")
	}
	if _, sibling := f.routes[Hostname("sibling")]; !sibling {
		t.Error("las rutas de los servicios hermanos no se tocan")
	}
}

func TestFullRouteLifecycle(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("sibling")] = 5555

	res := c.Apply("app", 4321, Ownership{})
	if !res.Succeeded() {
		t.Fatalf("el registro debe verificarse contra el proxy vivo: %+v", res)
	}
	if _, found, _ := c.Lookup("app"); !found {
		t.Fatal("la ruta debe existir tras registrarla")
	}

	if err := c.Remove("app"); err != nil {
		t.Fatalf("parar debe retirar la ruta: %v", err)
	}
	if _, still := f.routes[Hostname("app")]; still {
		t.Error("la ruta debe desaparecer al parar")
	}
	if _, sib := f.routes[Hostname("sibling")]; !sib {
		t.Error("las rutas de los servicios hermanos no se tocan")
	}

	if err := c.Remove("app"); err != nil {
		t.Fatalf("un stop repetido no puede fallar: %v", err)
	}
	if _, sib := f.routes[Hostname("sibling")]; !sib {
		t.Error("el hermano sigue intacto tras un stop repetido")
	}
}

func TestReRegisterMovesTheRouteToTheNewPort(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("app", 4000, Ownership{}); !res.Succeeded() {
		t.Fatalf("primer arranque: %+v", res)
	}
	if res := c.Apply("app", 4321, Ownership{Owned: true, Port: 4000}); !res.Succeeded() {
		t.Fatalf("segundo arranque: %+v", res)
	}

	port, found, err := c.Lookup("app")
	if err != nil || !found {
		t.Fatalf("la ruta debe seguir existiendo: %v", err)
	}
	if port != 4321 {
		t.Errorf("la misma ruta debe pasar a apuntar al puerto nuevo, got %d", port)
	}
	if len(f.routes) != 1 {
		t.Errorf("no puede quedar más de una ruta para el mismo servicio: %v", f.routes)
	}
}

// SCOPE (M3): flipping noProxy on the same fake only proves file persistence; real restart survival lives in TestRouteSurvivesAProxyRestart.
func TestRouteRegisteredWhileProxyDownIsServedWhenItReturns(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.noProxy = true

	res := c.Apply("app", 4321, Ownership{})
	if res.Succeeded() {
		t.Fatal("con el proxy parado no se puede reportar disponible")
	}
	if _, found, _ := c.Lookup("app"); !found {
		t.Fatal("la ruta debe quedar registrada aunque el proxy esté parado")
	}

	f.noProxy = false
	if port, found, _ := c.Lookup("app"); !found || port != 4321 {
		t.Fatalf("al volver el proxy la ruta debe seguir ahí, got %d found=%v", port, found)
	}
	if res := c.verify("app", 4321); !res.Succeeded() {
		t.Errorf("con el proxy de vuelta la ruta debe verificarse: %+v", res)
	}
}

// SCOPE (M4): a map the seam only adds a key to cannot prove alias does not evict a run route; the claim lives in TestIntegrationDoesNotEvictLivePortlessRoutes.
func TestSeamTouchesOnlyItsOwnRoute(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("live-app")] = 4628

	if res := c.Apply("vroom-app", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("la ruta de vroom debe registrarse: %+v", res)
	}

	livePort, liveStill := f.routes[Hostname("live-app")]
	if !liveStill || livePort != 4628 {
		t.Errorf("la ruta de la app viva debe quedar intacta: %d %v", livePort, liveStill)
	}
	if _, vroom := f.routes[Hostname("vroom-app")]; !vroom {
		t.Error("la ruta de vroom debe existir")
	}
	_ = c.Remove("vroom-app")
	if _, still := f.routes[Hostname("live-app")]; !still {
		t.Error("parar lo nuestro no puede expulsar la ruta de otro dueño")
	}
}
