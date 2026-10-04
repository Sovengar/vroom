//go:build unix

package process

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type DiscoveryResult struct {
	// 0 on its own is ambiguous: no TCP listener, not yet decided, or unresolved, so read the flags below.
	Port int
	// Ascending, so the caller can tell "no listener at all" from "several, one was chosen".
	All []int
	// The reserved port was among the listeners (R1), so the choice was deterministic and not a guess.
	HonoredReserved bool
	// R3 cannot tell which listener is main, so it must stay explicitly false instead of defaulting to true.
	Verified bool
	// The lineage was already dead: abandon discovery rather than burn the whole timeout.
	LineageDead bool
	// The deadline expired with no decidable main port; calling that "no port" would label a slow service forever, since nothing rediscovers later.
	Unresolved bool
}

// Ambiguity exists only when vroom has to guess: R1 the reserved port is among the listeners, R2 the best health_path response, R3 the lowest port with Verified=false (see docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
func DiscoverPort(rootPid int, reserved int, healthPath string, timeout time.Duration) DiscoveryResult {
	deadline := time.Now().Add(timeout)
	graceDeadline := deadline.Add(DefaultDynamicUnresolvedGrace)
	var prev []int
	changedAt := time.Now()

	for {
		if rootPid <= 0 || !lineageRunning([]int{rootPid}) {
			return DiscoveryResult{LineageDead: true}
		}

		listeners := lineageListenersAt(procRoot, rootPid)

		// R1 skips the settle window: the app took the reserved port, so nothing is being guessed even if it opens another listener later.
		if reserved > 0 && containsPid(listeners, reserved) {
			return DiscoveryResult{Port: reserved, All: listeners, HonoredReserved: true, Verified: true}
		}

		// Apps open listeners in stages (metrics first, main later), and accepting the first sample would pick the wrong one.
		if !samePorts(listeners, prev) {
			prev = append([]int(nil), listeners...)
			changedAt = time.Now()
		}

		now := time.Now()
		if len(listeners) > 0 {
			if time.Since(changedAt) >= discoverSettle {
				return decidePort(listeners, reserved, healthPath)
			}
			if now.After(deadline) {
				// Listeners that never settle are declared undecided instead of guessed.
				return DiscoveryResult{Unresolved: true}
			}
		} else if now.After(graceDeadline) {
			// Only after deadline plus grace may "no listener" be reported as "no TCP port".
			return DiscoveryResult{}
		}
		time.Sleep(discoverInterval)
	}
}

const discoverInterval = 100 * time.Millisecond

// Accepting the set only after it holds still this long covers the typical two-goroutine app; stalling longer yields port_unresolved rather than the wrong port (see docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
const discoverSettle = 500 * time.Millisecond

func samePorts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Split from the loop so R1/R2/R3 can be exercised without /proc.
func decidePort(listeners []int, reserved int, healthPath string) DiscoveryResult {
	if reserved > 0 && containsPid(listeners, reserved) {
		return DiscoveryResult{Port: reserved, All: listeners, HonoredReserved: true, Verified: true}
	}
	if len(listeners) == 1 {
		return DiscoveryResult{Port: listeners[0], All: listeners, Verified: true}
	}
	return pickMainPort(listeners, healthPath)
}

func pickMainPort(listeners []int, healthPath string) DiscoveryResult {
	best, bestRank := 0, -1
	for _, port := range listeners { // already ascending: a tie keeps the lowest
		if rank := healthRank(probeStatus(port, healthPath)); rank > bestRank {
			best, bestRank = port, rank
		}
	}
	if best > 0 && bestRank > 0 {
		return DiscoveryResult{Port: best, All: listeners, Verified: true}
	}
	// MEASURED (bug): listeners[0] panicked on an empty list, unreachable from the loop today but the first new caller would hit it mid-start.
	if len(listeners) == 0 {
		return DiscoveryResult{}
	}
	return DiscoveryResult{Port: listeners[0], All: listeners, Verified: false}
}

// 200 beats other 2xx/3xx beats 5xx beats 404, and 0 means "did not answer as HTTP" so it never wins.
func healthRank(status int) int {
	switch {
	case status == 200:
		return 4
	case status >= 200 && status < 400:
		return 3
	case status >= 500:
		return 2
	case status == 404:
		return 1
	default:
		return 0
	}
}

// Deliberately short: disambiguation runs inside the start-up budget, so a port that does not answer as HTTP must not eat it.
const probeTimeout = 1500 * time.Millisecond

func probeStatus(port int, healthPath string) int {
	if healthPath == "" {
		healthPath = "/"
	}
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath))
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	return resp.StatusCode
}

// /proc/net/tcp{,6} is joined to the lineage's fds by socket inode, because asking the network "who listens" cannot tell which address family the twin owns.
func lineageListenersAt(root string, rootPid int) []int {
	snap, err := procSnapshotAt(root)
	if err != nil {
		return nil
	}
	lineage := descendantsFrom(snap, rootPid)
	pids := append([]int{rootPid}, lineage...)

	owner := map[string]int{}
	for _, pid := range pids {
		fdDir := filepath.Join(root, strconv.Itoa(pid), "fd")
		entries, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
			if err != nil {
				continue
			}
			if ino, ok := socketInode(target); ok {
				owner[ino] = pid
			}
		}
	}

	seen := map[int]bool{}
	var ports []int
	for _, name := range []string{"net/tcp", "net/tcp6"} {
		for _, l := range listenSocketsAt(filepath.Join(root, name)) {
			if _, mine := owner[l.inode]; !mine || seen[l.port] {
				continue
			}
			seen[l.port] = true
			ports = append(ports, l.port)
		}
	}
	sort.Ints(ports)
	return ports
}

type listenSocket struct {
	addr  string // bind address in hex, e.g. "0100007F" is 127.0.0.1
	port  int
	inode string
}

// 0A is TCP_LISTEN and the port field is hex.
func listenSocketsAt(path string) []listenSocket {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var out []listenSocket
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		addr, portStr, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseInt(portStr, 16, 32)
		if err != nil {
			continue
		}
		out = append(out, listenSocket{addr: addr, port: int(port), inode: fields[9]})
	}
	return out
}

func socketInode(target string) (string, bool) {
	if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]"), true
}
