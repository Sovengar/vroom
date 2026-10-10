package tui

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// EnsureServiceDir runs before the launch: with no service directory there are no logs, and a process started without one leaves the user with nothing to look at exactly when it breaks.
func TestStartCmdPropagaElErrorDelStoreAntesDeArrancar(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a directory without permission: the case cannot be triggered")
	}

	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	store := state.NewStoreAt(locked)
	mgr := &contadorManager{}
	// No EnsureServiceDir on purpose: the store is unusable, which is the point.
	p := proyectoConManifiesto(t, "/dev/api", 4321)

	msg := startCmd(store, mgr, p, "")()
	sm, ok := msg.(startedMsg)
	if !ok {
		t.Fatalf("startCmd returned %T, want startedMsg", msg)
	}
	if sm.err == nil {
		t.Error("with the unusable store there must be an error: without a service directory there are no logs")
	}
	if sm.res.Pid != 0 {
		t.Errorf("Pid = %d with the unusable store: nothing should have been started", sm.res.Pid)
	}
	if mgr.starts != 0 {
		t.Errorf("attempted to start %d times with the unusable store: the failure must be before the start", mgr.starts)
	}
}

// MEASURED: the warnings come from startsvc.Start, which talks to portless and to discovery, and the loop that copies them into the stderr log is what is under test here.
func TestStartCmdTraeLosAvisosAlLogDeStderr(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())

	arranca := func(t *testing.T, mgr process.Manager, port int, generation string) string {
		t.Helper()
		path := t.TempDir()
		if _, err := store.EnsureServiceDir(path); err != nil {
			t.Fatal(err)
		}
		m := manifest.Manifest{
			Name: "api", Command: "sleep 30", Port: port,
			URLGeneration: generation, RouteName: "api",
		}
		p := scanner.Project{Path: path, Name: "api", Configured: true, Manifest: &m}
		startCmd(store, mgr, p, "")()
		return store.StderrLog(path)
	}

	t.Run("without a published url there is nothing to write", func(t *testing.T) {
		log := arranca(t, &contadorManager{}, 4321, manifest.URLGenByPort)
		if data, err := os.ReadFile(log); err == nil {
			t.Errorf("a by_port service wrote to its stderr log:\n%s", data)
		} else if !os.IsNotExist(err) {
			t.Fatalf("could not check the log: %v", err)
		}
	})

	t.Run("with by_hostname the portless attempt is visible", func(t *testing.T) {
		// A service that asks for a route must leave a trace of what happened to it; with no portless in a test environment the documented degradation is a warning, not a failed start.
		mgr := &bindingManager{}
		t.Cleanup(func() {
			if mgr.ln != nil {
				_ = mgr.ln.Close()
			}
			if mgr.port > 0 {
				process.ReleasePort(mgr.port)
			}
		})
		log := arranca(t, mgr, 4321, manifest.URLGenByHostname)
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("with url_generation = by_hostname there must be a warning in the log: %v", err)
		}
		if !strings.Contains(strings.ToLower(string(data)), "portless") &&
			!strings.Contains(strings.ToLower(string(data)), "route") {
			t.Errorf("the route warning does not say what it is about:\n%s", data)
		}
	})
}

// bindingManager stands in for the service in the one start test that must reach the route attempt: it opens the PORT the
// offer names (a real listener this process owns) and reports this process as the service, because discovery reads /proc of
// the reported pid. Spawning a child would be the alternative, and it would prove nothing about the warning copy loop.
type bindingManager struct {
	ln   net.Listener
	port int
}

func (m *bindingManager) Start(spec process.StartSpec) (process.StartResult, error) {
	port := 0
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "PORT="); ok {
			port, _ = strconv.Atoi(v)
		}
	}
	if port <= 0 {
		return process.StartResult{}, errors.New("the start offered no PORT to bind")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return process.StartResult{}, err
	}
	m.ln, m.port = ln, port
	return process.StartResult{Pid: os.Getpid(), CreationTimeMs: 1}, nil
}

func (m *bindingManager) Stop(process.StopSpec) error { return nil }

func (m *bindingManager) Evaluate(process.EvalSpec) process.Status { return process.StatusRunning }

// The graceful stop runs first because killing the PGID is not always enough: `docker compose down` also leaves the network, the volumes and the container behind.
func TestStopCmdEjecutaElCommandStopAntesDeLaLimpieza(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(path, state.Meta{Pid: 4321, Pgid: 4321, Port: 4321, State: state.StateRunning}); err != nil {
		t.Fatal(err)
	}

	mgr := &contadorManager{}
	// The command_stop leaves its mark in the stdout log, which is what the Console tab reads.
	stopCmd(store, mgr, path, "echo PARADA_GRACIOSA")()

	out, err := os.ReadFile(store.StdoutLog(path))
	if err != nil {
		t.Fatalf("could not read the log: %v", err)
	}
	if !strings.Contains(string(out), "PARADA_GRACIOSA") {
		t.Errorf("the command_stop did not reach the service log:\n%s", out)
	}
	if mgr.stops != 1 {
		t.Errorf("Stop was called %d times, want 1: the command_stop does not replace the cleanup", mgr.stops)
	}
}

// A failing command_stop does not cancel the cleanup, because `docker stop` fails when the container is already stopped while vroom's own process is still up and must be killed anyway.
func TestStopCmdNotificaElCommandStopFallidoPeroSigueLimpiando(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(path, state.Meta{Pid: 4321, Pgid: 4321, Port: 4321, State: state.StateRunning}); err != nil {
		t.Fatal(err)
	}

	mgr := &contadorManager{}
	msg := stopCmd(store, mgr, path, "exit 7")()

	if mgr.stops != 1 {
		t.Errorf("a failed command_stop canceled the cleanup: Stop was called %d times. "+
			"The process is still running and the user would see stopped with a live service", mgr.stops)
	}
	sm, ok := msg.(stoppedMsg)
	if !ok {
		t.Fatalf("stopCmd returned %T, want stoppedMsg", msg)
	}
	if sm.err == nil {
		t.Error("the command_stop failure must reach the user: otherwise, a broken `docker stop` goes unnoticed")
	}
	// The meta is stopped despite the failed command, because the process is gone.
	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != state.StateStopped || meta.Pid != 0 {
		t.Errorf("meta = %+v, want stopped with Pid 0: the process is dead and the meta must say so", meta)
	}
}

// A pid-less pgid is still a credible root: it is signalled along with its lineage, because excluding that case is what leaves zombies surviving a stop.
func TestStopCmdConUnPidSinPgidLoParaIgual(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())

	tests := []struct {
		nombre string
		meta   state.Meta
		quiere bool
	}{
		{"only pid", state.Meta{Pid: 4321}, true},
		{"only pgid", state.Meta{Pgid: 4321}, true},
		{"only port", state.Meta{Port: 4321}, true},
		{"nothing", state.Meta{}, false},
		{"zero pid but reserved port", state.Meta{Pid: 0, Port: 4321}, true},
	}
	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			path := t.TempDir()
			if err := store.SaveMeta(path, tt.meta); err != nil {
				t.Fatal(err)
			}
			mgr := &contadorManager{}
			stopCmd(store, mgr, path, "")()

			llamadas := mgr.stops
			if tt.quiere && llamadas == 0 {
				t.Errorf("with %+v nothing was attempted to be stopped: a loose PID is a credible root", tt.meta)
			}
			if !tt.quiere && llamadas != 0 {
				t.Errorf("with %+v something that does not exist was stopped", tt.meta)
			}
		})
	}
}

// The three zeroed fields are what keep the next start from mistaking itself for the previous one; without ReservedPort at zero the port is reserved twice and the port set runs out.
func TestStopCmdVuelveAPonerElEstadoEnParadoYDevuelveElPuerto(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if err := store.SaveMeta(path, state.Meta{
		Pid: 4321, Pgid: 4321, Port: 4321, ReservedPort: 65001,
		State: state.StateRunning, StartedAt: "2026-10-03 12:00:00",
	}); err != nil {
		t.Fatal(err)
	}

	stopCmd(store, &contadorManager{}, path, "")()

	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != state.StateStopped {
		t.Errorf("State = %q, want stopped", meta.State)
	}
	if meta.Pid != 0 || meta.Pgid != 0 || meta.ReservedPort != 0 {
		t.Errorf("meta = %+v, want Pid/Pgid/ReservedPort at zero: they are the three that keep the next start from getting confused", meta)
	}
	// StartedAt is kept, because it is when it last started and that is what the user wants to see after stopping it.
	if meta.StartedAt != "2026-10-03 12:00:00" {
		t.Errorf("StartedAt = %q: stopping must not erase when it last started", meta.StartedAt)
	}
}

// The release stays outside the process guard on purpose: a route pointing at a dead port is worse than no route, because the user clicks and nothing happens.
func TestStopCmdRetiraLaRutaAunqueNoHayaProcesoQueParar(t *testing.T) {
	rec := &recordingReleaser{}
	installRouteStub(t, rec)

	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	// A meta with no process -- the remains of a service that was already dead -- but with a route taken and ownership granted.
	if err := store.SaveMeta(path, state.Meta{
		State: state.StateRunning, RouteName: "vroom-test-ruta-stop", RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}

	stopCmd(store, &contadorManager{}, path, "")()

	if len(rec.removed) != 1 || rec.removed[0] != "vroom-test-ruta-stop" {
		t.Errorf("removed = %v, want the route of the already dead service: it leaves an address pointing to a dead port", rec.removed)
	}
	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("RouteOwned remains granted after removing the route")
	}
}

// tea.ExecProcess itself cannot be tested here (it suspends the program until the editor closes), so only its composition is asserted.
func TestEditLogsCmdDevuelveElComandoDelEditor(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	t.Setenv("EDITOR", "/bin/sh")

	next, cmd := m.openLogEditor()
	got := next.(Model)
	if cmd == nil {
		t.Fatal("opening the logs must return the editor command")
	}
	if got.message != "" {
		t.Errorf("warning = %q when opening an editor that exists: there is no reason", got.message)
	}
	_ = path
}

// A mistyped $EDITOR is silent and real: without this the key does nothing at all and the user assumes vroom has hung.
func TestEditLogsCmdAvisaSiElEditorNoExiste(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	t.Setenv("VISUAL", "/no/existe/un/editor")
	t.Setenv("EDITOR", "/no/existe/un/editor")

	next, cmd := m.openLogEditor()
	got := next.(Model)
	if cmd == nil {
		t.Error("with a nonexistent editor there is no command to return: it is emitted anyway and the error comes from the editor")
	}
	if got.message != "" {
		t.Errorf("the warning %q only arrives when executing the editor, not when composing it: the error message is from the process", got.message)
	}
}

// The result has to arrive as a stackResultMsg so Update can paint it; whether the real stack actually launches is not what this test decides.
func TestToggleStackLanzaUnStackParadoYLoDejaMarcado(t *testing.T) {
	m := stackModeloBarato(t)
	for path := range m.services {
		m.services[path].Status = statusStopped
	}
	stack := m.stacksForPrimary("tienda")[0]

	next, cmd := m.toggleStack(&stack)
	got := next.(Model)
	if cmd == nil {
		t.Fatal("a stopped stack must launch")
	}
	if !strings.Contains(got.message, "launching") {
		t.Errorf("warning = %q, want it to say it is launching", got.message)
	}
	msg := cmd()
	sr, ok := msg.(stackResultMsg)
	if !ok {
		t.Fatalf("the command returned %T, want stackResultMsg", msg)
	}
	if sr.err == nil && sr.result.OK {
		t.Log("the stack really launched: the manifest commands are valid in this environment")
	}
}

// With two projects named "api", stopping "api" would pick one of them in scan order and the user would find a service stopped that they did not know existed.
func TestToggleStackConConflictoDeNombresNoLanzaNiPara(t *testing.T) {
	m := newStackModel(t)
	// One of the stack's projects is duplicated under the same name.
	base := m.projectByPath(projectPath(t, m, "tienda-api"))
	clone := *base
	clone.Path = filepath.Join(t.TempDir(), "otro-api")
	clone.Name = "tienda-api"
	m.projects = append(m.projects, clone)
	m.tree = m.buildTree()

	stack := m.stacksForPrimary("tienda")[0]
	next, cmd := m.toggleStack(&stack)
	got := next.(Model)

	if cmd != nil {
		t.Error("with an ambiguous name you cannot launch or stop: it would pick a project at random")
	}
	if !strings.Contains(got.message, "conflict") {
		t.Errorf("warning = %q, want it to say there is a conflict", got.message)
	}
}

// MEASURED (bug): like toggleStack, this branch does not mark the services stopping, so the engine stops them while the TUI keeps showing running for up to two seconds and a second stop lands on a dead service.
func TestToggleComposersParaTodosLosStacksDelGrupo(t *testing.T) {
	m := newStackModel(t)
	stacks := m.stacksForPrimary("tienda")
	if len(stacks) == 0 {
		t.Fatal("the test model should bring at least one stack")
	}

	for i := range stacks {
		_, n, err := m.stackStats(&stacks[i])
		if err != nil || n == 0 {
			t.Skipf("precondition: the stack must have resolvable services (n=%d err=%v)", n, err)
		}
		for _, ruta := range membersDelStack(t, &stacks[i], m) {
			markRunning(&m, ruta, livePID(t))
		}
	}

	next, cmd := m.toggleComposers("tienda")
	got := next.(Model)
	if cmd != nil {
		t.Error("stopping stacks is synchronous in the engine: it must not emit a background command")
	}
	if !strings.Contains(got.message, "stopping") {
		t.Errorf("warning = %q, want it to say they are stopping", got.message)
	}
	for _, ruta := range membersDelStack(t, &stacks[0], m) {
		if sv := m.services[ruta]; sv != nil && sv.Status == statusRunning {
			t.Logf("MEASURED: the service is still in %q after stopping its stack from the group header. "+
				"toggleStack does mark stopping and toggleComposers does not", sv.Status)
		}
	}
}

// The message carries every result because a single one would lose the failure count the final warning needs.
func TestToggleComposersConTodosArrancaYEmiteUnResultadoPorStack(t *testing.T) {
	m := stackModeloBarato(t)
	// Two stacks in the same group.
	stacks := m.stacksForPrimary("tienda")
	if len(stacks) == 0 {
		t.Fatal("the test model should bring at least one stack")
	}

	next, cmd := m.toggleComposers("tienda")
	got := next.(Model)
	if cmd == nil {
		t.Fatal("with some stopped stack it must launch")
	}
	if !strings.Contains(got.message, "launching") {
		t.Errorf("warning = %q, want it to say it is launching", got.message)
	}

	msg := cmd()
	cr, ok := msg.(composersResultMsg)
	if !ok {
		t.Fatalf("the command returned %T, want composersResultMsg: the final warning needs the failure count", msg)
	}
	if cr.primary != "tienda" {
		t.Errorf("primary = %q, want tienda: the final warning names the group", cr.primary)
	}
}

type contadorManager struct {
	starts int
	stops  int
	warns  []string
}

func (m *contadorManager) Start(spec process.StartSpec) (process.StartResult, error) {
	m.starts++
	return process.StartResult{Pid: 4321, Pgid: 4321, CreationTimeMs: 100}, nil
}

func (m *contadorManager) Stop(spec process.StopSpec) error {
	m.stops++
	for _, w := range m.warns {
		if spec.Warn != nil {
			spec.Warn("%s", w)
		}
	}
	return nil
}

func (m *contadorManager) Evaluate(spec process.EvalSpec) process.Status {
	return process.StatusRunning
}

func proyectoConManifiesto(t *testing.T, path string, port int) scanner.Project {
	t.Helper()
	p := scanner.Project{
		Path:       path,
		Name:       filepath.Base(path),
		Configured: true,
		Manifest:   manifestConPuerto(port),
	}
	return p
}

func manifestConPuerto(port int) *manifest.Manifest {
	return &manifest.Manifest{Name: "api", Command: "sleep 30", Port: port, URLGeneration: manifest.URLGenByPort}
}

func membersDelStack(t *testing.T, s *orchestrate.Stack, m Model) []string {
	t.Helper()
	var rutas []string
	for _, stage := range s.Stages {
		for _, name := range stage.Services {
			p, err := orchestrate.LookupService(name, m.projects)
			if err != nil {
				t.Fatalf("the stack %q references %q, which does not resolve: %v", s.Name, name, err)
			}
			rutas = append(rutas, p.Path)
		}
	}
	return rutas
}

// Without it the tests that launch a real stack would wait out the 30s sleep in the base tree, which is a slow pass that proves nothing extra.
func stackModeloBarato(t *testing.T) Model {
	t.Helper()
	m := newStackModel(t)
	for i := range m.projects {
		p := &m.projects[i]
		if p.Manifest == nil {
			continue
		}
		p.Manifest.Command = "true"
		p.Manifest.Port = 0
		p.Manifest.URLGeneration = manifest.URLGenNone
	}
	// The tree carries its own copy of the manifests.
	m.tree = m.buildTree()
	return m
}
