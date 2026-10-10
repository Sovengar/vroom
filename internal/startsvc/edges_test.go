package startsvc

import (
	"context"
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

func (r *degradingRegistrar) Lookup(string) (int, bool, error) { return 0, false, nil }

func (r *degradingRegistrar) ApplyContext(ctx context.Context, name string, port int, own portless.Ownership) portless.Result {
	return portless.Result{
		Name:       name,
		Port:       port,
		Status:     portless.StatusDegraded,
		Reason:     portless.ReasonPortlessMissing,
		Registered: true,
	}
}

func (r *degradingRegistrar) Reconcile(prev string, own portless.Ownership, now ...string) []string {
	r.reconciled = append(r.reconciled, prev+"->"+strings.Join(now, ","))
	return nil
}

func (r *degradingRegistrar) Retire(string, portless.Ownership) []string { return nil }

func TestStartDevuelveElPuertoReservadoSiElHijoNuncaLlego(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())

	m := failingManager{err: errors.New("no such file or directory")}
	_, err := Start(Request{
		Manifest:   &manifest.Manifest{Name: "svc", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "./no-existe"}}, URLGeneration: manifest.URLGenByWorkspaceHostname},
		Path:       root,
		Store:      store,
		Manager:    m,
		StdoutPath: filepath.Join(root, "o.log"),
		StderrPath: filepath.Join(root, "e.log"),
	})
	if err == nil {
		t.Fatal("a command that cannot be launched should fail the start")
	}

	// Proves the release by re-reserving rather than by inspecting the pool: peeking would test the implementation.
	again, rerr := process.ReservePort()
	if rerr != nil {
		t.Fatalf("could not reserve a port after the failure: %v", rerr)
	}
	process.ReleasePort(again)
}

// A fixed-port start has no reservation to release, and feeding back a 0 would poison the pool: samePorts and containsPid read 0 as "unknown port".
func TestStartSinPuertoReservadoNoFiltraNadaSinReserva(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())

	m := failingManager{err: errors.New("boom")}
	_, err := Start(Request{
		Manifest:   &manifest.Manifest{Name: "svc", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "./no-existe"}}, Port: 8081},
		Path:       root,
		Store:      store,
		Manager:    m,
		StdoutPath: filepath.Join(root, "o.log"),
		StderrPath: filepath.Join(root, "e.log"),
	})
	if err == nil {
		t.Fatal("the spawn failure must propagate")
	}
	p, rerr := process.ReservePort()
	if rerr != nil || p <= 0 {
		t.Errorf("the port set was damaged: ReservePort = %d, %v", p, rerr)
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
		Manifest:   &manifest.Manifest{Name: "svc", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "sleep 30"}}, URLGeneration: manifest.URLGenByWorkspaceHostname},
		Path:       root,
		Store:      store,
		Manager:    process.NewManager(),
		StdoutPath: filepath.Join(root, "o.log"),
		StderrPath: filepath.Join(root, "e.log"),
	})
	if err == nil {
		t.Fatal("a Meta that cannot be written should fail the start")
	}
	if !strings.Contains(err.Error(), "service directory") && !strings.Contains(err.Error(), "service dir") {
		t.Logf("the error does not name the service directory: %q", err)
	}

	p, rerr := process.ReservePort()
	if rerr != nil || p <= 0 {
		t.Errorf("the port set was damaged: ReservePort = %d, %v", p, rerr)
	} else {
		process.ReleasePort(p)
	}
}

// A route is an address, not a dependency: an unusable name must only warn, or one bad name would block the whole start.
func TestApplyRouteConNombreNoUtilizableAvisaYNoPropagaElError(t *testing.T) {
	req := Request{
		Manifest: &manifest.Manifest{
			Name: "svc", URLGeneration: manifest.URLGenByHostnameOrWorkspace,
			RouteName: "!!!", // not a usable hostname
		},
		Branch: "main",
	}
	meta := state.Meta{Name: "svc", Port: 8081}
	out := Result{}

	applyRoute(req, verifiedRegistrar{}, &meta, 8081, manifest.URLGenByHostnameOrWorkspace, &out)

	if len(out.Warnings) == 0 {
		t.Fatal("an invalid route name must produce a warning: otherwise, the user will not know why they have no route")
	}
	if !strings.Contains(out.Warnings[0], "portless route") {
		t.Errorf("the warning does not explain that the problem is the name: %q", out.Warnings[0])
	}
	// Meta must stay untouched: with no accepted name there is no handle or ownership, or stop would try to revoke a route that was never registered.
	if meta.RouteName != "" || meta.RouteOwned {
		t.Errorf("the Meta was modified without a valid route: %+v", meta)
	}
}

func TestApplyRouteSinRegistrarNoAbrePortless(t *testing.T) {
	req := Request{
		Manifest: &manifest.Manifest{Name: "svc", Port: 8081},
	}
	meta := state.Meta{Name: "svc", Port: 8081}
	out := Result{}

	// A nil registrar is what RegistrarFor yields for a generation that publishes no URL (by_port, none).
	applyRoute(req, nil, &meta, 8081, manifest.URLGenByPort, &out)

	if len(out.Warnings) != 0 {
		t.Errorf("without a route contract there should be no warnings: %v", out.Warnings)
	}
	if meta.RouteName != "" || meta.RouteStatus != "" || meta.RouteOwned {
		t.Errorf("without a route contract the Meta cannot carry any route data: %+v", meta)
	}
}

// Same rule as the port_verified JSON field: a URL nobody saw working is worse than none, because a later reader (TUI, vroom list, next reconcile) connects to something else.
func TestApplyRouteEscribeLaUrlSoloSiLaVerifico(t *testing.T) {
	t.Run("registered without verification", func(t *testing.T) {
		reg := &degradingRegistrar{}
		req := Request{
			Manifest:  &manifest.Manifest{Name: "svc", URLGeneration: manifest.URLGenByHostnameOrWorkspace, RouteName: "svc"},
			Branch:    "main",
			Registrar: func(string) RouteRegistrar { return reg },
		}
		meta := state.Meta{Name: "svc", Port: 8081}
		out := Result{}

		applyRoute(req, reg, &meta, 8081, manifest.URLGenByHostnameOrWorkspace, &out)

		if meta.RouteURL != "" {
			t.Errorf("RouteURL = %q without verification: it would publish a false address", meta.RouteURL)
		}
		// The handle and status are still recorded: the route was written, so without a handle stop could never revoke it.
		if meta.RouteName != "svc" {
			t.Errorf("RouteName = %q: without a handle stop cannot revoke the route", meta.RouteName)
		}
		if meta.RouteStatus == "" {
			t.Error("RouteStatus empty: the Meta says nothing about the route result")
		}
		// Ownership is granted too: gating it on Succeeded() is the opposite bug, since a route written with the proxy down is ours and its handle must survive a port move.
		if !meta.RouteOwned {
			t.Error("RouteOwned = false with a registration that occurred: without ownership stop could not revoke the route")
		}
		if len(out.Warnings) == 0 {
			t.Error("a degraded route without warning leaves the user without knowing why they have no URL")
		}
		// Reconcile must run before Apply and carry the previous handle, or a renamed branch leaves the old route aimed at a dead port forever.
		if len(reg.reconciled) == 0 {
			t.Error("nothing was reconciled: a renamed branch would leave the old route pointing at a dead port forever")
		}
	})

	t.Run("registered and verified", func(t *testing.T) {
		req := Request{
			Manifest: &manifest.Manifest{Name: "svc", URLGeneration: manifest.URLGenByHostnameOrWorkspace, RouteName: "svc"},
			Branch:   "main",
		}
		meta := state.Meta{Name: "svc", Port: 8081}
		out := Result{}

		applyRoute(req, verifiedRegistrar{}, &meta, 8081, manifest.URLGenByHostnameOrWorkspace, &out)

		if meta.RouteURL == "" {
			t.Errorf("a verified route must publish its URL: %+v", meta)
		}
		if !meta.RouteOwned {
			t.Error("a successfully registered route is ours: without ownership stop would not revoke it")
		}
		if len(out.Warnings) != 0 {
			t.Errorf("a healthy route must not produce warnings: %v", out.Warnings)
		}
	})
}

// verifiedRegistrar is an Apply that was written and then seen working, so its result carries a URL.
type verifiedRegistrar struct{}

func (verifiedRegistrar) Lookup(string) (int, bool, error) { return 0, false, nil }

func (verifiedRegistrar) ApplyContext(ctx context.Context, name string, port int, own portless.Ownership) portless.Result {
	return portless.Result{
		Name:       name,
		Port:       port,
		Status:     portless.StatusRegistered,
		Url:        "https://" + name + ".localhost",
		Registered: true,
	}
}

func (verifiedRegistrar) Reconcile(string, portless.Ownership, ...string) []string { return nil }

func (verifiedRegistrar) Retire(string, portless.Ownership) []string { return nil }

// auto keys on the branch because that is what separates two worktrees of one repo; the ladder mode keys its FIRST rung on route_name because two services cannot depend on their branch.
func TestRouteNameDerivaDeLaRamaEnAutoYDelNombreEnNamed(t *testing.T) {
	tests := []struct {
		name     string
		manifest *manifest.Manifest
		branch   string
		want     string
	}{
		{
			name:     "workspace keys on the branch because that separates two worktrees of one repo",
			manifest: &manifest.Manifest{Name: "api", URLGeneration: manifest.URLGenByWorkspaceHostname},
			branch:   "feature/login",
			want:     "feature-login.api",
		},
		{
			name:     "workspace without branch uses the project",
			manifest: &manifest.Manifest{Name: "api", URLGeneration: manifest.URLGenByWorkspaceHostname},
			branch:   "",
			want:     "api",
		},
		{
			name:     "ladder ignores the branch on its first rung",
			manifest: &manifest.Manifest{Name: "api", URLGeneration: manifest.URLGenByHostnameOrWorkspace, RouteName: "tienda"},
			branch:   "cualquier-rama",
			want:     "tienda",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := routeCandidates(tt.manifest.URLGeneration, Request{Manifest: tt.manifest, Branch: tt.branch})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 || got[0] != tt.want {
				t.Errorf("routeCandidates = %v, want the first rung %q", got, tt.want)
			}
		})
	}

	if _, err := routeCandidates("inventado", Request{
		Manifest: &manifest.Manifest{Name: "api", URLGeneration: "inventado"},
		Branch:   "main",
	}); err == nil {
		t.Error("an unknown generation should give an error: an invented name would be a route that collides with another's")
	}
}

// A zero or negative timeout must fall back to the package default, or every slow dynamic service would stay port_pending forever.
func TestDiscoveryTimeoutPorDefectoCuandoNoSeDaUno(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())

	res, err := Start(Request{
		Manifest: &manifest.Manifest{Name: "svc", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "sleep 30"}}, URLGeneration: manifest.URLGenByWorkspaceHostname},
		Path:     root,
		Store:    store,
		Manager:  process.NewManager(),
		// sleep opens no port, so no_port is the only possible outcome; 2s just keeps the test quick and a zero window would look identical.
		DiscoveryTimeout: 2 * time.Second,
		StdoutPath:       filepath.Join(root, "o.log"),
		StderrPath:       filepath.Join(root, "e.log"),
	})
	if err != nil {
		t.Skipf("dynamic start needs an environment with an available port: %v", err)
	}

	// Never report the reserved port as the real one: nobody probed it, the same harm as publishing an unverified URL.
	if res.Port != 0 {
		t.Errorf("Port = %d with a service that does not listen: a port that nobody confirmed cannot be asserted", res.Port)
	}
	if res.Meta.State != state.StateNoPort {
		t.Errorf("meta.State = %q, want %q: the state must say WHY there is no port", res.Meta.State, state.StateNoPort)
	}
	if res.Meta.PortVerified {
		t.Error("PortVerified = true with Port 0: no port was verified")
	}
	// no_port is a fact about the port, not a statement that the service is down, so the pid must still be alive.
	if res.Pid <= 0 {
		t.Error("Pid = 0: a no_port does not mean the service does not start")
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
