// Package manifest parses and validates .vroom.toml files; port is the app's PORT=${PORT:-N} default, never the port vroom assigns in dynamic mode (see docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const FileName = ".vroom.toml"

const ReservedSecondaryGroup = "Composers"

// Manifest is one .vroom.toml: TOML, the Go fields and the vroom list JSON all speak commands.*, so every surface names the same thing the same way (hard rename, no alias).
type Manifest struct {
	Name           string   `toml:"name"`
	PrimaryGroup   string   `toml:"primary_group"`
	SecondaryGroup string   `toml:"secondary_group"`
	Port           int      `toml:"port"` // the app's default port, never the one vroom assigns in dynamic mode
	PortMode       string   `toml:"port_mode"`
	ProcessPattern string   `toml:"process_pattern"`
	HealthPath     string   `toml:"health_path"`
	RouteMode      string   `toml:"route_mode"` // "off" skips even resolving the portless binary, so manifests that omit it behave exactly as before this field existed
	RouteName      string   `toml:"route_name"` // the stable name OAuth callbacks and CORS rules need, because a route address must not follow a branch
	Commands       Commands `toml:"commands"`
}

// Commands is the [commands] section: everything vroom runs for this project. Start is a StartCommand because its hooks are part of how the service starts, not commands of their own.
type Commands struct {
	Start   StartCommand `toml:"start"`
	Build   Runnable     `toml:"build"`
	Install Runnable     `toml:"install"`
	Stop    Runnable     `toml:"stop"` // for services the PGID kill cannot reach, e.g. docker stop
}

// Runnable is one entry of the section: [commands.<kind>] run = "...".
type Runnable struct {
	Run string `toml:"run"`
}

type StartCommand struct {
	Run   string     `toml:"run"`
	Hooks StartHooks `toml:"hooks"`
}

// StartHooks: pre_run is fail-fast, because a prerequisite it could not satisfy must not be papered over by a service that starts anyway. post_run only warns — by the time it runs the service is already alive, and killing it over a post-hook would surprise more than it protects.
type StartHooks struct {
	PreRun  string `toml:"pre_run"`
	PostRun string `toml:"post_run"`
}

const (
	RouteModeOff = "off"
	// RouteModeAuto derives the name from the branch, so two worktrees on the same branch collide.
	RouteModeAuto = "auto"
	// RouteModeNamedWithAutoFallback claims route_name first and, when another worktree already holds it, falls back to <branch>.<project> instead of publishing no URL at all: the first worktree to start keeps the stable name and every other one still gets an address.
	RouteModeNamedWithAutoFallback = "named_with_auto_fallback"
)

func (m *Manifest) EffectiveRouteMode() string {
	switch m.RouteMode {
	case RouteModeAuto, RouteModeNamedWithAutoFallback, RouteModeOff:
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

// movedKeys are the pre-[commands] names. Rejecting them turns "the manifest silently lost its build command" into an error that names the new key — the fleet's manifests are being migrated one by one.
var movedKeys = map[string]string{
	"command_start":     "commands.start.run",
	"command_pre_start": "commands.start.hooks.pre_run",
	"command_build":     "commands.build.run",
	"command_install":   "commands.install.run",
	"command_stop":      "commands.stop.run",
}

func Parse(path string) (*Manifest, error) {
	var m Manifest
	md, err := toml.DecodeFile(path, &m)
	if err != nil {
		return nil, fmt.Errorf("invalid manifest %s: %w", path, err)
	}
	if err := checkKeys(md); err != nil {
		return nil, fmt.Errorf("invalid manifest %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest %s: %w", path, err)
	}
	return &m, nil
}

// checkKeys rejects the two mistakes TOML makes SILENT: a key that moved (a pre-[commands] command_*) and a top-level key swallowed by a [commands.*] header — `port` written after that header decodes as commands.start.port, so the service would keep parsing while its port contract vanished. Unknown keys anywhere else stay ignored on purpose (manifests written before a field existed must keep working).
func checkKeys(md toml.MetaData) error {
	for _, key := range md.Undecoded() {
		name := key.String()
		if to, ok := movedKeys[name]; ok {
			return fmt.Errorf("%s moved to %s (hard rename, no alias)", name, to)
		}
		if strings.HasPrefix(name, "commands.") {
			return fmt.Errorf("unknown key %q inside [commands]: only run and hooks live there; if this is a top-level key, it was written after a [commands.*] header — put the [commands] tables last or use commands.start.run = \"...\"", name)
		}
	}
	return nil
}

func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, FileName))
	return err == nil
}

func (m *Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("missing required field: name")
	}
	if m.Commands.Start.Run == "" {
		return fmt.Errorf("missing required field: commands.start.run")
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
	case "", RouteModeOff, RouteModeAuto, RouteModeNamedWithAutoFallback:
	default:
		return fmt.Errorf("route_mode must be %q, %q or %q, got %q",
			RouteModeOff, RouteModeAuto, RouteModeNamedWithAutoFallback, m.RouteMode)
	}
	if m.RouteName != "" && m.RouteMode != RouteModeNamedWithAutoFallback {
		// rejected because otherwise it is either ignored or applied by accident.
		return fmt.Errorf("route_name requires route_mode = %q, got %q",
			RouteModeNamedWithAutoFallback, m.RouteMode)
	}
	if m.EffectiveRouteMode() != RouteModeOff && !m.HasPort() {
		// a route points at a port, so without one vroom could not honour the promise.
		return fmt.Errorf("route_mode = %q requires a port in every mode but %q; declare port > 0 or drop route_mode",
			m.EffectiveRouteMode(), PortModeNone)
	}
	return nil
}
