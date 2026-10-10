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
			Name: "api", Command: "./api", URLGeneration: manifest.URLGenNone,
		}},
	}

	ch := engine.LaunchAsync(stack, projects)

	select {
	case result, ok := <-ch:
		if !ok {
			t.Fatal("the channel arrived closed without delivering a result")
		}
		if !result.OK || result.Stack != "async" {
			t.Errorf("result = %+v, want OK and stack async", result)
		}
		if len(result.Stages) != 1 || len(result.Stages[0].Services) != 1 {
			t.Fatalf("the result does not carry the stage with its service: %+v", result)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("LaunchAsync delivered nothing in 30s: the TUI would hang")
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("the channel delivered a second result")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the channel was not closed after delivering the result")
	}
}

// There is no error return here, so the failure must travel inside the result: closing the channel without sending would show the TUI a finished stack with no reason.
func TestLaunchAsyncConErrorDeValidacionLoEntregaComoResultado(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "broken", PrimaryGroup: "g1", Stages: []Stage{
		{Name: "s1", Services: []string{"does-not-exist"}, Timeout: time.Second},
	}}

	select {
	case result := <-engine.LaunchAsync(stack, nil):
		if result.OK {
			t.Error("a stack with a nonexistent service cannot come out OK")
		}
		if result.Stack != "broken" {
			t.Errorf("Stack = %q, want broken: the error has to say which stack it was on", result.Stack)
		}
		if result.Error == "" {
			t.Error("without Error the user sees a red stack with no explanation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LaunchAsync delivered nothing")
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

	engine.stopService(scanner.Project{Path: path, Name: "no-manifest", Manifest: nil})

	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 4242 {
		t.Errorf("Pid = %d after stopping a project without manifest, want 4242: there is no service to stop", meta.Pid)
	}
}

// The stop must stay silent when nothing was ever started, or vroom stop of a stack holding such a service would fail.
func TestStopServiceConMetaAusenteNoFalla(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	p := scanner.Project{
		Path:       t.TempDir(),
		Name:       "never-started",
		Configured: true,
		Manifest:   &manifest.Manifest{Name: "never-started", Command: "./x", URLGeneration: manifest.URLGenNone},
	}
	engine.stopService(p)

	stack := &Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"never-started"}}}}
	if err := engine.StopStack(stack, []scanner.Project{p}); err != nil {
		t.Errorf("StopStack of a service without meta gave error %v", err)
	}
}

// A service already dead never reached the release path, so its route would stay taken and block the next service that wants that name.
func TestStopServiceRetiraLaRutaYElPuertoDeUnServicioYaMuerto(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(root)
	engine := NewEngine(&mockManager{}, store)

	path := t.TempDir()
	const routeName = "vroom-test-dead-route"
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
		Name:     "dead",
		Manifest: &manifest.Manifest{Name: "dead", Command: "./x", URLGeneration: manifest.URLGenNone},
	})

	got, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.RouteOwned {
		t.Error("RouteOwned is still true after stopping a dead service: the route stays taken forever")
	}
	if got.State != state.StateStopped {
		t.Errorf("State = %q, want %q", got.State, state.StateStopped)
	}
	if got.Pid != 0 || got.Pgid != 0 {
		t.Errorf("Pid/Pgid = %d/%d after stopping, want 0/0", got.Pid, got.Pgid)
	}
}

// RouteOwned false means another service holds the handle, and the name survives revocation so reconciliation can still find it; deleting on that basis would remove a foreign route.
func TestStopServiceNoRetiraUnaRutaQueNoEraSuya(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	path := t.TempDir()
	if err := store.SaveMeta(path, state.Meta{
		State:      state.StateRunning,
		RouteName:  "vroom-test-foreign-route",
		RouteOwned: false,
	}); err != nil {
		t.Fatal(err)
	}

	engine.stopService(scanner.Project{
		Path:     path,
		Name:     "foreign",
		Manifest: &manifest.Manifest{Name: "foreign", Command: "./x", URLGeneration: manifest.URLGenNone},
	})

	got, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.RouteOwned {
		t.Error("RouteOwned = true after stopping a service whose route was not its own")
	}
	if got.RouteName != "vroom-test-foreign-route" {
		t.Errorf("RouteName = %q: another's route cannot disappear from the meta", got.RouteName)
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
			spec.Warn("descendant %d survived SIGKILL", 999)
		}
		return nil
	}}
	engine := NewEngine(mgr, store)

	meta := state.Meta{Pid: 4242, Pgid: 4242}
	engine.stopProcess(path, &meta)

	data, err := os.ReadFile(store.StderrLog(path))
	if err != nil {
		t.Fatalf("could not read the stderr log: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "stop:") {
		t.Errorf("the warning does not carry the stop prefix: %q", got)
	}
	if !strings.Contains(got, "survived SIGKILL") {
		t.Errorf("the manager's warning did not reach the log: %q", got)
	}
	// vroom warnings must not mix into the service stdout, which is what the user copies elsewhere.
	if _, err := os.Stat(store.StdoutLog(path)); err == nil {
		if data, err := os.ReadFile(store.StdoutLog(path)); err == nil && len(data) > 0 {
			t.Errorf("stop wrote into the service stdout log: %q", data)
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
		t.Error("stopping without warnings created an empty stderr log")
	}
}

// The service starts with its log files not existing yet, so without O_CREATE the first warning would be lost until the next start.
func TestAppendLineAnexaYSobreviveAUnLogQueNoExiste(t *testing.T) {
	dir := t.TempDir()
	// A missing parent directory must surface the error: creating the file is not creating the tree.
	path := filepath.Join(dir, "sub", "log")

	err := appendLine(path, "primera")
	if err == nil {
		t.Fatal("appendLine to a nonexistent directory must produce an error")
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
		t.Errorf("log = %q, want %q: it must append", data, want)
	}
}

// It deliberately does not reuse the TUI's uiStatus.alive(): separate packages, and the duplication is what keeps them from diverging.
func TestProcessAliveCubreTodosLosEstados(t *testing.T) {
	tests := []struct {
		status process.Status
		want   bool
		why    string
	}{
		{process.StatusRunning, true, "running: there is a process and port"},
		{process.StatusPortPending, true, "port_pending: discovery is still in flight, do not touch"},
		{process.StatusNoPort, true, "no_port: the service declares no port, that is valid"},
		{process.StatusPortUnresolved, true, "port_unresolved: the process lives even if the port is unknown"},
		{process.StatusStopped, false, "stopped: it must be stopped"},
		{process.StatusUnknown, false, "unknown: there is no proof of life, it must be started"},
		{process.Status(""), false, "empty status is absence of proof"},
		{process.Status("inventado"), false, "a status that does not exist is absence of proof"},
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

	stack := &Stack{Name: "flat", Stages: []Stage{{Name: "s1", Services: []string{"api"}}}}
	projects := []scanner.Project{
		{Path: "/dev/web", Name: "web", Configured: true, Manifest: &manifest.Manifest{Name: "web", Command: "./web"}},
	}

	result, err := engine.DryRun(stack, projects)
	if err == nil {
		t.Fatalf("DryRun with a nonexistent service returned %+v without error", result)
	}
	if result != nil {
		t.Errorf("result = %+v with error: a plan that cannot be executed is not published half-done", result)
	}
}

// The repeated-name case is what a user writes by accident (copying a stage), and it only dies at launch, on the ambiguous resolution.
func TestDryRunConStageSinServiciosDaError(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./api"}},
	}

	// MEASURED: a stage with no services is not an error; the "at least one service" rule lives in ParseComposeFile, and duplicating it here would give DryRun an obligation nobody has (TestParseStageNoServices covers it).
	stack := &Stack{Name: "flat", Stages: []Stage{{Name: "s1"}}}
	result, err := engine.DryRun(stack, projects)
	if err != nil {
		t.Errorf("DryRun of a stage without services gave error: %v", err)
	} else if len(result.Stages) != 1 || len(result.Stages[0].Services) != 0 {
		t.Errorf("result = %+v: the empty stage is published as-is", result)
	}

	dup := &Stack{Name: "flat", Stages: []Stage{
		{Name: "s1", Services: []string{"api"}},
		{Name: "s2", Services: []string{"api"}},
	}}
	if _, err := engine.DryRun(dup, projects); err != nil {
		t.Errorf("the same service in two stages is not an error in dry run: %v", err)
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
		t.Errorf("total = %d, want 2: api appears in two stages but is one service", total)
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
		t.Errorf("running = %d with a meta without PID, want 0", running)
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
	projects := []scanner.Project{{Path: path, Name: "weird", Configured: true, Manifest: &manifest.Manifest{Name: "weird", Command: "./x"}}}

	running, total, err := engine.StackStatus(&Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"weird"}}}}, projects)
	if err != nil {
		t.Errorf("an unreadable meta cannot fail the status: %v", err)
	}
	if total != 1 || running != 0 {
		t.Errorf("running/total = %d/%d, want 0/1", running, total)
	}
}
