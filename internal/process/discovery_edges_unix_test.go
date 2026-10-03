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

// ---------------------------------------------------------------------------
// El descubrimiento del puerto en sus tres desenlaces, y las degradaciones que
// sólo se ven cuando /proc no responde como se espera.
//
// Los tres desenlaces son los que el llamador de `DiscoverPort` tiene que saber
// distinguir, y cada uno tiene un camino de salida distinto:
//
//   - R1: el puerto reservado está escuchando. Determinista, sin heurística.
//   - Estabilización: hay listeners y el conjunto deja de crecer. Se acepta.
//   - Sin decidir: hay listeners, el conjunto no se estabiliza y el plazo vence.
//
// El segundo y el tercero son LA MISMA condición con distinta respuesta, y esa
// diferencia es el contrato: "todavía no puedo saberlo" no es "no tiene puerto".
// Por eso este archivo insiste en el tercero, que es el que se confunde con el
// cuarto veredicto (no expone puerto) y hace que la UI diga que un servicio está
// sano cuando en realidad nadie sabe qué puerto escucha.
// ---------------------------------------------------------------------------

// TestDiscoverPortEsperaAQueElConjuntoDeListenersSeEstabilice: la ventana de
// estabilización, cuando el listener NO es el puerto reservado.
//
// Es el caso que el resto de la suite no toca: los tests que ya existían pasaban
// el puerto reservado como `reserved`, así que salían por R1 —que devuelve sin
// esperar— y nunca llegaban a la cuenta atrás de `discoverSettle`.
//
// Y esa cuenta atrás es el comportamiento que importa: una app que abre listeners
// por etapas (metrics en una goroutine, main en otra) tiene un momento en el que
// el primer listener ya está accepting y el principal todavía no. Aceptar la
// primera muestra elige el de metrics, y vroom publicaría una URL que devuelve
// health checks del endpoint equivocado.
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
	// reserved = 0 a propósito: sin atajo R1, el único camino es estabilizar.
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
	// Un solo listener estabilizado es un hecho, no una suposición: por eso sale
	// `Verified`, que es lo que la UI usa para decir que la ruta funciona.
	if !d.Verified {
		t.Error("Verified en false con un solo listener estabilizado: la UI no puede afirmar que " +
			"la ruta funciona y el servicio aparece sin URL")
	}
	// Y tuvo que ESPERAR: la ventana de estabilización es el comportamiento, no un
	// detalle de temporización. Sin esta comprobación, un discovery que aceptase la
	// primera muestra pasaría el test anterior.
	if esperado < discoverSettle {
		t.Errorf("decidió en %s, want al menos %s: aceptó la primera muestra sin esperar a que el "+
			"conjunto dejara de crecer, que es exactamente el bug que la ventana previene",
			esperado, discoverSettle)
	}
	if d.Unresolved {
		t.Error("Unresolved con un único listener que no cambia: no hay nada que no se pueda decidir")
	}
}

// TestDiscoverPortNoDecideUnConjuntoQueNoSeEstabiliza: los listeners que no paran
// de aparecer.
//
// El helper abre y cierra un listener efímero cada 80 ms, así que el conjunto que
// ve el discovery cambia en cada muestra y nunca lleva `discoverSettle` quieto.
// Con el plazo vencido y listeners presentes, la respuesta tiene que ser "sin
// decidir", no "no tiene puerto".
//
// MEDIDO: la diferencia importa porque las dos se_reportan al usuario de forma
// distinta. `Unresolved` produce `port_unresolved`, que la TUI pinta como
// "arrancándose" y `vroom list --json` declara explícitamente; un `DiscoveryResult{}`
// vacío produciría `no_port`, que afirma una cosa que no se ha comprobado: que el
// servicio no expone NINGÚN puerto TCP.
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

	// Plazo corto a propósito: no hace falta agotarlo, sólo llegar a él con
	// listeners delante.
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
	// Y no es un veredicto de muerte: el linaje sigue vivo.
	if d.LineageDead {
		t.Error("LineageDead con el helper corriendo: sin deciding el puerto, el proceso sigue ahí")
	}
}

// TestDescendantsDeUnProcRootInexistenteNoDevuelveNada: la degradación del
// snapshot.
//
// `descendantsAt` es lo que hace que el Stop por linaje mate a los nietos en vez
// de sólo al hijo. Si /proc no se puede leer, la respuesta tiene que ser "no sé de
// quién es", que degrada a matar sólo la raíz, y no "no tiene descendientes" con
// la misma consequence… que es exactamente lo mismo aquí, pero la diferencia está
// en que el código tiene que distinguish la ausencia de conocimiento de la ausencia
// de hijos para no tratar el caso bueno por accidente.
func TestDescendantsDeUnProcRootInexistenteNoDevuelveNada(t *testing.T) {
	if got := descendantsAt("/proc/definitely-not-here", os.Getpid()); len(got) != 0 {
		t.Errorf("descendantsAt con un /proc inexistente devolvió %v, want nada", got)
	}
	// Y lo mismo para el camino que cruza sockets con el linaje.
	if got := lineageListenersAt("/proc/definitely-not-here", os.Getpid()); got != nil {
		t.Errorf("lineageListenersAt con un /proc inexistente devolvió %v, want nil", got)
	}
}

// TestDescendantsFromTerminaAnteUnCicloEnElArbol: la guarda de visitados.
//
// MEDIDO, y es lo que costó ver: en un /proc real la guarda NO se puede alcanzar por
// duplicados. Cada proceso aparece una vez en el snapshot y con un único `ppid`, así
// que `children[ppid]` no puede listar dos veces al mismo pid. Mi primera versión de
// este test montaba un "rombo" —4 hijo de 2 y de 3— y no era un rombo: con un `ppid`
// por proceso eso es imposible, y el `continue` no se ejecutaba nunca.
//
// Lo único que puede activarla es un CICLO: un pid cuyo padre es su propio
// descendiente. El kernel no lo produce, pero la función es un BFS sobre un mapa que
// le pasa quien llama, y un BFS sin guarda de visitados sobre un ciclo NO TERMINA.
//
// Y el fallo no es un return raro: `descendantsAt` es lo que `Stop` usa para decidir a
// quién matar, así que un bucle infinito ahí deja a vroom colgado con el servicio
// intacto y sin devolver el control. Un hang es el peor resultado posible de una
// función que sólo tiene que devolver una lista.
func TestDescendantsFromTerminaAnteUnCicloEnElArbol(t *testing.T) {
	// 1 → 2 → 3 → 1. La raíz reaparece como hija de su propio descendiente.
	snap := map[int]procInfo{
		1: {pid: 1, ppid: 3},
		2: {pid: 2, ppid: 1},
		3: {pid: 3, ppid: 3}, // padre de sí mismo: se salta al construir `children`
		4: {pid: 4, ppid: 2},
	}

	// Si la guarda no estuviera, esto no devolvería nunca y el test colgaría el
	// binario de test, que es la forma más clara de que el hang es real.
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

// TestDescendantsFromIgnoraUnProcesoQueEsSuPropioPadre: el otro filtro del BFS.
//
// El `continue` de la construcción de `children` existe por un caso que sí ocurre:
// `/proc` guarda el ppid del proceso que ya se está muriendo, y hay ventanas en las que
// un reaping va rápido. Más relevante: el pid 1 tiene ppid 0 y el kernel no garantiza
// que un pid zombie conserve su padre.
//
// El efecto de colgarlo de sí mismo es que `children[p]` incluye a `p`, y el BFS se
// visiting a sí mismo en cada vuelta. Con la guarda de visitados no cuelga, pero el
// proceso aparecería como descendiente de sí mismo en el Stop.
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

// TestParseThreadStatConUnStimeNoNumericoEsUnStatInvalido: el campo 15 roto.
//
// utime y stime se parsean por separado y con el mismo criterio, pero un stat real
// puede tener uno bueno y el otro basura: basta con un `/proc` de una versión
// distinta, o un fichero escrito a mano. Lo que no puede pasar es devolver un
// recuento de ticks con un campo del medio sin leer, porque el resultado se usa
// para calcular el % de CPU y un 0 silencioso hace que un hilo saturado parezca
// inactivo.
func TestParseThreadStatConUnStimeNoNumericoEsUnStatInvalido(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stat")

	// 13 campos después del ')': state + 12 más. utime en rest[11] bien, stime en
	// rest[12] con basura.
	escribirStat(t, path, "R 0 0 0 0 0 0 0 0 0 0 1234 no-es-un-número")
	if _, _, err := parseThreadStat(path); err == nil {
		t.Fatal("un stime no numérico tiene que ser un stat inválido, no ticks 1234")
	}

	// Y el caso bueno, para que la prueba sea del parsing y no del rechazo.
	escribirStat(t, path, "R 0 0 0 0 0 0 0 0 0 0 100 200")
	state, ticks, err := parseThreadStat(path)
	if err != nil {
		t.Fatalf("un stat válido dio error: %v", err)
	}
	if state != "R" {
		t.Errorf("state = %q, want R", state)
	}
	// ticks es utime + stime, no uno de los dos: el porcentaje de CPU de un hilo
	// cuenta el tiempo en sistema igual que en usuario.
	if ticks != 300 {
		t.Errorf("ticks = %d, want 300 (utime 100 + stime 200)", ticks)
	}
}

// escribirStat escribe un /proc/<tid>/stat sintético: pid, comm entre paréntesis y
// los campos a partir del estado.
func escribirStat(t *testing.T, path, rest string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("42 (un nombre con (paréntesis) "+rest+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// `Evaluate` cuando el PID del meta está muerto y hay que decidir por señales
// externas.
//
// Este es el caso que hace que vroom no se equivoque con un twin: un servicio
// reiniciado FUERA de vroom deja un meta con el PID viejo, que ya no existe, pero
// con el puerto abierto. Decir "running" porque el puerto responde sería
// afirmar la salud de quien lo haya abierto, que puede ser otro servicio con el
// mismo número de puerto.
// ---------------------------------------------------------------------------

// TestEvaluateConUnPuertoDePropietarioAmbiguoDegradaAIndeterminado: dos dueños,
// ninguna prueba.
//
// El mismo número de puerto escuchando en IPv4 y en IPv6 da DOS entradas en
// /proc/net/tcp{,6} para un solo proceso. `PortOwnerPID` no puede deducir cuál de
// las dos es la del servicio, así que devuelve 0 y el veredicto tiene que ser
// indeterminado.
//
// Y el orden importa: un `running` aquí haría que vroom dijera que el servicio
// está sano cuando lo único que sabe es que ALGO escucha en ese número, que
// puede ser el servicio de otro worktree.
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

	// MEDIDO: las dos entradas son del MISMO proceso, y aun así hay dos. La
	// ambigüedad no necesita dos procesos: basta con que el mismo escuche en las
	// dos familias. Por eso `PortOwnerPID` devuelve 0 con `len(owners) == 2`.
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

// TestEvaluateConElPuertoDeUnProcesoVivoLoDaPorRunningConPruebaDePropiedad: el
// caso bueno del mismo camino.
//
// Cuando hay UN solo dueño y su creation time es la que dice el meta, sí hay
// prueba, y el veredicto es `running`. La comparación de creation time es lo que
// distingue este caso del anterior: mismo puerto abierto, pero aquí se sabe de
// quién es.
//
// El listener se abre en el proceso de test a propósito: así el creation time que
// hay que comparar es el del propio binario de test, que se puede leer del mismo
// sitio que lee producción.
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

	// Y el contraste que da sentido a la comparación: el MISMO puerto con un
	// creation time que no es el suyo es de otro proceso que ocupó el número, así
	// que tiene que ser stopped y no running.
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

// TestReservePortSaltaLosPuertosQueElSistemaYaTieneOcupados: el `continue` del
// pool.
//
// El set en memoria y el sistema son dos fuentes de verdad distintas: algo que no
// es vroom —otra instancia, un servicio del sistema, un puerto efímero del kernel
// en su rango— puede tener tomado el puerto que el set cree libre. La respuesta
// correcta es pasar al siguiente del rango, no fallar: el pool tiene mil puertos y
// el rango es del kernel, no nuestro.
func TestReservePortSaltaLosPuertosQueElSistemaYaTieneOcupados(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: socket real")
	}

	// Se ocupa el PRIMER puerto del rango, que es exactamente por donde empieza
	// el bucle, así que la primera iteración tiene que fallar y continuar.
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
