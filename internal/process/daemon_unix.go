//go:build unix

package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

type unixManager struct{}

func NewManager() Manager { return &unixManager{} }

func (u *unixManager) Start(spec StartSpec) (StartResult, error) {
	if spec.Command == "" {
		return StartResult{}, fmt.Errorf("empty command")
	}
	if err := os.MkdirAll(filepath.Dir(spec.StdoutPath), 0o755); err != nil {
		return StartResult{}, fmt.Errorf("could not create log directory: %w", err)
	}

	_ = os.Truncate(spec.StdoutPath, 0)
	_ = os.Truncate(spec.StderrPath, 0)

	stdout, err := os.OpenFile(spec.StdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return StartResult{}, fmt.Errorf("could not open stdout.log: %w", err)
	}
	defer func() { _ = stdout.Close() }()

	stderr, err := os.OpenFile(spec.StderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return StartResult{}, fmt.Errorf("could not open stderr.log: %w", err)
	}
	defer func() { _ = stderr.Close() }()

	if spec.PreSpawn != nil {
		if err := spec.PreSpawn(); err != nil {
			return StartResult{}, err
		}
	}

	cmd := exec.Command("sh", "-c", spec.Command)
	cmd.Dir = spec.WorkDir
	// Stdin stays nil on purpose: that already hands the child the null device, which must never be the user's TUI terminal.
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Any non-nil cmd.Env replaces os.Environ() wholesale, so PORT has to be merged or the child would be left without PATH.
	cmd.Env = mergeEnv(os.Environ(), spec.Env)

	if err := cmd.Start(); err != nil {
		return StartResult{}, fmt.Errorf("failed to start %q: %w", spec.Command, err)
	}
	pid := cmd.Process.Pid

	// Without Wait() the child stays a zombie for as long as the TUI lives.
	go func() { _ = cmd.Wait() }()

	result := StartResult{Pid: pid, Pgid: pid} // setsid makes pgid == pid
	if p, err := gopsprocess.NewProcess(int32(pid)); err == nil {
		if ct, err := p.CreateTime(); err == nil {
			result.CreationTimeMs = ct
		}
	}
	return result, nil
}

// Spec keys override the parent's; with nothing to inject it returns nil so exec applies its normal inheritance.
func mergeEnv(parent, extra []string) []string {
	if len(extra) == 0 {
		return nil
	}
	out := make([]string, 0, len(parent)+len(extra))
	out = append(out, parent...)
	for _, kv := range extra {
		key, _, _ := strings.Cut(kv, "=")
		if key == "" {
			continue
		}
		replaced := false
		for i, existing := range out {
			if k, _, _ := strings.Cut(existing, "="); k == key {
				out[i] = kv
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	return out
}

// The lineage is captured before signalling because once the root dies its children are reparented to init and the relation is lost (see docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
func (u *unixManager) Stop(spec StopSpec) error {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}

	root, lineage := captureLineage(spec)

	// Only the decision (warn or stay silent) is exercisable in a test; whether anything survived SIGKILL needs a foreign user's process.
	spec.warnIfExhausted(root, lineage, root > 0 && !terminate(root, spec.Pgid, lineage, timeout))

	// Last resort: free the port, but only on proof of ownership.
	if spec.Port > 0 && PortOpen(spec.Port) {
		killPortHolderWith(spec.Port, root, lineage, PortOwnerPIDs, spec.Warn)
	}
	return nil
}

// root > 0 because terminate is not called at all without a root; didNotDie because a Stop that worked must leave no noise in the log.
func (s StopSpec) warnIfExhausted(root int, lineage []int, didNotDie bool) {
	if root > 0 && didNotDie {
		s.warnf("stop: processes still alive after SIGKILL (%s)", lineageDesc(root, lineage))
	}
}

// pgid is 0 on the PID-only path so kill(-0, sig) is never emitted; both roots share this single ladder so they cannot diverge.
func terminate(root, pgid int, lineage []int, timeout time.Duration) bool {
	signal := func(sig syscall.Signal) {
		if pgid > 0 {
			_ = syscall.Kill(-pgid, sig)
		}
		for _, pid := range lineage {
			_ = syscall.Kill(pid, sig)
		}
	}

	signal(syscall.SIGTERM)
	if waitLineageGone(pgid, lineage, timeout) {
		return true
	}

	signal(syscall.SIGKILL)
	return waitLineageGone(pgid, lineage, 2*time.Second)
}

func lineageDesc(root int, lineage []int) string {
	if len(lineage) <= 1 {
		return "pgid " + strconv.Itoa(root)
	}
	return "pgid " + strconv.Itoa(root) + " and " + strconv.Itoa(len(lineage)-1) + " descendant(s)"
}

func (u *unixManager) Evaluate(spec EvalSpec) Status {
	pidAlive := spec.Pid > 0 && Alive(spec.Pid, spec.CreationTimeMs)

	checked := false
	ok := false
	portOpen := false
	patternMatch := false
	if spec.Port > 0 {
		checked = true
		portOpen = PortOpen(spec.Port)
		ok = portOpen
	}
	if spec.ProcessPattern != "" {
		checked = true
		patternMatch = PatternMatch(spec.ProcessPattern)
		ok = ok || patternMatch
	}

	if pidAlive {
		if checked && !ok {
			if spec.PortPending && spec.Port > 0 {
				return StatusPortPending
			}
			return StatusUnknown
		}
		if spec.NoPort && spec.Port <= 0 {
			// Alive with no TCP port is a fact, not missing information: naming it stops the UI from reading it as healthy.
			return StatusNoPort
		}
		if spec.PortUnresolved && spec.Port <= 0 {
			// Alive with the port never decided is neither healthy nor port-less, hence its own name.
			return StatusPortUnresolved
		}
		return StatusRunning
	}

	if ok {
		if patternMatch {
			return StatusRunning
		}
		if portOpen {
			ownerPID := PortOwnerPID(spec.Port)
			if ownerPID <= 0 {
				// An ambiguous owner degrades to unknown: calling it running is the optimistic verdict that reports a twin worktree's process as ours.
				return StatusUnknown
			}
			ownerAlive := Alive(int(ownerPID), spec.CreationTimeMs)
			if !ownerAlive {
				return StatusStopped
			}
			return StatusRunning
		}
		// No StatusRunning here on purpose: ok && !patternMatch implies portOpen, so the branch above already covers it (MEASURED: this was the only return no test could reach).
	}
	return StatusStopped
}

// An empty pattern returns true (measured: pgrep -f "" lists every process), left uncorrected because callers already guard non-empty and a second guard here would duplicate the rule; the pattern also reaches `pgrep -f` unquoted, so pgrep treats it as a REGEX, not a literal: "a.b" matches "axb" and parentheses are groups.
func PatternMatch(pattern string) bool {
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		return false
	}
	myPid := os.Getpid()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		if pid != myPid {
			return true
		}
	}
	return false
}

func waitLineageGone(pgid int, lineage []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !pgidAlive(pgid) && !lineageRunning(lineage) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func pgidAlive(pgid int) bool {
	// kill(0, sig) means vroom's own process group: unguarded it always answers true and would signal vroom itself, and without a pgid there is no group to ask.
	if pgid <= 0 {
		return false
	}
	return groupExists(syscall.Kill(-pgid, syscall.Signal(0)))
}

// EPERM means the group exists but is not ours (alive, maybe not our processes), ESRCH means nobody is left; swapping them inverts the decision.
func groupExists(err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, syscall.ESRCH):
		return false
	case errors.Is(err, syscall.EPERM):
		return true // exists but is not ours
	default:
		return false
	}
}

// fuser -k runs only on proof of ownership: exactly one known owner, inside this lineage, otherwise nothing is killed and a warning is emitted (see docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
func killPortHolderWith(port, rootPid int, lineage []int, owners func(int) []int32, warn func(string, ...any)) {
	if warn == nil {
		warn = func(string, ...any) {}
	}

	candidates := distinctOwners(owners(port))
	if len(candidates) != 1 {
		warn("port %d occupied but vroom cannot prove who holds it: nothing is killed", port)
		return
	}
	owner := candidates[0]
	if owner != rootPid && !containsPid(lineage, owner) {
		warn("port %d is held by pid %d, outside this service's lineage: nothing is killed", port, owner)
		return
	}

	_ = exec.Command("fuser", "-k", fmt.Sprintf("%d/tcp", port)).Run()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !PortOpen(port) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	warn("port %d still occupied after kill: there is a process that refuses to die", port)
}

func distinctOwners(pids []int32) []int {
	seen := make(map[int32]bool, len(pids))
	out := make([]int, 0, len(pids))
	for _, p := range pids {
		if p <= 0 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, int(p))
	}
	return out
}

func containsPid(pids []int, pid int) bool {
	for _, p := range pids {
		if p == pid {
			return true
		}
	}
	return false
}
