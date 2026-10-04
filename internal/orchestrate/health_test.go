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

func listenOn(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

func TestWaitForPortOpensInTime(t *testing.T) {
	port, release := listenOn(t)
	defer release()
	if err := WaitForPort(port, 2*time.Second); err != nil {
		t.Errorf("puerto abierto debe pasar: %v", err)
	}
}

func TestWaitForPortTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // free port: nothing will listen

	start := time.Now()
	err = WaitForPort(port, time.Second)
	if err == nil {
		t.Fatal("un puerto que nunca abre debe fallar")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("el timeout no se respetó: %s", elapsed)
	}
}

// Riesgo 8 (legacy half): port = 0 stays a short sleep returning nil, with no new state, warning or extra hold.
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

func TestAwaitPortFixedGatesOnPort(t *testing.T) {
	port, release := listenOn(t)
	defer release()
	if err := AwaitPort(PortWait{Port: port, Mode: manifest.PortModeFixed}, 2*time.Second); err != nil {
		t.Errorf("fixed debe gatear contra el puerto real: %v", err)
	}
}

// The pending-port rule exists only in dynamic: fixed with a port that never opens simply fails.
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

func TestAwaitPortNoneDoesNotWait(t *testing.T) {
	start := time.Now()
	if err := AwaitPort(PortWait{Port: 0, Mode: manifest.PortModeNone}, 30*time.Second); err != nil {
		t.Errorf("none debe avanzar sin espera: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("none no debe esperar: tardó %s", elapsed)
	}
}

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

func TestAwaitPortDynamicGatesOnResolvedPort(t *testing.T) {
	port, release := listenOn(t)
	defer release()
	if err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, Port: port}, 2*time.Second); err != nil {
		t.Errorf("dynamic debe gatear contra el puerto resuelto: %v", err)
	}
	if process.PortOpen(port) != true {
		t.Fatal("precondición: el listener debe seguir abierto")
	}
	_ = strconv.Itoa(port)
}
