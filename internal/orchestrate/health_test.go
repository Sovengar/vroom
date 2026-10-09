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
		t.Errorf("open port must pass: %v", err)
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
		t.Fatal("a port that never opens must fail")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the timeout was not respected: %s", elapsed)
	}
}

// Risk 8 (legacy half): port = 0 stays a short sleep returning nil, with no new state, warning or extra hold.
func TestWaitForPortZeroIsLegacyShortSleep(t *testing.T) {
	start := time.Now()
	if err := WaitForPort(0, 30*time.Second); err != nil {
		t.Errorf("port = 0 must return nil: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("port = 0 must be a short sleep, took %s", elapsed)
	}
}

func TestAwaitPortFixedZeroMatchesLegacy(t *testing.T) {
	start := time.Now()
	err := AwaitPort(PortWait{Port: 0, Mode: manifest.PortModeFixed}, 30*time.Second)
	if err != nil {
		t.Errorf("fixed with port = 0 must return nil: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("fixed with port = 0 must not hold: took %s", elapsed)
	}
}

func TestAwaitPortFixedWithoutPortModeDeclaration(t *testing.T) {
	m := &manifest.Manifest{Name: "x", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "run"}}, Port: 0}
	mode := m.EffectivePortMode()

	start := time.Now()
	if err := AwaitPort(PortWait{Port: 0, Mode: mode}, 30*time.Second); err != nil {
		t.Errorf("without port_mode it must behave like fixed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("without port_mode it must not hold: took %s", elapsed)
	}
}

func TestAwaitPortFixedGatesOnPort(t *testing.T) {
	port, release := listenOn(t)
	defer release()
	if err := AwaitPort(PortWait{Port: port, Mode: manifest.PortModeFixed}, 2*time.Second); err != nil {
		t.Errorf("fixed must gate against the real port: %v", err)
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
		t.Fatal("fixed with closed port must fail")
	}
	if errors.Is(err, ErrPortPending) {
		t.Error("fixed does not gain pending-port semantics")
	}
}

func TestAwaitPortNoneDoesNotWait(t *testing.T) {
	start := time.Now()
	if err := AwaitPort(PortWait{Port: 0, Mode: manifest.PortModeNone}, 30*time.Second); err != nil {
		t.Errorf("none must proceed without waiting: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("none must not wait: took %s", elapsed)
	}
}

func TestAwaitPortDynamicPendingReportsDistinctCause(t *testing.T) {
	err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, PortPending: true}, 700*time.Millisecond)
	if err == nil {
		t.Fatal("dynamic with unresolved port must not be considered good")
	}
	if !errors.Is(err, ErrPortPending) {
		t.Errorf("the cause must be pending port, not a generic timeout: %v", err)
	}
	if err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, Port: 1, PortPending: true}, 400*time.Millisecond); !errors.Is(err, ErrPortPending) {
		t.Errorf("reserved port that does not open: expected pending cause, got %v", err)
	}
}

func TestAwaitPortDynamicNoPortIsBounded(t *testing.T) {
	start := time.Now()
	err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, NoPort: true}, 30*time.Second)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrNoPort) {
		t.Errorf("dynamic without port must be reported as no port: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("dynamic without port cannot hang until a raw timeout: %s", elapsed)
	}
}

func TestAwaitPortDynamicGatesOnResolvedPort(t *testing.T) {
	port, release := listenOn(t)
	defer release()
	if err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, Port: port}, 2*time.Second); err != nil {
		t.Errorf("dynamic must gate against the resolved port: %v", err)
	}
	if process.PortOpen(port) != true {
		t.Fatal("precondition: the listener must remain open")
	}
	_ = strconv.Itoa(port)
}
