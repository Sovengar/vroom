package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// brokenStateHome points XDG_STATE_HOME at a FILE: a file where a directory is expected fails for every uid, while a chmod-less directory does not (CI may run as root).
func brokenStateHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(writeFile(t, filepath.Join(t.TempDir(), "blocked"), "x"), "sub"))
}

// TestStoreIlegibleSeDistingueDeUnProyectoInexistente: an unreadable store must say "state directory" and never "project not found" (the agent would hunt for a typo) nor "scan error" (the store does not scan), because it picks where to look from that message.
func TestStoreIlegibleSeDistingueDeUnProyectoInexistente(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)
	brokenStateHome(t)

	for _, cmd := range []string{"list", "start", "stop", "build", "install", "logs"} {
		t.Run(cmd, func(t *testing.T) {
			_, _, err := dispatch([]string{cmd, "api"})
			if err == nil {
				t.Fatalf("%s should fail with an unusable state dir", cmd)
			}
			if strings.Contains(err.Error(), "project not found") {
				t.Errorf("%s: an unreadable store disguised itself as a nonexistent project: %q", cmd, err)
			}
			if strings.Contains(err.Error(), "scan error") {
				t.Errorf("%s: an unreadable store is not a scan failure: %q", cmd, err)
			}
			if !strings.Contains(err.Error(), "state directory") {
				t.Errorf("%s: the error does not explain that the problem is the state directory: %q", cmd, err)
			}
		})
	}
}

// TestEscaneoFallidoLlevaSuPropioPrefijo: the scan runs before the project lookup, so even a valid name fails, which is what pins that ordering.
func TestEscaneoFallidoLlevaSuPropioPrefijo(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)

	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeConfigScannerRoot(t, notADir)

	_, _, err := dispatch([]string{"start", "api"})
	if err == nil {
		t.Fatal("a root that is not a directory should fail")
	}
	if !strings.Contains(err.Error(), "scan error") {
		t.Errorf("err = %q, want the prefix 'scan error'", err)
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("the error does not preserve the cause of the scan failure: %q", err)
	}

	if _, err := cmdList(); err == nil || !strings.Contains(err.Error(), "scan error") {
		t.Errorf("list with an invalid root = %v, want the scan error", err)
	}
}

func TestCmdListConEscaneoFallidoNoDevuelveFilaVacia(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)
	writeConfigScannerRoot(t, filepath.Join(root, "api", ".vroom.toml")) // a file

	payload, err := cmdList()
	if err == nil {
		t.Fatalf("list returned %+v: a scan failure cannot be an empty list", payload)
	}
	if payload != nil {
		t.Errorf("with error no payload should be returned: %+v", payload)
	}
}

// TestCmdStartPropagaElFalloDeArranque: MEDIDO, only an empty command_start surfaces here; a nonexistent binary is discovered by the daemonized child's sh -c after Start already returned, and vroom never waits for liveness, so asserting it would freeze a defect as a contract.
func TestCmdStartPropagaElFalloDeArranque(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	store2 := mustStore(t)
	apiPath := filepath.Join(root, "api")
	if _, err := store2.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}
	// A directory at stdout.log makes OpenFile fail with EISDIR, the outermost start failure reachable without faking the process engine.
	if err := os.MkdirAll(store2.StdoutLog(apiPath), 0o755); err != nil {
		t.Fatal(err)
	}

	payload, err := cmdStart("api", "")
	if err == nil {
		t.Fatalf("a manifest without command_start cannot start: returned %+v", payload)
	}
	if !strings.Contains(err.Error(), "start failed") {
		t.Errorf("err = %q, want the prefix 'start failed' from the contract", err)
	}
	meta, merr := store.LoadMeta(apiPath)
	if merr == nil && meta.Pid != 0 {
		t.Errorf("meta.Pid = %d after a failed start: it would claim a process that does not exist", meta.Pid)
	}
}

func TestCmdStartConDirectorioDeServicioIlegible(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	// A file at services/<hash> makes EnsureServiceDir's MkdirAll fail for any uid.
	apiPath := filepath.Join(root, "api")
	if err := os.WriteFile(store.ServiceDir(apiPath), []byte("blocks the mkdir"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := cmdStart("api", "")
	if err == nil {
		t.Fatal("with services/<hash> occupied by a file, start should fail")
	}
	if !strings.Contains(err.Error(), "could not create service dir") {
		t.Errorf("err = %q, want the service directory prefix", err)
	}
	if strings.Contains(err.Error(), "start failed") {
		t.Errorf("a disk failure is not a start command failure: %q", err)
	}
}

// TestCmdStopPropagaElFalloDeClearPid: the one unrecoverable stopCleanup failure, because the Meta would keep advertising a live PID for an already-stopped service and the next list would publish a service that does not exist.
func TestCmdStopPropagaElFalloDeClearPid(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a directory without permission: the ClearPid failure cannot be provoked")
	}
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(apiPath, state.Meta{Name: "api", Pid: 999999}); err != nil {
		t.Fatal(err)
	}
	// ClearPid ignores os.IsNotExist by design, so the pid file must exist for the unlink to fail for real.
	if err := os.WriteFile(store.PidFile(apiPath), []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := store.ServiceDir(apiPath)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := cmdStop("api", "")
	if err == nil {
		t.Fatal("with a Meta that cannot be cleaned, stop should fail and not claim it stopped")
	}
	if !strings.Contains(err.Error(), "could not clear pid") {
		t.Errorf("err = %q, want the contract prefix", err)
	}
	payload, _, derr := dispatch([]string{"stop", "api"})
	if derr == nil {
		t.Fatal("stop should propagate the failure")
	}
	if payload != nil {
		t.Errorf("with error no payload should be returned: %+v", payload)
	}
}

func TestStopCleanupRegistraLosAvisosDelKillEnElLog(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(apiPath, state.Meta{Name: "api", Pid: 424242, Port: 4321}); err != nil {
		t.Fatal(err)
	}

	mgr := &aliveManager{}
	if err := stopCleanup(store, mgr, apiPath); err != nil {
		t.Fatal(err)
	}

	if len(mgr.stopped) != 1 {
		t.Fatalf("Stop was called %d times, want 1", len(mgr.stopped))
	}
	if got := mgr.stopped[0].Pid; got != 424242 {
		t.Errorf("Stop received Pid %d, want the one from Meta", got)
	}

	log := readFileString(t, store.StderrLog(apiPath))
	if !strings.Contains(log, "stopped pid 424242") {
		t.Errorf("the kill notice did not reach the service log:\n%s", log)
	}
	if !strings.Contains(log, "service stopped") {
		t.Errorf("missing the stop end marker in the log:\n%s", log)
	}
}

func TestStopCleanupSinProcesoNoTocaElManager(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(apiPath, state.Meta{Name: "api"}); err != nil {
		t.Fatal(err)
	}

	mgr := &aliveManager{}
	if err := stopCleanup(store, mgr, apiPath); err != nil {
		t.Fatal(err)
	}
	if len(mgr.stopped) != 0 {
		t.Errorf("Stop was called with no process to stop: %v", mgr.stopped)
	}
	meta, err := store.LoadMeta(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != state.StateStopped {
		t.Errorf("meta.State = %q, want stopped: stopping normalizes even if there was nothing to stop", meta.State)
	}
}

func TestStopCleanupConMetaAusenteIgualNormaliza(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}

	mgr := &aliveManager{}
	if err := stopCleanup(store, mgr, apiPath); err != nil {
		t.Errorf("without Meta the cleanup cannot fail: %v", err)
	}
	if len(mgr.stopped) != 0 {
		t.Errorf("Stop was called without Meta: %v", mgr.stopped)
	}
}

// TestReleaseRouteOnStopSoloRevocaSiLaRetiradaSurtioEfecto: revoking ownership over a still-live route orphans it with nobody left to clean it up, so a failed removal must keep the ownership.
func TestReleaseRouteOnStopSoloRevocaSiLaRetiradaSurtioEfecto(t *testing.T) {
	installCLIReleaser(t, &recordingReleaser{})

	meta := state.Meta{Name: "p", RouteName: "route", RouteOwned: true}
	releaseRouteOnStop(&meta)

	if meta.RouteOwned {
		t.Error("after an effective removal the ownership must be revoked")
	}
	// RouteName survives the revocation because next-boot reconciliation looks the handle up there.
	if meta.RouteName != "route" {
		t.Errorf("RouteName = %q: the reconciliation handle must be preserved", meta.RouteName)
	}
}

func TestReleaseRouteOnStopConservaLaPropiedadSiRetiradaFalla(t *testing.T) {
	installCLIFailingReleaser(t)

	meta := state.Meta{Name: "p", RouteName: "route", RouteOwned: true}
	releaseRouteOnStop(&meta)

	if !meta.RouteOwned {
		t.Error("a failed removal does not revoke: the route can stay alive with no owner to clean it up")
	}
}

func TestReleaseRouteOnStopNoRetiraLoQueNoEsNuestro(t *testing.T) {
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)

	meta := state.Meta{Name: "p", RouteName: "foreign", RouteOwned: false}
	releaseRouteOnStop(&meta)

	if len(rec.removed) != 0 {
		t.Errorf("a route that is never ours is not touched: %v", rec.removed)
	}
}

// TestCliRouteReleaserDevuelveNilFueraDeUnBinarioDeTest: the nil-vs-inert decision keys on os.Args[0], so a test fakes a production binary just by renaming it; that is the only way production's return nil is reachable from a test.
func TestCliRouteReleaserDevuelveNilFueraDeUnBinarioDeTest(t *testing.T) {
	orig := os.Args[0]
	t.Cleanup(func() { os.Args[0] = orig })

	os.Args[0] = "/usr/local/bin/vroom"
	if got := cliRouteReleaser(); got != nil {
		t.Error("in production the releaser is nil: nil means 'build the real client'")
	}

	// Outside a test binary the releaser stays inert, so a test that forgets installCLIReleaser cannot rewrite the developer's routes.json.
	os.Args[0] = "/tmp/vroom.test"
	got := cliRouteReleaser()
	if got == nil {
		t.Fatal("in a test binary the releaser cannot be nil: it would build the real client")
	}
	if err := got.RemoveAbsent("whatever"); err != nil {
		t.Errorf("the inert releaser returned %v, it must always be nil", err)
	}

	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)
	os.Args[0] = "/usr/local/bin/vroom"
	if got := cliRouteReleaser(); got == nil {
		t.Error("with the seam installed the seam takes precedence over the environment")
	}
}

// failingReleaser always fails, like a broken portless binary or a missing Node.
type failingReleaser struct{}

func (*failingReleaser) RemoveAbsent(string) error { return errors.New("requires Node >= 24") }

var _ portless.Releaser = (*failingReleaser)(nil)

func mustStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestBuildProjectInfoColapsaElGrupoSecundario: the key must match the TUI's, or an agent reads collapsed:true for a group the TUI shows expanded.
func TestBuildProjectInfoColapsaElGrupoSecundario(t *testing.T) {
	store := mustStore(t)

	conSecundario := &fakeProject{
		name: "api", primary: "shop", secondary: "backend",
	}
	sinSecundario := &fakeProject{
		name: "web", primary: "shop",
	}

	collapsed := map[string]bool{"shop/backend": true, "shop": false}
	if got := buildProjectInfo(nil, store, collapsed, conSecundario.project(t)); !got.Collapsed {
		t.Error("with secondary group the key is primary/secondary: collapse should be true")
	}
	if got := buildProjectInfo(nil, store, collapsed, sinSecundario.project(t)); got.Collapsed {
		t.Error("without secondary group the key is just primary, and here it is not collapsed")
	}

	if got := buildProjectInfo(nil, store, map[string]bool{"shop": true}, conSecundario.project(t)); got.Collapsed {
		t.Error("collapsing primary cannot collapse primary/secondary: they are distinct groups")
	}
}

type fakeProject struct {
	name      string
	primary   string
	secondary string
}

func (f *fakeProject) project(t *testing.T) scanner.Project {
	t.Helper()
	dir := t.TempDir()
	m := &manifest.Manifest{Name: f.name, Command: "sleep 30", PrimaryGroup: f.primary, SecondaryGroup: f.secondary}
	return scanner.Project{Path: dir, Name: f.name, Configured: true, Manifest: m}
}

// TestOutputJSONConFalloDeEscrituraSaleConUno: a silently unwritten response reads as "no projects" to an agent and gets cached, so a failed write must exit 1 and say so on stderr.
func TestOutputJSONConFalloDeEscrituraSaleConUno(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	var errBuf bytes.Buffer
	handled, code := runInto(failingWriter{}, &errBuf, []string{"list"})
	if !handled {
		t.Fatal("the command was handled")
	}
	if code != 1 {
		t.Errorf("code = %d, want 1: the agent needs to know there is no response", code)
	}
	if !strings.Contains(errBuf.String(), "could not write response") {
		t.Errorf("stderr does not say it could not write: %q", errBuf.String())
	}
	if strings.Contains(errBuf.String(), "projects") {
		t.Errorf("stderr must not carry the response: %q", errBuf.String())
	}
}

// TestRunLoggedPropagaLosFallosDeEscrituraAntesDeEjecutar: running without logs hands the agent a bare build ok with nothing to inspect, so a write failure must happen before the command runs.
func TestRunLoggedPropagaLosFallosDeEscrituraAntesDeEjecutar(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(work, "executed")

	tests := []struct {
		name       string
		stdoutPath string
		stderrPath string
	}{
		{
			name:       "log directory blocked by a file",
			stdoutPath: filepath.Join(writeFile(t, filepath.Join(base, "block1"), "x"), "sub", "stdout.log"),
			stderrPath: filepath.Join(base, "case1", "stderr.log"),
		},
		{
			name:       "stdout.log is a directory",
			stdoutPath: makeDir(t, filepath.Join(base, "stdout.log")),
			stderrPath: filepath.Join(base, "case2", "stderr.log"),
		},
		{
			// The banner fits, so only the second OpenFile of stdout.log would fail; the failure is provoked on stderr.log's own OpenFile instead.
			name:       "stderr.log is a directory",
			stdoutPath: filepath.Join(base, "case3", "stdout.log"),
			stderrPath: makeDir(t, filepath.Join(base, "stderr.log")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Remove(marker)
			elapsed, code, err := runLogged("build", "touch "+marker, work, tt.stdoutPath, tt.stderrPath)
			if err == nil {
				t.Fatalf("runLogged with %s should fail", tt.name)
			}
			if elapsed != 0 || code != 0 {
				t.Errorf("a write failure returns elapsed=%v code=%d, want 0,0", elapsed, code)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Error("the command executed despite not being able to write the log: the agent would see ok with no output")
			}
		})
	}
}

func TestRunLoggedDaElCodigoDeSalidaRealYLoAnotaEnElLog(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")

	elapsed, code, err := runLogged("build", "echo outside; echo inside >&2; exit 42", dir, out, errLog)
	if code != 42 {
		t.Errorf("code = %d, want 42", code)
	}
	if err == nil {
		t.Fatal("an exit 42 must be an error: the command did NOT run")
	}
	// MEDIDO: a 3ms command rounds to 0 elapsed, so elapsed cannot prove the command ran; only the rounding itself is asserted.
	if elapsed%10*time.Millisecond != 0 {
		t.Errorf("elapsed = %v is not rounded to 10ms", elapsed)
	}

	if got := readFileString(t, errLog); !strings.Contains(got, "inside") {
		t.Errorf("the command's stderr did not reach its log: %q", got)
	}
	body := readFileString(t, out)
	if !strings.Contains(body, "vroom ▶ build") {
		t.Errorf("missing the command banner:\n%s", body)
	}
	if !strings.Contains(body, "vroom ✗ build failed (exit 42") {
		t.Errorf("the log does not say the build failed with 42:\n%s", body)
	}
}

func TestRunLoggedRedondeaElTiempoADiezesDeMil(t *testing.T) {
	dir := t.TempDir()
	elapsed, _, err := runLogged("build", "sleep 0.05", dir, filepath.Join(dir, "o.log"), filepath.Join(dir, "e.log"))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed%10*time.Millisecond != 0 {
		t.Errorf("elapsed = %v is not rounded to 10ms: two runs would give different numbers", elapsed)
	}
}

// TestCmdLaunchPropagaElFalloDeSesionYDeEscaneo: launch resolves the stack first and builds store and scan only afterwards, which is why these two failures surface here and not in --list.
func TestCmdLaunchPropagaElFalloDeSesionYDeEscaneo(t *testing.T) {
	t.Run("unreadable store", func(t *testing.T) {
		root := cliEnv(t)
		t.Chdir(root)
		composeStack(t, root, "front", [2]string{"front", `"web"`})
		brokenStateHome(t)

		_, err := cmdLaunch([]string{"front"})
		if err == nil {
			t.Fatal("with an unusable state dir, launch should fail")
		}
		if strings.Contains(err.Error(), "scan error") {
			t.Errorf("an unreadable store is not a scan failure: %q", err)
		}
	})

	t.Run("failed scan", func(t *testing.T) {
		root := cliEnv(t)
		t.Chdir(root)
		composeStack(t, root, "front", [2]string{"front", `"web"`})
		writeConfigScannerRoot(t, filepath.Join(root, "api", ".vroom.toml")) // a file

		_, err := cmdLaunch([]string{"front"})
		if err == nil {
			t.Fatal("with a root that is not a directory, launch should fail")
		}
		if !strings.Contains(err.Error(), "scan error") {
			t.Errorf("err = %q, want the prefix 'scan error'", err)
		}
	})
}

// TestDispatchReparteLaunchYHelp: a case pasted into dispatch's default silently opens the TUI instead of answering, so these two out-of-table subcommands are pinned.
func TestDispatchReparteLaunchYHelp(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)
	composeStack(t, root, "front", [2]string{"front", `"web"`})

	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"launch", "--list"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			payload, handled, err := dispatch(args)
			if !handled || err != nil {
				t.Fatalf("dispatch(%v) = handled %v, err %v; if a command falls into default it opens the TUI instead of responding", args, handled, err)
			}
			if payload == nil {
				t.Errorf("dispatch(%v) returned nil payload", args)
			}
		})
	}
}

// TestCmdLaunchDryConUnServicioInexistenteEsError: a dry-run that OKs a plan the real launch would reject saves the agent nothing, so --dry validates exactly like a real launch.
func TestCmdLaunchDryConUnServicioInexistenteEsError(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)
	composeStack(t, root, "front", [2]string{"front", `"does-not-exist"`})

	payload, err := cmdLaunch([]string{"front", "--dry"})
	if err == nil {
		t.Fatalf("a --dry with a nonexistent service should fail, returned %+v", payload)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("err = %q, want the name of the service that is not found", err)
	}
	if payload != nil {
		t.Errorf("with error no plan should be returned: %+v", payload)
	}

	if _, err := cmdLaunch([]string{"front"}); err == nil {
		t.Error("the real launch of the same stack should fail with the same nonexistent service")
	}
}

// TestRunLoggedSinShDevuelveElErrorDeEjecucion: no process ran, so there is no exit code; code 0 together with Error is how the agent tells a phantom build from a real one.
func TestRunLoggedSinShDevuelveElErrorDeEjecucion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")

	_, code, err := runLogged("build", "echo hello", dir,
		filepath.Join(dir, "o.log"), filepath.Join(dir, "e.log"))
	if err == nil {
		t.Fatal("without interpreter the command cannot execute")
	}
	if code != 0 {
		t.Errorf("code = %d, want 0: there was no exit code because there was no process", code)
	}
	if body := readFileString(t, filepath.Join(dir, "o.log")); !strings.Contains(body, "build failed") {
		t.Errorf("the log does not record the failure:\n%s", body)
	}
}

// TestRunConUnComandoQueNoFallaDevuelveQueLoManejo: exit is injected rather than calling os.Exit because the failing-subcommand branch is otherwise unreachable from a test (see TestRunPideSalirConElCodigoDelSubcomandoQueFalla).
func TestRunConUnComandoQueNoFallaDevuelveQueLoManejo(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	salidas := 0
	exit := func(int) { salidas++ }

	if !Run([]string{"help"}, exit) {
		t.Fatal("Run(help) should handle the command")
	}
	if Run(nil, exit) {
		t.Error("Run without arguments should not handle anything: main would launch the TUI twice")
	}
	if salidas != 0 {
		t.Errorf("exit was requested %d times, want 0: a command that goes well and a start without "+
			"subcommand do not kill the process", salidas)
	}
}

func TestRunPideSalirConElCodigoDelSubcomandoQueFalla(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	var pedidos []int
	handled := Run([]string{"stop", "servicio-que-no-existe"}, func(c int) { pedidos = append(pedidos, c) })

	if !handled {
		t.Fatal("stop is a known subcommand: it must say it handled it even if it fails")
	}
	if len(pedidos) != 1 {
		t.Fatalf("exit = %v, want exactly one call: a failing subcommand kills the "+
			"process only once", pedidos)
	}
	if pedidos[0] == 0 {
		t.Errorf("exit = 0 for a failed subcommand: a 0 tells the shell and the agent that everything " +
			"went well, and the service remains as it was")
	}
}

// TestNormalizePathConUnDirectorioBorradoNoFalla: MEDIDO, with no CWD filepath.Abs fails, so normalizePath returns the path as-is instead of erroring, because findByPath normalizes both sides anyway and a typo must not surface as "cannot resolve working dir".
func TestNormalizePathConUnDirectorioBorradoNoFalla(t *testing.T) {
	gone := t.TempDir()
	t.Chdir(gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	got := normalizePath("api")
	if got != "api" {
		t.Errorf("normalizePath = %q without CWD, want the path as-is", got)
	}

	t.Chdir(t.TempDir())
	full := filepath.Join(mustGetwd(t), "api")
	if got := normalizePath("api"); got != full {
		t.Errorf("normalizePath = %q with valid CWD, want %q", got, full)
	}
	real := t.TempDir()
	link := filepath.Join(filepath.Dir(real), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := normalizePath(link); got != real {
		t.Errorf("normalizePath(symlink) = %q, want the destination %q", got, real)
	}

}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
