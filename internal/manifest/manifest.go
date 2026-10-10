// Package manifest parses and validates .vroom.toml files; port is the app's PORT=${PORT:-N} default, never the port vroom assigns in dynamic mode (see docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
package manifest

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const FileName = ".vroom.toml"

const ReservedSecondaryGroup = "Composers"

// Manifest is one .vroom.toml: TOML keys keep the command_* prefix while the Go fields stay short, so the two vocabularies deliberately diverge.
type Manifest struct {
	Name           string `toml:"name"`
	PrimaryGroup   string `toml:"primary_group"`
	SecondaryGroup string `toml:"secondary_group"`
	Command        string `toml:"command_start"`
	PreStart       string `toml:"command_pre_start"` // fail-fast hook run before command_start spawns, for environment prep the app command itself must not carry (e.g. free a stale debug port)
	Port           int    `toml:"port"`              // the app's default port, never the one vroom assigns when the generation gets an ephemeral one
	ProcessPattern string `toml:"process_pattern"`
	Install        string `toml:"command_install"`
	Build          string `toml:"command_build"`
	Stop           string `toml:"command_stop"` // for services the PGID kill cannot reach, e.g. docker stop
	HealthPath     string `toml:"health_path"`
	// URLGeneration is the ONE start axis: how the started service is addressed (its port, a stable hostname, a branch hostname, a claim ladder, or nothing). It replaces the former port_mode + route_mode pair so the manifest states the same choices the TUI's s menu offers.
	URLGeneration string `toml:"url_generation"`
	// Worktrees overrides URLGeneration when this copy is a git worktree, so ONE committed manifest serves main (e.g. by_hostname) and every worktree (e.g. the ladder) without editing per copy.
	Worktrees Worktrees `toml:"worktrees"`
	// RouteName is the stable hostname OAuth callbacks and CORS rules need, because a route address must not follow a branch.
	RouteName string `toml:"route_name"`
}

// Worktrees is the per-copy policy: absent means a worktree inherits the top-level url_generation.
type Worktrees struct {
	URLGeneration string `toml:"url_generation"`
}

const (
	// URLGenByPort binds the manifest's port and publishes nothing else: the address is localhost:<port>.
	URLGenByPort = "by_port"
	// URLGenByHostname claims route_name and only route_name: a foreign holder is an error, because a URL that must not move (OAuth callback, CORS origin) must not silently become a branch URL. The port is ephemeral; the hostname is the address.
	URLGenByHostname = "by_hostname"
	// URLGenByWorkspaceHostname derives <branch>.<project>: branch scope, so two worktrees on one branch collide into a clean conflict instead of a second address.
	URLGenByWorkspaceHostname = "by_workspace_hostname"
	// URLGenByHostnameOrWorkspace is the claim ladder: route_name first and, when another worktree already holds it, <branch>.<project> instead of publishing no URL at all — whoever starts first keeps the stable name and every other worktree still gets an address.
	URLGenByHostnameOrWorkspace = "by_hostname_or_workspace"
	// URLGenNone is a headless service: no port and no URL, and vroom does not even look for the portless binary.
	URLGenNone = "none"
)

// PublishesURL is the portless gate: by_port and none never resolve the binary, exactly like the former route_mode = "off".
func PublishesURL(gen string) bool {
	switch gen {
	case URLGenByHostname, URLGenByWorkspaceHostname, URLGenByHostnameOrWorkspace:
		return true
	default:
		return false
	}
}

// NeedsRouteName: the two generations whose address IS a name chosen by the user.
func NeedsRouteName(gen string) bool {
	return gen == URLGenByHostname || gen == URLGenByHostnameOrWorkspace
}

// Port vocabulary is DERIVED from the generation, never declared apart: by_port binds the declared port, the hostname generations hand out an ephemeral one (their address is the hostname, and two worktrees could not share a port anyway), none has no port. Kept as the vocabulary the port-wait and display code already speak.
const (
	PortModeFixed   = "fixed"
	PortModeDynamic = "dynamic"
	PortModeNone    = "none"
)

func PortMode(gen string) string {
	switch gen {
	case URLGenByPort:
		return PortModeFixed
	case URLGenNone:
		return PortModeNone
	default:
		return PortModeDynamic
	}
}

// EffectiveURLGeneration resolves the manifest side only (the TUI's armed choice and the last recorded one outrank it at start time, in startsvc): a worktree takes [worktrees].url_generation when declared, then the top-level value, and a manifest declaring neither keeps the pre-url_generation behaviour — a declared port means by_port, port 0 means none.
func (m *Manifest) EffectiveURLGeneration(isWorktree bool) string {
	if isWorktree && m.Worktrees.URLGeneration != "" {
		return m.Worktrees.URLGeneration
	}
	switch m.URLGeneration {
	case "":
		if m.Port > 0 {
			return URLGenByPort
		}
		return URLGenNone
	default:
		return m.URLGeneration // invalid: Validate rejects it upstream, never silently defaulted
	}
}

// HasPort: a declared port exists in every generation but none (in the hostname ones it is the app's default behind PORT=${PORT:-N}, never the port vroom assigns).
func (m *Manifest) HasPort() bool {
	return m.Port > 0 && m.EffectiveURLGeneration(false) != URLGenNone
}

const DefaultHealthPath = "/"

func (m *Manifest) HealthURLPath() string {
	if m.HealthPath == "" {
		return DefaultHealthPath
	}
	return m.HealthPath
}

func Parse(path string) (*Manifest, error) {
	var m Manifest
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest %s: %w", path, err)
	}
	return &m, nil
}

func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, FileName))
	return err == nil
}

func (m *Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("missing required field: name")
	}
	if m.Command == "" {
		return fmt.Errorf("missing required field: command_start")
	}
	if m.Port < 0 || m.Port > 65535 {
		return fmt.Errorf("port must be 0 (disabled) or 1-65535, got %d", m.Port)
	}
	gens := []struct {
		where string
		value string
	}{
		{"url_generation", m.URLGeneration},
		{"worktrees.url_generation", m.Worktrees.URLGeneration},
	}
	for _, g := range gens {
		if g.value != "" && !validGeneration(g.value) {
			return fmt.Errorf("%s must be %q, %q, %q, %q or %q, got %q",
				g.where, URLGenByPort, URLGenByHostname, URLGenByWorkspaceHostname, URLGenByHostnameOrWorkspace, URLGenNone, g.value)
		}
		if g.value != "" && NeedsRouteName(g.value) && m.RouteName == "" {
			// rejected at parse time: the generation IS that name, so a start could only fail later, at claim time.
			return fmt.Errorf("%s = %q requires route_name (the stable hostname it claims)", g.where, g.value)
		}
	}
	// A declared port is required unless the copy is headless. An EXPLICIT by_port is the one that must have it; the legacy "" (resolving to none at port 0) is the headless door and needs none. Both copies are bound because either may be the one running.
	if m.Port == 0 {
		for _, g := range gens {
			if g.value != "" && g.value != URLGenNone {
				return fmt.Errorf("%s = %q requires a default port > 0 (it is the app's PORT=${PORT:-N} fallback)",
					g.where, g.value)
			}
		}
	}
	if m.RouteName != "" && !NeedsRouteName(m.URLGeneration) && !NeedsRouteName(m.Worktrees.URLGeneration) { // rejected because otherwise it is either ignored or applied by accident.
		return fmt.Errorf("route_name requires url_generation = %q or %q, got %q", URLGenByHostname, URLGenByHostnameOrWorkspace, m.URLGeneration)
	}
	if m.HealthPath != "" && !m.HasPort() {
		return fmt.Errorf("health_path requires a declared port; declare port > 0 or drop health_path")
	}
	if m.SecondaryGroup == ReservedSecondaryGroup {
		return fmt.Errorf("secondary_group %q is reserved for orchestration stacks", ReservedSecondaryGroup)
	}
	return nil
}

func validGeneration(gen string) bool {
	switch gen {
	case URLGenByPort, URLGenByHostname, URLGenByWorkspaceHostname, URLGenByHostnameOrWorkspace, URLGenNone:
		return true
	default:
		return false
	}
}
