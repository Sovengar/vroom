//go:build unix

package process

import (
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// La reserva cae dentro de la banda dynamic y devuelve un puerto usable.
func TestReservePortInRange(t *testing.T) {
	for i := 0; i < 5; i++ {
		port, err := ReservePort()
		if err != nil {
			t.Fatalf("ReservePort: %v", err)
		}
		if port < DynamicPortLow || port > DynamicPortHigh {
			t.Fatalf("puerto %d fuera del rango %d-%d", port, DynamicPortLow, DynamicPortHigh)
		}
	}
}

// cmd.Env no-nil reemplaza os.Environ() por completo: fusionar es
// obligatorio, no cosmético.
func TestMergeEnvKeepsParent(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "HOME=/home/u", "PORT=8080"}

	merged := mergeEnv(parent, []string{"PORT=41501", "HOST=127.0.0.1"})

	want := map[string]string{
		"PATH": "/usr/bin", // el padre sobrevive
		"HOME": "/home/u",
		"PORT": "41501", // el spec pisa
		"HOST": "127.0.0.1",
	}
	got := map[string]string{}
	for _, kv := range merged {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (merged=%v)", k, got[k], v, merged)
		}
	}
	if len(merged) != len(want) {
		t.Errorf("merged = %v, want %d entradas sin duplicados", merged, len(want))
	}
}

// Sin nada que inyectar se devuelve nil, para que exec aplique la herencia
// normal en vez de una copia congelada del entorno.
func TestMergeEnvNilWhenNothingToInject(t *testing.T) {
	if got := mergeEnv([]string{"PATH=/usr/bin"}, nil); got != nil {
		t.Errorf("sin spec.Env debe quedar nil (herencia), got %v", got)
	}
}

// Un listener ajeno NO se atribuye al linaje del servicio: se cruza el
// inodo del socket con los fd del linaje.
func TestLineageListenersExcludesForeignSockets(t *testing.T) {
	requireProc(t)

	// El proceso de test es el dueño de este listener.
	port := freePortForHelper(t)
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	// Un linaje ajeno (sleep) no puede tener listeners: no debe aparecer.
	res := startSleep(t, newTestManager(t), StartSpec{
		Command:    "sleep 60",
		WorkDir:    t.TempDir(),
		StdoutPath: t.TempDir() + "/out.log",
		StderrPath: t.TempDir() + "/err.log",
	})
	t.Cleanup(func() { _ = newTestManager(t).Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	if got := lineageListenersAt(procRoot, res.Pid); len(got) != 0 {
		t.Errorf("un sleep no tiene listeners, got %v", got)
	}
	if got := lineageListenersAt(procRoot, os.Getpid()); !containsPid(got, port) {
		t.Errorf("el listener %d del proceso de test debe aparecer en su linaje, got %v", port, got)
	}
}

// Un helper que sí escucha aparece en su propio linaje y en el de su padre
// (es descendiente suyo), pero no en el de un árbol ajeno.
func TestDiscoverPortFindsListenerInLineage(t *testing.T) {
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
		Command:    testBinary(t) + " -test.run=^TestHelperListener$ & sleep 60",
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	waitPortOpen(t, port, 5*time.Second)

	d := DiscoverPort(res.Pid, port, "/health", 3*time.Second)
	if d.Port != port {
		t.Errorf("DiscoverPort = %+v, want el puerto %d del listener", d, port)
	}
	if !d.HonoredReserved {
		t.Error("un listener igual al reservado es R1: determinista y sin heurística")
	}
}

// Un linaje muerto se reporta de inmediato en vez de agotar el timeout:
// arrancar un servicio que muere no debe costar segundos.
func TestDiscoverPortFailsFastOnDeadLineage(t *testing.T) {
	requireProc(t)

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "true", // muere inmediatamente
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})

	start := time.Now()
	d := DiscoverPort(res.Pid, 0, "/health", 30*time.Second)
	elapsed := time.Since(start)

	if !d.LineageDead {
		t.Errorf("un linaje muerto debe abortar el discovery: %+v", d)
	}
	if elapsed > 3*time.Second {
		t.Errorf("el fallo rápido debe ser del orden de 1s, tardó %s", elapsed)
	}
}

// Un linaje vivo que nunca abre puerto TCP termina acotado y sin puertos:
// "sin puerto" es un estado, no un cuelgue.
func TestDiscoverPortNoPortIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "sleep 60",
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	start := time.Now()
	d := DiscoverPort(res.Pid, 0, "/health", 1*time.Second)
	elapsed := time.Since(start)

	if d.Port != 0 || len(d.All) != 0 {
		t.Errorf("sin listeners: %+v", d)
	}
	if d.LineageDead {
		t.Error("el linaje está vivo: no es un fallo de arranque")
	}
	if d.Unresolved {
		t.Error("sin un solo listener en toda la ventana no es 'sin resolver', es 'sin puerto'")
	}
	// El presupuesto es plazo + gracia: la gracia existe porque el
	// vencimiento del plazo no prueba ausencia. Acotado sigue siendo
	// acotado, que es lo que importa.
	budget := 1*time.Second + DefaultDynamicUnresolvedGrace
	if elapsed > budget+3*time.Second {
		t.Errorf("el discovery debe respetar plazo+gracia (%s): tardó %s", budget, elapsed)
	}
}

// socketInode sólo acepta enlaces de socket reales.
func TestSocketInode(t *testing.T) {
	if ino, ok := socketInode("socket:[4242]"); !ok || ino != "4242" {
		t.Errorf("socketInode = %q, %v", ino, ok)
	}
	if _, ok := socketInode("/dev/null"); ok {
		t.Error("un fd que no es socket debe rechazarse")
	}
	if _, ok := socketInode("pipe:[3]"); ok {
		t.Error("una pipe no es un socket de red")
	}
}

// H1: reservas concurrentes en un mismo proceso nunca devuelven el mismo
// puerto. Antes del set en memoria, todas las goroutines entraban por el
// primer hueco libre del rango y salían con el mismo número.
//
// Aserta sobre len(reservedPorts), no sobre que "una reserva posterior tenga
// éxito": eso no distingue "no hubo colisión" de "se agotó el rango", que es
// justo el fallo que un test honesto tiene que cazar.
func TestReservePortIsConcurrencySafe(t *testing.T) {
	const workers = 400

	baseline := ReservedPortCount()

	type result struct {
		port int
		err  error
	}
	results := make([]result, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			port, err := ReservePort()
			results[i] = result{port: port, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	seen := make(map[int]int, workers)
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("reserva %d falló: %v", i, r.err)
		}
		if first, dup := seen[r.port]; dup {
			t.Fatalf("reservas %d y %d devolvieron el puerto %d", first, i, r.port)
		}
		seen[r.port] = i
	}
	if got := ReservedPortCount() - baseline; got != workers {
		t.Fatalf("el set creció en %d, want %d: una reserva o liberada o duplicada",
			got, workers)
	}

	// Devolverlas todas deja el set como estaba. Este es el assert que
	// importa de verdad: el set tiene que ser devuelto, no solo escrito.
	ports := make([]int, 0, workers)
	for port := range seen {
		ports = append(ports, port)
	}
	for _, port := range ports {
		ReleasePort(port)
	}
	if got := ReservedPortCount(); got != baseline {
		t.Errorf("tras devolver %d puertos el set mide %d, want %d", len(ports), got, baseline)
	}
}

// M-B: el set tiene que devolver lo que se le entrega. La versión anterior
// de este test no tenía aserto en el cuerpo del bucle y pasaba aunque
// ReleasePort fuese un no-op.
func TestReleasePortShrinksTheSet(t *testing.T) {
	baseline := ReservedPortCount()

	port, err := ReservePort()
	if err != nil {
		t.Fatalf("ReservePort: %v", err)
	}
	if got := ReservedPortCount(); got != baseline+1 {
		t.Fatalf("tras reservar el set mide %d, want %d", got, baseline+1)
	}

	ReleasePort(port)

	if got := ReservedPortCount(); got != baseline {
		t.Errorf("tras liberar el set mide %d, want %d: la reserva no volvió",
			got, baseline)
	}
}

// Liberar un puerto que nunca se reservó no debe tocar el set.
func TestReleasePortIgnoresUnreservedAndNonPositive(t *testing.T) {
	baseline := ReservedPortCount()

	ReleasePort(0)
	ReleasePort(-1)
	ReleasePort(DynamicPortHigh + 1)

	if got := ReservedPortCount(); got != baseline {
		t.Errorf("liberar puertos sin reservar cambió el set: %d != %d", got, baseline)
	}
}
