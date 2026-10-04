package tui

import (
	"os"
	"path/filepath"
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
		t.Skip("root puede escribir en un directorio sin permiso: el caso no se puede provocar")
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

	msg := startCmd(store, mgr, p)()
	sm, ok := msg.(startedMsg)
	if !ok {
		t.Fatalf("startCmd devolvió %T, want startedMsg", msg)
	}
	if sm.err == nil {
		t.Error("con el store inservible tiene que haber error: sin directorio de servicio no hay logs")
	}
	if sm.res.Pid != 0 {
		t.Errorf("Pid = %d con el store inservible: no debería haberse arrancado nada", sm.res.Pid)
	}
	if mgr.starts != 0 {
		t.Errorf("se intentó arrancar %d veces con el store inservible: el fallo tiene que ser antes del start", mgr.starts)
	}
}

// MEDIDO: the warnings come from startsvc.Start, which talks to portless and to discovery, and the loop that copies them into the stderr log is what is under test here.
func TestStartCmdTraeLosAvisosAlLogDeStderr(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())

	arranca := func(t *testing.T, port int, mode string) string {
		t.Helper()
		path := t.TempDir()
		if _, err := store.EnsureServiceDir(path); err != nil {
			t.Fatal(err)
		}
		m := manifest.Manifest{
			Name: "api", Command: "sleep 30", Port: port,
			PortMode: manifest.PortModeFixed, RouteMode: mode,
		}
		p := scanner.Project{Path: path, Name: "api", Configured: true, Manifest: &m}
		startCmd(store, &contadorManager{}, p)()
		return store.StderrLog(path)
	}

	t.Run("sin route_mode no hay nada que escribir", func(t *testing.T) {
		// MEDIDO (bug): this same service used to write `portless route: unknown route_mode "off"` on every start, because portless's nil pointer travelled inside an interface and the `req.Routes == nil` guard never fired.
		log := arranca(t, 4321, "")
		if data, err := os.ReadFile(log); err == nil {
			t.Errorf("un servicio sin route_mode escribió en su log de stderr:\n%s", data)
		} else if !os.IsNotExist(err) {
			t.Fatalf("no se pudo comprobar el log: %v", err)
		}
	})

	t.Run("con route_mode auto el intento es visible", func(t *testing.T) {
		// A service that asks for a route must leave a trace of what happened to it; with no portless in a test environment the documented degradation is a warning, not a failed start.
		log := arranca(t, 4321, manifest.RouteModeAuto)
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("con route_mode = auto tiene que haber aviso en el log: %v", err)
		}
		if !strings.Contains(strings.ToLower(string(data)), "portless") &&
			!strings.Contains(strings.ToLower(string(data)), "route") {
			t.Errorf("el aviso de ruta no dice de qué va:\n%s", data)
		}
		// The bug's own warning never appears, since it only made sense for mode off.
		if strings.Contains(string(data), `unknown route_mode "off"`) {
			t.Errorf("apareció el aviso del bug con route_mode = auto:\n%s", data)
		}
	})
}

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
		t.Fatalf("no se pudo leer el log: %v", err)
	}
	if !strings.Contains(string(out), "PARADA_GRACIOSA") {
		t.Errorf("el command_stop no llegó al log del servicio:\n%s", out)
	}
	if mgr.stops != 1 {
		t.Errorf("se llamó a Stop %d veces, want 1: el command_stop no sustituye a la limpieza", mgr.stops)
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
		t.Errorf("un command_stop fallido canceló la limpieza: se llamó a Stop %d veces. "+
			"El proceso sigue en pie y el usuario vería stopped con un servicio vivo", mgr.stops)
	}
	sm, ok := msg.(stoppedMsg)
	if !ok {
		t.Fatalf("stopCmd devolvió %T, want stoppedMsg", msg)
	}
	if sm.err == nil {
		t.Error("el fallo del command_stop tiene que llegar al usuario: si no, `docker stop` roto pasa por alto")
	}
	// The meta is stopped despite the failed command, because the process is gone.
	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != state.StateStopped || meta.Pid != 0 {
		t.Errorf("meta = %+v, want stopped con Pid 0: el proceso está muerto y el meta tiene que decirlo", meta)
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
		{"sólo pid", state.Meta{Pid: 4321}, true},
		{"sólo pgid", state.Meta{Pgid: 4321}, true},
		{"sólo puerto", state.Meta{Port: 4321}, true},
		{"nada", state.Meta{}, false},
		{"pid cero pero puerto reservado", state.Meta{Pid: 0, Port: 4321}, true},
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
				t.Errorf("con %+v no se intentó parar nada: un PID suelto es una raíz creíble", tt.meta)
			}
			if !tt.quiere && llamadas != 0 {
				t.Errorf("con %+v se paró algo que no hay", tt.meta)
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
		t.Errorf("meta = %+v, want Pid/Pgid/ReservedPort a cero: son los tres que hacen que el siguiente start no se confunda", meta)
	}
	// StartedAt is kept, because it is when it last started and that is what the user wants to see after stopping it.
	if meta.StartedAt != "2026-10-03 12:00:00" {
		t.Errorf("StartedAt = %q: parar no debe borrar cuándo arrancó por última vez", meta.StartedAt)
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
		t.Errorf("removed = %v, want la ruta del servicio ya muerto: deja una dirección que apunta a un puerto muerto", rec.removed)
	}
	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("RouteOwned sigue concedido tras retirar la ruta")
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
		t.Fatal("abrir los logs tiene que devolver el comando del editor")
	}
	if got.message != "" {
		t.Errorf("aviso = %q al abrir un editor que existe: no hay motivo", got.message)
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
		t.Error("con un editor inexistente no hay comando que devolver: se emite igual y el error llega del editor")
	}
	if got.message != "" {
		t.Errorf("el aviso %q llega sólo al ejecutar el editor, no al componerlo: el mensaje de error es del proceso", got.message)
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
		t.Fatal("un stack parado tiene que lanzar")
	}
	if !strings.Contains(got.message, "launching") {
		t.Errorf("aviso = %q, want que diga que se está lanzando", got.message)
	}
	msg := cmd()
	sr, ok := msg.(stackResultMsg)
	if !ok {
		t.Fatalf("el comando devolvió %T, want stackResultMsg", msg)
	}
	if sr.err == nil && sr.result.OK {
		t.Log("el stack se lanzó de verdad: las órdenes del manifiesto son válidas en este entorno")
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
		t.Error("con un nombre ambiguo no se puede lanzar ni parar: elegiría un proyecto al azar")
	}
	if !strings.Contains(got.message, "conflict") {
		t.Errorf("aviso = %q, want que diga que hay un conflicto", got.message)
	}
}

// MEDIDO (bug): like toggleStack, this branch does not mark the services stopping, so the engine stops them while the TUI keeps showing running for up to two seconds and a second stop lands on a dead service.
func TestToggleComposersParaTodosLosStacksDelGrupo(t *testing.T) {
	m := newStackModel(t)
	stacks := m.stacksForPrimary("tienda")
	if len(stacks) == 0 {
		t.Fatal("el modelo de test debería traer al menos un stack")
	}

	for i := range stacks {
		_, n, err := m.stackStats(&stacks[i])
		if err != nil || n == 0 {
			t.Skipf("precondición: el stack tiene que tener servicios resolubles (n=%d err=%v)", n, err)
		}
		for _, ruta := range membersDelStack(t, &stacks[i], m) {
			markRunning(&m, ruta, livePID(t))
		}
	}

	next, cmd := m.toggleComposers("tienda")
	got := next.(Model)
	if cmd != nil {
		t.Error("parar stacks es síncrono en el motor: no debe emitir un comando en background")
	}
	if !strings.Contains(got.message, "stopping") {
		t.Errorf("aviso = %q, want que diga que se están parando", got.message)
	}
	for _, ruta := range membersDelStack(t, &stacks[0], m) {
		if sv := m.services[ruta]; sv != nil && sv.Status == statusRunning {
			t.Logf("MEDIDO: el servicio sigue en %q tras parar su stack desde el header de grupo. "+
				"toggleStack sí marca stopping y toggleComposers no", sv.Status)
		}
	}
}

// The message carries every result because a single one would lose the failure count the final warning needs.
func TestToggleComposersConTodosArrancaYEmiteUnResultadoPorStack(t *testing.T) {
	m := stackModeloBarato(t)
	// Two stacks in the same group.
	stacks := m.stacksForPrimary("tienda")
	if len(stacks) == 0 {
		t.Fatal("el modelo de test debería traer al menos un stack")
	}

	next, cmd := m.toggleComposers("tienda")
	got := next.(Model)
	if cmd == nil {
		t.Fatal("con algún stack parado tiene que lanzar")
	}
	if !strings.Contains(got.message, "launching") {
		t.Errorf("aviso = %q, want que diga que se está lanzando", got.message)
	}

	msg := cmd()
	cr, ok := msg.(composersResultMsg)
	if !ok {
		t.Fatalf("el comando devolvió %T, want composersResultMsg: el aviso final necesita el recuento de fallos", msg)
	}
	if cr.primary != "tienda" {
		t.Errorf("primary = %q, want tienda: el aviso final nombra el grupo", cr.primary)
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
	return &manifest.Manifest{Name: "api", Command: "sleep 30", Port: port, PortMode: manifest.PortModeFixed}
}

func membersDelStack(t *testing.T, s *orchestrate.Stack, m Model) []string {
	t.Helper()
	var rutas []string
	for _, stage := range s.Stages {
		for _, name := range stage.Services {
			p, err := orchestrate.LookupService(name, m.projects)
			if err != nil {
				t.Fatalf("el stack %q referencia %q, que no resuelve: %v", s.Name, name, err)
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
		p.Manifest.PortMode = manifest.PortModeNone
		p.Manifest.RouteMode = manifest.RouteModeOff
	}
	// The tree carries its own copy of the manifests.
	m.tree = m.buildTree()
	return m
}
