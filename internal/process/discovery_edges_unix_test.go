package process

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

// The other tests all passed the reserved port, so they exited by R1 without waiting and never reached the discoverSettle countdown.
func TestDiscoverPortEsperaAQueElConjuntoDeListenersSeEstabilice(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	port := freePortForHelper(t)
	t.Setenv("VROOM_TEST_HELPER", "listener")
	t.Setenv("VROOM_TEST_PORT", strconv.Itoa(port))

	m := newTestManager(t)
	dir := t.TempDir()
	// reserved = 0 on purpose: without the R1 shortcut the only path is to settle.
	res := startSleep(t, m, StartSpec{
		Command:    "setsid " + testBinary(t) + " -test.run=^TestHelperListener$ & sleep 120",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "out.log"),
		StderrPath: filepath.Join(dir, "err.log"),
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	waitPortOpen(t, port, 5*time.Second)

	inicio := time.Now()
	d := DiscoverPort(res.Pid, 0, "/health", 5*time.Second)
	esperado := time.Since(inicio)

	if d.Port != port {
		t.Errorf("DiscoverPort = %+v, want el puerto %d del único listener", d, port)
	}
	if !d.Verified {
		t.Error("Verified en false con un solo listener estabilizado: la UI no puede afirmar que " +
			"la ruta funciona y el servicio aparece sin URL")
	}
	// It must have WAITED: accepting the first sample is exactly the bug the settle window prevents.
	if esperado < discoverSettle {
		t.Errorf("decidió en %s, want al menos %s: aceptó la primera muestra sin esperar a que el "+
			"conjunto dejara de crecer, que es exactamente el bug que la ventana previene",
			esperado, discoverSettle)
	}
	if d.Unresolved {
		t.Error("Unresolved con un único listener que no cambia: no hay nada que no se pueda decidir")
	}
}

// MEDIDO: Unresolved yields port_unresolved while an empty result yields no_port, which asserts something never checked - that the service exposes no TCP port at all.
func TestDiscoverPortNoDecideUnConjuntoQueNoSeEstabiliza(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	t.Setenv("VROOM_TEST_HELPER", "churn")

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "setsid " + testBinary(t) + " -test.run=^TestHelperListener$ & sleep 120",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "out.log"),
		StderrPath: filepath.Join(dir, "err.log"),
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	// Short deadline on purpose: reaching it with listeners present is enough.
	d := DiscoverPort(res.Pid, 0, "/health", 900*time.Millisecond)

	if !d.Unresolved {
		t.Errorf("DiscoverPort = %+v, want Unresolved: hay listeners y el plazo venció sin que el "+
			"conjunto se estabilizara, y hay que decirlo en vez de inventar un puerto", d)
	}
	if d.Port != 0 {
		t.Errorf("Port = %d con Unresolved, want 0: un puerto sin decidir es peor que ninguno, "+
			"porque la UI lo publica", d.Port)
	}
	if d.Verified {
		t.Error("Verified en true sin decidir: afirmaría que la ruta funciona sin haberlo comprobado")
	}
	if d.LineageDead {
		t.Error("LineageDead con el helper corriendo: sin deciding el puerto, el proceso sigue ahí")
	}
}

func TestDescendantsDeUnProcRootInexistenteNoDevuelveNada(t *testing.T) {
	if got := descendantsAt("/proc/definitely-not-here", os.Getpid()); len(got) != 0 {
		t.Errorf("descendantsAt con un /proc inexistente devolvió %v, want nada", got)
	}
	if got := lineageListenersAt("/proc/definitely-not-here", os.Getpid()); got != nil {
		t.Errorf("lineageListenersAt con un /proc inexistente devolvió %v, want nil", got)
	}
}

func TestDescendantsFromTerminaAnteUnCicloEnElArbol(t *testing.T) {
	// 1 -> 2 -> 3 -> 1: the root reappears as a child of its own descendant.
	snap := map[int]procInfo{
		1: {pid: 1, ppid: 3},
		2: {pid: 2, ppid: 1},
		3: {pid: 3, ppid: 3}, // padre de sí mismo: se salta al construir `children`
		4: {pid: 4, ppid: 2},
	}

	// Without the guard this never returns and hangs the test binary, which is the clearest proof that the hang is real.
	got := descendantsFrom(snap, 1)

	if len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Errorf("descendantsFrom = %v, want [2 4]: tiene que recorrer el ciclo una vez y "+
			"parar, sin incluir la raíz ni repetir", got)
	}
	for _, pid := range got {
		if pid == 1 {
			t.Error("la raíz aparece como descendiente suyo: el Stop por linaje se intentaría a sí mismo")
		}
	}
}

// The kernel keeps a dying process's ppid, pid 1 has ppid 0, and a zombie is not guaranteed to keep its parent, so children[p] can contain p.
func TestDescendantsFromIgnoraUnProcesoQueEsSuPropioPadre(t *testing.T) {
	snap := map[int]procInfo{
		1: {pid: 1, ppid: 0},
		2: {pid: 2, ppid: 1},
		3: {pid: 3, ppid: 3}, // su propio padre
	}

	got := descendantsFrom(snap, 1)
	for _, pid := range got {
		if pid == 3 {
			t.Errorf("descendantsFrom = %v incluye al 3, que es su propio padre: se colgaría solo", got)
		}
	}
	if len(got) != 1 || got[0] != 2 {
		t.Errorf("descendantsFrom = %v, want [2]", got)
	}
}

// A real stat can hold one good field and one garbage one, but never a tick count with a field skipped, because a silent 0 makes a saturated thread look idle.
func TestParseThreadStatConUnStimeNoNumericoEsUnStatInvalido(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stat")

	// 13 fields after the ')': utime in rest[11] is good, stime in rest[12] is garbage.
	escribirStat(t, path, "R 0 0 0 0 0 0 0 0 0 0 1234 no-es-un-número")
	if _, _, err := parseThreadStat(path); err == nil {
		t.Fatal("un stime no numérico tiene que ser un stat inválido, no ticks 1234")
	}

	// The good case too, so this tests parsing and not only rejection.
	escribirStat(t, path, "R 0 0 0 0 0 0 0 0 0 0 100 200")
	state, ticks, err := parseThreadStat(path)
	if err != nil {
		t.Fatalf("un stat válido dio error: %v", err)
	}
	if state != "R" {
		t.Errorf("state = %q, want R", state)
	}
	if ticks != 300 {
		t.Errorf("ticks = %d, want 300 (utime 100 + stime 200)", ticks)
	}
}

func escribirStat(t *testing.T, path, rest string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("42 (un nombre con (paréntesis) "+rest+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A service restarted outside vroom leaves a meta with a dead pid but an open port, so answering running would vouch for whoever opened it.

func TestEvaluateConUnPuertoDePropietarioAmbiguoDegradaAIndeterminado(t *testing.T) {
	port := freePortForHelper(t)

	v4, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v4.Close() }()

	v6, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port))
	if err != nil {
		t.Skipf("esta máquina no puede escuchar en ::1 con el mismo número: %v", err)
	}
	defer func() { _ = v6.Close() }()

	// MEDIDO: both entries belong to the SAME process and there are still two, so ambiguity needs no second process - one process on both address families is enough.
	if pids := PortOwnerPIDs(port); len(pids) != 2 {
		t.Skipf("esta máquina no da dos dueños para el mismo número de puerto (obtuve %v): "+
			"la condición del test no se cumple", pids)
	}

	got := NewManager().Evaluate(EvalSpec{
		Pid:  0, // el PID del meta está muerto: es el fallback externo
		Port: port,
	})
	if got != StatusUnknown {
		t.Errorf("Evaluate = %v, want unknown con un propietario ambiguo: afirmar running sería "+
			"reportar sano un servicio del que no hay prueba, que es justo el twin de otro worktree", got)
	}
}

// The listener is opened in the test process so the creation time compared is the test binary's, read from the same source production reads.
func TestEvaluateConElPuertoDeUnProcesoVivoLoDaPorRunningConPruebaDePropiedad(t *testing.T) {
	port := freePortForHelper(t)
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	self, err := gopsprocess.NewProcess(int32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := self.CreateTime()
	if err != nil {
		t.Fatal(err)
	}

	got := NewManager().Evaluate(EvalSpec{
		Pid:            0, // el meta no tiene PID: se decidió por puerto
		Port:           port,
		CreationTimeMs: ct,
	})
	if got != StatusRunning {
		t.Errorf("Evaluate = %v, want running: hay un único dueño vivo y su creation time es la del meta", got)
	}

	otro := NewManager().Evaluate(EvalSpec{
		Pid:            0,
		Port:           port,
		CreationTimeMs: ct + 1,
	})
	if otro != StatusStopped {
		t.Errorf("Evaluate = %v con un creation time ajeno, want stopped: el puerto lo tiene alguien "+
			"que no es el servicio del meta", otro)
	}
}

// The in-memory set and the system are two sources of truth, and a foreign holder means take the next port rather than fail: the range belongs to the kernel, not to vroom.
func TestReservePortSaltaLosPuertosQueElSistemaYaTieneOcupados(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: socket real")
	}

	// Occupies the FIRST port of the range, exactly where the loop starts, so the first iteration must fail and continue.
	bloqueo, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", DynamicPortLow))
	if err != nil {
		t.Skipf("el puerto %d del rango está ocupado por otra cosa: %v", DynamicPortLow, err)
	}
	defer func() { _ = bloqueo.Close() }()

	p, err := ReservePort()
	if err != nil {
		t.Fatalf("ReservePort: %v", err)
	}
	t.Cleanup(func() { ReleasePort(p) })

	if p == DynamicPortLow {
		t.Errorf("ReservePort devolvió %d, que está ocupado: el pool tiene que saltar al siguiente "+
			"del rango, no devolver un puerto que nadie puede abrir", DynamicPortLow)
	}
	if p < DynamicPortLow || p > DynamicPortHigh {
		t.Errorf("ReservePort = %d, fuera del rango [%d, %d]", p, DynamicPortLow, DynamicPortHigh)
	}
}
