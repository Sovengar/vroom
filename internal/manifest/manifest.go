// Package manifest parsea y valida manifiestos .vroom.toml.
//
// Schema (campos en inglés, campos desconocidos se ignoran):
//
//	name            string  requerido
//	primary_group   string  default "" (nivel superior de agrupación)
//	secondary_group string  default "" (nivel interno; solo con primary_group)
//	command_start   string  requerido
//	port            int     default 0; el puerto POR DEFECTO de la app,
//	                        el mismo valor de PORT=${PORT:-N}. En dynamic
//	                        es ese default, no el puerto asignado.
//	port_mode       string  default "fixed"; "fixed" | "dynamic" | "none"
//	process_pattern string  default ""
//	command_install string  default "" (comando one-shot de la tecla i)
//	command_build   string  default "" (comando one-shot de la tecla b)
//	command_stop    string  default "" (parada graciosa de la tecla s;
//	                        para servicios donde matar el PGID no basta,
//	                    ej. `docker stop ...`; se ejecuta antes del
//	                    SIGTERM/SIGKILL de limpieza)
//	health_path     string  default "/" (ruta HTTP de la tab Health;
//	                        requiere un puerto en algún modo)
//	route_mode      string  default "off"; "off" | "auto" | "named"
//	route_name      string  default "" (sólo con route_mode = "named";
//	                        requiere un puerto en algún modo)
package manifest

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// FileName es el nombre del fichero de manifiesto por proyecto.
const FileName = ".vroom.toml"

// ReservedSecondaryGroup es el nombre reservado para el header de stacks
// de orquestación. Los manifiestos no pueden usar este valor.
const ReservedSecondaryGroup = "Composers"

// Manifest representa el contenido de un .vroom.toml. Las claves TOML
// usan el prefijo command_*; los campos Go conservan nombres cortos.
type Manifest struct {
	Name           string `toml:"name"`
	PrimaryGroup   string `toml:"primary_group"`
	SecondaryGroup string `toml:"secondary_group"`
	Command        string `toml:"command_start"`
	Port           int    `toml:"port"`      // puerto por defecto de la app; el real en dynamic
	PortMode       string `toml:"port_mode"` // "fixed" | "dynamic" | "none"; "" = fixed
	ProcessPattern string `toml:"process_pattern"`
	Install        string `toml:"command_install"` // comando one-shot (tecla i); puede ser `mise run install`
	Build          string `toml:"command_build"`   // comando one-shot (tecla b); puede ser `mise run build`
	Stop           string `toml:"command_stop"`    // parada graciosa opcional: corre antes del SIGTERM/SIGKILL al PGID
	HealthPath     string `toml:"health_path"`     // ruta HTTP del probe de la tab Health ("" = "/")
	// RouteMode es la INTENCIÓN de ruta. "off" es el default y significa que
	// vroom ni siquiera busca el binario de portless, así que un manifiesto que
	// no declara nada se comporta exactamente como antes de este campo.
	RouteMode string `toml:"route_mode"` // "off" | "auto" | "named"; "" = off
	// RouteName es el nombre estable de la ruta, sólo con route_mode =
	// "named". Es lo que exigen un callback OAuth o una regla CORS, donde la
	// dirección no puede depender de una rama.
	RouteName string `toml:"route_name"`
}

// Semántica de tres estados de route_mode. El default es off, de modo que
// añadir el campo no molesta a ningún manifiesto existente: con off, vroom no
// toca portless en absoluto.
const (
	// RouteModeOff: sin ruta. vroom no resuelve ni el binario.
	RouteModeOff = "off"
	// RouteModeAuto: nombre derivado de la RAMA, sin escribir nada. Es scope
	// de rama: dos worktrees en la misma rama colisionan.
	RouteModeAuto = "auto"
	// RouteModeNamed: nombre estable y explícito (route_name).
	RouteModeNamed = "named"
)

// EffectiveRouteMode resuelve route_mode con su default: ausente es off.
func (m *Manifest) EffectiveRouteMode() string {
	switch m.RouteMode {
	case RouteModeAuto, RouteModeNamed, RouteModeOff:
		return m.RouteMode
	default:
		return RouteModeOff // inválido: Validate lo rechaza
	}
}

// Semántica de tres estados de port_mode. El default es fixed, de modo que
// un manifiesto que no declara nada se comporta exactamente como antes.
const (
	// PortModeFixed: el servicio usa `port` tal cual. Es el legacy.
	PortModeFixed = "fixed"
	// PortModeDynamic: vroom reserva un puerto libre, lo inyecta como PORT y
	// descubre+verifica el real antes de devolver el control.
	PortModeDynamic = "dynamic"
	// PortModeNone: el servicio no tiene puerto por diseño. Sin espera.
	PortModeNone = "none"
)

// EffectivePortMode resuelve port_mode con su default: ausente es fixed.
// port = 0 sigue siendo un alias silencioso de "none" para no romper
// manifiestos que nunca declararon el campo.
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
		return m.PortMode // inválido: Validate lo rechaza
	}
}

// HasPort dice si este servicio tiene puerto en algún sentido. En fixed es
// el declarado; en dynamic, uno que vroom aún no ha reservado.
func (m *Manifest) HasPort() bool {
	return m.EffectivePortMode() != PortModeNone && m.Port > 0
}

// DefaultHealthPath es la ruta HTTP usada por la tab Health cuando el
// manifiesto no define health_path.
const DefaultHealthPath = "/"

// HealthURLPath devuelve la ruta del probe de salud: health_path o "/".
func (m *Manifest) HealthURLPath() string {
	if m.HealthPath == "" {
		return DefaultHealthPath
	}
	return m.HealthPath
}

// Parse lee y valida el manifiesto en path.
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

// Exists reporta si dir contiene un .vroom.toml.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, FileName))
	return err == nil
}

// Validate aplica las reglas del schema: name y command_start requeridos,
// port debe ser 0 o 1-65535, port_mode debe ser known, secondary_group no
// puede ser "Composers", y health_path exige un puerto en algún modo.
//
// Esa última es la primera regla cross-field del validador: antes existía
// sólo en el doc de arriba, no en el código.
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
		// route_name sin named es un nombre que nadie va a usar: o se ignora
		// en silencio o se aplica sin querer. Se rechaza.
		return fmt.Errorf("route_name requires route_mode = %q, got %q",
			RouteModeNamed, m.RouteMode)
	}
	if m.EffectiveRouteMode() != RouteModeOff && !m.HasPort() {
		// Una ruta apunta a un puerto. Sin puerto no hay nada honesto que
		// apuntar, y aceptarlo sería una promesa que vroom no puede cumplir.
		return fmt.Errorf("route_mode = %q requires a port in every mode but %q; declare port > 0 or drop route_mode",
			m.EffectiveRouteMode(), PortModeNone)
	}
	return nil
}
