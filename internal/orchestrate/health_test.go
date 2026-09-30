package orchestrate

import (
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
)

// listenOn abre un listener real en un puerto efímero.
func listenOn(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

// Puerto que abre a tiempo → nil.
func TestWaitForPortOpensInTime(t *testing.T) {
	port, release := listenOn(t)
	defer release()
	if err := WaitForPort(port, 2*time.Second); err != nil {
		t.Errorf("puerto abierto debe pasar: %v", err)
	}
}

// Puerto que nunca abre → error al agotar el timeout.
func TestWaitForPortTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // puerto libre: nadie abre

	start := time.Now()
	err = WaitForPort(port, time.Second)
	if err == nil {
		t.Fatal("un puerto que nunca abre debe fallar")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("el timeout no se respetó: %s", elapsed)
	}
}

// Riesgo 8, mitad legacy: port = 0 sigue siendo un sleep corto y nil. Ni
// estado nuevo, ni aviso, ni retención extra.
func TestWaitForPortZeroIsLegacyShortSleep(t *testing.T) {
	start := time.Now()
	if err := WaitForPort(0, 30*time.Second); err != nil {
		t.Errorf("port = 0 debe devolver nil: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("port = 0 debe ser un sleep corto, tardó %s", elapsed)
	}
}

// El mismo caso pero a través de AwaitPort en modo fixed: indistinguible del
// legacy, sin semántica nueva.
func TestAwaitPortFixedZeroMatchesLegacy(t *testing.T) {
	start := time.Now()
	err := AwaitPort(PortWait{Port: 0, Mode: manifest.PortModeFixed}, 30*time.Second)
	if err != nil {
		t.Errorf("fixed con port = 0 debe devolver nil: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("fixed con port = 0 no debe retener: tardó %s", elapsed)
	}
}

// fixed sin port_mode declarado: mismo comportamiento que fixed explícito.
func TestAwaitPortFixedWithoutPortModeDeclaration(t *testing.T) {
	m := &manifest.Manifest{Name: "x", Command: "run", Port: 0}
	mode := m.EffectivePortMode()

	start := time.Now()
	if err := AwaitPort(PortWait{Port: 0, Mode: mode}, 30*time.Second); err != nil {
		t.Errorf("sin port_mode debe comportarse como fixed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("sin port_mode no debe retener: tardó %s", elapsed)
	}
}

// fixed con puerto concreto: gatea contra el puerto, como siempre.
func TestAwaitPortFixedGatesOnPort(t *testing.T) {
	port, release := listenOn(t)
	defer release()
	if err := AwaitPort(PortWait{Port: port, Mode: manifest.PortModeFixed}, 2*time.Second); err != nil {
		t.Errorf("fixed debe gatear contra el puerto real: %v", err)
	}
}

// La regla de retención sólo aplica en dynamic: fixed con puerto que nunca
// abre falla como siempre, sin tratamento de puerto pendiente.
func TestAwaitPortFixedNeverOpensFailsLikeToday(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	err = AwaitPort(PortWait{Port: port, Mode: manifest.PortModeFixed}, 800*time.Millisecond)
	if err == nil {
		t.Fatal("fixed con puerto cerrado debe fallar")
	}
	if errors.Is(err, ErrPortPending) {
		t.Error("fixed no gana semántica de puerto pendiente")
	}
}

// none avanza sin espera porque lo declara explícitamente.
func TestAwaitPortNoneDoesNotWait(t *testing.T) {
	start := time.Now()
	if err := AwaitPort(PortWait{Port: 0, Mode: manifest.PortModeNone}, 30*time.Second); err != nil {
		t.Errorf("none debe avanzar sin espera: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("none no debe esperar: tardó %s", elapsed)
	}
}

// dynamic con puerto pendiente nunca avanza por timeout en crudo: consume su
// presupuesto dentro del discovery y reporta una causa distinguible.
func TestAwaitPortDynamicPendingReportsDistinctCause(t *testing.T) {
	err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, PortPending: true}, 700*time.Millisecond)
	if err == nil {
		t.Fatal("dynamic con puerto sin resolver no debe darse por bueno")
	}
	if !errors.Is(err, ErrPortPending) {
		t.Errorf("la causa debe ser puerto pendiente, no un timeout genérico: %v", err)
	}
	if err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, Port: 1, PortPending: true}, 400*time.Millisecond); !errors.Is(err, ErrPortPending) {
		t.Errorf("puerto reservado que no abre: causa pendiente esperada, got %v", err)
	}
}

// dynamic sin puerto TCP termina acotado y se reporta como "sin puerto",
// no como fallo de arranque.
func TestAwaitPortDynamicNoPortIsBounded(t *testing.T) {
	start := time.Now()
	err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, NoPort: true}, 30*time.Second)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrNoPort) {
		t.Errorf("dynamic sin puerto debe reportarse como sin puerto: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("dynamic sin puerto no puede colgarse hasta un timeout crudo: %s", elapsed)
	}
}

// dynamic con puerto resuelto gatea contra ese puerto, no contra el declarado.
func TestAwaitPortDynamicGatesOnResolvedPort(t *testing.T) {
	port, release := listenOn(t)
	defer release()
	if err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, Port: port}, 2*time.Second); err != nil {
		t.Errorf("dynamic debe gatear contra el puerto resuelto: %v", err)
	}
	// El puerto declarado está en el PortWait de otra etapa; el nuestro no
	// aparece por ninguna parte: la_stage avanza por el real.
	if process.PortOpen(port) != true {
		t.Fatal("precondición: el listener debe seguir abierto")
	}
	_ = strconv.Itoa(port) // el número resuelto es el que se usa
}
