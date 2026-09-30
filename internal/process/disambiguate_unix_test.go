//go:build unix

package process

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// ---- R1: el puerto reservado es el principal ----

// La app honra PORT y además abre un listener de metrics ANTES que el
// principal. R1 gana sin heurística: si la app honra PORT, no hay nada que
// adivinar aunque abra listeners por etapas.
func TestR1ReservedPortWinsOverEarlierMetricsPort(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	reserved := freePortForHelper(t)
	metrics := freePortForHelper(t)
	t.Setenv("VROOM_TEST_HELPER", "two-ports")
	t.Setenv("VROOM_TEST_PORT", strconv.Itoa(reserved))
	t.Setenv("VROOM_TEST_PORT2", strconv.Itoa(metrics))

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    testBinary(t) + " -test.run=^TestHelperListener$ & sleep 300",
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	// El metrics abre primero; el principal va detrás. Aceptar la primera
	// muestra elegiría el listener equivocado.
	d := DiscoverPort(res.Pid, reserved, "/health", 8*time.Second)
	if d.Port != reserved {
		t.Errorf("R1 debe elegir el puerto reservado %d, eligió %d (listeners=%v)", reserved, d.Port, d.All)
	}
	if !d.Verified {
		t.Error("R1 es determinista: el puerto está verificado")
	}
	if !d.HonoredReserved {
		t.Error("R1 significa que la app tomó el puerto ofrecido")
	}
}

// Un único listener es trivialmente el principal, y verificado.
func TestR1SingleListenerIsTrivial(t *testing.T) {
	only, release := listenerOn(t, statusHandler(200))
	defer release()

	d := decidePort([]int{only}, 0, "/health")
	if d.Port != only {
		t.Errorf("con un solo listener debe ganar ese, got %d", d.Port)
	}
	if !d.Verified {
		t.Error("un único listener está verificado por construcción")
	}
}

// ---- R2: la app ignora PORT y health_path decide ----

// Dos listeners, sólo uno responde bien en health_path.
func TestR2HealthPathDecides(t *testing.T) {
	good, releaseGood := listenerOn(t, statusHandler(200))
	defer releaseGood()
	bad, releaseBad := listenerOn(t, statusHandler(404))
	defer releaseBad()

	d := decidePort([]int{bad, good}, 0, "/health")

	if d.Port != good {
		t.Errorf("R2 debe elegir el que mejor responde (%d), eligió %d", good, d.Port)
	}
	if !d.Verified {
		t.Error("R2 verifica el puerto: alguien respondió")
	}
}

// El ranking declarado es 200 > 2xx/3xx > 5xx > 404, y el resto no gana.
func TestR2HealthRanking(t *testing.T) {
	if healthRank(200) <= healthRank(301) {
		t.Error("200 debe ganar al 3xx")
	}
	if healthRank(301) <= healthRank(503) {
		t.Error("3xx debe ganar al 5xx")
	}
	if healthRank(503) <= healthRank(404) {
		t.Error("5xx debe ganar al 404")
	}
	if healthRank(404) <= healthRank(0) {
		t.Error("el 404 debe ganar a quien no respondió")
	}
	if healthRank(0) != 0 {
		t.Error("sin respuesta HTTP no hay rank")
	}
}

// Un 5xx gana a un 404: el servidor está vivo aunque la ruta no sea la buena.
func TestR2ServerErrorBeatsNotFound(t *testing.T) {
	err5xx, releaseA := listenerOn(t, statusHandler(503))
	defer releaseA()
	notFound, releaseB := listenerOn(t, statusHandler(404))
	defer releaseB()

	d := decidePort([]int{notFound, err5xx}, 0, "/health")
	if d.Port != err5xx {
		t.Errorf("el 5xx debe ganar al 404: eligió %d, esperaba %d", d.Port, err5xx)
	}
}

// ---- R3: empate o protocolo no-HTTP ----

// Empieje: ambos responden 200 en health_path. Gana el menor, siempre.
func TestR3TiePicksLowestPort(t *testing.T) {
	a, releaseA := listenerOn(t, statusHandler(200))
	defer releaseA()
	b, releaseB := listenerOn(t, statusHandler(200))
	defer releaseB()

	for i := 0; i < 3; i++ { // determinista entre corridas
		d := decidePort([]int{minPort(a, b), maxPort(a, b)}, 0, "/health")
		if d.Port != minPort(a, b) {
			t.Errorf("el empate debe ganar el menor puerto %d, eligió %d", minPort(a, b), d.Port)
		}
		if !d.Verified {
			t.Error("R2 con respuesta válida verifica el puerto")
		}
	}
}

// Protocolo no-HTTP: sockets crudos que no responden como HTTP. Nadie gana,
// se aplica R3 y se declara que no se puede saber.
func TestR3NonHTTPFallsBackToLowestAndDeclaresUnverified(t *testing.T) {
	rawA, releaseA := rawListenerOn(t)
	defer releaseA()
	rawB, releaseB := rawListenerOn(t)
	defer releaseB()

	d := decidePort([]int{minPort(rawA, rawB), maxPort(rawA, rawB)}, 0, "/health")

	if d.Port != minPort(rawA, rawB) {
		t.Errorf("R3 debe elegir el menor puerto %d, eligió %d", minPort(rawA, rawB), d.Port)
	}
	if d.Verified {
		t.Error("sin respuesta HTTP el puerto NO está verificado: eso hay que declararlo")
	}
}

// La enumeración de listeners no depende del número de listeners para terminar: el
// trabajo es acotado por el tamaño de /proc, no por lo que la app abra.
func TestListenerEnumerationIsBoundedByProcNotByApp(t *testing.T) {
	requireProc(t)

	start := time.Now()
	for i := 0; i < 5; i++ {
		_ = lineageListenersAt(procRoot, os.Getpid())
	}
	elapsed := time.Since(start)

	// 5 enumeraciones completas del árbol de procesos de esta máquina.
	// Si el coste escalara con los listeners de la app, esto no lo ocultaría.
	if elapsed > 2*time.Second {
		t.Errorf("enumerar listeners tardó %s: demasiado para el arranque", elapsed)
	}
}

// ---- helpers ----

func listenerOn(t *testing.T, h http.Handler) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = http.Serve(ln, h) }()
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

// statusHandler responde con un código fijo, sin ruta: todos los health_path
// dan la misma respuesta, que es justo el caso "no distingo por ruta".
func statusHandler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	})
}

// rawListenerOn abre un socket que acepta y no responde: simula un
// protocolo no-HTTP (gRPC-like, base de datos, etc.).
func rawListenerOn(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close() // acepta y cuelga: no es HTTP
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

func minPort(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxPort(a, b int) int {
	if a > b {
		return a
	}
	return b
}
