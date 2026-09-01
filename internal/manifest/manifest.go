// Package manifest parsea y valida manifiestos .vroom.toml.
//
// Schema (campos en inglés, campos desconocidos se ignoran):
//
//	name            string  requerido
//	group           string  default ""
//	command         string  requerido
//	port            int     default 0 (0 = deshabilitado, si no 1-65535)
//	process_pattern string  default ""
package manifest

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// FileName es el nombre del fichero de manifiesto por proyecto.
const FileName = ".vroom.toml"

// Manifest representa el contenido de un .vroom.toml.
type Manifest struct {
	Name           string `toml:"name"`
	Group          string `toml:"group"`
	Command        string `toml:"command"`
	Port           int    `toml:"port"`
	ProcessPattern string `toml:"process_pattern"`
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

// Validate aplica las reglas del schema: name y command requeridos,
// port debe ser 0 o 1-65535.
func (m *Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("missing required field: name")
	}
	if m.Command == "" {
		return fmt.Errorf("missing required field: command")
	}
	if m.Port < 0 || m.Port > 65535 {
		return fmt.Errorf("port must be 0 (disabled) or 1-65535, got %d", m.Port)
	}
	return nil
}
