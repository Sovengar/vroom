// Package process owns the dynamic port range policy, kept portable on purpose so the TUI, the CLI and the stack engine can all give a reservation back on stop; the /proc reading that finds listeners stays in dynamic_unix.go (see docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
package process

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// DefaultDynamicPortTimeout bounds the start-to-bind window.
const DefaultDynamicPortTimeout = 8 * time.Second

// Second window granted when the budget expires, because expiring does not prove absence: a build that takes 12 s and a UDP-only worker look identical for those 12 s.
const DefaultDynamicUnresolvedGrace = 8 * time.Second

// Fixed for now, widenable later: 4000-4999 is the band development backends already use, so a foreign listener landing there is unlikely but possible.
const (
	DynamicPortLow  = 4000
	DynamicPortHigh = 4999
)

// Within one process two concurrent reservations never return the same port (measured: 99.5% collisions across concurrent pairs before this set); separate processes keep their own set and so remain exposed to the TOCTOU below.
func ReservePort() (int, error) {
	return reserveWith(net.Listen)
}

// The open is injected because the Close right after it can only fail if the listener was already closed, which made the branch untestable, and a port that opens but cannot be returned must NOT be marked reserved or the range loses one hole per failed close.
func reserveWith(abrir func(network, addr string) (net.Listener, error)) (int, error) {
	reserveMu.Lock()
	defer reserveMu.Unlock()

	for port := DynamicPortLow; port <= DynamicPortHigh; port++ {
		if reservedPorts[port] {
			continue
		}
		ln, err := abrir("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		if err := ln.Close(); err != nil {
			return 0, fmt.Errorf("could not release the reserved port %d: %w", port, err)
		}
		reservedPorts[port] = true
		return port, nil
	}
	return 0, fmt.Errorf("no free port in range %d-%d", DynamicPortLow, DynamicPortHigh)
}

// Every stop path must call it or the set grows monotonically, and the caller must clear its persisted reservation in the same step or the next stop releases a port some other service now owns (H1 all over again).
func ReleasePort(port int) {
	if port <= 0 {
		return
	}
	reserveMu.Lock()
	defer reserveMu.Unlock()
	delete(reservedPorts, port)
}

// Exists so the set itself can be observed, which a later failed reserve cannot distinguish from an exhausted range.
func ReservedPortCount() int {
	reserveMu.Lock()
	defer reserveMu.Unlock()
	return len(reservedPorts)
}

var (
	reserveMu     sync.Mutex
	reservedPorts = make(map[int]bool, 16)
)
