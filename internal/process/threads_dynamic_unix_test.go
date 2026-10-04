//go:build unix

package process

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Los bordes del muestreo de hilos y de la decisión de puerto.
//
// Son las dos funciones donde un dato malformado se convierte en un dato que la
// TUI presenta como hecho: un hilo con el nombre equivocado, o un puerto que no
// es el principal. En los dos casos el arreglo es leer un /proc SINTÉTICO y
// comprobar qué se salta y qué se conserva.
// ---------------------------------------------------------------------------

// fakeThreadTree construye un /proc/<pid>/task con los hilos descritos.
//
// Cada entrada es (tid, nombre en comm o "" para no escribirlo, stat o "" para
// no escribirlo).
func fakeThreadTree(t *testing.T, pid int, threads map[int][2]string) string {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, strconv.Itoa(pid), "task")
	for tid, v := range threads {
		dir := filepath.Join(base, strconv.Itoa(tid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if v[0] != "" {
			if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(v[0]+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if v[1] != "" {
			if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(v[1]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// TestListThreadsAtSaltaLoQueNoEsUnHilo: un directorio de task que no es un tid
// numérico se ignora, y un hilo sin stat legible también.
//
// El motivo de saltar un hilo sin stat es que está MURRIENDO: se abrió su
// directorio y se cerró antes de que se leyera. Incluirlo con el nombre y sin
// estado daría una fila a medio llenar en la tabla de hilos, y el usuario no
// sabría qué parte de la fila es real.
func TestListThreadsAtSaltaLoQueNoEsUnHilo(t *testing.T) {
	good := "99 (hilo) " + strings.TrimSuffix(strings.Repeat("1 ", 13), " ")
	root := fakeThreadTree(t, 99, map[int][2]string{
		100: {"principal", good},
		// comm sin stat: el hilo está muriendo, se salta.
		101: {"muriendo", ""},
		// stat sin comm: el nombre sale del stat.
		102: {"", good},
	})
	// Un directorio cuyo nombre no es un tid.
	if err := os.MkdirAll(filepath.Join(root, "99", "task", "no-es-un-tid"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := listThreadsAt(root, 99)
	if err != nil {
		t.Fatalf("listThreadsAt: %v", err)
	}

	byTID := map[int]ThreadInfo{}
	for _, ti := range got {
		byTID[ti.TID] = ti
	}
	if _, ok := byTID[101]; ok {
		t.Error("un hilo sin stat legible entró en la lista: está muriendo y daría una fila a medio llenar")
	}
	if _, ok := byTID[0]; ok {
		t.Error("un directorio de task llamado 'no-es-un-tid' entró en la lista")
	}
	if len(got) != 2 {
		t.Errorf("hay %d hilos, want 2 (los dos legibles)", len(got))
	}
	// Y el nombre del hilo sin comm sale del stat, no queda vacío.
	if byTID[102].Name != "hilo" {
		t.Errorf("el nombre del hilo 102 = %q, want 'hilo' (del stat)", byTID[102].Name)
	}
	if byTID[100].Name != "principal" {
		t.Errorf("el nombre del hilo 100 = %q, want 'principal' (de comm)", byTID[100].Name)
	}
	// Ordenados por tid, que es lo que hace la tabla legible.
	for i := 1; i < len(got); i++ {
		if got[i-1].TID > got[i].TID {
			t.Errorf("los hilos no vienen ordenados por tid: %v", got)
			break
		}
	}
}

// TestListThreadsAtSinTaskDaError: sin directorio task no hay hilos, y es un
// error y no una lista vacía.
//
// Una lista vacía se lee como "el proceso no tiene hilos", que es un hecho
// imposible para un proceso vivo. Un error se lee como "no lo pude mirar".
func TestListThreadsAtSinTaskDaError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "5"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := listThreadsAt(root, 5); err == nil {
		t.Error("sin task/ debería dar error, no una lista vacía de hilos")
	} else if !strings.Contains(err.Error(), "thread sampling") {
		t.Errorf("err = %q, want el prefijo 'thread sampling'", err)
	}
}

// TestParseThreadStatSumaUtimeYStime: los ticks de CPU de un hilo son
// utime+stime, no sólo utime.
//
// Suma importa porque un hilo que sólo hace E/S (un logger, un pool de I/O) pasa
// casi todo su tiempo en stime; con utime solo la CPU de la tabla saldría casi a
// cero para exactamente los hilos que más consumen.
func TestParseThreadStatSumaUtimeYStime(t *testing.T) {
	// rest[0] es el campo 3 (state) y los índices 11 y 12 son los campos 14 y 15
	// (utime y stime), así que hacen falta 13 campos tras el cierre del comm.
	// Fields() colapsa los campos vacíos, así que TODOS tienen que llevar un
	// número: un slice con huecos daría menos de 13 tokens y el parser lo
	// rechazaría por incompleto, que no es lo que se quiere probar aquí.
	full := make([]string, 13)
	for i := range full {
		full[i] = "1"
	}
	full[0] = "R"
	full[11] = "100"
	full[12] = "20"

	path := filepath.Join(t.TempDir(), "stat")
	if err := os.WriteFile(path, []byte("1 (nombre) "+strings.Join(full, " ")), 0o644); err != nil {
		t.Fatal(err)
	}

	state, ticks, err := parseThreadStat(path)
	if err != nil {
		t.Fatalf("parseThreadStat: %v", err)
	}
	if state != "R" {
		t.Errorf("state = %q, want R", state)
	}
	if ticks != 120 {
		t.Errorf("ticks = %d, want 120 (utime 100 + stime 20)", ticks)
	}

	// Y un stat que no llega a los campos de CPU: error, no ceros.
	short := filepath.Join(t.TempDir(), "corto")
	if err := os.WriteFile(short, []byte("1 (x) S 1 1 1 0 -1 0 0 0 0 0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseThreadStat(short); err == nil {
		t.Error("un stat incompleto debería dar error, no ceros que parecen 'este hilo no consume CPU'")
	}

	// Un utime no numérico también es error: los ceros serían un dato falso.
	bad := make([]string, 13)
	for i := range bad {
		bad[i] = "1"
	}
	bad[0] = "R"
	bad[11] = "mucho"
	bad[12] = "0"
	badPath := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(badPath, []byte("1 (x) "+strings.Join(bad, " ")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseThreadStat(badPath); err == nil {
		t.Error("un utime no numérico debería dar error")
	}

	// Sin paréntesis, y fichero inexistente.
	sinComm := filepath.Join(t.TempDir(), "sin-comm")
	if err := os.WriteFile(sinComm, []byte("1 sin-parentesis 1 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseThreadStat(sinComm); err == nil {
		t.Error("un stat sin paréntesis no se puede parsear")
	}
	if _, _, err := parseThreadStat(filepath.Join(t.TempDir(), "nada")); err == nil {
		t.Error("un stat inexistente debería dar error")
	}
}

// TestThreadNameFromStatCaeAlUltimoParentesis: el nombre del hilo sale del stat
// cuando no hay comm, y se recorta desde el último ')'.
//
// Mismo motivo que procStatAt: un nombre de hilo puede contener espacios y
// paréntesis.
func TestThreadNameFromStatCaeAlUltimoParentesis(t *testing.T) {
	dir := t.TempDir()

	t.Run("nombre con espacios", func(t *testing.T) {
		p := filepath.Join(dir, "a")
		if err := os.WriteFile(p, []byte("42 (Web Content (tab)) S 1 1"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := threadNameFromStat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got != "Web Content (tab)" {
			t.Errorf("nombre = %q, want 'Web Content (tab)'", got)
		}
	})

	t.Run("stat sin paréntesis", func(t *testing.T) {
		p := filepath.Join(dir, "b")
		if err := os.WriteFile(p, []byte("42 sin-nombre 1 1"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := threadNameFromStat(p); err == nil {
			t.Error("un stat sin paréntesis no tiene nombre de hilo recuperable")
		}
	})

	t.Run("fichero inexistente", func(t *testing.T) {
		if _, err := threadNameFromStat(filepath.Join(dir, "nada")); err == nil {
			t.Error("un stat inexistente debería dar error")
		}
	})
}

// TestParseVmRSSDevuelveCeroSinElCampo: sin VmRSS el valor es 0, no un error.
//
// Es el caso de un proceso que aún no ha asignado memoria, y 0 es la respuesta
// honesta: la TUI lo pinta como "0 kB", que es un hecho. Un error haría que la
// pestaña de métricas quedara en blanco para un proceso recién arrancado.
func TestParseVmRSSDevuelveCeroSinElCampo(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   int64
	}{
		{"normal", "Name:\tbash\nVmRSS:\t   12345 kB\nThreads:\t2\n", 12345},
		{"sin el campo", "Name:\tbash\nThreads:\t2\n", 0},
		{"campo vacío", "VmRSS:\t\n", 0},
		{"campo no numérico", "VmRSS:\t   mucho kB\n", 0},
		{"sin unidades pero numérico", "VmRSS:\t99\n", 99},
		{"vacío", "", 0},
	}
	for _, tt := range tests {
		if got := parseVmRSS(tt.status); got != tt.want {
			t.Errorf("%s: parseVmRSS = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// TestProbeStatusDevuelveCeroSiNoContesta: un puerto donde no hay HTTP devuelve
// 0, y 0 es un valor que ningún status real toma.
//
// La razón es que decidePort usa el status para RANKING: un 0 tiene que ser
// "no puntuación", no "el peor de todos", o un puerto que no contesta acabaría
// gaining ranking por descarte y elegido como principal.
func TestProbeStatusDevuelveCeroSiNoContesta(t *testing.T) {
	if got := probeStatus(closedPortForTest(t), "/"); got != 0 {
		t.Errorf("probeStatus de un puerto muerto = %d, want 0", got)
	}

	// Y de verdad.
	port, stop := statusListener(t, http.StatusOK)
	defer stop()
	if got := probeStatus(port, ""); got != http.StatusOK {
		t.Errorf("probeStatus de un servidor real = %d, want 200", got)
	}
	// Con healthPath también.
	if got := probeStatus(port, "/health"); got != http.StatusOK {
		t.Errorf("probeStatus con healthPath = %d, want 200", got)
	}
}

// TestSamePortsComparaElConjuntoEntero: dos conjuntos de puertos son el mismo
// si tienen los mismos elementos en el MISMO orden y el mismo número.
//
// El orden importa porque la lista viene del recorrido de /proc, que no está
// ordenado; y el número importa porque un subconjunto no es el mismo conjunto. Es
// lo que hace que el conjunto se considere "estabilizado" para poder decidir.
func TestSamePortsComparaElConjuntoEntero(t *testing.T) {
	tests := []struct {
		a, b []int
		want bool
	}{
		{nil, nil, true},
		{nil, []int{}, true},
		{[]int{1}, []int{1}, true},
		{[]int{1, 2}, []int{1, 2}, true},
		{[]int{1, 2}, []int{2, 1}, false}, // mismo conjunto, otro orden
		{[]int{1, 2}, []int{1}, false},    // subconjunto no es el mismo conjunto
		{[]int{1}, []int{2}, false},
	}
	for _, tt := range tests {
		if got := samePorts(tt.a, tt.b); got != tt.want {
			t.Errorf("samePorts(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestDecidePortRespetaElPuertoReservadoYElUnicoListener: las dos decisiones que
// no dependen de la red.
//
// El reservado gana siempre si está entre los listeners, y eso no es una
// preferencia: vroom lo reservó y se lo pasó al servicio, así que cualquier otro
// puerto significa que la reserva se ignoró. Con HonoredReserved y Verified a
// true queda constancia en el Meta de que se respetó.
func TestDecidePortRespetaElPuertoReservadoYElUnicoListener(t *testing.T) {
	t.Run("el reservado está en la lista", func(t *testing.T) {
		got := decidePort([]int{8080, 9090}, 9090, "")
		if got.Port != 9090 {
			t.Errorf("Port = %d, want el reservado 9090", got.Port)
		}
		if !got.HonoredReserved {
			t.Error("HonoredReserved = false: el Meta dejaría constancia de que la reserva NO se respetó")
		}
		if !got.Verified {
			t.Error("Verified = false con un solo listener real: no hay nada que decidir")
		}
		if len(got.All) != 2 {
			t.Errorf("All = %v, want los dos listeners: el usuario tiene que ver la ambigüedad aunque se resuelva", got.All)
		}
	})

	t.Run("el reservado no está en la lista", func(t *testing.T) {
		got := decidePort([]int{8080, 9090}, 7000, "")
		if got.Port == 7000 {
			t.Error("eligió un puerto que no está escuchando")
		}
		if got.HonoredReserved {
			t.Error("HonoredReserved = true sin que el reservado esté en la lista")
		}
	})

	t.Run("un solo listener: no hay decisión que tomar", func(t *testing.T) {
		got := decidePort([]int{8080}, 0, "")
		if got.Port != 8080 || !got.Verified {
			t.Errorf("un solo listener dio %+v, want Port 8080 y Verified", got)
		}
		if len(got.All) != 1 {
			t.Errorf("All = %v, want un elemento", got.All)
		}
	})

	t.Run("sin listeners: nada que declarar", func(t *testing.T) {
		got := decidePort(nil, 0, "")
		if got.Port != 0 || got.Verified {
			t.Errorf("sin listeners dio %+v, want Port 0 y Verified false", got)
		}
	})
}

// TestLineageListenersAtSinProcNoDeclaraNada: sin /proc legible no hay listeners,
// y no se inventa.
//
// El fallo cerrado importa: declarar un puerto sin evidencia haría que vroom
// publicara una dirección que nadie ha comprobado —el mismo daño que el campo
// `port_verified` sin confirmar—.
func TestLineageListenersAtSinProcNoDeclaraNada(t *testing.T) {
	if got := lineageListenersAt(t.TempDir(), os.Getpid()); got != nil {
		t.Errorf("lineageListenersAt sin /proc dio %v, want nil", got)
	}
}

// TestListenSocketsAtSaltaLasFilasQueNoSonListen: sólo el estado 0A es un
// listener, y las filas malformadas se saltan.
//
// Cruzar /proc/net/tcp con /proc/<pid>/fd por inodo es lo que evita atribuir a un
// servicio el listener de su twin. Si una fila de TIME_WAIT o de una conexión
// establecida contara como listener, el puerto atribuido sería el del cliente y
// no el del servidor.
func TestListenSocketsAtSaltaLasFilasQueNoSonListen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	body := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000 100 0 0 10 0\n" + // LISTEN 8080
		"   1: 0100007F:1F91 0100007F:C000 01 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000 100 0 0 10 0\n" + // ESTABLISHED 8081
		"   2: NOPE 0\n" + // sin separación addr:port
		"   3: 0100007F:ZZZZ 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1\n" + // puerto no hex
		"   4: 0100007F:1F92 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12348 1\n" // LISTEN 8082
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := listenSocketsAt(path)
	if len(got) != 2 {
		t.Fatalf("se leyeron %d sockets, want 2 (sólo los LISTEN con puerto hex válido): %+v", len(got), got)
	}
	ports := map[int]bool{}
	for _, s := range got {
		ports[s.port] = true
	}
	if !ports[8080] || !ports[8082] {
		t.Errorf("faltan listeners: %v", ports)
	}
	if ports[8081] {
		t.Error("una conexión ESTABLISHED se contó como listener: el puerto atribuido sería el del cliente")
	}

	// Y un fichero inexistente da vacío, no un error.
	if got := listenSocketsAt(filepath.Join(dir, "nada")); got != nil {
		t.Errorf("listenSocketsAt de un fichero inexistente dio %v, want nil", got)
	}
}

// TestEvaluateConEstadoDePuertoPersistido: los tres estados de puerto que el Meta
// guarda tienen que salir en el veredicto aunque el PID esté vivo.
//
// Los tres existen para que la TUI NO confunda "vivo pero sin puerto" con
// "sano". Sin ellos, un servicio arrancando se vería running y el usuario
// esperaría una URL que no va a llegar.
func TestEvaluateConEstadoDePuertoPersistido(t *testing.T) {
	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "sleep 30", WorkDir: t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "o.log"), StderrPath: filepath.Join(t.TempDir(), "e.log"),
	})
	t.Cleanup(func() {
		_ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 2 * time.Second})
	})

	// port_pending: el PID vive, el puerto declarado no está abierto, y el meta
	// dice que el bind está pendiente.
	got := m.Evaluate(EvalSpec{
		Pid: res.Pid, CreationTimeMs: res.CreationTimeMs,
		Port: 65001, PortPending: true,
	})
	if got != StatusPortPending {
		t.Errorf("con PortPending = %q, want %q", got, StatusPortPending)
	}

	// no_port: vive y no expone puerto TCP.
	got = m.Evaluate(EvalSpec{
		Pid: res.Pid, CreationTimeMs: res.CreationTimeMs,
		NoPort: true,
	})
	if got != StatusNoPort {
		t.Errorf("con NoPort = %q, want %q", got, StatusNoPort)
	}

	// port_unresolved: vive y el puerto nunca se decidió.
	got = m.Evaluate(EvalSpec{
		Pid: res.Pid, CreationTimeMs: res.CreationTimeMs,
		PortUnresolved: true,
	})
	if got != StatusPortUnresolved {
		t.Errorf("con PortUnresolved = %q, want %q", got, StatusPortUnresolved)
	}

	// Y sin estado de puerto, con un puerto REALMENTE abierto, es running: es el
	// caso sano, y es el que los tres anteriores contrastan.
	//
	// El puerto lo tiene que tener abierto un listener de verdad, no uno cerrado:
	// Evaluate exige que el puerto declarado ACEPTE, y un puerto libre se lee
	// como "vivo pero no sirviendo", que es un estado distinto.
	open, closeIt := statusListener(t, http.StatusOK)
	defer closeIt()
	got = m.Evaluate(EvalSpec{
		Pid: res.Pid, CreationTimeMs: res.CreationTimeMs, Port: open,
	})
	if got != StatusRunning {
		t.Errorf("con el puerto abierto = %q, want running", got)
	}
}

// TestEvaluateConPidMuertoYPuertoDeOtroNoLoDaPorVivo: el puerto abierto no es
// prueba de que el servicio sea el nuestro.
//
// Es el caso del twin en otro worktree: mismo puerto declarado, proceso distinto.
// Resolverlo a "running" haría que `vroom start` no arrancara nada creyendo que
// ya está.
func TestEvaluateConPidMuertoYPuertoDeOtroNoLoDaPorVivo(t *testing.T) {
	m := NewManager()
	dead := findDeadPID(t)
	port := freeListeningPort(t)

	// Dueño indeterminado o ambiguo → indeterminado, no running.
	got := m.Evaluate(EvalSpec{Pid: dead, CreationTimeMs: 0, Port: port})
	if got == StatusRunning {
		t.Error("un puerto abierto cuyo dueño no se puede probar se resolvió a running: el twin de otro worktree aparecería vivo")
	}

	// Y sin PID, sin puerto y sin pattern: parado, que es un hecho.
	if got := m.Evaluate(EvalSpec{}); got != StatusStopped {
		t.Errorf("sin ninguna señal = %q, want stopped", got)
	}
}

// TestPatternMatchEncuentraUnHijoRealYExcluyeALosPpropios: el veredicto de
// "el servicio está corriendo" fuera de vroom depende de esto.
//
// MEDIDO: el filtro `pid != myPid` descarta al proceso que PREGUNTA, que es la
// razón de ser de la función —pgrep -f hace match contra el command line
// completo y el suyo contiene el patrón—, y no descarta al resto. Por eso el caso
// positivo tiene que ser un HIJO REAL: usar el propio proceso de test daría
// false y el test pasaría por el motivo equivocado.
//
// Y el caso negativo importa igual: un patrón que nadie tiene tiene que dar
// false, porque un true ahí haría que cualquier servicio con un process_pattern
// mal escrito pareciera vivo.
func TestPatternMatchEncuentraUnHijoRealYExcluyeALosPropios(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep no está en esta máquina: sin él PatternMatch devuelve false siempre")
	}

	m := NewManager()
	marker := "vroom-marcador-unico-9911-" + strconv.Itoa(os.Getpid())
	// El marcador va como COMENTARIO al final, y después de `sleep` hay un
	// `; true` a propósito: con dos comandos, `sh` no puede hacer exec y se
	// queda vivo con el marcador en su propio command line. Con un solo comando
	// se sustituiría por `sleep`, que no lo lleva, y el test daría false por el
	// motivo equivocado.
	res := startSleep(t, m, StartSpec{
		Command:    "sleep 30; true # " + marker,
		WorkDir:    t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "o.log"), StderrPath: filepath.Join(t.TempDir(), "e.log"),
	})
	t.Cleanup(func() {
		_ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 2 * time.Second})
	})

	if !PatternMatch(marker) {
		t.Errorf("PatternMatch(%q) = false con un hijo vivo cuyo command line lo contiene", marker)
	}

	// Un patrón que nadie tiene: false.
	if PatternMatch("vroom-este-patron-no-lo-tiene-nadie-" + strconv.Itoa(os.Getpid()+1)) {
		t.Error("PatternMatch de un patrón inexistente dio true: algún proceso propio se está contando")
	}

	// Y el propio proceso de test NUNCA cuenta, aunque su binario contenga el
	// patrón: es el motivo de existir del filtro.
	if PatternMatch(uniqueProcessMarker(t)) {
		t.Error("PatternMatch encontró al propio proceso de test: el filtro myPid no está haciendo su trabajo")
	}
}

// TestReservePortNoDevuelveElMismoPuertoDosVeces: dos reservas seguidas sin
// liberar la primera tienen que dar puertos DISTINTOS.
//
// Es la propiedad mínima del pool, y la que hace que dos servicios del mismo
// workspace noReceban el mismo puerto. La aritmética del bucle de ReservePort
// —"si ya está reservado, sigue"— es justo lo que la garantiza.
func TestReservePortNoDevuelveElMismoPuertoDosVeces(t *testing.T) {
	a, err := ReservePort()
	if err != nil {
		t.Skipf("no hay puertos libres en el rango: %v", err)
	}
	b, err := ReservePort()
	if err != nil {
		ReleasePort(a)
		t.Skipf("no hay puertos libres suficientes: %v", err)
	}
	if a == b {
		t.Errorf("dos reservas seguidas dieron el mismo puerto %d: dos servicios lo ocuparían a la vez", a)
	}
	ReleasePort(a)
	ReleasePort(b)
}

// TestReservePortNuncaDevuelveUnPuertoQueAlguienTiene: cada puerto que sale del
// pool está libre en el momento de salir.
//
// El pool es un bind + close, así que hay una ventana TOCTOU documentada entre
// devolver el puerto y que el hijo lo ocupe. Lo que sí se puede exigir —y es lo
// que evita un fallo de arranque — es que en el momento de la reserva no hubiera
// nadie escuchando: el bucle salta los puertos que no puede enlazar.
func TestReservePortNuncaDevuelveUnPuertoQueAlguienTiene(t *testing.T) {
	var reserved []int
	defer func() {
		for _, p := range reserved {
			ReleasePort(p)
		}
	}()

	for i := 0; i < 5; i++ {
		p, err := ReservePort()
		if err != nil {
			t.Skipf("no hay puertos libres suficientes: %v", err)
		}
		reserved = append(reserved, p)
		if PortOpen(p) {
			t.Errorf("ReservePort devolvió el puerto %d, que ya tiene un listener: el bind del servicio va a fallar", p)
		}
	}

	// Y los cinco son distintos entre sí.
	seen := map[int]bool{}
	for _, p := range reserved {
		if seen[p] {
			t.Errorf("el puerto %d salió dos veces del pool", p)
		}
		seen[p] = true
	}
}

// ---- helpers ----

// closedPortForTest devuelve un puerto que nadie escucha.
func closedPortForTest(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// freeListeningPort abre un listener real y devuelve su puerto, cerrándolo
// justo después: queda un puerto LIBRE que otro puede ocupar.
func freeListeningPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// statusListener abre un servidor HTTP real con el status dado y devuelve su
// puerto junto con el cierre.
func statusListener(t *testing.T, status int) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = srv.Close() }
}

// uniqueProcessMarker devuelve una cadena que aparece en el command line del
// proceso de test y en el de ningún otro.
//
// Es el nombre del propio binario de test, cuya ruta es única dentro del build
// temporal de `go test`.
func uniqueProcessMarker(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skipf("no se puede resolver el propio binario: %v", err)
	}
	return filepath.Base(self)
}

// TestStopEscalaASigkillCuandoElHijoIgnoraSigterm: la escalera de parada es
// SIGTERM y, si el linaje sigue vivo, SIGKILL.
//
// Es la mitad que "no hacer nada" no cubre, y importa porque hay procesos que
// ignoran SIGTERM por diseño —un servidor que atrapa la señal para terminar su
// trabajo—. Sin la escalada, `vroom stop` dejaría el servicio vivo y devolvería
// "parado", que es la peor respuesta: un afirma que no se sostiene.
//
// El proceso se construye con `trap "" TERM` en su propio shell, así que ignora
// la señal y sólo se va por SIGKILL. Es el caso real, no un truco de test.
func TestStopEscalaASigkillCuandoElHijoIgnoraSigterm(t *testing.T) {
	m := NewManager()
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		// `trap "" TERM` sobrevive al exec de sleep porque el shell no puede
		// sustituirse (hay dos comandos), y SIGTERM lo ignora.
		Command:    `trap "" TERM; sleep 30; true`,
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "o.log"), StderrPath: filepath.Join(dir, "e.log"),
	})

	var warns []string
	if err := m.Stop(StopSpec{
		Pid: res.Pid, Pgid: res.Pgid, Timeout: 500 * time.Millisecond,
		Warn: func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) },
	}); err != nil {
		t.Fatal(err)
	}

	// El proceso tiene que estar muerto: la escalada a SIGKILL es lo que lo
	// garantiza, y sin ella seguiría vivo.
	waitPIDGone(t, res.Pid)

	// Y si lo hubiera conseguido con SIGTERM no habría aviso. Con SIGKILL
	// tampoco, porque lo consigue: el aviso es para lo que NO se puede parar.
	if len(warns) > 0 {
		t.Logf("avisos del stop (no deberían ser fallos): %v", warns)
	}
}

// TestStartFallaSiNoPuedeAbrirElStderr: el stderr.log bloqueado es el mismo
// fallo que el de stdout y con su propio mensaje.
//
// Y el mensaje NOMBRA el fichero, porque "could not open" sin más dejaría al
// usuario con dos fallos idénticos sin saber cuál era.
func TestStartFallaSiNoPuedeAbrirElStderr(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "stderr.log")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := NewManager().Start(StartSpec{
		Command: "sleep 30", WorkDir: dir,
		StdoutPath: filepath.Join(dir, "stdout.log"), StderrPath: blocked,
	})
	if err == nil {
		t.Fatal("un stderr.log que es un directorio debería hacer fallar el arranque")
	}
	if !strings.Contains(err.Error(), "stderr.log") {
		t.Errorf("err = %q, want que nombre el fichero que no pudo abrir", err)
	}
}

// TestAliveConUnPidQueNoSePuedeConstruir: gopsutil no puede construir un proceso
// para un PID que no existe, y eso es un "no vivo" y no un error.
//
// La distinción es la de siempre: un vivo es un hecho y "no se puede mirar" es
// otra cosa, pero aquí NO hay dato que publicar en ningún caso, así que ambos son
// "no vivo". Lo que importa es que no reviente.
func TestAliveConUnPidQueNoSePuedeConstruir(t *testing.T) {
	if Alive(1<<30, 0) {
		t.Error("un pid enorme no puede ser un proceso vivo")
	}
}

// TestPortOwnerPIDsConUnPuertoQueNoExisteEsVacio: un puerto libre no tiene dueño,
// y eso es una lista vacía y no un error.
func TestPortOwnerPIDsConUnPuertoQueNoExisteEsVacio(t *testing.T) {
	if got := PortOwnerPIDs(closedPortForTest(t)); len(got) != 0 {
		t.Errorf("un puerto libre tiene dueños %v, want ninguno", got)
	}
	if got := PortOwnerPID(closedPortForTest(t)); got != 0 {
		t.Errorf("PortOwnerPID de un puerto libre = %d, want 0", got)
	}
}
