// Package-level reservation of the dynamic port range. Portable on purpose:
// the TUI, the CLI and the stack engine all need to give a reservation back
// on stop, and they are not unix-only. The /proc reading that discovers
// listeners stays in dynamic_unix.go; this file only owns the range policy.
package process

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// DefaultDynamicPortTimeout acota la ventana de arranque→bind.
const DefaultDynamicPortTimeout = 8 * time.Second

// DefaultDynamicUnresolvedGrace is the second window granted when the budget
// expires. It exists because expiring does not prove absence: a Next.js build
// that takes 12 s and a UDP-only worker look identical for 12 s. It is the
// bounded recovery path: if the port shows up here it resolves as usual and
// nothing is left unresolved.
const DefaultDynamicUnresolvedGrace = 8 * time.Second

// Dynamic port range. Fixed for now, widenable later: 4000-4999 is the band
// development backends already use, so a legitimate foreign listener landing
// in there is unlikely but possible.
const (
	DynamicPortLow  = 4000
	DynamicPortHigh = 4999
)

// ReservePort hands out a free port from the dynamic range and records it as
// handed out to this process.
//
// What it protects: two concurrent reservations INSIDE one vroom process never
// return the same port. Without the set, every concurrent caller took the
// first free slot: a keystroke on a group node with two dynamic members
// (toggleNode returns tea.Batch and bubbletea runs them in parallel) or a
// stack stage (Launch starts each service in its own goroutine) handed out the
// same number. Measured before the fix: 99.5% collisions across concurrent
// pairs.
//
// What it does NOT protect: two SEPARATE vroom processes. Each has its own set
// and both do bind+close against the same kernel pool. That window is the
// documented TOCTOU below; closing it would need socket passing, which does
// not fit `sh -c`.
//
// The set is process-local and dies with the process, so a crash cannot
// permanently shrink the range: the next vroom starts with an empty set.
// Within a process the invariant is the reverse of what a "leak" implies —
// entries are added on start and given back on stop, so the set size tracks
// live reservations rather than growing without bound. A stale entry (a
// reservation whose service is still running) can only cause a free port to
// be skipped, never a collision, because bind is the ground truth for
// occupancy.
//
// TOCTOU, documented: bind(127.0.0.1:port) + close returns the port to the
// pool before the child gets to bind. The window exists.
func ReservePort() (int, error) {
	reserveMu.Lock()
	defer reserveMu.Unlock()

	for port := DynamicPortLow; port <= DynamicPortHigh; port++ {
		if reservedPorts[port] {
			continue // already handed out to another start in this process
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue // taken or unavailable; try the next in range
		}
		if err := ln.Close(); err != nil {
			return 0, fmt.Errorf("could not release the reserved port %d: %w", port, err)
		}
		reservedPorts[port] = true
		return port, nil
	}
	return 0, fmt.Errorf("no free port in range %d-%d", DynamicPortLow, DynamicPortHigh)
}

// ReleasePort gives a handed-out port back to the set. Every stop path must
// call it, or the set grows monotonically and a long-lived process eventually
// reports "no free port in range" for ports that are in fact free.
//
// The caller must clear the reservation from its persisted state in the same
// step. Otherwise a repeated stop re-reads the stale reservation and releases
// a port that some other service has since taken, dropping a live
// reservation out of the set — which is the H1 race all over again.
func ReleasePort(port int) {
	if port <= 0 {
		return
	}
	reserveMu.Lock()
	defer reserveMu.Unlock()
	delete(reservedPorts, port)
}

// ReservedPortCount is how many ports this process currently holds. It exists
// for tests that need to assert on the set rather than on the side effect of a
// later reserve succeeding, which cannot distinguish "released" from "range
// exhausted".
func ReservedPortCount() int {
	reserveMu.Lock()
	defer reserveMu.Unlock()
	return len(reservedPorts)
}

var (
	reserveMu     sync.Mutex
	reservedPorts = make(map[int]bool, 16)
)
