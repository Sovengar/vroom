// Package mise descubre tasks definidos en la sección [tasks.*] de un
// mise.toml (spec 0003 R28).
//
// El acoplamiento con mise es opcional: listar tasks solo parsea el
// fichero (no requiere el binario); ejecutar un task lanza
// `mise run <name>`, que sí lo necesita.
package mise

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// FileName es el fichero de configuración de mise que se parsea.
const FileName = "mise.toml"

// Task es un task ejecutable de mise.
type Task struct {
	Name        string
	Description string
}

// taskDef es la definición declarativa de un task en mise.toml. Los
// campos que no nos interesan (run, depends, env, ...) se ignoran.
type taskDef struct {
	Description string `toml:"description"`
	Hide        bool   `toml:"hide"`
}

type miseConfig struct {
	Tasks map[string]taskDef `toml:"tasks"`
}

// HasMiseToml reporta si dir contiene un mise.toml.
func HasMiseToml(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, FileName))
	return err == nil
}

// Tasks parsea los tasks del mise.toml de dir, ordenados alfabéticamente
// y sin los marcados con hide = true.
func Tasks(dir string) ([]Task, error) {
	path := filepath.Join(dir, FileName)
	var cfg miseConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("invalid mise.toml %s: %w", path, err)
	}
	names := make([]string, 0, len(cfg.Tasks))
	for name, def := range cfg.Tasks {
		if def.Hide {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Task, 0, len(names))
	for _, name := range names {
		out = append(out, Task{Name: name, Description: cfg.Tasks[name].Description})
	}
	return out, nil
}
