//go:build unix

package process

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---- helpers de integración ----

// requireProc salta si el entorno no expone /proc (Linux sin procfs).
func requireProc(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/proc/self/task"); err != nil {
		t.Skip("no /proc en este entorno")
	}
}

// pidProcInfo lee /proc/<pid>/stat. Un zombie cuenta como muerto: ya no
// ejecuta ni puede tener el puerto, sólo espera a que su padre lo recoja.
func pidProcInfo(t *testing.T, pid int) (procInfo, bool) {
	t.Helper()
	info, err := procStatAt(filepath.Join(procRoot, strconv.Itoa(pid)))
	if err != nil || !info.running() {
		return procInfo{}, false
	}
	return info, true
}

// listenPortAndHold abre un listener real en un puerto efímero y devuelve
// el puerto. El proceso de test es el dueño: sirve para probar que Stop no
// toca procesos ajenos.
func listenPortAndHold(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return port, func() { _ = ln.Close() }
}

// freePortForHelper reserva un puerto efímero y lo cierra para que lo
// tompex el proceso helper. allow_reuse evita EADDRINUSE por TIME_WAIT.
func freePortForHelper(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// waitFor escanea hasta que cond sea cierta; falla tras timeout.
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout esperando: %s", desc)
}

// waitPortOpen espera a que un puerto acepte conexiones.
func waitPortOpen(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, "puerto abierto", func() bool { return PortOpen(port) })
}

// ---- helper process ----

// TestHelperListener NO es un test: es el binario helper que se lanza como
// descendiente re-sid para probar el Stop por linaje. Se invoca con
// VROOM_TEST_HELPER=listener y VROOM_TEST_PORT=<puerto>.
func TestHelperListener(t *testing.T) {
	if os.Getenv("VROOM_TEST_HELPER") != "listener" {
		t.Skip("proceso helper, no un test")
	}
	port := os.Getenv("VROOM_TEST_PORT")
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(3)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	time.Sleep(120 * time.Second)
}

// ---- Escenario: Stop mata a un descendiente que se ha re-sid ----

func TestStopKillsResidDescendantAndFreesPort(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	port := freePortForHelper(t)
	t.Setenv("VROOM_TEST_HELPER", "listener")
	t.Setenv("VROOM_TEST_PORT", strconv.Itoa(port))

	m := newTestManager(t)
	dir := t.TempDir()
	// El hijo arranca su propio backend con setsid: el backend queda en
	// OTRO process group, fuera del PGID registrado.
	res := startSleep(t, m, StartSpec{
		Command:    "setsid " + testBinary(t) + " -test.run=^TestHelperListener$ & sleep 300",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() {
		_ = syscall.Kill(-res.Pgid, syscall.SIGKILL)
		_ = m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second})
	})

	// El backend ya está escuchando y es un descendiente en otro grupo.
	waitPortOpen(t, port, 5*time.Second)
	backendPID := PortOwnerPID(port)
	if backendPID <= 0 {
		t.Fatal("no se pudo determinar el PID del backend re-sid")
	}
	backend, alive := pidProcInfo(t, int(backendPID))
	if !alive {
		t.Fatalf("el backend %d no está en /proc", backendPID)
	}
	if backend.pgid == res.Pgid {
		t.Fatalf("precondición del test: el backend debe estar en otro pgid (tiene %d)", backend.pgid)
	}
	if backend.ppid != res.Pid {
		t.Fatalf("precondición del test: el backend debe ser descendiente directo (ppid=%d, root=%d)", backend.ppid, res.Pid)
	}

	// El linaje se captura ANTES del stop: después se pierde, porque al morir
	// el root sus descendientes se reparentan a init.
	lineageBefore := append([]int{res.Pid}, descendantsAt(procRoot, res.Pid)...)
	if len(lineageBefore) < 2 {
		t.Fatalf("precondición: se esperaba al menos un descendiente, linaje=%v", lineageBefore)
	}
	if !containsPid(lineageBefore, int(backendPID)) {
		t.Fatalf("precondición: el backend %d debe estar en el linaje %v", backendPID, lineageBefore)
	}

	var warns []string
	if err := m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 3 * time.Second, Warn: func(f string, a ...any) {
		warns = append(warns, fmt.Sprintf(f, a...))
	}}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// El proceso principal termina.
	if _, alive := pidProcInfo(t, res.Pid); alive {
		t.Errorf("el proceso principal %d sigue vivo tras Stop", res.Pid)
	}
	// El backend re-sid también termina.
	if _, alive := pidProcInfo(t, int(backendPID)); alive {
		t.Errorf("el backend re-sid %d sigue vivo tras Stop: kill(-pgid) no alcanza al linaje", backendPID)
	}
	// Ningún PID del linaje sobrevive.
	for _, pid := range lineageBefore {
		if _, alive := pidProcInfo(t, pid); alive {
			t.Errorf("el pid %d del linaje sobrevive al stop", pid)
		}
	}
	// El puerto que el backend escuchaba queda libre.
	waitFor(t, 3*time.Second, "puerto liberado", func() bool { return !PortOpen(port) })
	if len(warns) != 0 {
		t.Errorf("stop limpio no debe emitir avisos: %v", warns)
	}
}

// ---- Escenario: Stop sigue funcionando con un solo process group ----

func TestStopSingleProcessGroupNoRegression(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	port := freePortForHelper(t)
	t.Setenv("VROOM_TEST_HELPER", "listener")
	t.Setenv("VROOM_TEST_PORT", strconv.Itoa(port))

	m := newTestManager(t)
	dir := t.TempDir()
	// Sin setsid: el helper comparte el PGID del servicio.
	res := startSleep(t, m, StartSpec{
		Command:    testBinary(t) + " -test.run=^TestHelperListener$ & sleep 300",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() {
		_ = syscall.Kill(-res.Pgid, syscall.SIGKILL)
		_ = m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second})
	})

	waitPortOpen(t, port, 5*time.Second)
	listenerPID := PortOwnerPID(port)
	if listenerPID <= 0 {
		t.Fatal("no se pudo determinar el PID del listener")
	}
	info, alive := pidProcInfo(t, int(listenerPID))
	if !alive || info.pgid != res.Pgid {
		t.Fatalf("precondición: el listener debe compartir el pgid %d (info=%+v alive=%v)", res.Pgid, info, alive)
	}

	if err := m.Stop(StopSpec{Pgid: res.Pgid, Timeout: 3 * time.Second}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := syscall.Kill(-res.Pgid, syscall.Signal(0)); err != syscall.ESRCH {
		t.Errorf("el process group %d sigue vivo tras Stop: %v", res.Pgid, err)
	}
	waitFor(t, 3*time.Second, "puerto liberado", func() bool { return !PortOpen(port) })
}

// ---- Escenario: Stop NO mata a un proceso ajeno que ocupa el puerto ----

// Servicio ya detenido (PGID 0) cuyo puerto preservado está ocupado por el
// twin de otro worktree. Stop no debe ejecutar fuser -k.
func TestStopDoesNotKillForeignPortHolder(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: proceso real en el puerto")
	}
	port, release := listenPortAndHold(t)
	defer release()

	// El servicio ya está parado: sólo queda el puerto en el meta.
	meta := StopSpec{Pgid: 0, Port: port, Timeout: time.Second}
	var warns []string
	meta.Warn = func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) }

	m := newTestManager(t)
	if err := m.Stop(meta); err != nil {
		t.Fatalf("Stop de servicio ya parado no debe fallar: %v", err)
	}

	// El proceso ajeno sigue vivo y el puerto sigue abierto.
	if !PortOpen(port) {
		t.Error("vroom cerró el puerto de un proceso ajeno")
	}
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 500*time.Millisecond); err != nil {
		t.Errorf("el puerto ajeno debe seguir aceptando conexiones: %v", err)
	}
	if len(warns) == 0 {
		t.Error("Stop debe avisar de que no pudo liberar el puerto")
	}
	if !strings.Contains(warns[0], strconv.Itoa(port)) {
		t.Errorf("el aviso debe nombrar el puerto: %q", warns[0])
	}
}

// ---- Escenario: propietario del puerto desconocido falla de forma cerrada ----

// El puerto está ocupado pero vroom no puede determinar quién lo ocupa:
// no se mata nada y se avisa.
func TestStopUnknownPortOwnerFailsClosed(t *testing.T) {
	port, release := listenPortAndHold(t)
	defer release()

	var warns []string
	warn := func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) }

	// Se inyecta un resolvedor que no sabe nada del puerto.
	killPortHolderWith(port, os.Getpid(), nil, func(int) []int32 { return nil }, warn)

	if !PortOpen(port) {
		t.Error("con propietario desconocido no se debe tocar el puerto")
	}
	if len(warns) != 1 {
		t.Fatalf("se esperaba exactamente 1 aviso, got %d: %v", len(warns), warns)
	}
	if !strings.Contains(warns[0], strconv.Itoa(port)) {
		t.Errorf("el aviso debe nombrar el puerto: %q", warns[0])
	}
}

// Dueños múltiples (dual-stack o dos procesos en el mismo puerto) siguen
// sin prueba de propiedad: tampoco se mata.
func TestStopAmbiguousPortOwnersFailsClosed(t *testing.T) {
	port, release := listenPortAndHold(t)
	defer release()

	var warns []string
	warn := func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) }

	owners := func(int) []int32 { return []int32{int32(os.Getpid()), 1} }
	killPortHolderWith(port, os.Getpid(), nil, owners, warn)

	if !PortOpen(port) {
		t.Error("con propietarios ambiguos no se debe tocar el puerto")
	}
	if len(warns) != 1 {
		t.Fatalf("se esperaba 1 aviso, got %d: %v", len(warns), warns)
	}
}

// Dueño probado como propio (dentro del linaje) → sí libera el puerto.
// El dueño es un listener hijo real: matar al proceso de test sería peor que
// el bug que se está probando.
func TestStopKillsOwnedPortHolder(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	port := freePortForHelper(t)
	t.Setenv("VROOM_TEST_HELPER", "listener")
	t.Setenv("VROOM_TEST_PORT", strconv.Itoa(port))

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    testBinary(t) + " -test.run=^TestHelperListener$ & sleep 300",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() {
		_ = syscall.Kill(-res.Pgid, syscall.SIGKILL)
		_ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second})
	})

	waitPortOpen(t, port, 5*time.Second)
	owner := PortOwnerPID(port)
	if owner <= 0 {
		t.Fatal("no se pudo determinar el PID del listener")
	}
	lineage := append([]int{res.Pid}, descendantsAt(procRoot, res.Pid)...)
	if !containsPid(lineage, int(owner)) {
		t.Fatalf("precondición: el owner %d debe estar en el linaje %v", owner, lineage)
	}

	var warns []string
	killPortHolderWith(port, res.Pid, lineage, func(int) []int32 { return []int32{owner} },
		func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) })

	if len(warns) != 0 {
		t.Errorf("dueño probado como propio no debe emitir avisos: %v", warns)
	}
	if PortOpen(port) {
		t.Errorf("el puerto %d debía liberarse: el dueño estaba en el linaje", port)
	}
}

// ---- Escenario: Stop es idempotente ----

func TestStopIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "sleep 300",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})

	if err := m.Stop(StopSpec{Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("primer Stop: %v", err)
	}
	// Segundo stop: el PGID ya no existe, no hay puerto que liberar.
	var warns []string
	if err := m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second, Warn: func(f string, a ...any) {
		warns = append(warns, fmt.Sprintf(f, a...))
	}}); err != nil {
		t.Fatalf("Stop debe ser idempotente, no fallar: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("stop idempotente no debe emitir avisos: %v", warns)
	}
}

// ---- lineage: fixtures sintéticos de /proc ----

func writeProcFixture(t *testing.T, tree map[int]procInfo) string {
	t.Helper()
	root := t.TempDir()
	for pid, info := range tree {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// pid (comm) state ppid pgrp session ...
		stat := strconv.Itoa(pid) + " (proc" + strconv.Itoa(pid) + ") S " +
			strconv.Itoa(info.ppid) + " " + strconv.Itoa(info.pgid) + " " +
			strconv.Itoa(info.pgid) + " 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// PID 1 siempre existe en un /proc real; algunos readers lo asumen.
	if _, ok := tree[1]; !ok {
		dir := filepath.Join(root, "1")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		stat := "1 (init) S 0 1 1 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Descendientes transitivos, incluido el que hizo setsid (otro pgid) y
// excluyendo procesos de otros linajes y el propio init.
func TestDescendantsAtFollowsResidChild(t *testing.T) {
	root := writeProcFixture(t, map[int]procInfo{
		100: {ppid: 1, pgid: 100},   // raíz del servicio
		101: {ppid: 100, pgid: 100}, // hijo en el mismo grupo
		102: {ppid: 101, pgid: 102}, // nieto que hizo setsid
		103: {ppid: 102, pgid: 102}, // bisnieto del re-sid
		200: {ppid: 1, pgid: 200},   // twin de otro worktree
		201: {ppid: 200, pgid: 200},
	})
	got := descendantsAt(root, 100)
	want := []int{101, 102, 103}
	if len(got) != len(want) {
		t.Fatalf("descendants = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("descendants = %v, want %v (orden ascendente)", got, want)
		}
	}
}

// El pid re-sid se detecta como tal leyendo /proc: su pgid != el de su
// padre, luego kill(-pgid) no lo alcanza.
func TestProcSnapshotDetectsResidPgid(t *testing.T) {
	root := writeProcFixture(t, map[int]procInfo{
		100: {ppid: 1, pgid: 100},
		102: {ppid: 100, pgid: 102},
	})
	snap, err := procSnapshotAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if snap[100].pgid != 100 || snap[102].pgid != 102 {
		t.Errorf("pgid mal leído: %+v", snap)
	}
	if snap[102].ppid != 100 {
		t.Errorf("ppid mal leído: %+v", snap[102])
	}
}

// comm con paréntesis (p.ej. "Web Content") no rompe el parseo de stat.
func TestProcStatAtCommWithParens(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "55")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := "55 (Web Content (tab)) S 7 55 55 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := procStatAt(filepath.Join(root, "55"))
	if err != nil {
		t.Fatal(err)
	}
	if info.ppid != 7 || info.pgid != 55 {
		t.Errorf("procInfo = %+v, want ppid=7 pgid=55", info)
	}
}

func TestProcSnapshotAtMissingRoot(t *testing.T) {
	if _, err := procSnapshotAt(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("una raíz /proc inexistente debe devolver error")
	}
}

// testBinary devuelve la ruta del binario de test actual.
func testBinary(t *testing.T) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return shellQuote(bin)
}

// shellQuote entrecomilla para sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
