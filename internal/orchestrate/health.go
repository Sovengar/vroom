package orchestrate

import (
	"errors"
	"fmt"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
)

// WaitForPort espera a que el puerto esté abierto, sondeando cada 500ms.
// Si port es 0 (servicio sin puerto), espera el timeout y asume sano.
//
// Esta es la ruta legacy y no cambia: la conservan los llamadores que no
// conocen port_mode. Para los que sí, usar AwaitPort.
func WaitForPort(port int, timeout time.Duration) error {
	if port == 0 {
		// Servicio sin puerto configurado: asumir que arranca rápido
		time.Sleep(time.Second)
		return nil
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if process.PortOpen(port) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("port %d not open after %s", port, timeout)
}

// PortWait describe qué debe esperar una etapa antes de darse por sana.
// El modo decide, no el número: port == 0 sigue siendo ambiguo entre "sin
// puerto por diseño" y "el puerto aún no se ha resuelto", y confundirlos es
// exactamente lo que hace que un gate pase sin verificar nada.
type PortWait struct {
	Port int
	Mode string // manifest.PortMode*
	// PortPending indica que el puerto está reservado y el discovery sigue
	// en vuelo.
	PortPending bool
	// NoPort indica que el proceso vive y no tiene puerto TCP, y que eso
	// es su estado y no una espera.
	NoPort bool
}

// ErrNoPort marca "el servicio está vivo y no tiene puerto TCP": no es un
// fallo de arranque y no debe abortar el stack como si lo fuera.
var ErrNoPort = errors.New("service has no TCP port")

// ErrPortPending marca que se agotó el presupuesto con el discovery en
// vuelo. Distinto de un timeout de puerto: la causa es "nadie ha hecho
// bind todavía", no "el puerto no abrió".
var ErrPortPending = errors.New("service port still pending")

// AwaitPort gatea la salud de una etapa respetando port_mode.
//
// El alcance del modo nuevo es deliberadamente estrecho: fixed conserva
// literalmente el comportamiento anterior (incluido port == 0, que duerme un
// segundo y avanza), y none avanza sin espera porque lo declara. Sólo
// dynamic cambia, y sólo cuando hay un puerto realmente reservado.
func AwaitPort(w PortWait, timeout time.Duration) error {
	switch w.Mode {
	case manifest.PortModeNone:
		return nil
	case manifest.PortModeDynamic:
		if w.NoPort {
			return ErrNoPort
		}
		if w.Port <= 0 {
			return ErrPortPending
		}
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if process.PortOpen(w.Port) {
				return nil
			}
			if w.PortPending {
				// El discovery sigue en vuelo: sondear fino. El puerto
				// puede abrir en cualquier momento y saltarse 500ms
				// desperdicia presupuesto.
				time.Sleep(100 * time.Millisecond)
				continue
			}
			time.Sleep(500 * time.Millisecond)
		}
		if w.PortPending {
			return fmt.Errorf("%w: port %d", ErrPortPending, w.Port)
		}
		return fmt.Errorf("port %d not open after %s", w.Port, timeout)
	default: // fixed, o "" (manifiesto sin port_mode): la ruta de siempre
		return WaitForPort(w.Port, timeout)
	}
}
