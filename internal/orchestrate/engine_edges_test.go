package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// LaunchAsync is the TUI's launch path, so a result that never arrives, arrives twice, or is delivered on a channel left open leaves it waiting forever.
func TestLaunchAsyncEntregaElResultadoYCierraElCanal(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "async", PrimaryGroup: "g1", Stages: []Stage{
		{Name: "s1", Services: []string{"api"}, Timeout: 10 * time.Second},
	}}
	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{
			Name: "api", Command: "./api", PortMode: manifest.PortModeNone,
		}},
	}

	ch := engine.LaunchAsync(stack, projects)

	select {
	case result, ok := <-ch:
		if !ok {
			t.Fatal("el canal llegó cerrado sin entregar resultado")
		}
		if !result.OK || result.Stack != "async" {
			t.Errorf("resultado = %+v, want OK y stack async", result)
		}
		if len(result.Stages) != 1 || len(result.Stages[0].Services) != 1 {
			t.Fatalf("el resultado no trae la etapa con su servicio: %+v", result)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("LaunchAsync no entregó nada en 30s: la TUI se quedaría colgada")
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("el canal entregó un segundo resultado")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("el canal no se cerró tras entregar el resultado")
	}
}

// There is no error return here, so the failure must travel inside the result: closing the channel without sending would show the TUI a finished stack with no reason.
func TestLaunchAsyncConErrorDeValidacionLoEntregaComoResultado(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "roto", PrimaryGroup: "g1", Stages: []Stage{
		{Name: "s1", Services: []string{"no-existe"}, Timeout: time.Second},
	}}

	select {
	case result := <-engine.LaunchAsync(stack, nil):
		if result.OK {
			t.Error("un stack con un servicio inexistente no puede salir OK")
		}
		if result.Stack != "roto" {
			t.Errorf("Stack = %q, want roto: el error tiene que decir sobre qué stack fue", result.Stack)
		}
		if result.Error == "" {
			t.Error("sin Error el usuario ve un stack rojo sin explicación")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LaunchAsync no entregó nada")
	}
}

// StopStack resolves names against the scanned project list, which includes unconfigured rows, so a nil Manifest must be tolerated before any store access.
func TestStopServiceSinManifiestoNoHaceNada(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(root)
	engine := NewEngine(&mockManager{}, store)

	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(path, state.Meta{Pid: 4242, State: state.StateRunning}); err != nil {
		t.Fatal(err)
	}

	engine.stopService(scanner.Project{Path: path, Name: "sin-manifiesto", Manifest: nil})

	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 4242 {
		t.Errorf("Pid = %d tras parar un proyecto sin manifiesto, want 4242: no hay servicio que parar", meta.Pid)
	}
}

// The stop must stay silent when nothing was ever started, or vroom stop of a stack holding such a service would fail.
func TestStopServiceConMetaAusenteNoFalla(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	p := scanner.Project{
		Path:       t.TempDir(),
		Name:       "nunca-arrancado",
		Configured: true,
		Manifest:   &manifest.Manifest{Name: "nunca-arrancado", Command: "./x", PortMode: manifest.PortModeNone},
	}
	engine.stopService(p)

	stack := &Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"nunca-arrancado"}}}}
	if err := engine.StopStack(stack, []scanner.Project{p}); err != nil {
		t.Errorf("StopStack de un servicio sin meta dio error %v", err)
	}
}

// A service already dead never reached the release path, so its route would stay taken and block the next service that wants that name.
func TestStopServiceRetiraLaRutaYElPuertoDeUnServicioYaMuerto(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(root)
	engine := NewEngine(&mockManager{}, store)

	path := t.TempDir()
	const routeName = "vroom-test-ruta-muerta"
	// All zeros means a dead service, but the route is still taken and owned.
	meta := state.Meta{
		State:      state.StateRunning,
		RouteName:  routeName,
		RouteOwned: true,
	}
	if err := store.SaveMeta(path, meta); err != nil {
		t.Fatal(err)
	}

	engine.stopService(scanner.Project{
		Path:     path,
		Name:     "muerto",
		Manifest: &manifest.Manifest{Name: "muerto", Command: "./x", PortMode: manifest.PortModeNone},
	})

	got, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.RouteOwned {
		t.Error("RouteOwned sigue en true tras parar un servicio muerto: la ruta queda tomada para siempre")
	}
	if got.State != state.StateStopped {
		t.Errorf("State = %q, want %q", got.State, state.StateStopped)
	}
	if got.Pid != 0 || got.Pgid != 0 {
		t.Errorf("Pid/Pgid = %d/%d tras parar, want 0/0", got.Pid, got.Pgid)
	}
}

// RouteOwned false means another service holds the handle, and the name survives revocation so reconciliation can still find it; deleting on that basis would remove a foreign route.
func TestStopServiceNoRetiraUnaRutaQueNoEraSuya(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	path := t.TempDir()
	if err := store.SaveMeta(path, state.Meta{
		State:      state.StateRunning,
		RouteName:  "vroom-test-ruta-ajena",
		RouteOwned: false,
	}); err != nil {
		t.Fatal(err)
	}

	engine.stopService(scanner.Project{
		Path:     path,
		Name:     "ajeno",
		Manifest: &manifest.Manifest{Name: "ajeno", Command: "./x", PortMode: manifest.PortModeNone},
	})

	got, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.RouteOwned {
		t.Error("RouteOwned = true tras parar un servicio cuya ruta no era suya")
	}
	if got.RouteName != "vroom-test-ruta-ajena" {
		t.Errorf("RouteName = %q: la ruta de otro no puede desaparecer del meta", got.RouteName)
	}
}

// Warnings are written after the stop and carry a prefix: written before, the log would read as if the warning belonged to the start.
func TestStopProcessEscribeLosAvisosEnElLogDeStderrDelServicio(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(root)
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}

	mgr := &mockManager{stopFunc: func(spec process.StopSpec) error {
		if spec.Warn != nil {
			spec.Warn("descendiente %d sobrevivió a SIGKILL", 999)
		}
		return nil
	}}
	engine := NewEngine(mgr, store)

	meta := state.Meta{Pid: 4242, Pgid: 4242}
	engine.stopProcess(path, &meta)

	data, err := os.ReadFile(store.StderrLog(path))
	if err != nil {
		t.Fatalf("no se pudo leer el log de stderr: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "stop:") {
		t.Errorf("el aviso no lleva el prefijo de stop: %q", got)
	}
	if !strings.Contains(got, "sobrevivió a SIGKILL") {
		t.Errorf("el aviso del manager no llegó al log: %q", got)
	}
	// vroom warnings must not mix into the service stdout, which is what the user copies elsewhere.
	if _, err := os.Stat(store.StdoutLog(path)); err == nil {
		if data, err := os.ReadFile(store.StdoutLog(path)); err == nil && len(data) > 0 {
			t.Errorf("stop escribió en el log de stdout del servicio: %q", data)
		}
	}
}

// An empty stderr file suggests something was written and lost, so the log is only created when there is something to say.
func TestStopProcessSinAvisosNoCreaElLogDeStderr(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)
	path := t.TempDir()

	engine.stopProcess(path, &state.Meta{Pid: 4242, Pgid: 4242})

	if _, err := os.Stat(store.StderrLog(path)); err == nil {
		t.Error("parar sin avisos creó un log de stderr vacío")
	}
}

// The service starts with its log files not existing yet, so without O_CREATE the first warning would be lost until the next start.
func TestAppendLineAnexaYSobreviveAUnLogQueNoExiste(t *testing.T) {
	dir := t.TempDir()
	// A missing parent directory must surface the error: creating the file is not creating the tree.
	path := filepath.Join(dir, "sub", "log")

	err := appendLine(path, "primera")
	if err == nil {
		t.Fatal("appendLine a un directorio inexistente tiene que dar error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want NotExist", err)
	}

	real := filepath.Join(dir, "log")
	if err := appendLine(real, "primera"); err != nil {
		t.Fatal(err)
	}
	if err := appendLine(real, "segunda"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	want := "primera\nsegunda\n"
	if string(data) != want {
		t.Errorf("log = %q, want %q: tiene que anexar", data, want)
	}
}

// It deliberately does not reuse the TUI's uiStatus.alive(): separate packages, and the duplication is what keeps them from diverging.
func TestProcessAliveCubreTodosLosEstados(t *testing.T) {
	tests := []struct {
		status process.Status
		want   bool
		why    string
	}{
		{process.StatusRunning, true, "running: hay proceso y puerto"},
		{process.StatusPortPending, true, "port_pending: el discovery sigue en vuelo, no se toca"},
		{process.StatusNoPort, true, "no_port: el servicio no declara puerto, eso es válido"},
		{process.StatusPortUnresolved, true, "port_unresolved: el proceso vive aunque no se sepa el puerto"},
		{process.StatusStopped, false, "stopped: hay que pararlo"},
		{process.StatusUnknown, false, "unknown: no hay prueba de vida, hay que arrancarlo"},
		{process.Status(""), false, "estado vacío es ausencia de prueba"},
		{process.Status("inventado"), false, "un estado que no existe es ausencia de prueba"},
	}
	for _, tt := range tests {
		if got := processAlive(tt.status); got != tt.want {
			t.Errorf("processAlive(%q) = %v, want %v (%s)", tt.status, got, tt.want, tt.why)
		}
	}
}

// A plan naming a service that resolves nowhere must not be published, or the user approves it and the launch dies at the stage.
func TestDryRunConServicioQueNoResuelveDaError(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "plano", Stages: []Stage{{Name: "s1", Services: []string{"api"}}}}
	projects := []scanner.Project{
		{Path: "/dev/web", Name: "web", Configured: true, Manifest: &manifest.Manifest{Name: "web", Command: "./web"}},
	}

	result, err := engine.DryRun(stack, projects)
	if err == nil {
		t.Fatalf("DryRun con un servicio inexistente devolvió %+v sin error", result)
	}
	if result != nil {
		t.Errorf("result = %+v con error: un plan que no se puede ejecutar no se publica a medias", result)
	}
}

// The repeated-name case is what a user writes by accident (copying a stage), and it only dies at launch, on the ambiguous resolution.
func TestDryRunConStageSinServiciosDaError(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./api"}},
	}

	// MEDIDO: a stage with no services is not an error; the "at least one service" rule lives in ParseComposeFile, and duplicating it here would give DryRun an obligation nobody has (TestParseStageNoServices covers it).
	stack := &Stack{Name: "plano", Stages: []Stage{{Name: "s1"}}}
	result, err := engine.DryRun(stack, projects)
	if err != nil {
		t.Errorf("DryRun de una etapa sin servicios dio error: %v", err)
	} else if len(result.Stages) != 1 || len(result.Stages[0].Services) != 0 {
		t.Errorf("resultado = %+v: la etapa vacía se publica tal cual", result)
	}

	dup := &Stack{Name: "plano", Stages: []Stage{
		{Name: "s1", Services: []string{"api"}},
		{Name: "s2", Services: []string{"api"}},
	}}
	if _, err := engine.DryRun(dup, projects); err != nil {
		t.Errorf("el mismo servicio en dos etapas no es error en dry run: %v", err)
	}
}

// The total must count distinct services, or a service repeated in two stages makes the TUI counter disagree with the rows under it.
func TestStackStatusConServicioDuplicadoLoCuentaUnaSolaVez(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "dup", Stages: []Stage{
		{Name: "s1", Services: []string{"api", "web"}},
		{Name: "s2", Services: []string{"api"}},
	}}
	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./api"}},
		{Path: "/dev/web", Name: "web", Configured: true, Manifest: &manifest.Manifest{Name: "web", Command: "./web"}},
	}

	running, total, err := engine.StackStatus(stack, projects)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2: api sale en dos etapas pero es un servicio", total)
	}
	_ = running
}

// meta.Pid > 0 is what separates "started at some point" from "running now"; without it a stopped service stays up in the TUI while stopService already treats it as dead.
func TestStackStatusConMetaDePidCeroNoCuentaComoRunning(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	path := t.TempDir()
	if err := store.SaveMeta(path, state.Meta{State: state.StateStopped}); err != nil {
		t.Fatal(err)
	}
	projects := []scanner.Project{{Path: path, Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./api"}}}

	running, total, err := engine.StackStatus(&Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"api"}}}}, projects)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if running != 0 {
		t.Errorf("running = %d con un meta sin PID, want 0", running)
	}
}

// Failing the whole status on one unreadable meta would hide the rest of the stack, which is what the user needs to see.
func TestStackStatusConMetaIlegibleNoFalla(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	path := t.TempDir()
	if err := os.MkdirAll(filepath.Join(path, ".."), 0o755); err != nil {
		t.Fatal(err)
	}
	projects := []scanner.Project{{Path: path, Name: "raro", Configured: true, Manifest: &manifest.Manifest{Name: "raro", Command: "./x"}}}

	running, total, err := engine.StackStatus(&Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"raro"}}}}, projects)
	if err != nil {
		t.Errorf("un meta que no se puede leer no puede fallar el status: %v", err)
	}
	if total != 1 || running != 0 {
		t.Errorf("running/total = %d/%d, want 0/1", running, total)
	}
}
