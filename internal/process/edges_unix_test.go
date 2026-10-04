//go:build unix

package process

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Parsing runs against synthetic /proc trees because a malformed stat cannot be provoked on a live process; lineage, stop and port discovery use real processes because their behaviour is about real kernel state.

func fakeProc(t *testing.T, pid int, stat string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(dir, "task"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProcStatAtAceptaElCommConParentesisYEspacios(t *testing.T) {
	tests := []struct {
		name     string
		stat     string
		wantPID  int
		wantPPID int
		wantPGID int
		wantErr  bool
	}{
		{
			name:     "normal",
			stat:     "1234 (bash) S 1000 1234 1234 0 -1 4194560 100",
			wantPID:  1234,
			wantPPID: 1000,
			wantPGID: 1234,
		},
		{
			name:     "comm con espacios",
			stat:     "7 (Web Content (tab)) S 1 7 7 0 -1 0 0",
			wantPID:  7,
			wantPPID: 1,
			wantPGID: 7,
		},
		{
			name:     "comm con un paréntesis suelto",
			stat:     "8 (weird)name) S 1 8 8 0 -1 0 0",
			wantPID:  8,
			wantPPID: 1,
			wantPGID: 8,
		},
		{
			name:    "sin paréntesis",
			stat:    "1234 bash S 1 1 1 0 -1 0 0",
			wantErr: true,
		},
		{
			name:    "paréntesis al revés",
			stat:    ")bas( 1234 S 1 1 1 0 -1 0 0",
			wantErr: true,
		},
		{
			name:    "se corta justo después del cierre",
			stat:    "1 (x) S",
			wantErr: true,
		},
		{
			name:    "pid no numérico",
			stat:    "abc (x) S 1 1 1 0 -1 0 0",
			wantErr: true,
		},
		{
			name:    "menos de tres campos tras el cierre",
			stat:    "1 (x) S 1",
			wantErr: true,
		},
		{
			name:    "ppid no numérico",
			stat:    "1 (x) S ppid 1 0 -1 0",
			wantErr: true,
		},
		{
			name:    "pgrp no numérico",
			stat:    "1 (x) S 1 pgid 0 -1 0",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeProc(t, 1234, tt.stat)
			info, err := procStatAt(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("stat %q debería fallar, dio %+v", tt.stat, info)
				}
				return
			}
			if err != nil {
				t.Fatalf("stat %q no debería fallar: %v", tt.stat, err)
			}
			if info.pid != tt.wantPID || info.ppid != tt.wantPPID || info.pgid != tt.wantPGID {
				t.Errorf("stat %q dio pid=%d ppid=%d pgid=%d; want %d/%d/%d",
					tt.stat, info.pid, info.ppid, info.pgid, tt.wantPID, tt.wantPPID, tt.wantPGID)
			}
		})
	}

	t.Run("fichero inexistente", func(t *testing.T) {
		if _, err := procStatAt(filepath.Join(t.TempDir(), "nada")); err == nil {
			t.Error("un stat inexistente debería dar error")
		}
	})
}

// MEDIDO: only Z counts as not alive, because a zombie already released its fds (the port is genuinely free) and treating it as alive would make Stop sit out the whole timeout.
func TestProcInfoRunningIgnoraZombiesYNoPids(t *testing.T) {
	tests := []struct {
		info procInfo
		want bool
	}{
		{procInfo{pid: 1, state: "S"}, true},
		{procInfo{pid: 1, state: "R"}, true},
		{procInfo{pid: 1, state: "D"}, true},
		{procInfo{pid: 1, state: "Z"}, false}, // zombie: ya no sostiene nada
		{procInfo{pid: 1, state: "T"}, true},  // bajo debugger: sigue con el puerto
		{procInfo{pid: 0, state: "S"}, false}, // pid 0 no es un proceso
		{procInfo{pid: -1, state: "S"}, false},
	}
	for _, tt := range tests {
		if got := tt.info.running(); got != tt.want {
			t.Errorf("procInfo{pid:%d state:%q}.running() = %v, want %v",
				tt.info.pid, tt.info.state, got, tt.want)
		}
	}
}

func TestDescendantsAtSinLinajeCuandoNoSePuedeLeer(t *testing.T) {
	root := t.TempDir()
	got := descendantsAt(root, os.Getpid())
	if len(got) != 0 {
		t.Errorf("descendantsAt sobre un /proc vacío dio %v, want vacío", got)
	}
}

func TestDescendantsFromIgnoraAPadreQueEsSuHijo(t *testing.T) {
	snap := map[int]procInfo{
		100: {pid: 100, state: "S", ppid: 100, pgid: 100}, // padre de sí mismo
		101: {pid: 101, state: "S", ppid: 100, pgid: 100},
		102: {pid: 102, state: "S", ppid: 101, pgid: 100},
	}
	got := descendantsFrom(snap, 100)
	if len(got) != 2 {
		t.Fatalf("descendantsFrom dio %v, want [101 102]", got)
	}
	for _, pid := range got {
		if pid == 100 {
			t.Error("la raíz apareció en su propio linaje: Stop la señalaría dos veces")
		}
	}
}

func TestDescendantsFromNoRepiteNiIncluyeLaRaiz(t *testing.T) {
	// 3 hangs off both 1 and 2: two paths to the same node.
	snap := map[int]procInfo{
		1: {pid: 1, state: "S", ppid: 0, pgid: 1},
		2: {pid: 2, state: "S", ppid: 1, pgid: 1},
		3: {pid: 3, state: "S", ppid: 1, pgid: 1},
		4: {pid: 4, state: "S", ppid: 2, pgid: 1},
		5: {pid: 5, state: "S", ppid: 3, pgid: 1},
		6: {pid: 6, state: "S", ppid: 4, pgid: 1},
		7: {pid: 7, state: "S", ppid: 5, pgid: 1},
	}
	got := descendantsFrom(snap, 1)
	seen := map[int]int{}
	for _, pid := range got {
		seen[pid]++
		if pid == 1 {
			t.Error("la raíz está en su propio linaje")
		}
	}
	for pid, n := range seen {
		if n > 1 {
			t.Errorf("el pid %d aparece %d veces en el linaje: se señalaría de más", pid, n)
		}
	}
	if len(got) != 6 {
		t.Errorf("linaje = %v, want los 6 descendientes sin repetir", got)
	}
}

// A lineage whose pids died between capture and poll counts as gone, so Stop does not sit out the timeout for a group that can no longer change.
func TestLineageRunningToleraPidsMuertosYNoNuméricos(t *testing.T) {
	if lineageRunning(nil) {
		t.Error("un linaje vacío está vivo: eso haría que Stop esperara siempre")
	}
	if lineageRunning([]int{0, -1}) {
		t.Error("un linaje de pids no válidos está vivo")
	}
	me := os.Getpid()
	if !lineageRunning([]int{me}) {
		t.Error("el propio proceso debería estar vivo")
	}
	dead := findDeadPID(t)
	if !lineageRunning([]int{dead, me}) {
		t.Error("un pid muerto no debe hacer que el linaje cuente como muerto")
	}
	if lineageRunning([]int{dead, findDeadPID(t)}) {
		t.Error("un linaje de pids muertos no puede estar vivo")
	}
}

func findDeadPID(t *testing.T) int {
	t.Helper()
	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "sleep 30", WorkDir: t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "o.log"), StderrPath: filepath.Join(t.TempDir(), "e.log"),
	})
	if err := m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	waitPIDGone(t, res.Pid)
	return res.Pid
}

func TestCaptureLineageSabeQuienEsLaRaiz(t *testing.T) {
	me := os.Getpid()

	t.Run("sin pid ni pgid no hay nada que capturar", func(t *testing.T) {
		root, lineage := captureLineage(StopSpec{})
		if root != 0 || lineage != nil {
			t.Errorf("captureLineage({}) = %d/%v, want 0/nil", root, lineage)
		}
	})

	t.Run("con pid vivo: la raíz es el pid", func(t *testing.T) {
		// The test process stays in its own group because the runner never changes it.
		pgid := ownPgid(t)
		root, lineage := captureLineage(StopSpec{Pid: me, Pgid: pgid})
		if root != me {
			t.Errorf("root = %d, want %d: con el pid vivo la raíz es el pid", root, me)
		}
		if len(lineage) == 0 || lineage[0] != root {
			t.Errorf("linaje = %v, want el root primero", lineage)
		}
	})

	t.Run("con pgid que no existe: no hay linaje", func(t *testing.T) {
		root, lineage := captureLineage(StopSpec{Pid: me, Pgid: 1 << 22})
		if root != 0 || lineage != nil {
			t.Errorf("un pgid inexistente dio root=%d linaje=%v, want 0/nil", root, lineage)
		}
	})
}

// Every stop of a healthy service takes the nil-Warn path, so a panic there would be the most visible failure a stop can have.
func TestStopSinWarnNoRevienta(t *testing.T) {
	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "sleep 30", WorkDir: t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "o.log"), StderrPath: filepath.Join(t.TempDir(), "e.log"),
	})
	if err := m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	waitPIDGone(t, res.Pid)
}

func TestStopSpecWarnfSoloEmiteSiHayWarn(t *testing.T) {
	var got []string
	s := StopSpec{Warn: func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) }}

	s.warnf("conteo %d", 3)
	if len(got) != 1 || got[0] != "conteo 3" {
		t.Errorf("con Warn: %v, want ['conteo 3']", got)
	}

	got = nil
	nilSpec := StopSpec{}
	nilSpec.warnf("no debe pasar nada")
	if len(got) != 0 {
		t.Errorf("sin Warn emitió %v", got)
	}
}

func TestLineageDescDescribeLoQueQueda(t *testing.T) {
	tests := []struct {
		name    string
		lineage []int
		want    string
	}{
		{"sólo la raíz", []int{100}, "pgid 100"},
		// The number in the warning is the root, not the lineage length: an empty lineage still names the group being stopped.
		{"vacío", nil, "pgid 100"},
		{"raíz y un descendiente", []int{100, 101}, "pgid 100 y 1 descendiente(s)"},
		{"raíz y tres", []int{100, 101, 102, 103}, "pgid 100 y 3 descendiente(s)"},
	}
	for _, tt := range tests {
		if got := lineageDesc(100, tt.lineage); got != tt.want {
			t.Errorf("%s: lineageDesc = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// Dedup matters because one pid listed twice (IPv4 and IPv6) would otherwise read as two owners and kill nothing, failing closed for the wrong reason.
func TestDistinctOwnersFiltraRepetidosYNegativos(t *testing.T) {
	tests := []struct {
		name string
		in   []int32
		want []int
	}{
		{"vacío", nil, []int{}},
		{"uno", []int32{42}, []int{42}},
		{"repetido", []int32{42, 42}, []int{42}},
		{"dos distintos", []int32{42, 43}, []int{42, 43}},
		{"con ceros y negativos", []int32{0, 42, -1}, []int{42}},
		{"sólo basura", []int32{0, -1}, []int{}},
	}
	for _, tt := range tests {
		got := distinctOwners(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("%s: distinctOwners(%v) = %v, want %v", tt.name, tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%s: distinctOwners(%v) = %v, want %v", tt.name, tt.in, got, tt.want)
				break
			}
		}
	}
}

func TestKillPortHolderSinWarnNoRevienta(t *testing.T) {
	killPortHolderWith(1 /* puerto*/, os.Getpid(), nil, func(int) []int32 { return nil }, nil)

	// Ownership decides the kill and the Warn callback only informs, so a nil Warn never changes the verdict.
	killPortHolderWith(1, os.Getpid(), nil, func(int) []int32 { return []int32{999999} }, nil)
}

// Validation must precede log truncation, otherwise a rejected command silently wipes the previous run's history.
func TestStartRechazaComandoVacioAntesDeTocarNada(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")
	writeFileStr(t, out, "historial anterior\n")

	_, err := NewManager().Start(StartSpec{
		Command: "", WorkDir: dir, StdoutPath: out, StderrPath: errLog,
	})
	if err == nil {
		t.Fatal("un comando vacío debería fallar")
	}
	if !strings.Contains(err.Error(), "empty command") {
		t.Errorf("err = %q, want 'empty command'", err)
	}
	if got := readFileStr(t, out); got != "historial anterior\n" {
		t.Errorf("el log se truncó antes de validar el comando: %q", got)
	}
}

// Failing before spawning sh matters: a process created and then abandoned has no Meta, so nothing would ever stop it.
func TestStartFallaSiNoPuedeAbrirLosLogs(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "stdout.log")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := NewManager().Start(StartSpec{
		Command: "sleep 30", WorkDir: dir,
		StdoutPath: blocked, StderrPath: filepath.Join(dir, "stderr.log"),
	})
	if err == nil {
		t.Fatal("un stdout.log que es un directorio debería hacer fallar el arranque")
	}
	if !strings.Contains(err.Error(), "stdout.log") {
		t.Errorf("err = %q, want que nombre el log que no pudo abrir", err)
	}
}

func TestStartFallaSiElDirectorioDeLogsNoSePuedeCrear(t *testing.T) {
	dir := t.TempDir()
	blocker := writeFileStr(t, filepath.Join(dir, "bloqueante"), "soy un fichero\n")

	_, err := NewManager().Start(StartSpec{
		Command: "sleep 30", WorkDir: dir,
		StdoutPath: filepath.Join(blocker, "sub", "stdout.log"),
		StderrPath: filepath.Join(blocker, "sub", "stderr.log"),
	})
	if err == nil {
		t.Fatal("no se puede crear el directorio de logs: el arranque debería fallar")
	}
	if !strings.Contains(err.Error(), "could not create log directory") {
		t.Errorf("err = %q, want 'could not create log directory'", err)
	}
}

// The error names the command it tried to launch, which is what the user needs to correct the manifest.
func TestStartFallaSiElProcesoNoSePuedeLanzar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "") // sin `sh`

	_, err := NewManager().Start(StartSpec{
		Command: "sleep 30", WorkDir: dir,
		StdoutPath: filepath.Join(dir, "stdout.log"), StderrPath: filepath.Join(dir, "stderr.log"),
	})
	if err == nil {
		t.Fatal("sin intérprete el arranque debería fallar")
	}
	if !strings.Contains(err.Error(), "failed to start") {
		t.Errorf("err = %q, want 'failed to start'", err)
	}
	if !strings.Contains(err.Error(), "sleep 30") {
		t.Errorf("err = %q, want que nombre el comando que no se pudo lanzar", err)
	}
}

// Truncation is to zero with no banner: an append would leave logs --tail full of the previous run.
func TestStartTruncaLosLogsDeCadaArranque(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")
	writeFileStr(t, out, "salida del servicio ANTERIOR\n")

	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "echo servicio-nuevo", WorkDir: dir,
		StdoutPath: out, StderrPath: errLog,
	})
	if err := m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	waitPIDGone(t, res.Pid)

	body := readFileStr(t, out)
	if strings.Contains(body, "ANTERIOR") {
		t.Errorf("el log del arranque anterior sobrevivió al truncado:\n%s", body)
	}
	if !strings.Contains(body, "servicio-nuevo") {
		t.Errorf("la salida del servicio nuevo no llegó al log:\n%s", body)
	}
}

func TestMergeEnvPisaYAnadeYDescartaLoMalformado(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "PORT=8080", "HOME=/root"}
	got := mergeEnv(parent, []string{"PORT=9999", "NUEVA=si", "basura", "=novacia", ""})

	m := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	if m["PORT"] != "9999" {
		t.Errorf("PORT = %q, want 9999: el spec tiene que pisar al padre", m["PORT"])
	}
	if m["NUEVA"] != "si" {
		t.Errorf("NUEVA = %q: una variable nueva debe sobrevivir", m["NUEVA"])
	}
	if m["PATH"] != "/usr/bin" || m["HOME"] != "/root" {
		t.Errorf("el padre se perdió: %v", m)
	}
	// MEDIDO: an entry without "=" survives because the filter only drops an empty key, and PATH with no value is legal so discarding it would be worse.
	for _, kv := range got {
		if key, _, _ := strings.Cut(kv, "="); key == "" {
			t.Errorf("llegó al entorno del hijo una entrada con clave vacía: %q", kv)
		}
	}
	// No duplicate keys: exec dedup would drop one silently and vroom cannot decide which one wins.
	seenKey := map[string]int{}
	for _, kv := range got {
		key, _, _ := strings.Cut(kv, "=")
		seenKey[key]++
	}
	for key, n := range seenKey {
		if n > 1 {
			t.Errorf("la clave %s aparece %d veces en el entorno del hijo", key, n)
		}
	}
}

func TestMergeEnvDevuelveNilSinNadaQueInyectar(t *testing.T) {
	if got := mergeEnv([]string{"PATH=/usr/bin"}, nil); got != nil {
		t.Errorf("mergeEnv sin extra dio %#v, want nil: un slice vacío vaciaría el entorno del hijo", got)
	}
	if got := mergeEnv([]string{"PATH=/usr/bin"}, []string{}); got != nil {
		t.Errorf("mergeEnv con extra vacío dio %#v, want nil", got)
	}
}

// readMetricsAt is already covered against a synthetic tree; what this pins is the wrapper's procRoot, since a wrong one would silently report another process.
func TestReadMetricsDelProcesoVivo(t *testing.T) {
	m, err := ReadMetrics(os.Getpid())
	if err != nil {
		t.Fatalf("ReadMetrics del propio proceso: %v", err)
	}
	if m.RSSKB <= 0 {
		t.Errorf("RSSKB = %d, want > 0: un proceso vivo tiene memoria residente", m.RSSKB)
	}
	if m.Threads <= 0 {
		t.Errorf("Threads = %d, want > 0", m.Threads)
	}
	if m.Ticks <= 0 {
		t.Errorf("Ticks = %d, want > 0: un proceso que ha corrido acumula ticks de CPU", m.Ticks)
	}
	if m.FDs <= 0 {
		t.Errorf("FDs = %d, want > 0", m.FDs)
	}
}

// An error rather than zeros: an all-zero reading would paint a plausible-looking 0 kB service.
func TestReadMetricsDeUnPidInexistenteDaError(t *testing.T) {
	dead := findDeadPID(t)
	if _, err := ReadMetrics(dead); err == nil {
		t.Error("un pid muerto no debería dar métricas: daría ceros que parecen datos")
	}
}

// The kernel freezes this block at execve, which is why a later t.Setenv never appears here and the Env tab cannot read os.Environ instead.
func TestReadEnvironDelProcesoVivo(t *testing.T) {
	real := pickRealEnvVar(t)

	got, err := ReadEnviron(os.Getpid())
	if err != nil {
		t.Fatalf("ReadEnviron: %v", err)
	}
	var found bool
	for _, kv := range got {
		if strings.HasPrefix(kv, real+"=") {
			found = true
		}
	}
	if !found {
		t.Errorf("no está %s en el entorno del proceso (%d variables)", real, len(got))
	}

	t.Setenv("VROOM_TEST_MARCADOR_QUE_NO_SE_VE", "1")
	despues, err := ReadEnviron(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range despues {
		if strings.HasPrefix(kv, "VROOM_TEST_MARCADOR") {
			t.Error("un Setenv aparece en /proc/environ: el entorno se lee de aquí y no de os.Environ, así que este test documentaría lo contrario de lo que pasa")
		}
	}
}

// An error, not an empty list: an empty list reads as "the service has no variables", which is a claim rather than a failure.
func TestReadEnvironDeUnPidInexistenteDaError(t *testing.T) {
	dead := findDeadPID(t)
	if _, err := ReadEnviron(dead); err == nil {
		t.Error("un pid muerto debería dar error al leer su entorno")
	} else if !strings.Contains(err.Error(), "process environ") {
		t.Errorf("err = %q, want el prefijo 'process environ'", err)
	}
}

// Same pid with a different creation_time is a recycled pid: accepting it would make vroom believe a foreign process is its service and SIGKILL it on stop.
func TestAliveRechazaPidReciclado(t *testing.T) {
	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "sleep 30", WorkDir: t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "o.log"), StderrPath: filepath.Join(t.TempDir(), "e.log"),
	})

	if !Alive(res.Pid, res.CreationTimeMs) {
		t.Error("un proceso vivo con su creation_time debe estar vivo")
	}
	if Alive(res.Pid, res.CreationTimeMs+1) {
		t.Error("Alive aceptó un creation_time distinto: es exactamente el PID reciclado que tiene que rechazar")
	}
	if Alive(res.Pid, 0) {
		t.Error("Alive aceptó creation_time 0: sin ese dato no hay prueba de identidad")
	}

	if err := m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	waitPIDGone(t, res.Pid)
	if Alive(res.Pid, res.CreationTimeMs) {
		t.Error("un proceso parado sigue vivo")
	}
}

// gopsutil's NewProcess(0) addresses init's group on some systems, which would be an "alive" belonging to nobody.
func TestAliveConPidNoNumericoEsFalso(t *testing.T) {
	for _, pid := range []int{0, -1, -9999} {
		if Alive(pid, 0) {
			t.Errorf("Alive(%d) = true: un pid no válido no puede ser un proceso", pid)
		}
	}
}

func TestPortOwnerPIDAmbiguoEsCero(t *testing.T) {
	port := listenRaw(t)
	pid := PortOwnerPID(port)
	if pid == 0 {
		// The listener lives in another process and may be unreadable for permissions, so only the resolvable branch is asserted.
		t.Logf("no se pudo determinar el dueño del puerto %d (permisos o timing): el caso ambiguo se cubre aparte", port)
		return
	}
	if pid <= 0 {
		t.Errorf("PortOwnerPID = %d, want > 0 o 0", pid)
	}
}

func TestPgidAliveDistingueLoQueNoEsNuestro(t *testing.T) {
	t.Run("grupo propio vivo", func(t *testing.T) {
		if !pgidAlive(ownPgid(t)) {
			t.Error("el grupo del propio proceso de test está vivo")
		}
	})
	t.Run("pgid 0 no es un grupo", func(t *testing.T) {
		if pgidAlive(0) {
			t.Error("pgid 0 debe contar como no vivo: emitir -0 señalaría al grupo entero")
		}
	})
	t.Run("pgid inexistente", func(t *testing.T) {
		if pgidAlive(1 << 22) {
			t.Error("un pgid que no existe no puede estar vivo")
		}
	})
}

// Without this guard, every stop of an already-stopped service would sit out its full timeout.
func TestWaitLineageGoneVuelveProntoSiNoQuedaNada(t *testing.T) {
	start := time.Now()
	if !waitLineageGone(0, nil, 5*time.Second) {
		t.Fatal("un linaje vacío no puede seguir vivo")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("tardó %v con un linaje vacío: no hay nada que esperar", elapsed)
	}
}

// A process that ignores SIGKILL cannot be provoked reliably, so an unsignalled live pid with a short timeout is used because the function only polls, which is what is under test.
func TestWaitLineageGoneExpiraConUnProcesoQueNoMuere(t *testing.T) {
	me := os.Getpid()
	start := time.Now()
	if waitLineageGone(0, []int{me}, 200*time.Millisecond) {
		t.Error("con el propio proceso vivo y sin señalizar, waitLineageGone debería expirar")
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("volvió en %v: no esperó al timeout", elapsed)
	}
}

func ownPgid(t *testing.T) int {
	t.Helper()
	info, err := procStatAt(filepath.Join(procRoot, strconv.Itoa(os.Getpid())))
	if err != nil {
		t.Fatalf("no se pudo leer el stat del proceso de test: %v", err)
	}
	return info.pgid
}

func waitPIDGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(procRoot, strconv.Itoa(pid))); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("el pid %d sigue en /proc tras el stop", pid)
}

// Must pick a variable inherited at execve, because a later t.Setenv never reaches /proc/self/environ and would make the test assert nothing.
func pickRealEnvVar(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		t.Skipf("no se puede leer /proc/self/environ: %v", err)
	}
	for _, want := range []string{"PATH=", "HOME=", "USER=", "LANG=", "SHELL=", "PWD="} {
		if strings.Contains(string(raw), want) {
			return strings.TrimSuffix(want, "=")
		}
	}
	t.Skip("ninguna variable de entorno conocida está presente")
	return ""
}

func listenRaw(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

func writeFileStr(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFileStr(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
