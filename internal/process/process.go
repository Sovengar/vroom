// Package process supervises daemons behind the cross-platform Manager interface, with every platform behaviour isolated in build-tagged files; Stop signals the whole lineage and fails closed on port ownership (see docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
package process

import "time"

type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusUnknown Status = "unknown"
	// A named state so "starting" must not masquerade as either healthy or broken.
	StatusPortPending Status = "port_pending"
	// Alive with no TCP port: that is the state, not a pending wait.
	StatusNoPort Status = "no_port"
	// Distinct from StatusNoPort on purpose: "no port" and "not decided yet" cannot share a verdict in the UI or the JSON.
	StatusPortUnresolved Status = "port_unresolved"
)

// DefaultStopTimeout is the SIGTERM grace period before SIGKILL.
const DefaultStopTimeout = 5 * time.Second

type StartSpec struct {
	Command    string
	WorkDir    string
	StdoutPath string
	StderrPath string

	// Injected KEY=VALUE vars, merged with the parent environment and never replacing it; nil means the child inherits as before.
	Env []string

	// PreSpawn runs after the logs are truncated and opened and before the child exists, so what it writes is the FIRST entry of this run's log and a failure leaves no process to clean up. It is the seam for the manifest's command_pre_start: run before Start instead, its output would be wiped by the truncation above and a successful hook would leave no trace.
	PreSpawn func() error
}

type StartResult struct {
	Pid            int
	Pgid           int // with setsid, Pgid == Pid
	CreationTimeMs int64
}

type StopSpec struct {
	Pid     int // lineage root; 0 = derive it from the pgid
	Pgid    int
	Port    int // 0 = do not verify the port after stop
	Timeout time.Duration

	// Non-fatal stop warnings (e.g. port ownership could not be proven) travel to the service log; nil discards them.
	Warn func(format string, args ...any)
}

func (s StopSpec) warnf(format string, args ...any) {
	if s.Warn != nil {
		s.Warn(format, args...)
	}
}

type EvalSpec struct {
	Pid            int
	CreationTimeMs int64
	Port           int
	ProcessPattern string
	// Only dynamic start sets it: alive with the port reserved but not listening yet is pending, not unknown.
	PortPending bool
	// Alive and possibly not bound yet: neither "no port" nor "all good".
	PortUnresolved bool
	// Without it a UDP-only worker reports as running, indistinguishable from a healthy service.
	NoPort bool
}

type Manager interface {
	Start(spec StartSpec) (StartResult, error)

	Stop(spec StopSpec) error

	Evaluate(spec EvalSpec) Status
}
