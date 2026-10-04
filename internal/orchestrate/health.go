package orchestrate

import (
	"errors"
	"fmt"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
)

// WaitForPort is the unchanged legacy path for callers without port_mode (fixed mode falls back to it), where port 0 means "assume healthy".
func WaitForPort(port int, timeout time.Duration) error {
	if port == 0 {
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

// The mode decides, never the port number: port == 0 is ambiguous between "no port by design" and "not resolved yet", and conflating them lets a gate pass unchecked.
type PortWait struct {
	Port int
	Mode string // manifest.PortMode*
	// PortPending means the port is reserved and discovery is still in flight.
	PortPending bool
	// NoPort is a terminal state, not a wait: the process is alive and has no TCP port.
	NoPort bool
	// Unresolved means discovery already ended and will not look again, unlike NoPort (it does have listeners) and unlike PortPending (still in flight).
	Unresolved bool
}

// ErrNoPort marks "the service is alive and has no TCP port": not a start failure, so it must never abort the stack as one.
var ErrNoPort = errors.New("service has no TCP port")

// ErrPortPending means the budget ran out with discovery still in flight (nobody bound yet), which is why it must stay distinct from a port that failed to open.
var ErrPortPending = errors.New("service port still pending")

// ErrPortUnresolved is terminal, not transient: discovery stopped looking, so unlike ErrPortPending it can never fail a stage nor take its siblings down.
var ErrPortUnresolved = errors.New("service port unresolved")

// AwaitPort changes only dynamic mode: fixed keeps the legacy path verbatim (including port 0 sleeping a second) and none skips waiting because it declares it.
func AwaitPort(w PortWait, timeout time.Duration) error {
	switch w.Mode {
	case manifest.PortModeNone:
		return nil
	case manifest.PortModeDynamic:
		// Unresolved is checked before the generic Port <= 0, which reports ErrPortPending and would fail a stage whose discovery had already ended.
		if w.Unresolved {
			return ErrPortUnresolved
		}
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
				// Poll fine while discovery is in flight: a 500ms sleep can skip the moment the port opens and wastes the budget.
				time.Sleep(100 * time.Millisecond)
				continue
			}
			time.Sleep(500 * time.Millisecond)
		}
		if w.PortPending {
			return fmt.Errorf("%w: port %d", ErrPortPending, w.Port)
		}
		return fmt.Errorf("port %d not open after %s", w.Port, timeout)
	default:
		return WaitForPort(w.Port, timeout)
	}
}
