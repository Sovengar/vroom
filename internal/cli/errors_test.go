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
	t.Setenv("XDG_STATE_HOME", filepath.Join(writeFile(t, filepath.Join(t.TempDir(), "bloqueado"), "x"), "sub"))
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
				t.Fatalf("%s con un state dir inutilizable debería fallar", cmd)
			}
			if strings.Contains(err.Error(), "project not found") {
				t.Errorf("%s: un store ilegible se disfrazo de proyecto inexistente: %q", cmd, err)
			}
			if strings.Contains(err.Error(), "scan error") {
				t.Errorf("%s: un store ilegible no es un fallo de escaneo: %q", cmd, err)
			}
			if !strings.Contains(err.Error(), "state directory") {
				t.Errorf("%s: el error no explica que el problema es el directorio de estado: %q", cmd, err)
			}
		})
	}
}

// TestEscaneoFallidoLlevaSuPropioPrefijo: the scan runs before the project lookup, so even a valid name fails, which is what pins that ordering.
func TestEscaneoFallidoLlevaSuPropioPrefijo(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)

	notADir := filepath.Join(t.TempDir(), "fichero")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeConfigScannerRoot(t, notADir)

	_, _, err := dispatch([]string{"start", "api"})
	if err == nil {
		t.Fatal("un root que no es un directorio debería fallar")
	}
	if !strings.Contains(err.Error(), "scan error") {
		t.Errorf("err = %q, want el prefijo 'scan error'", err)
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("el error no conserva la causa del fallo de escaneo: %q", err)
	}

	if _, err := cmdList(); err == nil || !strings.Contains(err.Error(), "scan error") {
		t.Errorf("list con un root inválido = %v, want el error de escaneo", err)
	}
}

func TestCmdListConEscaneoFallidoNoDevuelveFilaVacia(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)
	writeConfigScannerRoot(t, filepath.Join(root, "api", ".vroom.toml")) // un fichero

	payload, err := cmdList()
	if err == nil {
		t.Fatalf("list devolvió %+v: un fallo de escaneo no puede ser una lista vacía", payload)
	}
	if payload != nil {
		t.Errorf("con error no debe devolverse payload: %+v", payload)
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
		t.Fatalf("un manifiesto sin command_start no puede arrancar: devolvió %+v", payload)
	}
	if !strings.Contains(err.Error(), "start failed") {
		t.Errorf("err = %q, want el prefijo 'start failed' del contrato", err)
	}
	meta, merr := store.LoadMeta(apiPath)
	if merr == nil && meta.Pid != 0 {
		t.Errorf("meta.Pid = %d tras un arranque fallido: se afirmaría un proceso que no existe", meta.Pid)
	}
}

func TestCmdStartConDirectorioDeServicioIlegible(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	// A file at services/<hash> makes EnsureServiceDir's MkdirAll fail for any uid.
	apiPath := filepath.Join(root, "api")
	if err := os.WriteFile(store.ServiceDir(apiPath), []byte("bloquea el mkdir"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := cmdStart("api", "")
	if err == nil {
		t.Fatal("con services/<hash> ocupado por un fichero el arranque debería fallar")
	}
	if !strings.Contains(err.Error(), "could not create service dir") {
		t.Errorf("err = %q, want el prefijo del directorio de servicio", err)
	}
	if strings.Contains(err.Error(), "start failed") {
		t.Errorf("un fallo de disco no es un fallo del comando de arranque: %q", err)
	}
}

// TestCmdStopPropagaElFalloDeClearPid: the one unrecoverable stopCleanup failure, because the Meta would keep advertising a live PID for an already-stopped service and the next list would publish a service that does not exist.
func TestCmdStopPropagaElFalloDeClearPid(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede escribir en un directorio sin permiso: el fallo de ClearPid no se puede provocar")
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
		t.Fatal("con un Meta que no se puede limpiar, stop debería fallar y no afirmar que paró")
	}
	if !strings.Contains(err.Error(), "could not clear pid") {
		t.Errorf("err = %q, want el prefijo del contrato", err)
	}
	payload, _, derr := dispatch([]string{"stop", "api"})
	if derr == nil {
		t.Fatal("stop debería propagar el fallo")
	}
	if payload != nil {
		t.Errorf("con error no debe devolverse payload: %+v", payload)
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
		t.Fatalf("Stop se llamó %d veces, want 1", len(mgr.stopped))
	}
	if got := mgr.stopped[0].Pid; got != 424242 {
		t.Errorf("Stop recibió Pid %d, want el del Meta", got)
	}

	log := readFileString(t, store.StderrLog(apiPath))
	if !strings.Contains(log, "stopped pid 424242") {
		t.Errorf("el aviso del kill no llegó al log del servicio:\n%s", log)
	}
	if !strings.Contains(log, "service stopped") {
		t.Errorf("falta la marca de fin de stop en el log:\n%s", log)
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
		t.Errorf("Stop se llamó sin proceso que parar: %v", mgr.stopped)
	}
	meta, err := store.LoadMeta(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != state.StateStopped {
		t.Errorf("meta.State = %q, want stopped: parar normaliza aunque no hubiera nada que parar", meta.State)
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
		t.Errorf("sin Meta el cleanup no puede fallar: %v", err)
	}
	if len(mgr.stopped) != 0 {
		t.Errorf("Stop se llamó sin Meta: %v", mgr.stopped)
	}
}

// TestReleaseRouteOnStopSoloRevocaSiLaRetiradaSurtioEfecto: revoking ownership over a still-live route orphans it with nobody left to clean it up, so a failed removal must keep the ownership.
func TestReleaseRouteOnStopSoloRevocaSiLaRetiradaSurtioEfecto(t *testing.T) {
	installCLIReleaser(t, &recordingReleaser{})

	meta := state.Meta{Name: "p", RouteName: "ruta", RouteOwned: true}
	releaseRouteOnStop(&meta)

	if meta.RouteOwned {
		t.Error("tras una retirada efectiva la propiedad debe revocarse")
	}
	// RouteName survives the revocation because next-boot reconciliation looks the handle up there.
	if meta.RouteName != "ruta" {
		t.Errorf("RouteName = %q: el handle de reconcilización debe conservarse", meta.RouteName)
	}
}

func TestReleaseRouteOnStopConservaLaPropiedadSiRetiradaFalla(t *testing.T) {
	installCLIFailingReleaser(t)

	meta := state.Meta{Name: "p", RouteName: "ruta", RouteOwned: true}
	releaseRouteOnStop(&meta)

	if !meta.RouteOwned {
		t.Error("una retirada fallida no revoca: la ruta puede seguir viva y sin dueño que la limpie")
	}
}

func TestReleaseRouteOnStopNoRetiraLoQueNoEsNuestro(t *testing.T) {
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)

	meta := state.Meta{Name: "p", RouteName: "ajena", RouteOwned: false}
	releaseRouteOnStop(&meta)

	if len(rec.removed) != 0 {
		t.Errorf("una ruta nunca nuestra no se toca: %v", rec.removed)
	}
}

// TestCliRouteReleaserDevuelveNilFueraDeUnBinarioDeTest: the nil-vs-inert decision keys on os.Args[0], so a test fakes a production binary just by renaming it; that is the only way production's return nil is reachable from a test.
func TestCliRouteReleaserDevuelveNilFueraDeUnBinarioDeTest(t *testing.T) {
	orig := os.Args[0]
	t.Cleanup(func() { os.Args[0] = orig })

	os.Args[0] = "/usr/local/bin/vroom"
	if got := cliRouteReleaser(); got != nil {
		t.Error("en producción el releaser es nil: nil significa 'construye el cliente real'")
	}

	// Outside a test binary the releaser stays inert, so a test that forgets installCLIReleaser cannot rewrite the developer's routes.json.
	os.Args[0] = "/tmp/vroom.test"
	got := cliRouteReleaser()
	if got == nil {
		t.Fatal("en un binario de test el releaser no puede ser nil: construiría el cliente real")
	}
	if err := got.RemoveAbsent("lo-que-sea"); err != nil {
		t.Errorf("el releaser inerte devolvió %v, debe ser nil siempre", err)
	}

	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)
	os.Args[0] = "/usr/local/bin/vroom"
	if got := cliRouteReleaser(); got == nil {
		t.Error("con el seam instalado el seam manda sobre el entorno")
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
		name: "api", primary: "tienda", secondary: "backend",
	}
	sinSecundario := &fakeProject{
		name: "web", primary: "tienda",
	}

	collapsed := map[string]bool{"tienda/backend": true, "tienda": false}
	if got := buildProjectInfo(nil, store, collapsed, conSecundario.project(t)); !got.Collapsed {
		t.Error("con grupo secundario la clave es primary/secondary: el collapse tenía que salir true")
	}
	if got := buildProjectInfo(nil, store, collapsed, sinSecundario.project(t)); got.Collapsed {
		t.Error("sin grupo secundario la clave es primary a secas, y aquí no está colapsada")
	}

	if got := buildProjectInfo(nil, store, map[string]bool{"tienda": true}, conSecundario.project(t)); got.Collapsed {
		t.Error("colapsar primary no puede colapsar primary/secondary: son grupos distintos")
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
		t.Fatal("el comando sí se manejó")
	}
	if code != 1 {
		t.Errorf("code = %d, want 1: el agente necesita saber que no hay respuesta", code)
	}
	if !strings.Contains(errBuf.String(), "could not write response") {
		t.Errorf("stderr no dice que no se pudo escribir: %q", errBuf.String())
	}
	if strings.Contains(errBuf.String(), "projects") {
		t.Errorf("stderr no debe llevar la respuesta: %q", errBuf.String())
	}
}

// TestRunLoggedPropagaLosFallosDeEscrituraAntesDeEjecutar: running without logs hands the agent a bare build ok with nothing to inspect, so a write failure must happen before the command runs.
func TestRunLoggedPropagaLosFallosDeEscrituraAntesDeEjecutar(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(work, "ejecutado")

	tests := []struct {
		name       string
		stdoutPath string
		stderrPath string
	}{
		{
			name:       "directorio de log bloqueado por un fichero",
			stdoutPath: filepath.Join(writeFile(t, filepath.Join(base, "bloque1"), "x"), "sub", "stdout.log"),
			stderrPath: filepath.Join(base, "caso1", "stderr.log"),
		},
		{
			name:       "stdout.log es un directorio",
			stdoutPath: makeDir(t, filepath.Join(base, "stdout.log")),
			stderrPath: filepath.Join(base, "caso2", "stderr.log"),
		},
		{
			// The banner fits, so only the second OpenFile of stdout.log would fail; the failure is provoked on stderr.log's own OpenFile instead.
			name:       "stderr.log es un directorio",
			stdoutPath: filepath.Join(base, "caso3", "stdout.log"),
			stderrPath: makeDir(t, filepath.Join(base, "stderr.log")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Remove(marker)
			elapsed, code, err := runLogged("build", "touch "+marker, work, tt.stdoutPath, tt.stderrPath)
			if err == nil {
				t.Fatalf("runLogged con %s debería fallar", tt.name)
			}
			if elapsed != 0 || code != 0 {
				t.Errorf("un fallo de escritura devuelve elapsed=%v code=%d, want 0,0", elapsed, code)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Error("el comando se ejecutó pese a no poder escribir el log: el agente vería ok sin salida")
			}
		})
	}
}

func TestRunLoggedDaElCodigoDeSalidaRealYLoAnotaEnElLog(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")

	elapsed, code, err := runLogged("build", "echo fuera; echo dentro >&2; exit 42", dir, out, errLog)
	if code != 42 {
		t.Errorf("code = %d, want 42", code)
	}
	if err == nil {
		t.Fatal("un exit 42 debe ser error: el comando NO se hizo")
	}
	// MEDIDO: a 3ms command rounds to 0 elapsed, so elapsed cannot prove the command ran; only the rounding itself is asserted.
	if elapsed%10*time.Millisecond != 0 {
		t.Errorf("elapsed = %v no está redondeado a 10ms", elapsed)
	}

	if got := readFileString(t, errLog); !strings.Contains(got, "dentro") {
		t.Errorf("el stderr del comando no llegó a su log: %q", got)
	}
	body := readFileString(t, out)
	if !strings.Contains(body, "vroom ▶ build") {
		t.Errorf("falta el banner del comando:\n%s", body)
	}
	if !strings.Contains(body, "vroom ✗ build failed (exit 42") {
		t.Errorf("el log no dice que el build falló con 42:\n%s", body)
	}
}

func TestRunLoggedRedondeaElTiempoADiezesDeMil(t *testing.T) {
	dir := t.TempDir()
	elapsed, _, err := runLogged("build", "sleep 0.05", dir, filepath.Join(dir, "o.log"), filepath.Join(dir, "e.log"))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed%10*time.Millisecond != 0 {
		t.Errorf("elapsed = %v no está redondeado a 10ms: dos ejecuciones darían números distintos", elapsed)
	}
}

// TestCmdLaunchPropagaElFalloDeSesionYDeEscaneo: launch resolves the stack first and builds store and scan only afterwards, which is why these two failures surface here and not in --list.
func TestCmdLaunchPropagaElFalloDeSesionYDeEscaneo(t *testing.T) {
	t.Run("store ilegible", func(t *testing.T) {
		root := cliEnv(t)
		t.Chdir(root)
		composeStack(t, root, "front", [2]string{"front", `"web"`})
		brokenStateHome(t)

		_, err := cmdLaunch([]string{"front"})
		if err == nil {
			t.Fatal("con un state dir inutilizable, launch debería fallar")
		}
		if strings.Contains(err.Error(), "scan error") {
			t.Errorf("un store ilegible no es un fallo de escaneo: %q", err)
		}
	})

	t.Run("escaneo fallido", func(t *testing.T) {
		root := cliEnv(t)
		t.Chdir(root)
		composeStack(t, root, "front", [2]string{"front", `"web"`})
		writeConfigScannerRoot(t, filepath.Join(root, "api", ".vroom.toml")) // un fichero

		_, err := cmdLaunch([]string{"front"})
		if err == nil {
			t.Fatal("con un root que no es un directorio, launch debería fallar")
		}
		if !strings.Contains(err.Error(), "scan error") {
			t.Errorf("err = %q, want el prefijo 'scan error'", err)
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
				t.Fatalf("dispatch(%v) = handled %v, err %v; si un comando cae en default abre la TUI en vez de responder", args, handled, err)
			}
			if payload == nil {
				t.Errorf("dispatch(%v) devolvió payload nil", args)
			}
		})
	}
}

// TestCmdLaunchDryConUnServicioInexistenteEsError: a dry-run that OKs a plan the real launch would reject saves the agent nothing, so --dry validates exactly like a real launch.
func TestCmdLaunchDryConUnServicioInexistenteEsError(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)
	composeStack(t, root, "front", [2]string{"front", `"no-existe"`})

	payload, err := cmdLaunch([]string{"front", "--dry"})
	if err == nil {
		t.Fatalf("un --dry con un servicio inexistente debería fallar, devolvió %+v", payload)
	}
	if !strings.Contains(err.Error(), "no-existe") {
		t.Errorf("err = %q, want el nombre del servicio que no se encuentra", err)
	}
	if payload != nil {
		t.Errorf("con error no debe devolverse plan: %+v", payload)
	}

	if _, err := cmdLaunch([]string{"front"}); err == nil {
		t.Error("el launch real del mismo stack debería fallar con el mismo servicio inexistente")
	}
}

// TestRunLoggedSinShDevuelveElErrorDeEjecucion: no process ran, so there is no exit code; code 0 together with Error is how the agent tells a phantom build from a real one.
func TestRunLoggedSinShDevuelveElErrorDeEjecucion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")

	_, code, err := runLogged("build", "echo hola", dir,
		filepath.Join(dir, "o.log"), filepath.Join(dir, "e.log"))
	if err == nil {
		t.Fatal("sin intérprete el comando no puede ejecutarse")
	}
	if code != 0 {
		t.Errorf("code = %d, want 0: no hubo exit code porque no hubo proceso", code)
	}
	if body := readFileString(t, filepath.Join(dir, "o.log")); !strings.Contains(body, "build failed") {
		t.Errorf("el log no anota el fallo:\n%s", body)
	}
}

// TestRunConUnComandoQueNoFallaDevuelveQueLoManejo: exit is injected rather than calling os.Exit because the failing-subcommand branch is otherwise unreachable from a test (see TestRunPideSalirConElCodigoDelSubcomandoQueFalla).
func TestRunConUnComandoQueNoFallaDevuelveQueLoManejo(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	salidas := 0
	exit := func(int) { salidas++ }

	if !Run([]string{"help"}, exit) {
		t.Fatal("Run(help) debería manejar el comando")
	}
	if Run(nil, exit) {
		t.Error("Run sin argumentos no debe manejar nada: main lanzaría la TUI dos veces")
	}
	if salidas != 0 {
		t.Errorf("se pidió salir %d veces, want 0: un comando que va bien y un arranque sin "+
			"subcomando no matan el proceso", salidas)
	}
}

func TestRunPideSalirConElCodigoDelSubcomandoQueFalla(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	var pedidos []int
	handled := Run([]string{"stop", "servicio-que-no-existe"}, func(c int) { pedidos = append(pedidos, c) })

	if !handled {
		t.Fatal("stop es un subcomando conocido: tiene que decir que lo manejó aunque falle")
	}
	if len(pedidos) != 1 {
		t.Fatalf("exit = %v, want exactamente una llamada: un subcomando que falla mata el "+
			"proceso una sola vez", pedidos)
	}
	if pedidos[0] == 0 {
		t.Errorf("exit = 0 por un subcomando fallido: un 0 le dice al shell y al agente que todo " +
			"fue bien, y el servicio sigue como estaba")
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
		t.Errorf("normalizePath = %q sin CWD, want el path tal cual", got)
	}

	t.Chdir(t.TempDir())
	full := filepath.Join(mustGetwd(t), "api")
	if got := normalizePath("api"); got != full {
		t.Errorf("normalizePath = %q con CWD válido, want %q", got, full)
	}
	real := t.TempDir()
	link := filepath.Join(filepath.Dir(real), "enlace")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := normalizePath(link); got != real {
		t.Errorf("normalizePath(symlink) = %q, want el destino %q", got, real)
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
