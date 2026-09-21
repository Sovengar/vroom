package orchestrate

import (
	"fmt"
	"time"

	"vroom/internal/process"
)

// WaitForPort espera a que el puerto esté abierto, sondeando cada 500ms.
// Si port es 0 (servicio sin puerto), espera el timeout y asume sano.
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
