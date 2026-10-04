// Package state is the only writer of vroom's on-disk service state: {base}/services/{path-hash}/{meta.json,pid,pgid,stdout.log,stderr.log}, with base resolved at runtime from $XDG_STATE_HOME (default ~/.local/state/vroom).
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	StateRunning = "running"
	StateStopped = "stopped"
	StateUnknown = "unknown"
	// StatePortPending is alive but unresolved, persisted so a TUI restart cannot mistake it for "no port".
	StatePortPending = "port_pending"
	StateNoPort      = "no_port"
	// StatePortUnresolved means discovery timed out undecided and the process may not have bound yet; unlike StateNoPort the port is unknown rather than absent, so the UI must not fall back to the declared port.
	StatePortUnresolved = "port_unresolved"
)

type Meta struct {
	Name        string `json:"name"`
	ProjectPath string `json:"project_path"`
	Port        int    `json:"port"`
	// ReservedPort is the port offered to the child, kept apart from Port because it is the one returned to the pool on stop.
	ReservedPort   int    `json:"reserved_port,omitempty"`
	ProcessPattern string `json:"process_pattern"`
	Command        string `json:"command"`
	Pid            int    `json:"pid"`
	Pgid           int    `json:"pgid"`
	CreationTimeMs int64  `json:"creation_time_ms"`
	StartedAt      string `json:"started_at"` // RFC3339
	State          string `json:"state"`
	// PortVerified false does not mean "no listener": the port was picked without proof (multi-port, no heuristic), so callers must treat it as unconfirmed (docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
	PortVerified bool `json:"port_verified"`
	// Route fields outlive the service because routes survive a proxy restart (M3) and prune never removes them (M5): without them stop and start-up reconciliation cannot find the route (docs/adr/adr-0013-vroom-registers-portless-routes.md).
	RouteName   string `json:"route_name,omitempty"`
	RoutePort   int    `json:"route_port,omitempty"`
	RouteStatus string `json:"route_status,omitempty"` // registered | degraded
	// RouteOwned is not implied by RouteName/RoutePort: those are a name and a number that never expire, so only this says the name is still ours to move.
	RouteOwned bool `json:"route_owned,omitempty"`
	// RouteReason explains a degradation; route status never changes State because service health does not depend on the address.
	RouteReason string `json:"route_reason,omitempty"`
	RouteURL    string `json:"route_url,omitempty"`
}

type Store struct {
	base string
}

func DefaultBaseDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "vroom"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "vroom"), nil
}

func NewStore() (*Store, error) {
	base, err := DefaultBaseDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(base, "services"), 0o755); err != nil {
		return nil, fmt.Errorf("could not create state directory %s (check permissions): %w", base, err)
	}
	return &Store{base: base}, nil
}

func NewStoreAt(base string) *Store {
	return &Store{base: base}
}

func (s *Store) Base() string { return s.base }

func (s *Store) ServiceDir(projectPath string) string {
	return filepath.Join(s.base, "services", PathKey(projectPath))
}

func (s *Store) EnsureServiceDir(projectPath string) (string, error) {
	dir := s.ServiceDir(projectPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("could not create service directory %s (check permissions): %w", dir, err)
	}
	return dir, nil
}

func (s *Store) StdoutLog(projectPath string) string {
	return filepath.Join(s.ServiceDir(projectPath), "stdout.log")
}

func (s *Store) StderrLog(projectPath string) string {
	return filepath.Join(s.ServiceDir(projectPath), "stderr.log")
}

func (s *Store) PidFile(projectPath string) string {
	return filepath.Join(s.ServiceDir(projectPath), "pid")
}

func (s *Store) PgidFile(projectPath string) string {
	return filepath.Join(s.ServiceDir(projectPath), "pgid")
}

func (s *Store) SaveMeta(projectPath string, m Meta) error {
	if _, err := s.EnsureServiceDir(projectPath); err != nil {
		return err
	}
	target := filepath.Join(s.ServiceDir(projectPath), "meta.json")
	if err := writeJSONAtomic(target, m); err != nil {
		return fmt.Errorf("could not save %s: %w", filepath.Base(target), err)
	}
	return nil
}

// Marshal errors panic on purpose: encoding/json cannot fail on the types stored here (Meta, map[string]bool), so the check was dead code and a panic names the offending type at the layer that added it.
func writeJSONAtomic(target string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("json.MarshalIndent de %T falló y eso no puede pasar con los tipos que "+
			"vroom guarda aquí: %v", v, err))
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func (s *Store) LoadMeta(projectPath string) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(filepath.Join(s.ServiceDir(projectPath), "meta.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("corrupt meta.json: %w", err)
	}
	return m, nil
}

func (s *Store) RegisterPid(projectPath string, pid, pgid int) error {
	dir, err := s.EnsureServiceDir(projectPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(fmt.Sprintf("%d\n", pid)), 0o644); err != nil {
		return fmt.Errorf("could not write pid file: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pgid"), []byte(fmt.Sprintf("%d\n", pgid)), 0o644); err != nil {
		return fmt.Errorf("could not write pgid file: %w", err)
	}
	return nil
}

func (s *Store) ClearPid(projectPath string) error {
	for _, f := range []string{s.PidFile(projectPath), s.PgidFile(projectPath)} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not clean up %s: %w", f, err)
		}
	}
	return nil
}

func (s *Store) CollapsedFile() string {
	return filepath.Join(s.base, "collapsed.json")
}

// Keys are "<primary>" or "<primary>/<secondary>", which is how the TUI addresses a group.
func (s *Store) SaveCollapsed(groups map[string]bool) error {
	target := s.CollapsedFile()
	if err := writeJSONAtomic(target, groups); err != nil {
		return fmt.Errorf("could not save %s: %w", filepath.Base(target), err)
	}
	return nil
}

func (s *Store) LoadCollapsed() map[string]bool {
	data, err := os.ReadFile(s.CollapsedFile())
	if err != nil {
		return nil
	}
	groups := make(map[string]bool)
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil
	}
	return groups
}
