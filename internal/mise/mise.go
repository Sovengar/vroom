// Package mise discovers the [tasks.*] of a mise.toml by parsing the file only; listing never needs the mise binary, running a task always does.
package mise

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

const FileName = "mise.toml"

type Task struct {
	Name        string
	Description string
}

type taskDef struct {
	Description string `toml:"description"`
	Hide        bool   `toml:"hide"`
}

type miseConfig struct {
	Tasks map[string]taskDef `toml:"tasks"`
}

func HasMiseToml(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, FileName))
	return err == nil
}

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
