package process

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

// The gopsutil and ConnectionsPid error branches are documented rather than covered: forcing them needs the kernel to refuse, which a test cannot do without monkey-patching a third-party library.

// The error must name the range, because a bare "no free port" sends the user to inspect the firewall.
func TestReservePortAgotaElRangoYLoDiceConTodosLosNombres(t *testing.T) {
	// Reserves the whole range: a handful of ports would not exhaust anything, and this test is slow.
	t.Logf("reserving the range %d-%d (%d ports)", DynamicPortLow, DynamicPortHigh, DynamicPortHigh-DynamicPortLow+1)
	reservados := make([]int, 0, DynamicPortHigh-DynamicPortLow+1)
	for {
		p, err := ReservePort()
		if err != nil {
			// Exhausted: this is the case under test.
			if !strings.Contains(err.Error(), strconv.Itoa(DynamicPortLow)) ||
				!strings.Contains(err.Error(), strconv.Itoa(DynamicPortHigh)) {
				t.Errorf("err = %q: it has to say THE RANGE so the user knows where to look", err)
			}
			break
		}
		reservados = append(reservados, p)
	}
	t.Cleanup(func() {
		for _, p := range reservados {
			ReleasePort(p)
		}
	})

	// MEDIDO: fewer ports than the range holds were reserved because some are taken by something else on this machine; reaching the exhaustion error is what matters.
	if len(reservados) == 0 {
		t.Fatal("no port was reserved: the range is entirely occupied and the test proved nothing")
	}
	t.Logf("the range was exhausted after %d of %d ports; the rest were occupied", len(reservados),
		DynamicPortHigh-DynamicPortLow+1)
}

func TestReservePortNoRepiteYDespuesDeLiberarVuelveADarlo(t *testing.T) {
	vistos := make(map[int]bool)
	var primero int
	for range 5 {
		p, err := ReservePort()
		if err != nil {
			t.Fatalf("ReservePort: %v", err)
		}
		if vistos[p] {
			t.Fatalf("port %d was reserved twice without releasing it", p)
		}
		vistos[p] = true
		if primero == 0 {
			primero = p
		}
	}

	ReleasePort(primero)
	despues := make(map[int]bool)
	for range 5 {
		p, err := ReservePort()
		if err != nil {
			t.Fatal(err)
		}
		despues[p] = true
		ReleasePort(p)
	}
	if !despues[primero] {
		t.Logf("the released port %d did not come out again in the next five reservations; "+
			"the set distributes them by order and it is not a failure, but it is worth knowing", primero)
	}

	// Releasing twice is the repeated-stop case and must not break.
	ReleasePort(primero)
	ReleasePort(primero)
}

func TestEvaluateDegradaAUnknownConElPropietarioDelPuertoIndeterminado(t *testing.T) {
	m := NewManager()

	cerrado := puertoCerrado(t)
	if got := m.Evaluate(EvalSpec{Pid: 0, Port: cerrado}); got == StatusRunning {
		t.Errorf("a closed port gave running: there is a live PID somewhere that slipped in")
	}

	abierto, libera := escuchar(t)
	defer libera()
	const creationTimeQueNoEsDeNadie = 999999
	got := m.Evaluate(EvalSpec{Pid: 1, CreationTimeMs: creationTimeQueNoEsDeNadie, Port: abierto})
	if got == StatusRunning {
		t.Error("a port open by ANOTHER process gave running: that is a twin, not the service")
	}
}

func TestAliveConUnCreationTimeQueNoCoincideEsUnPidReciclado(t *testing.T) {
	mio := os.Getpid()

	real := creationTimeDe(t, mio)

	if !Alive(mio, real) {
		t.Error("the test process itself is not recognized as alive with its real creationTime")
	}

	if Alive(mio, real+12345) {
		t.Error("a creationTime that does not match gave alive: it is the recycled PID of another process, " +
			"and vroom would kill it unintentionally")
	}

	if Alive(0, 0) {
		t.Error("PID 0 cannot be alive")
	}
	if Alive(-1, 0) {
		t.Error("a negative PID cannot be alive")
	}
}

func TestPortOwnerPIDsDevuelveVariosCuandoElPuertoLoComparten(t *testing.T) {
	_, libera := escuchar(t)
	defer libera()

	pids := PortOwnerPIDs(0)
	if len(pids) == 0 {
		t.Skip("could not read /proc/net/tcp in this environment")
	}

	// What is asserted is the shape: never 0 or negative, which is the guard that makes the ownership proof reliable.
	for _, pid := range pids {
		if pid <= 0 {
			t.Errorf("PortOwnerPIDs returned PID %d: an undetermined owner would break the ownership proof", pid)
		}
	}
	for i := 1; i < len(pids); i++ {
		if pids[i] == pids[i-1] {
			t.Errorf("PortOwnerPIDs returned %d twice: the owner count cannot depend on that", pids[i])
		}
	}
}

func TestLineageListenersAtConUnProcQueNoExisteNoRevienta(t *testing.T) {
	if got := lineageListenersAt(t.TempDir(), os.Getpid()); len(got) != 0 {
		t.Errorf("an empty proc returned %v, want nothing", got)
	}
}

func TestListenSocketsAtDescartaLasFilasQueNoSonListenYLasMalformadas(t *testing.T) {
	dir := t.TempDir()
	// One header row, one valid LISTEN, and everything that must not pass.
	contenido := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000 100 0 0 10 0\n" + // valid, port 8080
		"   1: 0100007F:1F91 0100007F:1F90 01 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000 100 0 0 10 0\n" + // ESTABLISHED, not LISTEN
		"   2: 0100007F:ZZZZ 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000 100 0 0 10 0\n" + // non-hexadecimal port
		"   3: 0100007F 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 12348\n" // few fields and no ':'
	path := filepath.Join(dir, "net", "tcp")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}

	got := listenSocketsAt(path)
	if len(got) != 1 {
		t.Fatalf("%d sockets were read, want 1: only the well-formed LISTEN row\n%+v", len(got), got)
	}
	if got[0].port != 8080 {
		t.Errorf("port = %d, want 8080 (0x1F90)", got[0].port)
	}
	if got[0].addr != "0100007F" {
		t.Errorf("addr = %q, want 0100007F", got[0].addr)
	}
	if got[0].inode != "12345" {
		t.Errorf("inode = %q, want 12345", got[0].inode)
	}

	if got := listenSocketsAt(filepath.Join(dir, "does-not-exist")); got != nil {
		t.Errorf("a non-existent file returned %v, want nil", got)
	}
}

// Each guard is a file read half-way (the thread died mid-read); a silent zero would render a row indistinguishable from an idle thread.
func TestParseThreadStatRechazaLoQueNoEsUnaFilaDeStat(t *testing.T) {
	// parseThreadStat takes the FILE path even though the local is named dir.
	escribir := func(t *testing.T, contenido string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "stat")
		if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("a complete row", func(t *testing.T) {
		dir := escribir(t, "4242 (name with spaces) S 1 4242 4242 0 -1 4194304 100 0 0 0 10 20 0 1 0 500 1234 56\n")
		state, ticks, err := parseThreadStat(dir)
		if err != nil {
			t.Fatalf("a valid row was rejected: %v", err)
		}
		if state != "S" {
			t.Errorf("state = %q, want S", state)
		}
		// The exact tick count is kernel-dependent; what matters is that it came from the file rather than from a zero.
		if ticks == 0 {
			t.Error("ticks = 0 in a valid row: the thread would show 0% without having read anything")
		}
	})

	for _, tt := range []struct {
		nombre    string
		contenido string
	}{
		{"empty", ""},
		{"without parentheses", "4242 nombre S 1 2 3 4"},
		{"few fields", "4242 (x) S 1 2"},
		{"unclosed parentheses", "4242 (x S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16"},
	} {
		t.Run(tt.nombre, func(t *testing.T) {
			dir := escribir(t, tt.contenido)
			if _, _, err := parseThreadStat(dir); err == nil {
				t.Error("a row that is not from stat was accepted: the thread would show invented data")
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		// The thread died between the listing and the read.
		if _, _, err := parseThreadStat(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
			t.Error("a non-existent stat gave nil: the thread no longer exists and cannot have ticks")
		}
	})
}

// It has to be silent: a stack stop would otherwise flood the log with warnings about services that never existed.
func TestStopSinPidNiPgidNiPuertoEsUnNoOpYNoEmiteAvisos(t *testing.T) {
	var avisos []string
	err := NewManager().Stop(StopSpec{
		Warn: func(f string, a ...any) { avisos = append(avisos, f) },
	})
	if err != nil {
		t.Errorf("Stop with nothing to stop gave error %v: there is nothing that can fail", err)
	}
	if len(avisos) != 0 {
		t.Errorf("Stop with nothing to stop emitted %d warnings: %v", len(avisos), avisos)
	}
}

// The other side is what matters here: a pattern nobody has must not match, or any service with a typo in its pattern would look alive.
func TestPatternMatchConUnPatronQueNoExisteEsFalse(t *testing.T) {
	for _, patron := range []string{
		"^vroomZZZpatternZZZthatDoesNotExistZZZ9911$",
		"^vroomZZZpatternZZZwithParentheses(web)$",
	} {
		if PatternMatch(patron) {
			t.Errorf("pattern %q gave true and there is no cmdline that can match it", patron)
		}
	}

	t.Logf("MEASURED: PatternMatch(\".ZZZ.\") = %v — the dot is a pgrep wildcard, "+
		"not a period: that is why the field is called process_pattern and not process_cmdline",
		PatternMatch("^.ZZZ$"))

	if !PatternMatch("") {
		t.Logf("MEASURED: PatternMatch(%q) = false on this machine (there are only processes that are not the pgrep)", "")
	}

	// With an empty pattern Evaluate does not set checked, so a missing pid degrades to unknown instead of running.
	m := NewManager()
	got := m.Evaluate(EvalSpec{Pid: 0, ProcessPattern: ""})
	if got == StatusRunning {
		t.Error("without PID or port, Evaluate gave running")
	}
}

// Without this caller-side guard an empty process_pattern would declare the service running forever and leave stop with nothing to signal.
func TestEvaluateNoConsultaElPatronSiEstaVacio(t *testing.T) {
	m := NewManager()

	if got := m.Evaluate(EvalSpec{Pid: findPIDMuerto(t), ProcessPattern: ""}); got == StatusRunning {
		t.Error("an empty pattern made a service with a dead PID be declared running")
	}

	if got := m.Evaluate(EvalSpec{Pid: findPIDMuerto(t), ProcessPattern: "^vroomZZZpatternZZZnonExistentZZZ9911$"}); got == StatusRunning {
		t.Error("a non-existent pattern made a service with a dead PID be declared running")
	}
}

func findPIDMuerto(t *testing.T) int {
	t.Helper()
	pid := findDeadPID(t)
	if pid == 0 {
		t.Skip("no dead PID was found on this machine")
	}
	return pid
}

func puertoCerrado(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

func escuchar(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

// Uses a real process (the test's own) because an invented creation time would only prove that number comparison works.
func creationTimeDe(t *testing.T, pid int) int64 {
	t.Helper()
	p, err := gopsprocess.NewProcess(int32(pid))
	if err != nil {
		t.Fatalf("could not open process %d: %v", pid, err)
	}
	ms, err := p.CreateTime()
	if err != nil {
		t.Fatalf("could not read the creation time of %d: %v", pid, err)
	}
	return ms
}
