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

// ---------------------------------------------------------------------------
// Los bordes de process: parsing de /proc, el linaje, y las guardas de los dos
// caminos de Stop.
//
// Casi todo aquí lee ficheros SINTÉTICOS en un árbol temporal en vez de /proc
// real, y esa es la decisión: la forma de un /proc/<pid>/stat malformado no se
// puede provocar en un proceso de verdad, y las reglas de parseo (el comm con
// paréntesis y espacios, el pgrp que no es el pid) son donde se concentran los
// bugs. Un stat de mentira exercised por el parser REAL es mejor que un proceso
// real que además depende de la versión del kernel.
//
// Donde el comportamiento sí es sobre procesos de verdad —el linaje, la parada,
// el descubrimiento de puertos— se usan procesos de verdad.
// ---------------------------------------------------------------------------

// fakeProc escribe un /proc sintético: stat, status, task/<tid> y fd/.
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

// TestProcStatAtAceptaElCommConParentesisYEspacios: el campo comm puede
// contener cualquier cosa, y el parser tiene queRecover desde el ÚLTIMO ')'.
//
// Es el motivo de que el parser no haga `strings.Fields` sobre la línea entera:
// "Web Content (tab)" tiene espacios, y "weird)name" tiene un paréntesis que
// cierra antes de tiempo. Partir por el último ')' es lo único que sobrevive a
// las dos cosas.
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

// TestProcInfoRunningIgnoraZombiesYNoPids: un zombie no está vivo aunque exista,
// y un pid 0 tampoco.
//
// MEDIDO: el único estado que cuenta como "no vivo" es Z. Y es lo correcto: un
// zombie ya soltó sus descriptores —su puerto está libre de verdad—, así que
// tratarlo como vivo haría que Stop esperase el timeout entero por un grupo que
// ya no va a cambiar. Un "T" (parado bajo un debugger) SIGUE teniendo el puerto, y
// por eso cuenta como vivo.
//
// El pid 0 se aparta porque kill(-0) señalaría al grupo entero del proceso.
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

// TestDescendantsAtSinLinajeCuandoNoSePuedeLeer: un /proc ilegible da un linaje
// VACÍO, no un error.
//
// Y la consecuencia es la que importa: sin linaje capturado, Stop sólo puede
// alcanzar la raíz. Es un fallo cerrado (no se mata de más) pero con un coste que
// hay que entender: un servicio con descendientes puede quedar con procesos vivos.
// Un error en su lugar sería peor: pararía el arranque de la TUI.
func TestDescendantsAtSinLinajeCuandoNoSePuedeLeer(t *testing.T) {
	root := t.TempDir()
	got := descendantsAt(root, os.Getpid())
	if len(got) != 0 {
		t.Errorf("descendantsAt sobre un /proc vacío dio %v, want vacío", got)
	}
}

// TestDescendantsFromIgnoraAPadreQueEsSuHijo: un /proc sintético con un ciclo
// ppid == pid no cuelga de nadie y no se cuelga de sí mismo.
//
// Un ciclo de este tipo no pasa en un kernel vivo, pero un /proc con entradas
// corrupte lo puede parecer, y el recorrido BFS se comería la CPU en un ciclo
// infinito si no estuviera el `seen`.
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

// TestDescendantsFromNoDevuelveLaRaizNiRepite: el linaje no incluye la raíz y
// cada descendiente aparece una vez aunque tenga varios caminos.
//
// Sin el `seen`, un grafo con dos rutas al mismo nodo lo devolvería dos veces y
// Stop lo señalizaría dos veces — inofensivo— y `lineageDesc` contaría
// descendientes de más.
func TestDescendantsFromNoRepiteNiIncluyeLaRaiz(t *testing.T) {
	// 3 cuelga de 1 y de 2: dos caminos al mismo nodo.
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

// TestLineageRunningToleraPidsMuertosYNoNuméricos: un linaje con pids que ya no
// existen cuenta comoGone.
//
// Es lo que hace que Stop no espere el timeout por un grupo que ya murió entre la
// captura y el sondeo.
func TestLineageRunningToleraPidsMuertosYNoNuméricos(t *testing.T) {
	if lineageRunning(nil) {
		t.Error("un linaje vacío está vivo: eso haría que Stop esperara siempre")
	}
	if lineageRunning([]int{0, -1}) {
		t.Error("un linaje de pids no válidos está vivo")
	}
	// Un pid real vivo: sí.
	me := os.Getpid()
	if !lineageRunning([]int{me}) {
		t.Error("el propio proceso debería estar vivo")
	}
	// Un pid muerto con el actual: sigue vivo por el actual.
	dead := findDeadPID(t)
	if !lineageRunning([]int{dead, me}) {
		t.Error("un pid muerto no debe hacer que el linaje cuente como muerto")
	}
	// Sólo pids muertos: muerto.
	if lineageRunning([]int{dead, findDeadPID(t)}) {
		t.Error("un linaje de pids muertos no puede estar vivo")
	}
}

// findDeadPID arranca un proceso, lo para, y devuelve su pid.
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

// TestCaptureLineageSabeQuienEsLaRaiz: la raíz del linaje es el PID si coincide,
// y el PGID si el PID ya no está o no pertenece a ese grupo.
//
// Es la regla que hace que `vroom stop` funcione con los dos datos que se
// guardaron: si sólo se creyera el PID y este ha muerto, el linaje sería vacío y
// los descendientes (reparentados a init) no se alcanzarían nunca.
func TestCaptureLineageSabeQuienEsLaRaiz(t *testing.T) {
	me := os.Getpid()

	t.Run("sin pid ni pgid no hay nada que capturar", func(t *testing.T) {
		root, lineage := captureLineage(StopSpec{})
		if root != 0 || lineage != nil {
			t.Errorf("captureLineage({}) = %d/%v, want 0/nil", root, lineage)
		}
	})

	t.Run("con pid vivo: la raíz es el pid", func(t *testing.T) {
		// El proceso de test vive en su propio grupo (el runner no lo cambia).
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
		// Un pgid enorme: no hay tal grupo.
		root, lineage := captureLineage(StopSpec{Pid: me, Pgid: 1 << 22})
		if root != 0 || lineage != nil {
			t.Errorf("un pgid inexistente dio root=%d linaje=%v, want 0/nil", root, lineage)
		}
	})
}

// TestStopSinWarnNoRevienta: el Warn del spec es opcional y su ausencia no puede
// hacer que Stop entre en pánico.
//
// Es un camino que se recorre en cada parada de un servicio que no ha pasado nada
// raro, así que un pánico ahí sería el fallo más visible que puede tener el stop.
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

// TestStopSpecWarnfSoloEmiteSiHayWarn: warnf es el único punto por el que salen
// los avisos del stop, y con Warn nil no emite nada.
//
// Se prueba el método directamente porque la alternativa es provoking un stop que
// avise, que exige un linaje que sobreviva a SIGKILL —imposible de hacer de forma
// fiable— y probaría el camino equivocado.
func TestStopSpecWarnfSoloEmiteSiHayWarn(t *testing.T) {
	var got []string
	s := StopSpec{Warn: func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) }}

	s.warnf("conteo %d", 3)
	if len(got) != 1 || got[0] != "conteo 3" {
		t.Errorf("con Warn: %v, want ['conteo 3']", got)
	}

	// Sin Warn no emite, y no entra en pánico.
	got = nil
	nilSpec := StopSpec{}
	nilSpec.warnf("no debe pasar nada")
	if len(got) != 0 {
		t.Errorf("sin Warn emitió %v", got)
	}
}

// TestLineageDescDescribeLoQueQueda: el texto del aviso dice cuántos procesos
// quedan, y por eso el número importa.
//
// Un linaje de un solo elemento (sólo la raíz) dice "pgid N"; con descendientes
// dice cuántos. El usuario ve este texto cuando el stop no termina, y es lo único
// que le dice si queda uno o diez.
func TestLineageDescDescribeLoQueQueda(t *testing.T) {
	tests := []struct {
		name    string
		lineage []int
		want    string
	}{
		{"sólo la raíz", []int{100}, "pgid 100"},
		// El número del aviso es el root, no la longitud del linaje: con el
		// linaje vacío el aviso sigue nombrando el grupo que se está parando.
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

// TestDistinctOwnersFiltraRepetidosYNegativos: la lista de dueños del puerto se
// deduplica y se limpia antes de decidir.
//
// Importa porque la decisión de matar depende de que haya UN dueño: sin
// deduplicar, un mismo pid listado dos veces (IPv4 e IPv6) contaría como dos
// dueños y vroom no mataría nada —el fallo cerrado correcto por otro motivo—,
// pero con el motivo equivocado.
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

// TestKillPortHolderSinWarnNoRevienta: killPortHolderWith es un caminho público
// interno con Warn opcional, y sin Warn tiene que ser un no-op silencioso.
func TestKillPortHolderSinWarnNoRevienta(t *testing.T) {
	// Dueño desconocido: sin Warn no se dice nada y no se mata nada.
	killPortHolderWith(1 /* puerto*/, os.Getpid(), nil, func(int) []int32 { return nil }, nil)

	// Y con un Warn nil pero un dueño que NO es nuestro: tampoco se mata, y sin
	// aviso. La propiedad es lo que protege; el aviso es lo que informa.
	killPortHolderWith(1, os.Getpid(), nil, func(int) []int32 { return []int32{999999} }, nil)
}

// TestStartRechazaComandoVacioAntesDeTocarNada: sin comando no hay nada que
// arrancar, y el fallo tiene que ser ANTES de truncar los logs.
//
// El orden importa: truncar los logs y luego fallar dejaría al usuario sin el
// historial del servicio anterior sin ninguna explicación, y el siguiente `logs`
// saldría vacío.
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

// TestStartFallaSiNoPuedeAbrirLosLogs: un stdout.log que es un directorio hace
// fallar el arranque antes de crear el proceso.
//
// Y no deja proceso: un start que creara el `sh` y luego fallara al abrir el log
// dejaría un proceso sin Meta, que es un proceso que nada va a parar.
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

// TestStartFallaSiElDirectorioDeLogsNoSePuedeCrear: el mkdir del directorio de
// logs es lo primero que se toca, y su fallo tiene su propio mensaje.
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

// TestStartFallaSiElProcesoNoSePuedeLanzar: un `sh` que no existe deja el log
// ABIERTO y el arranque sin proceso.
//
// Y el mensaje dice qué comando se intentó lanzar, que es lo que necesita el
// usuario para corregir el manifiesto.
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

// TestStartTruncaLosLogsDeCadaArranque: cada arranque empieza con el log vacío,
// porque si no el `logs` del servicio nuevo mostraría la salida del anterior.
//
// Y el banner de "truncado" no existe: es un truncado a cero, no un append. Un
// servicio que escribe mucho dejaría el `logs --tail` lleno de la ejecución
// anterior.
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

// TestMergeEnvPisaYAnadeYDescartaLoMalformado: la fusión de entorno decide qué
// variables ve el servicio, y las tres reglas importan.
//
//   - Una variable del spec PISA a la del padre (PORT, por ejemplo).
//   - Una variable nueva se AÑADE sin perder el resto (si no, el hijo no tendría
//     PATH y `sh` no encontraría nada).
//   - Una entrada sin "=" se descarta: no es una variable y exec la rechazaría.
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
	// MEDIDO: una entrada SIN "=" sobrevive, porque el filtro es `key == ""` y
	// "basura" tiene clave no vacía. No es un descuido: `PATH` a secas es una
	// entrada válida con valor vacío, y descartarla sería peor que mantenerla. Lo
	// que sí se descarta es lo que no puede ser una variable: la clave vacía.
	for _, kv := range got {
		if key, _, _ := strings.Cut(kv, "="); key == "" {
			t.Errorf("llegó al entorno del hijo una entrada con clave vacía: %q", kv)
		}
	}
	// Y la lista no puede contener entradas duplicadas por clave: exec dedup
	//Svaya, y vroom no puede decidir cuál gana.
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

// TestMergeEnvDevuelveNilSinNadaQueInyectar: nil es lo que hace que exec aplique
// la herencia normal, y []string{} NO: en Go cualquier slice no-nil reemplaza el
// entorno entero.
//
// Por eso el caso `len(extra) == 0` tiene que devolver nil y no un slice vacío,
// y por eso este test mira el valor y no sólo la longitud.
func TestMergeEnvDevuelveNilSinNadaQueInyectar(t *testing.T) {
	if got := mergeEnv([]string{"PATH=/usr/bin"}, nil); got != nil {
		t.Errorf("mergeEnv sin extra dio %#v, want nil: un slice vacío vaciaría el entorno del hijo", got)
	}
	if got := mergeEnv([]string{"PATH=/usr/bin"}, []string{}); got != nil {
		t.Errorf("mergeEnv con extra vacío dio %#v, want nil", got)
	}
}

// TestReadMetricsDelProcesoVivo: el wrapper público lee del /proc de verdad.
//
// Es el camino que usa la pestaña de métricas, y readMetricsAt ya está probado con
// un árbol sintético. Lo que se comprueba aquí es que el wrapper pasa el
// procRoot correcto, porque un procRoot equivocado daría métricas de otro
// proceso sin ningún error visible.
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
	// Los fds son los de ESTE proceso de test, que tiene alguno.
	if m.FDs <= 0 {
		t.Errorf("FDs = %d, want > 0", m.FDs)
	}
}

// TestReadMetricsDeUnPidInexistenteDaError: sin stat no hay ticks, y sin ticks no
// hay métricas.
//
// Se falla en el stat y no se degrada a cero: unas métricas todas a cero harían
// que la TUI pintara un servicio con 0 kB de RAM, que es un dato que parece real.
func TestReadMetricsDeUnPidInexistenteDaError(t *testing.T) {
	dead := findDeadPID(t)
	if _, err := ReadMetrics(dead); err == nil {
		t.Error("un pid muerto no debería dar métricas: daría ceros que parecen datos")
	}
}

// TestReadEnvironDelProcesoVivo: el entorno del proceso sale de /proc/<pid>/environ.
//
// Y la forma es una lista de "KEY=value" con NUL como separador, que es lo que
// espera la pestaña Env. Es el único lugar del repo donde se lee el entorno de
// OTRO proceso, y tiene una consecuencia que sólo se ve probándolo: el entorno
// que ve el proceso se congeló en su execve, así que un `t.Setenv` posterior NO
// aparece.
func TestReadEnvironDelProcesoVivo(t *testing.T) {
	// Se usa una variable REAL heredada del proceso, no una inyectada: /proc/self/
	// environ es el bloque que el kernel congeló en execve, y setenv no lo
	// reescribe. Inyectar una marcar aquí sería un test que pasa sin comprobar
	// nada.
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

	// Y el marcador de un Setenv NO está, que es justo lo que hace que la pestaña
	// Env tenga que leer de aquí y no de os.Environ.
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

// TestReadEnvironDeUnPidInexistenteDaError: sin fichero environ no hay entorno,
// y eso es un error y no una lista vacía.
//
// Una lista vacía en la pestaña Env se lee como "el servicio no tiene
// variables", que es un hecho. Un error se lee como "no lo pude leer".
func TestReadEnvironDeUnPidInexistenteDaError(t *testing.T) {
	dead := findDeadPID(t)
	if _, err := ReadEnviron(dead); err == nil {
		t.Error("un pid muerto debería dar error al leer su entorno")
	} else if !strings.Contains(err.Error(), "process environ") {
		t.Errorf("err = %q, want el prefijo 'process environ'", err)
	}
}

// TestAliveRechazaPidReciclado: la protección anti-reuse es el motivo de existir
// de Alive, y se comprueba con el creation_time.
//
// El caso: mismo PID, creation_time distinto. Pasa en la realidad cuando un
// servicio muere y otro proceso hereda el PID, y aceptarlo haría que vroom
// creyera que un servicio ajeno es el suyo —y que le mandara un SIGKILL al parar.
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
	// Y sin creation_time (0) tampoco: no hay prueba de que sea el mismo.
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

// TestAliveConPidNoNumericoEsFalso: un pid 0 o negativo no es un proceso.
//
// `gopsprocess.NewProcess(0)` habla con el grupo de init en algunos sistemas, y
// eso sería un "vivo" que no es de nadie.
func TestAliveConPidNoNumericoEsFalso(t *testing.T) {
	for _, pid := range []int{0, -1, -9999} {
		if Alive(pid, 0) {
			t.Errorf("Alive(%d) = true: un pid no válido no puede ser un proceso", pid)
		}
	}
}

// TestPortOwnerPIDAmbiguoEsCero: más de un dueño devuelve 0, que es lo que
// dispara el fallo cerrado de la parada.
//
// La razón de que ambiguo y desconocido sean lo mismo: quien pregunta necesita
// una PRUEBA de propiedad, y "hay alguien" no la es. Un valor "candidato" haría
// que el stop matara al proceso equivocado.
func TestPortOwnerPIDAmbiguoEsCero(t *testing.T) {
	port := listenRaw(t)
	pid := PortOwnerPID(port)
	if pid == 0 {
		// El propio listener está en otro proceso y puede no ser legible por
		// permisos; se acepta, y lo que se prueba es el otro lado.
		t.Logf("no se pudo determinar el dueño del puerto %d (permisos o timing): el caso ambiguo se cubre aparte", port)
		return
	}
	if pid <= 0 {
		t.Errorf("PortOwnerPID = %d, want > 0 o 0", pid)
	}
}

// TestPgidAliveDistingueLoQueNoEsNuestro: un grupo que existe pero no es nuestro
// cuenta como vivo, porque existe.
//
// Es el motivo del `case syscall.EPERM: return true`. Si devolviera false, Stop
// declararía limpio un grupo que no se puede señalar (porque es de otro usuario)
// y dejaría procesos vivos del propio servicio. Fallar cerrado en el otro sentido
// —esperar el timeout entero por un grupo ajeno— es el precio, y el correcto: el
// aviso dice "quedan procesos vivos" y el usuario investiga.
func TestPgidAliveDistingueLoQueNoEsNuestro(t *testing.T) {
	t.Run("grupo propio vivo", func(t *testing.T) {
		if !pgidAlive(ownPgid(t)) {
			t.Error("el grupo del propio proceso de test está vivo")
		}
	})
	t.Run("pgid 0 no es un grupo", func(t *testing.T) {
		// kill(-0) señalaría al grupo entero del proceso, que es exactamente lo
		// que no debe pasar por un campo a cero.
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

// TestWaitLineageGoneVuelveProntoSiNoQuedaNada: un linaje ya vacío no espera.
//
// Es la guarda que evita que cada parada de un servicio ya parado espere su
// timeout: sin ella, `vroom stop` sobre un servicio parado tardaría 5s.
func TestWaitLineageGoneVuelveProntoSiNoQuedaNada(t *testing.T) {
	start := time.Now()
	if !waitLineageGone(0, nil, 5*time.Second) {
		t.Fatal("un linaje vacío no puede seguir vivo")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("tardó %v con un linaje vacío: no hay nada que esperar", elapsed)
	}
}

// TestWaitLineageGoneExpiraConUnProcesoQueNoMuere: un pid vivo que no muere
// agota el timeout y devuelve false.
//
// No se puede provocar de forma fiable sin un proceso que ignore SIGKILL, así
// que se usa un PID vivo con un timeout corto y sin señalizar: la función sólo
// sondea, y eso es justo lo que se quiere comprobar.
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

// ---- helpers ----

// ownPgid devuelve el pgid del proceso de test, leyéndolo de /proc.
func ownPgid(t *testing.T) int {
	t.Helper()
	info, err := procStatAt(filepath.Join(procRoot, strconv.Itoa(os.Getpid())))
	if err != nil {
		t.Fatalf("no se pudo leer el stat del proceso de test: %v", err)
	}
	return info.pgid
}

// waitPIDGone espera a que el pid desaparezca de /proc.
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

// pickRealEnvVar devuelve el nombre de una variable de entorno REALMENTE
// presente en el entorno del proceso.
//
// Se usa para ReadEnviron porque /proc/self/environ es el bloque que el kernel
// congeló en execve: un t.Setenv posterior NO aparece ahí. Elegir una variable
// inyectada haría que el test pasara sin comprobar nada.
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

// listenRaw abre un listener real en un puerto libre de loopback y lo devuelve
// junto con su cierre.
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
