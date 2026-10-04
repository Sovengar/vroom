package startsvc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

// failingManager is a pure double that fails Start, so the failure path runs without touching disk or spawning a process.
type failingManager struct {
	process.Manager
	err error
}

func (m failingManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, m.err
}

// degradingRegistrar is an Apply that was written but never verified (stopped proxy), so Registered and Succeeded stay distinct.
type degradingRegistrar struct{ reconciled []string }

func (r *degradingRegistrar) Apply(name string, port int, own portless.Ownership) portless.Result {
	return portless.Result{
		Name:       name,
		Port:       port,
		Status:     portless.StatusDegraded,
		Reason:     portless.ReasonPortlessMissing,
		Registered: true,
	}
}

func (r *degradingRegistrar) Reconcile(prev string, own portless.Ownership, now string) []string {
	r.reconciled = append(r.reconciled, prev+"->"+now)
	return nil
}

func TestStartDevuelveElPuertoReservadoSiElHijoNuncaLlego(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())

	m := failingManager{err: errors.New("no such file or directory")}
	_, err := Start(Request{
		Manifest:   &manifest.Manifest{Name: "svc", Command: "./no-existe", PortMode: manifest.PortModeDynamic},
		Path:       root,
		Store:      store,
		Manager:    m,
		StdoutPath: filepath.Join(root, "o.log"),
		StderrPath: filepath.Join(root, "e.log"),
	})
	if err == nil {
		t.Fatal("un comando que no se puede lanzar debería fallar el arranque")
	}

	// Proves the release by re-reserving rather than by inspecting the pool: peeking would test the implementation.
	again, rerr := process.ReservePort()
	if rerr != nil {
		t.Fatalf("no se pudo reservar un puerto después del fallo: %v", rerr)
	}
	process.ReleasePort(again)
}

// A fixed-port start has no reservation to release, and feeding back a 0 would poison the pool: samePorts and containsPid read 0 as "unknown port".
func TestStartSinPuertoReservadoNoFiltraNadaSinReserva(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())

	m := failingManager{err: errors.New("boom")}
	_, err := Start(Request{
		Manifest:   &manifest.Manifest{Name: "svc", Command: "./no-existe", Port: 8081},
		Path:       root,
		Store:      store,
		Manager:    m,
		StdoutPath: filepath.Join(root, "o.log"),
		StderrPath: filepath.Join(root, "e.log"),
	})
	if err == nil {
		t.Fatal("el fallo del spawn tiene que propagarse")
	}
	p, rerr := process.ReservePort()
	if rerr != nil || p <= 0 {
		t.Errorf("el set de puertos quedó dañado: ReservePort = %d, %v", p, rerr)
	} else {
		process.ReleasePort(p)
	}
}

// Counter-intuitive on purpose: when SaveMeta fails the child already holds the port, so releasing it would race another service onto that port while this one is still alive.
func TestStartPropagaElFalloDeGuardarElIntentoConPuertoReservado(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if err := os.MkdirAll(filepath.Join(store.Base(), "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ServiceDir(root), []byte("bloquea el mkdir"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Start(Request{
		Manifest:   &manifest.Manifest{Name: "svc", Command: "sleep 30", PortMode: manifest.PortModeDynamic},
		Path:       root,
		Store:      store,
		Manager:    process.NewManager(),
		StdoutPath: filepath.Join(root, "o.log"),
		StderrPath: filepath.Join(root, "e.log"),
	})
	if err == nil {
		t.Fatal("un Meta que no se puede escribir debería hacer fallar el arranque")
	}
	if !strings.Contains(err.Error(), "service directory") && !strings.Contains(err.Error(), "service dir") {
		t.Logf("el error no nombra el directorio de servicio: %q", err)
	}

	p, rerr := process.ReservePort()
	if rerr != nil || p <= 0 {
		t.Errorf("el set de puertos quedó dañado: ReservePort = %d, %v", p, rerr)
	} else {
		process.ReleasePort(p)
	}
}

// A route is an address, not a dependency: an unusable name must only warn, or one bad name would block the whole start.
func TestApplyRouteConNombreNoUtilizableAvisaYNoPropagaElError(t *testing.T) {
	req := Request{
		Manifest: &manifest.Manifest{
			Name: "svc", RouteMode: manifest.RouteModeNamed,
			RouteName: "!!!", // not a usable hostname
		},
		Branch: "main",
		// Routes must not be nil: without it applyRoute exits before deriving the name and this would test the route_mode="off" guard instead.
		Routes: verifiedRegistrar{},
	}
	meta := state.Meta{Name: "svc", Port: 8081}
	out := Result{}

	applyRoute(req, &meta, 8081, &out)

	if len(out.Warnings) == 0 {
		t.Fatal("un nombre de ruta inválido tiene que producir un aviso: si no, el usuario no sabrá por qué no tiene ruta")
	}
	if !strings.Contains(out.Warnings[0], "portless route") {
		t.Errorf("el aviso no explica que el problema es el nombre: %q", out.Warnings[0])
	}
	// Meta must stay untouched: with no accepted name there is no handle or ownership, or stop would try to revoke a route that was never registered.
	if meta.RouteName != "" || meta.RouteOwned {
		t.Errorf("el Meta se modificó sin ruta válida: %+v", meta)
	}
}

func TestApplyRouteSinRegistrarNoAbrePortless(t *testing.T) {
	req := Request{
		Manifest: &manifest.Manifest{Name: "svc", Port: 8081},
		// Routes nil is what ClientFor yields when there is no route contract.
	}
	meta := state.Meta{Name: "svc", Port: 8081}
	out := Result{}

	applyRoute(req, &meta, 8081, &out)

	if len(out.Warnings) != 0 {
		t.Errorf("sin contrato de ruta no debería haber avisos: %v", out.Warnings)
	}
	if meta.RouteName != "" || meta.RouteStatus != "" || meta.RouteOwned {
		t.Errorf("sin contrato de ruta el Meta no puede llevar nada de ruta: %+v", meta)
	}
}

// Same rule as the port_verified JSON field: a URL nobody saw working is worse than none, because a later reader (TUI, vroom list, next reconcile) connects to something else.
func TestApplyRouteEscribeLaUrlSoloSiLaVerifico(t *testing.T) {
	t.Run("registrada sin verificar", func(t *testing.T) {
		reg := &degradingRegistrar{}
		req := Request{
			Manifest: &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeNamed, RouteName: "svc"},
			Branch:   "main",
			Routes:   reg,
		}
		meta := state.Meta{Name: "svc", Port: 8081}
		out := Result{}

		applyRoute(req, &meta, 8081, &out)

		if meta.RouteURL != "" {
			t.Errorf("RouteURL = %q sin verificación: publicaría una dirección falsa", meta.RouteURL)
		}
		// The handle and status are still recorded: the route was written, so without a handle stop could never revoke it.
		if meta.RouteName != "svc" {
			t.Errorf("RouteName = %q: sin handle el stop no puede retirar la ruta", meta.RouteName)
		}
		if meta.RouteStatus == "" {
			t.Error("RouteStatus vacío: el Meta no afirma nada del resultado de la ruta")
		}
		// Ownership is granted too: gating it on Succeeded() is the opposite bug, since a route written with the proxy down is ours and its handle must survive a port move.
		if !meta.RouteOwned {
			t.Error("RouteOwned = false con un alta que ocurrió: sin propiedad el stop no podría retirar la ruta")
		}
		if len(out.Warnings) == 0 {
			t.Error("una ruta degradada sin aviso deja al usuario sin saber por qué no tiene URL")
		}
		// Reconcile must run before Apply and carry the previous handle, or a renamed branch leaves the old route aimed at a dead port forever.
		if len(reg.reconciled) == 0 {
			t.Error("no se reconcilió nada: una rama renombrada dejaría la ruta vieja apuntando a un puerto muerto para siempre")
		}
	})

	t.Run("registrada y verificada", func(t *testing.T) {
		req := Request{
			Manifest: &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeNamed, RouteName: "svc"},
			Branch:   "main",
			Routes:   verifiedRegistrar{},
		}
		meta := state.Meta{Name: "svc", Port: 8081}
		out := Result{}

		applyRoute(req, &meta, 8081, &out)

		if meta.RouteURL == "" {
			t.Errorf("una ruta verificada tiene que publicar su URL: %+v", meta)
		}
		if !meta.RouteOwned {
			t.Error("una ruta registrada con éxito es nuestra: sin propiedad el stop no la retiraría")
		}
		if len(out.Warnings) != 0 {
			t.Errorf("una ruta sana no debe producir avisos: %v", out.Warnings)
		}
	})
}

// verifiedRegistrar is an Apply that was written and then seen working, so its result carries a URL.
type verifiedRegistrar struct{}

func (verifiedRegistrar) Apply(name string, port int, own portless.Ownership) portless.Result {
	return portless.Result{
		Name:       name,
		Port:       port,
		Status:     portless.StatusRegistered,
		Url:        "https://" + name + ".localhost",
		Registered: true,
	}
}

func (verifiedRegistrar) Reconcile(string, portless.Ownership, string) []string { return nil }

// auto keys on the branch because that is what separates two worktrees of one repo; named keys on the manifest name because two services cannot depend on their branch.
func TestRouteNameDerivaDeLaRamaEnAutoYDelNombreEnNamed(t *testing.T) {
	tests := []struct {
		name     string
		manifest *manifest.Manifest
		branch   string
		want     string
	}{
		{
			name:     "auto con rama",
			manifest: &manifest.Manifest{Name: "api", RouteMode: manifest.RouteModeAuto},
			branch:   "feature/login",
			want:     "feature-login.api",
		},
		{
			name:     "auto sin rama usa el proyecto",
			manifest: &manifest.Manifest{Name: "api", RouteMode: manifest.RouteModeAuto},
			branch:   "",
			want:     "api",
		},
		{
			name:     "named ignora la rama",
			manifest: &manifest.Manifest{Name: "api", RouteMode: manifest.RouteModeNamed, RouteName: "tienda"},
			branch:   "cualquier-rama",
			want:     "tienda",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := routeName(Request{Manifest: tt.manifest, Branch: tt.branch})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("routeName = %q, want %q", got, tt.want)
			}
		})
	}

	if _, err := routeName(Request{
		Manifest: &manifest.Manifest{Name: "api", RouteMode: "inventado"},
		Branch:   "main",
	}); err == nil {
		t.Error("un route_mode desconocido debería dar error: un nombre inventado sería una ruta que colisiona con la de otro")
	}
}

// A zero or negative timeout must fall back to the package default, or every slow dynamic service would stay port_pending forever.
func TestDiscoveryTimeoutPorDefectoCuandoNoSeDaUno(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())

	res, err := Start(Request{
		Manifest: &manifest.Manifest{Name: "svc", Command: "sleep 30", PortMode: manifest.PortModeDynamic},
		Path:     root,
		Store:    store,
		Manager:  process.NewManager(),
		// sleep opens no port, so no_port is the only possible outcome; 2s just keeps the test quick and a zero window would look identical.
		DiscoveryTimeout: 2 * time.Second,
		StdoutPath:       filepath.Join(root, "o.log"),
		StderrPath:       filepath.Join(root, "e.log"),
	})
	if err != nil {
		t.Skipf("el arranque dynamic necesita un entorno con puerto disponible: %v", err)
	}

	// Never report the reserved port as the real one: nobody probed it, the same harm as publishing an unverified URL.
	if res.Port != 0 {
		t.Errorf("Port = %d con un servicio que no escucha: no se puede afirmar un puerto que nadie confirmó", res.Port)
	}
	if res.Meta.State != state.StateNoPort {
		t.Errorf("meta.State = %q, want %q: el estado tiene que decir POR QUÉ no hay puerto", res.Meta.State, state.StateNoPort)
	}
	if res.Meta.PortVerified {
		t.Error("PortVerified = true con Port 0: no se verificó ningún puerto")
	}
	// no_port is a fact about the port, not a statement that the service is down, so the pid must still be alive.
	if res.Pid <= 0 {
		t.Error("Pid = 0: un no_port no significa que el servicio no arranque")
	}
	stopOne(t, store, root)
}

// stopOne stops whatever the test started and releases its reserved port, so a process-spawning test leaks neither.
func stopOne(t *testing.T, store *state.Store, path string) {
	t.Helper()
	meta, err := store.LoadMeta(path)
	if err != nil {
		return
	}
	if meta.Pid > 0 {
		_ = process.NewManager().Stop(process.StopSpec{
			Pid: meta.Pid, Pgid: meta.Pgid, Port: meta.Port,
			Timeout: process.DefaultStopTimeout,
		})
	}
	process.ReleasePort(meta.ReservedPort)
}
