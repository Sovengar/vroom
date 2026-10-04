package startsvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"vroom/internal/process"
	"vroom/internal/state"
)

// A Pid of 0 means there is no child, and for kill group 0 is vroom's own process group, so an unguarded Stop here would kill the TUI itself.
func TestStopAfterPersistFailureNoHaceNadaSinPid(t *testing.T) {
	paradas := 0
	req := Request{
		Store:   state.NewStoreAt(t.TempDir()),
		Manager: &contadorManager{paradas: &paradas},
	}

	stopAfterPersistFailure(req, process.StartResult{})
	stopAfterPersistFailure(req, process.StartResult{Pid: -1, Pgid: -1})

	if paradas != 0 {
		t.Errorf("se pararon %d veces sin un pid válido: sin proceso no hay nada que parar, y un "+
			"Pid de 0 es 'mi propio grupo' para kill(-0)", paradas)
	}
}

func TestStopAfterPersistFailureParaElHijoQueHay(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	req := Request{Store: store, Manager: process.NewManager()}

	pid, pgid := lanzarHijo(t, "sleep", "30")
	if !vivo(pid) {
		t.Fatalf("el hijo %d no llegó a arrancar", pid)
	}

	stopAfterPersistFailure(req, process.StartResult{Pid: pid, Pgid: pgid})

	esperarAQueMuera(t, pid)
}

// Regression: Start used to fail while leaving the child alive, so the next vroom start spawned a second process over the same port.
func TestPersistOrKillParaElHijoCuandoElMetaNoSePuedeGuardar(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	const proyecto = "/srv/api"

	dir, err := store.EnsureServiceDir(proyecto)
	if err != nil {
		t.Fatal(err)
	}
	if err := mkdirConContenido(filepath.Join(dir, "meta.json")); err != nil {
		t.Fatal(err)
	}

	pid, pgid := lanzarHijo(t, "sleep", "30")
	req := Request{Path: proyecto, Store: store, Manager: process.NewManager()}

	if err := persistOrKill(req, state.Meta{Name: "api"}, process.StartResult{Pid: pid, Pgid: pgid}); err == nil {
		t.Fatal("persistOrKill = nil con un meta que no se puede guardar")
	} else if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want que nombre el fichero: el mensaje es lo que le dice al usuario "+
			"que su directorio de estado está en mal estado", err)
	}

	esperarAQueMuera(t, pid)
}

func TestPersistOrKillGuardaCuandoPuede(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	const proyecto = "/srv/api"

	pid, pgid := lanzarHijo(t, "sleep", "30")
	req := Request{Path: proyecto, Store: store, Manager: process.NewManager()}

	if err := persistOrKill(req, state.Meta{Name: "api", Pid: pid, State: state.StateRunning},
		process.StartResult{Pid: pid, Pgid: pgid}); err != nil {
		t.Fatalf("persistOrKill: %v", err)
	}

	meta, err := store.LoadMeta(proyecto)
	if err != nil {
		t.Fatalf("el meta no se guardó: %v", err)
	}
	if meta.Pid != pid {
		t.Errorf("meta.Pid = %d, want %d", meta.Pid, pid)
	}
	if !vivo(pid) {
		t.Error("el hijo fue parado aunque el meta se guardó bien: un Start que nunca arranca " +
			"nada tampoco es un acierto")
	}
}

// Helpers here spawn real children, not doubles: what persistOrKill must prove is that a process stops existing, which a counter cannot show.
func lanzarHijo(t *testing.T, name string, args ...string) (pid, pgid int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid = cmd.Process.Pid
	pgid = pid // setsid makes pgid == pid
	// This is the reaper real Start runs, and it is what keeps these tests fast: an unreaped child stays ZOMBIE and waitLineageGone counts it alive until its 5s timeout.
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { parar(t, pid) })
	return pid, pgid
}

// parar is lanzarHijo's t.Cleanup and may run on an already-dead pid, so every error is discarded on purpose.
func parar(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.Signal(0)) // signal 0 probes liveness without signalling
}

func esperarAQueMuera(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !vivo(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("el proceso %d seguía vivo 10s después de pedirle que parara: el helper no lo paró", pid)
}

// MEDIDO: /proc/<pid> merely existing is not enough, because an unreaped child of this test process stays ZOMBIE there; the stat state field is internal/process's own criterion.
func vivo(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 || i+2 >= len(data) {
		return false
	}
	return string(data[i+2]) != "Z"
}

// mkdirConContenido creates a NON-empty directory, which is what makes a file rename over it fail with EISDIR.
func mkdirConContenido(path string) error {
	return os.MkdirAll(filepath.Join(path, "bloqueo"), 0o755)
}

// contadorManager counts Stop calls without touching anything, for the test that asserts nothing is stopped.
type contadorManager struct {
	process.Manager
	paradas *int
}

func (m *contadorManager) Stop(process.StopSpec) error {
	*m.paradas++
	return nil
}

// Third of the three post-spawn saves sharing persistOrKill, and the costliest to cover because the failure has to arrive on the final write alone. MEDIDO: the real failure is the atomic rename of meta.json.tmp onto a directory, with EISDIR; persistence is never touched, a state dir simply got replaced mid-start.
func TestElMetaFinalDeDynamicSeGuardaConElHijoYaVivoYLoParaSiNo(t *testing.T) {
	f := newFixture(t)
	// The helper waits before binding, opening the window where discovery is in flight and the final meta is not written yet.
	f.command(t, "port", "VROOM_HELPER_DELAY=2s")

	// The helper waits 2s and discovery allows 4s, so there is room to break the meta while discovery is still running.
	const discovery = 4 * time.Second

	// Wait until the child really started (the helper writes its report on startup), or the first save fails instead, which is another path.
	go func() {
		esperarAQueAparezca(t, f.outFile)
		// The attempt save lands just after the spawn, so this needs a little more margin.
		time.Sleep(300 * time.Millisecond)
		dir, err := f.store.EnsureServiceDir(f.dir)
		if err != nil {
			return
		}
		_ = os.Remove(filepath.Join(dir, "meta.json"))
		_ = mkdirConContenido(filepath.Join(dir, "meta.json"))
	}()

	res, err := f.start(t, discovery)
	if err == nil {
		t.Fatalf("Start = %+v sin error con el meta final inservible: el servicio se daría por "+
			"arrancado sin registro, y el siguiente start abriría un segundo proceso", res)
	}
	if res.Pid != 0 {
		t.Errorf("Result.Pid = %d con un arranque fallido, want 0: con `Result{}` el caller no tiene "+
			"forma de parar al hijo, y por eso `persistOrKill` lo para por su cuenta", res.Pid)
	}
	if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want que nombre el fichero: el mensaje es lo que le dice al usuario "+
			"que su directorio de estado está en mal estado", err)
	}
}

func esperarAQueAparezca(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
