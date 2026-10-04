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
	Port           int    `toml:"port"` // the app's default port, never the one vroom assigns in dynamic mode
	PortMode       string `toml:"port_mode"`
	ProcessPattern string `toml:"process_pattern"`
	Install        string `toml:"command_install"`
	Build          string `toml:"command_build"`
	Stop           string `toml:"command_stop"` // for services the PGID kill cannot reach, e.g. docker stop
	HealthPath     string `toml:"health_path"`
	RouteMode      string `toml:"route_mode"` // "off" skips even resolving the portless binary, so manifests that omit it behave exactly as before this field existed
	RouteName      string `toml:"route_name"` // the stable name OAuth callbacks and CORS rules need, because a route address must not follow a branch
}

const (
	RouteModeOff = "off"
	// RouteModeAuto derives the name from the branch, so two worktrees on the same branch collide.
	RouteModeAuto  = "auto"
	RouteModeNamed = "named"
)

func (m *Manifest) EffectiveRouteMode() string {
	switch m.RouteMode {
	case RouteModeAuto, RouteModeNamed, RouteModeOff:
		return m.RouteMode
	default:
		return RouteModeOff // invalid: Validate rejects it upstream, never silently defaulted
	}
}

const (
	PortModeFixed   = "fixed"
	PortModeDynamic = "dynamic"
	PortModeNone    = "none"
)

// port = 0 stays a silent alias of none: manifests written before port_mode existed must keep working.
func (m *Manifest) EffectivePortMode() string {
	switch m.PortMode {
	case PortModeDynamic, PortModeNone, PortModeFixed:
		return m.PortMode
	case "":
		if m.Port == 0 {
			return PortModeNone
		}
		return PortModeFixed
	default:
		return m.PortMode // invalid: Validate rejects it upstream, never silently defaulted
	}
}

// HasPort: in dynamic mode the port is only a default that vroom has not reserved yet.
func (m *Manifest) HasPort() bool {
	return m.EffectivePortMode() != PortModeNone && m.Port > 0
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
	switch m.PortMode {
	case "", PortModeFixed, PortModeDynamic, PortModeNone:
	default:
		return fmt.Errorf("port_mode must be %q, %q or %q, got %q",
			PortModeFixed, PortModeDynamic, PortModeNone, m.PortMode)
	}
	if m.EffectivePortMode() == PortModeDynamic && m.Port == 0 {
		return fmt.Errorf("port_mode = %q requires a default port > 0 (it is the app's PORT=${PORT:-N} fallback)", PortModeDynamic)
	}
	if m.HealthPath != "" && !m.HasPort() {
		return fmt.Errorf("health_path requires a port in every mode but %q; declare port > 0 or drop health_path", PortModeNone)
	}
	if m.SecondaryGroup == ReservedSecondaryGroup {
		return fmt.Errorf("secondary_group %q is reserved for orchestration stacks", ReservedSecondaryGroup)
	}
	switch m.RouteMode {
	case "", RouteModeOff, RouteModeAuto, RouteModeNamed:
	default:
		return fmt.Errorf("route_mode must be %q, %q or %q, got %q",
			RouteModeOff, RouteModeAuto, RouteModeNamed, m.RouteMode)
	}
	if m.RouteName != "" && m.RouteMode != RouteModeNamed {
		// rejected because otherwise it is either ignored or applied by accident.
		return fmt.Errorf("route_name requires route_mode = %q, got %q",
			RouteModeNamed, m.RouteMode)
	}
	if m.EffectiveRouteMode() != RouteModeOff && !m.HasPort() {
		// a route points at a port, so without one vroom could not honour the promise.
		return fmt.Errorf("route_mode = %q requires a port in every mode but %q; declare port > 0 or drop route_mode",
			m.EffectiveRouteMode(), PortModeNone)
	}
	return nil
}
