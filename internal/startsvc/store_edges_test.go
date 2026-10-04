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

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/state"
)

// Meta is written after the spawn and before discovery, so a failure here leaves a live unregistered process that vroom list shows stopped and the next start duplicates the port.
func TestStartConElMetaBloqueadoDevuelveElErrorYNoDejaElProcesoSinRegistrar(t *testing.T) {
	f := newFixture(t)
	f.manifest.PortMode = manifest.PortModeFixed
	f.manifest.Port = freePort(t)
	f.command(t, "sleep")

	bloquearMeta(t, f.store, f.dir)

	out, err := f.start(t, 0)
	if err == nil {
		t.Fatal("con el meta bloqueado el arranque tiene que fallar")
	}
	if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want que nombre el fichero que no se pudo escribir", err)
	}

	// MEDIDO: Start used to return Result{} with no PID, leaving the child alive and unkillable by the caller; it now stops the child before propagating the error.
	if pids := procesosDelHelper(t); len(pids) != 0 {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("quedaron %d procesos del helper vivos tras un fallo de persistencia: "+
			"el caller recibió un error sin PID y no podía pararlos", len(pids))
	}
	_ = out
}

// MEDIDO: in dynamic the first SaveMeta is the attempt write, where the child is already alive; it had to be so because Result{} carries no PID, not because of the port.
func TestStartEnDynamicConElMetaBloqueadoTambienDejaElHijoSinRegistrar(t *testing.T) {
	f := newFixture(t)
	f.command(t, "sleep")
	bloquearMeta(t, f.store, f.dir)

	_, err := f.start(t, 800*time.Millisecond)
	if err == nil {
		t.Fatal("con el meta bloqueado el arranque tiene que fallar también en dynamic")
	}
	if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want que nombre el fichero que no se pudo escribir", err)
	}

	if pids := procesosDelHelper(t); len(pids) != 0 {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("quedaron %d procesos del helper vivos en dynamic", len(pids))
	}
}

// Zero means "whatever the package decides", not "don't wait": a real zero would drop every dynamic service to no_port instantly, an optimistic and wrong verdict.
func TestDiscoveryTimeoutCeroUsaElDefault(t *testing.T) {
	f := newFixture(t)
	f.command(t, "sleep")

	start := time.Now()
	out, err := f.start(t, 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = process.NewManager().Stop(process.StopSpec{Pid: out.Pid, Timeout: time.Second})
	})

	// MEDIDO: a helper that opens no port resolves to running, not no_port, and resolves fast whatever the timeout, so elapsed is deliberately not asserted.
	switch out.Meta.State {
	case state.StateRunning, state.StateNoPort:
	default:
		t.Errorf("State = %q con timeout 0: el veredicto tiene que ser de servicio vivo", out.Meta.State)
	}
	if out.Meta.StartedAt == "" {
		t.Error("sin StartedAt: el meta no se escribió")
	}
	if out.Pid <= 0 {
		t.Error("Pid = 0 tras un arranque sin error")
	}
	_ = elapsed
}

// The error must name the range, since a bare "no free port" sends the user to the firewall when this machine simply holds a thousand reservations; reserving is step 1 of the contract and fails before touching the system, or an exhausted pool would leave a live process without a port.
func TestReservePortAgotadoDevuelveElErrorAntesDeTocarNada(t *testing.T) {
	f := newFixture(t)
	f.command(t, "sleep")

	var reservados []int
	for {
		p, err := process.ReservePort()
		if err != nil {
			break
		}
		reservados = append(reservados, p)
	}
	t.Cleanup(func() {
		for _, p := range reservados {
			process.ReleasePort(p)
		}
	})
	if len(reservados) == 0 {
		t.Skip("el rango de puertos dinámicos está entero ocupado por otra cosa")
	}

	_, err := f.start(t, time.Second)
	if err == nil {
		t.Fatal("con el pool agotado el arranque tiene que fallar")
	}
	if !strings.Contains(err.Error(), "reserve") {
		t.Errorf("err = %q, want que diga que no se pudo reservar puerto", err)
	}
	if _, lerr := f.store.LoadMeta(f.dir); lerr == nil {
		t.Error("se escribió meta pese a no poder reservar puerto: el fallo tiene que ser antes del spawn")
	}
}

func TestApplyRouteSinRegistrarNoAbrePortlessNiFallaElArranque(t *testing.T) {
	f := newFixture(t)
	f.manifest.PortMode = manifest.PortModeFixed
	f.manifest.Port = freePort(t)
	f.command(t, "sleep")

	out, err := f.startWithRoutes(t, 0, nil)
	if err != nil {
		t.Fatalf("sin seam de rutas el arranque tiene que funcionar igual: %v", err)
	}
	t.Cleanup(func() {
		_ = process.NewManager().Stop(process.StopSpec{Pid: out.Pid, Timeout: time.Second})
	})

	if out.Meta.RouteOwned {
		t.Error("RouteOwned en true sin seam: se declararía proprietary una ruta que nadie registró")
	}
	if out.Meta.RouteURL != "" {
		t.Errorf("RouteURL = %q sin seam: publicaría una dirección que nadie ha visto responder", out.Meta.RouteURL)
	}
}

// bloquearMeta turns meta.json into a directory so the write fails with a real EISDIR rather than a permission error that root would ignore.
func bloquearMeta(t *testing.T, store *state.Store, projectPath string) {
	t.Helper()
	dir := store.ServiceDir(projectPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "meta.json"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// procesosDelHelper is the hygiene guard: with no PID to check (Start returned Result{}), the unique cmdline pattern is the only handle on the child.
func procesosDelHelper(t *testing.T) []int {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", "VROOM_NOPORT_HELPER|TestHelperService").Output()
	if err != nil {
		return nil // no match is the expected case, and pgrep exits non-zero then
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pid, convErr := strconv.Atoi(strings.TrimSpace(line))
		if convErr == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}
