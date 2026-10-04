package process

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	gopsnet "github.com/shirou/gopsutil/v3/net"
)

// Each of these consults something outside the process, so the lookup was injected rather than the check relaxed: the verdict stays, only the source of the datum moves.

// EPERM and ESRCH cannot both be provoked without a second user on the machine, so the whole errno map is pinned here as a table.
func TestGrupoExisteTraduceCadaErrno(t *testing.T) {
	casos := []struct {
		err  error
		want bool
		por  string
	}{
		{nil, true, "kill con éxito: el grupo existe y es consultable"},
		{syscall.ESRCH, false, "ESRCH: no hay ningún proceso en ese grupo"},
		{syscall.EPERM, true, "EPERM: el grupo existe, pero es de otro usuario. Decir false " +
			"haría que Stop creyera que ya no queda nadie por lo que hacer"},
		{syscall.EINVAL, false, "un errno que no es un veredicto sobre el grupo no se traduce a " +
			"'existe': la consulta falló, y no se afirma nada"},
		{errors.New("otro"), false, "un error que no es un errno tampoco"},
	}
	for _, c := range casos {
		if got := grupoExiste(c.err); got != c.want {
			t.Errorf("grupoExiste(%v) = %v, want %v: %s", c.err, got, c.want, c.por)
		}
	}

	if pgidAlive(0) {
		t.Error("pgidAlive(0) = true: preguntaría por el grupo de vroom, que siempre existe")
	}
	if pgidAlive(-1) {
		t.Error("pgidAlive(-1) = true: un pgid negativo no es un grupo")
	}
}

func TestVivoConDistingueLosTresVeredictos(t *testing.T) {
	const guardado = 1_700_000_000_000

	if !vivoCon(func(int) (int64, error) { return guardado, nil }, 42, guardado) {
		t.Error("vivoCon = false con el creation_time intacto: un servicio en marcha se declararía parado")
	}
	if vivoCon(func(int) (int64, error) { return guardado + 1, nil }, 42, guardado) {
		t.Error("vivoCon = true con un creation_time distinto: el PID fue reciclado y no es " +
			"nuestro servicio")
	}
	if vivoCon(func(int) (int64, error) { return 0, syscall.ESRCH }, 42, guardado) {
		t.Error("vivoCon = true para un PID que no existe")
	}
	if vivoCon(func(int) (int64, error) { return 0, errors.New("se fue mientras lo miraba") }, 42, guardado) {
		t.Error("vivoCon = true sin poder preguntar: afirmar que algo vive sin haberlo visto es el " +
			"peor error posible aquí")
	}
}

// nil and not an empty slice is what makes it explicit: nil is "could not ask", [] is "asked and nobody owns it".
func TestDueñosConTrataIgualNoVerYNoSaber(t *testing.T) {
	const puerto = 4321

	escuchan := func(status string, pid int32, p uint32) gopsnet.ConnectionStat {
		return gopsnet.ConnectionStat{Status: status, Pid: pid, Laddr: gopsnet.Addr{Port: p}}
	}
	conns := []gopsnet.ConnectionStat{
		escuchan("LISTEN", 100, puerto),
		escuchan("LISTEN", 200, puerto+1), // otro puerto
		escuchan("ESTABLISHED", 300, puerto),
		escuchan("LISTEN", 0, puerto), // sin dueño: no cuenta
	}

	got := dueñosCon(func() ([]gopsnet.ConnectionStat, error) { return conns, nil }, puerto)
	if len(got) != 1 || got[0] != 100 {
		t.Errorf("dueñosCon = %v, want [100]: sólo LISTEN, sólo ese puerto y sólo con pid", got)
	}

	if g := dueñosCon(func() ([]gopsnet.ConnectionStat, error) { return nil, errors.New("proc restricted") },
		puerto); g != nil {
		t.Errorf("dueñosCon con error = %v, want nil: sin prueba de propiedad no se mata nada", g)
	}
	if g := dueñosCon(func() ([]gopsnet.ConnectionStat, error) { return nil, nil }, puerto); g != nil {
		t.Errorf("dueñosCon sin conexiones = %v, want nil", g)
	}
}

func TestDescendantsFromTerminaConUnCiclo(t *testing.T) {
	snap := map[int]procInfo{
		1: {ppid: 2},
		2: {ppid: 1},
		3: {ppid: 1},
	}

	got := descendantsFrom(snap, 1)
	// From 1 the children are 2 and 3; 2's child is 1, already seen, so the walk stops and the result is [2, 3].
	if len(got) != 2 {
		t.Fatalf("descendantsFrom = %v, want [2 3]: el ciclo tiene que cortar en el nodo ya visto", got)
	}
	for _, pid := range got {
		if pid != 2 && pid != 3 {
			t.Errorf("descendantsFrom = %v, want sólo los descendientes reales de 1", got)
		}
	}
}

// An empty directory as the /proc root is exactly what a process in a container without proc mounted sees.
func TestCaptureLineDegradaSinProcs(t *testing.T) {
	vacio := t.TempDir() // existe y está vacío: ReadDir funciona, no hay procesos

	casos := []struct {
		nombre string
		spec   StopSpec
	}{
		{"sin pid ni pgid", StopSpec{}},
		{"sin snapshot", StopSpec{Pid: 12345, Pgid: 12345}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			root, lineage := captureLineageWith(c.spec, vacio)
			if root != 0 || lineage != nil {
				t.Errorf("captureLineageWith = (%d, %v), want (0, nil): sin prueba de linaje no se "+
					"señala a nadie", root, lineage)
			}
		})
	}

	// A root that is not a directory: ReadDir fails with ENOTDIR, the other path to the same error.
	fichero := filepath.Join(t.TempDir(), "no-soy-proc")
	if err := os.WriteFile(fichero, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if root, lineage := captureLineageWith(StopSpec{Pid: 1, Pgid: 1}, fichero); root != 0 || lineage != nil {
		t.Errorf("captureLineageWith con un root ilegible = (%d, %v), want (0, nil)", root, lineage)
	}
}

func TestReserveWithNoMarcaUnPuertoQueNoSePudoDevolver(t *testing.T) {
	// An already-closed listener: Close on it fails with "use of closed".
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cerrado := ln
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	// The set is snapshotted first because the exact port attempted depends on what the rest of the suite already reserved; what matters is that the set does not grow.
	antes := puertosReservados()

	_, err = reserveWith(func(string, string) (net.Listener, error) { return cerrado, nil })
	if err == nil {
		t.Fatal("reserveWith = nil con un listener que no se puede cerrar: el puerto puede seguir " +
			"ocupado por él")
	}
	if !strings.Contains(err.Error(), "could not release the reserved port") {
		t.Errorf("err = %q, want que diga que no se pudo devolver el puerto", err)
	}
	despues := puertosReservados()
	for p := range despues {
		if !antes[p] {
			t.Errorf("el puerto %d quedó marcado como reservado tras un cierre fallido: el rango "+
				"se encogería un hueco por cada fallo, sin explicación", p)
		}
	}
}

// MEDIDO: a fake pgrep on the PATH controls only the shape of its output, which is what this function promises to understand; the "another process exists" result comes from a real pid.
func TestPatternMatchToleraUnaSalidaDePgrepQueNoEsSoloPids(t *testing.T) {
	otro := procesosDePrueba(t, 1) // un `sleep` real con un nombre reconocible
	pid := strconv.Itoa(otro[0])

	casos := []struct {
		nombre string
		salida string
		want   bool
	}{
		{"una línea en blanco y un pid", "\n" + pid + "\n\n", true},
		{"una línea que no es un número", "pgrep: algo raro\n" + pid + "\n", true},
		{"sólo basura", "esto no es un pid\n", false},
		{"nada, pero con salida 0", "", false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			conPgrepFalso(t, c.salida)
			if got := PatternMatch(pruebaPatron); got != c.want {
				t.Errorf("PatternMatch con la salida %q = %v, want %v", c.salida, got, c.want)
			}
		})
	}
}

// MEDIDO: the last-resort killer is fuser -k, so a fake fuser that kills nothing stands in for a container without fuser; the port outlives the wait and a warning is owed.
func TestKillPortHolderAvisaCuandoElPuertoNoSeLibera(t *testing.T) {
	// An open port with a known owner, which is what lets killPortHolderWith reach the last resort instead of refusing outright.
	ln := escucharEn(t)
	puerto := puertoDe(t, ln)
	pid := os.Getpid()

	var avisos []string
	conFuserFalso(t) // no mata nada
	killPortHolderWith(puerto, pid, []int{pid}, func(int) []int32 { return []int32{int32(pid)} },
		func(f string, a ...any) { avisos = append(avisos, fmt.Sprintf(f, a...)) })

	if len(avisos) != 1 {
		t.Fatalf("avisos = %v, want exactamente uno: sin dueño atribuible hay dos "+
			"renuncias, y con dueño propio pero puerto ocupado hay una", avisos)
	}
	if !strings.Contains(avisos[0], "sigue ocupado") {
		t.Errorf("aviso = %q, want que diga que el puerto sigue ocupado tras el kill", avisos[0])
	}
}

// It does not have to match anything real: what is controlled is what the fake prints.
const pruebaPatron = "vroom-test-patterno-que-no-existe"

// It exits 0 on purpose even with no matches, which is the rare case that lets an empty output reach the parser.
func conPgrepFalso(t *testing.T, salida string) {
	t.Helper()
	dir := t.TempDir()
	// The output goes in a file the fake cats: a heredoc looked nicer but cat reads stdin, and exec.Command gives the child /dev/null, not the heredoc.
	if err := os.WriteFile(filepath.Join(dir, "salida.txt"), []byte(salida), 0o644); err != nil {
		t.Fatal(err)
	}
	// PATH is prepended rather than replaced: with a single-directory PATH the script itself cannot find cat, and that is exactly how a real pgrep fails.
	if err := os.WriteFile(filepath.Join(dir, "pgrep"),
		[]byte("#!/bin/sh\ncat "+filepath.Join(dir, "salida.txt")+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func conFuserFalso(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fuser"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func procesosDePrueba(t *testing.T, n int) []int {
	t.Helper()
	var pids []int
	for range n {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, cmd.Process.Pid)
		// No Wait: the test cleanup reaps them.
		t.Cleanup(func() { _ = cmd.Process.Kill() })
	}
	return pids
}

func puertosReservados() map[int]bool {
	reserveMu.Lock()
	defer reserveMu.Unlock()
	out := make(map[int]bool, len(reservedPorts))
	for p, v := range reservedPorts {
		out[p] = v
	}
	return out
}

func escucharEn(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func puertoDe(t *testing.T, ln net.Listener) int {
	t.Helper()
	return ln.Addr().(*net.TCPAddr).Port
}

// MEDIDO: the surviving-SIGKILL case cannot be built here (setpriv --reuid gives EPERM and unshare -U leaves children with the same uid), so what is asserted is WHEN the ladder warns.
func TestElAvisoDeEscalonDependeDeQueHuboRootYDeQueNoMurio(t *testing.T) {
	casos := []struct {
		nombre    string
		root      int
		noMurio   bool
		wantAviso bool
		por       string
	}{
		{"hubo root y sobrevivió alguien", 100, true, true, "se agotó la escalera: hay que decirlo"},
		{"hubo root y todo murió", 100, false, false, "el servicio se paró bien: un aviso aquí " +
			"sería ruido"},
		{"no hubo root", 0, true, false, "sin root no había nada que parar y `terminate` ni se " +
			"llama; avisar de un linaje vacío sería mentir"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			var avisos []string
			StopSpec{Warn: func(f string, a ...any) {
				avisos = append(avisos, fmt.Sprintf(f, a...))
			}}.avisaSiSeAgotó(c.root, []int{c.root, 200}, c.noMurio)

			if c.wantAviso {
				if len(avisos) != 1 {
					t.Fatalf("avisos = %v, want exactamente uno", avisos)
				}
				if !strings.Contains(avisos[0], "SIGKILL") {
					t.Errorf("aviso = %q, want que nombre el SIGKILL: si se agotó la escalera, "+
						"el usuario tiene que saber que no fue culpa de la cortesía", avisos[0])
				}
				if !strings.Contains(avisos[0], strconv.Itoa(c.root)) {
					t.Errorf("aviso = %q, want el pid del root: sin él el usuario no puede buscar "+
						"el proceso que se resiste", avisos[0])
				}
				return
			}
			if len(avisos) != 0 {
				t.Errorf("avisos = %v, want ninguno: %s", avisos, c.por)
			}
		})
	}
}

// MEDIDO: a kernel thread fits exactly - /proc reports state R so lineageRunning counts it alive, while a normal user gets EPERM on SIGKILL.
func TestTerminateDevuelveFalsoConUnProcesoQueNoSePuedeMatar(t *testing.T) {
	kthread := hiloDelKernel()
	if kthread == 0 {
		t.Skip("esta máquina no tiene un hilo del kernel legible en /proc")
	}
	// If signalling ever succeeds the scenario no longer applies and terminate would go back to returning true.
	if err := syscall.Kill(kthread, syscall.SIGKILL); err == nil {
		t.Skip("este usuario puede matar hilos del kernel: el escenario de prueba ya no aplica")
	}

	root := lanzar(t, "sleep", "30")

	if terminate(root, 0, []int{root, kthread}, 200*time.Millisecond) {
		t.Error("terminate = true con un proceso imparable en el linaje: la escalera se agotó y " +
			"`Stop` no avisaría de nada")
	}
}

// Picks a kernel thread: a live state with ppid 0 or 2, or 0 if none is readable.
func hiloDelKernel() int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid < 2 {
			continue
		}
		info, err := procStatAt(filepath.Join(procRoot, e.Name()))
		if err != nil || info.ppid > 2 || !info.running() {
			continue
		}
		return pid
	}
	return 0
}

func lanzar(t *testing.T, name string, args ...string) int {
	t.Helper()
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}
