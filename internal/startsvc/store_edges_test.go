package startsvc

import (
	"os"
	"path/filepath"
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
	f.manifest.URLGeneration = manifest.URLGenByPort
	f.manifest.Port = freePort(t)
	f.command(t, "sleep")

	bloquearMeta(t, f.store, f.dir)

	out, err := f.start(t, 0)
	if err == nil {
		t.Fatal("with meta blocked the start must fail")
	}
	if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want it to name the file that could not be written", err)
	}

	// MEDIDO: Start used to return Result{} with no PID, leaving the child alive and unkillable by the caller; it now stops the child before propagating the error. Matched by executable like every guard in this repo, and only against LIVE processes: the child is our own setsid'd descendant that exits(2) in milliseconds (by_port injects no PORT), and Manager never Waits a daemonized child — so it lingered as an unreaped zombie whose /proc/<pid>/exe still resolves, which a `pgrep -f` reported as "remained alive" only while its cmdline had not yet gone blank.
	if pids := leakedTestBinaries(); len(pids) != 0 {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("%d helper processes remained alive after a persistence failure: "+
			"the caller received an error without PID and could not stop them", len(pids))
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
		t.Fatal("with meta blocked the start must fail also in dynamic")
	}
	if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want it to name the file that could not be written", err)
	}

	if pids := leakedTestBinaries(); len(pids) != 0 {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("%d helper processes remained alive in dynamic", len(pids))
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
		t.Errorf("State = %q with timeout 0: the verdict must be of a live service", out.Meta.State)
	}
	if out.Meta.StartedAt == "" {
		t.Error("no StartedAt: the meta was not written")
	}
	if out.Pid <= 0 {
		t.Error("Pid = 0 after a start without error")
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
		t.Skip("the dynamic port range is entirely occupied by something else")
	}

	_, err := f.start(t, time.Second)
	if err == nil {
		t.Fatal("with the pool exhausted the start must fail")
	}
	if !strings.Contains(err.Error(), "reserve") {
		t.Errorf("err = %q, want it to say that a port could not be reserved", err)
	}
	if _, lerr := f.store.LoadMeta(f.dir); lerr == nil {
		t.Error("meta was written despite not being able to reserve a port: the failure must be before the spawn")
	}
}

func TestApplyRouteSinRegistrarNoAbrePortlessNiFallaElArranque(t *testing.T) {
	f := newFixture(t)
	f.manifest.URLGeneration = manifest.URLGenByPort
	f.manifest.Port = freePort(t)
	f.command(t, "sleep")

	out, err := f.startWithRoutes(t, 0, nil)
	if err != nil {
		t.Fatalf("without route seam the start must work the same: %v", err)
	}
	t.Cleanup(func() {
		_ = process.NewManager().Stop(process.StopSpec{Pid: out.Pid, Timeout: time.Second})
	})

	if out.Meta.RouteOwned {
		t.Error("RouteOwned true without seam: a route that nobody registered would be declared proprietary")
	}
	if out.Meta.RouteURL != "" {
		t.Errorf("RouteURL = %q without seam: it would publish an address that nobody has seen respond", out.Meta.RouteURL)
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
