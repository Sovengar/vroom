// Package manifest parsea y valida manifiestos .vroom.toml.
//
// Schema (campos en inglés, campos desconocidos se ignoran):
//
//	name            string  requerido
//	primary_group   string  default "" (nivel superior de agrupación)
//	secondary_group string  default "" (nivel interno; solo con primary_group)
//	command_start   string  requerido
//	port            int     default 0 (0 = deshabilitado, si no 1-65535)
//	process_pattern string  default ""
//	command_install string  default "" (comando one-shot de la tecla i)
//	command_build   string  default "" (comando one-shot de la tecla b)
//	command_stop    string  default "" (parada graciosa de la tecla s;
//	                        para servicios donde matar el PGID no basta,
//	                    ej. `docker stop ...`; se ejecuta antes del
//	                    SIGTERM/SIGKILL de limpieza)
//	health_path     string  default "/" (ruta HTTP de la tab Health;
//	                        requiere port > 0)
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
	Port           int    `toml:"port"`
	ProcessPattern string `toml:"process_pattern"`
	Install        string `toml:"command_install"` // comando one-shot (tecla i); puede ser `mise run install`
	Build          string `toml:"command_build"`   // comando one-shot (tecla b); puede ser `mise run build`
	Stop           string `toml:"command_stop"`    // parada graciosa opcional: corre antes del SIGTERM/SIGKILL al PGID
	HealthPath     string `toml:"health_path"`     // ruta HTTP del probe de la tab Health ("" = "/")
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
// port debe ser 0 o 1-65535, secondary_group no puede ser "Composers".
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
	if m.SecondaryGroup == ReservedSecondaryGroup {
		return fmt.Errorf("secondary_group %q is reserved for orchestration stacks", ReservedSecondaryGroup)
	}
	return nil
}
