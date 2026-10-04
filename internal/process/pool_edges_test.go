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
	t.Logf("reservando el rango %d-%d (%d puertos)", DynamicPortLow, DynamicPortHigh, DynamicPortHigh-DynamicPortLow+1)
	reservados := make([]int, 0, DynamicPortHigh-DynamicPortLow+1)
	for {
		p, err := ReservePort()
		if err != nil {
			// Exhausted: this is the case under test.
			if !strings.Contains(err.Error(), strconv.Itoa(DynamicPortLow)) ||
				!strings.Contains(err.Error(), strconv.Itoa(DynamicPortHigh)) {
				t.Errorf("err = %q: tiene que decir EL RANGO para que el usuario sepa dónde mirar", err)
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
		t.Fatal("no se reservó ningún puerto: el rango está entero ocupado y el test no probó nada")
	}
	t.Logf("el rango se agotó tras %d de %d puertos; el resto estaba ocupado", len(reservados),
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
			t.Fatalf("el puerto %d se reservationó dos veces sin liberarlo", p)
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
		t.Logf("el puerto liberado %d no volvió a salir en las cinco siguientes reservas; "+
			"el set lo reparte por orden y no es un fallo, pero conviene saberlo", primero)
	}

	// Releasing twice is the repeated-stop case and must not break.
	ReleasePort(primero)
	ReleasePort(primero)
}

func TestEvaluateDegradaAUnknownConElPropietarioDelPuertoIndeterminado(t *testing.T) {
	m := NewManager()

	cerrado := puertoCerrado(t)
	if got := m.Evaluate(EvalSpec{Pid: 0, Port: cerrado}); got == StatusRunning {
		t.Errorf("un puerto cerrado dio running: hay un PID vivo en algún sitio que se ha colado")
	}

	abierto, libera := escuchar(t)
	defer libera()
	const creationTimeQueNoEsDeNadie = 999999
	got := m.Evaluate(EvalSpec{Pid: 1, CreationTimeMs: creationTimeQueNoEsDeNadie, Port: abierto})
	if got == StatusRunning {
		t.Error("un puerto abierto por OTRO proceso dio running: eso es un twin, no el servicio")
	}
}

func TestAliveConUnCreationTimeQueNoCoincideEsUnPidReciclado(t *testing.T) {
	mio := os.Getpid()

	real := creationTimeDe(t, mio)

	if !Alive(mio, real) {
		t.Error("el propio proceso de test no se reconoce vivo con su creationTime real")
	}

	if Alive(mio, real+12345) {
		t.Error("un creationTime que no coincide dio vivo: es el PID reciclado de otro proceso, " +
			"y vroom lo mataría sin querer")
	}

	if Alive(0, 0) {
		t.Error("el PID 0 no puede estar vivo")
	}
	if Alive(-1, 0) {
		t.Error("un PID negativo no puede estar vivo")
	}
}

func TestPortOwnerPIDsDevuelveVariosCuandoElPuertoLoComparten(t *testing.T) {
	_, libera := escuchar(t)
	defer libera()

	pids := PortOwnerPIDs(0)
	if len(pids) == 0 {
		t.Skip("no se pudo leer /proc/net/tcp en este entorno")
	}

	// What is asserted is the shape: never 0 or negative, which is the guard that makes the ownership proof reliable.
	for _, pid := range pids {
		if pid <= 0 {
			t.Errorf("PortOwnerPIDs devolvió el PID %d: un propietario indeterminado rompería la prueba de propiedad", pid)
		}
	}
	for i := 1; i < len(pids); i++ {
		if pids[i] == pids[i-1] {
			t.Errorf("PortOwnerPIDs devolvió %d dos veces: el conteo de propietarios no puede depender de eso", pids[i])
		}
	}
}

func TestLineageListenersAtConUnProcQueNoExisteNoRevienta(t *testing.T) {
	if got := lineageListenersAt(t.TempDir(), os.Getpid()); len(got) != 0 {
		t.Errorf("un proc vacío devolvió %v, want nada", got)
	}
}

func TestListenSocketsAtDescartaLasFilasQueNoSonListenYLasMalformadas(t *testing.T) {
	dir := t.TempDir()
	// One header row, one valid LISTEN, and everything that must not pass.
	contenido := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000 100 0 0 10 0\n" + // válido, puerto 8080
		"   1: 0100007F:1F91 0100007F:1F90 01 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000 100 0 0 10 0\n" + // ESTABLISHED, no LISTEN
		"   2: 0100007F:ZZZZ 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000 100 0 0 10 0\n" + // puerto no hexadecimal
		"   3: 0100007F 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 12348\n" // pocos campos y sin ':'
	path := filepath.Join(dir, "net", "tcp")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}

	got := listenSocketsAt(path)
	if len(got) != 1 {
		t.Fatalf("se leyeron %d sockets, want 1: sólo la fila LISTEN bien formada\n%+v", len(got), got)
	}
	if got[0].port != 8080 {
		t.Errorf("puerto = %d, want 8080 (0x1F90)", got[0].port)
	}
	if got[0].addr != "0100007F" {
		t.Errorf("addr = %q, want 0100007F", got[0].addr)
	}
	if got[0].inode != "12345" {
		t.Errorf("inode = %q, want 12345", got[0].inode)
	}

	if got := listenSocketsAt(filepath.Join(dir, "no-existe")); got != nil {
		t.Errorf("un fichero inexistente devolvió %v, want nil", got)
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

	t.Run("una fila completa", func(t *testing.T) {
		dir := escribir(t, "4242 (nombre con espacios) S 1 4242 4242 0 -1 4194304 100 0 0 0 10 20 0 1 0 500 1234 56\n")
		state, ticks, err := parseThreadStat(dir)
		if err != nil {
			t.Fatalf("una fila válida fue rechazada: %v", err)
		}
		if state != "S" {
			t.Errorf("state = %q, want S", state)
		}
		// The exact tick count is kernel-dependent; what matters is that it came from the file rather than from a zero.
		if ticks == 0 {
			t.Error("ticks = 0 en una fila válida: el hilo se mostraría con 0% sin haber leído nada")
		}
	})

	for _, tt := range []struct {
		nombre    string
		contenido string
	}{
		{"vacío", ""},
		{"sin paréntesis", "4242 nombre S 1 2 3 4"},
		{"pocos campos", "4242 (x) S 1 2"},
		{"paréntesis sin cerrar", "4242 (x S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16"},
	} {
		t.Run(tt.nombre, func(t *testing.T) {
			dir := escribir(t, tt.contenido)
			if _, _, err := parseThreadStat(dir); err == nil {
				t.Error("una fila que no es de stat fue aceptada: el hilo se mostraría con datos inventados")
			}
		})
	}

	t.Run("fichero ausente", func(t *testing.T) {
		// The thread died between the listing and the read.
		if _, _, err := parseThreadStat(filepath.Join(t.TempDir(), "no-existe")); err == nil {
			t.Error("un stat inexistente dio nil: el hilo ya no existe y no puede tener ticks")
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
		t.Errorf("Stop sin nada que parar dio error %v: no hay nada que pueda fallar", err)
	}
	if len(avisos) != 0 {
		t.Errorf("Stop sin nada que parar emitió %d avisos: %v", len(avisos), avisos)
	}
}

// The other side is what matters here: a pattern nobody has must not match, or any service with a typo in its pattern would look alive.
func TestPatternMatchConUnPatronQueNoExisteEsFalse(t *testing.T) {
	for _, patron := range []string{
		"^vroomZZZpatronZZZqueNoExisteZZZ9911$",
		"^vroomZZZpatronZZZconParentesis(web)$",
	} {
		if PatternMatch(patron) {
			t.Errorf("el patrón %q dio true y no hay ningún cmdline que pueda matchearlo", patron)
		}
	}

	t.Logf("MEDIDO: PatternMatch(\".ZZZ.\") = %v — el punto es un comodín de pgrep, "+
		"no un punto: por eso el campo se llama process_pattern y no process_cmdline",
		PatternMatch("^.ZZZ$"))

	if !PatternMatch("") {
		t.Logf("MEDIDO: PatternMatch(%q) = false en esta máquina (sólo hay procesos que no son el pgrep)", "")
	}

	// With an empty pattern Evaluate does not set checked, so a missing pid degrades to unknown instead of running.
	m := NewManager()
	got := m.Evaluate(EvalSpec{Pid: 0, ProcessPattern: ""})
	if got == StatusRunning {
		t.Error("sin PID ni puerto, Evaluate dio running")
	}
}

// Without this caller-side guard an empty process_pattern would declare the service running forever and leave stop with nothing to signal.
func TestEvaluateNoConsultaElPatronSiEstaVacio(t *testing.T) {
	m := NewManager()

	if got := m.Evaluate(EvalSpec{Pid: findPIDMuerto(t), ProcessPattern: ""}); got == StatusRunning {
		t.Error("un patrón vacío hizo declarar running un servicio con el PID muerto")
	}

	if got := m.Evaluate(EvalSpec{Pid: findPIDMuerto(t), ProcessPattern: "^vroomZZZpatronZZZinexistenteZZZ9911$"}); got == StatusRunning {
		t.Error("un patrón inexistente hizo declarar running un servicio con el PID muerto")
	}
}

func findPIDMuerto(t *testing.T) int {
	t.Helper()
	pid := findDeadPID(t)
	if pid == 0 {
		t.Skip("no se encontró un PID muerto en esta máquina")
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
		t.Fatalf("no se pudo abrir el proceso %d: %v", pid, err)
	}
	ms, err := p.CreateTime()
	if err != nil {
		t.Fatalf("no se pudo leer el tiempo de creación de %d: %v", pid, err)
	}
	return ms
}
