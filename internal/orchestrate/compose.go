// Package orchestrate implementa el motor de orquestación de arranque
// de stacks de servicios definidos en .vroom-compose.toml.
//
// Un compose file contiene múltiples stacks, cada uno con etapas
// secuenciales y servicios paralelos. Los servicios se resuelven contra
// los proyectos escaneados por el scanner.
package orchestrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const ComposeFileName = ".vroom-compose.toml"

// ComposeFile representa un fichero .vroom-compose.toml parseado.
type ComposeFile struct {
	PrimaryGroup string  // default para todos los stacks (puede ser sobrescrito)
	Stacks       []Stack `toml:"stack"`
}

// Stack es un grupo de etapas de orquestación.
type Stack struct {
	Name         string  `toml:"name"`
	PrimaryGroup string  `toml:"primary_group"` // override del top-level
	Stages       []Stage `toml:"stage"`
}

// Stage es una etapa con servicios que arrancan en paralelo.
type Stage struct {
	Name     string        `toml:"name"`
	Services []string      `toml:"services"`
	Timeout  time.Duration `toml:"timeout"`
}

// StageRaw se usa para el parsing con timeout como string.
type StageRaw struct {
	Name     string   `toml:"name"`
	Services []string `toml:"services"`
	Timeout  string   `toml:"timeout"`
}

// StackRaw se usa para el parsing con stages como raw.
type StackRaw struct {
	Name         string     `toml:"name"`
	PrimaryGroup string     `toml:"primary_group"`
	Stages       []StageRaw `toml:"stage"`
}

// ComposeFileRaw se usa para el parsing intermedio.
type ComposeFileRaw struct {
	PrimaryGroup string     `toml:"primary_group"`
	Stacks       []StackRaw `toml:"stack"`
}

// DefaultStageTimeout es el timeout por defecto para health checks.
const DefaultStageTimeout = 30 * time.Second

// ParseComposeFile lee y valida un .vroom-compose.toml en dir.
func ParseComposeFile(dir string) (*ComposeFile, error) {
	path := filepath.Join(dir, ComposeFileName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("no %s found in current directory", ComposeFileName)
	}

	var raw ComposeFileRaw
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("invalid compose file %s: %w", path, err)
	}

	cf := &ComposeFile{
		PrimaryGroup: raw.PrimaryGroup,
		Stacks:       make([]Stack, 0, len(raw.Stacks)),
	}
	for i, rs := range raw.Stacks {
		if rs.Name == "" {
			return nil, fmt.Errorf("missing required field: name (stack #%d)", i+1)
		}
		// Resolver primary_group: stack override → top-level default → error
		pg := rs.PrimaryGroup
		if pg == "" {
			pg = cf.PrimaryGroup
		}
		if pg == "" {
			return nil, fmt.Errorf("missing primary_group: set it at top level or in stack %q", rs.Name)
		}
		if len(rs.Stages) == 0 {
			return nil, fmt.Errorf("stack %q must have at least one stage", rs.Name)
		}
		stack := Stack{Name: rs.Name, PrimaryGroup: pg}
		for j, rsr := range rs.Stages {
			if rsr.Name == "" {
				return nil, fmt.Errorf("missing required field: name (stage #%d in stack %q)", j+1, rs.Name)
			}
			if len(rsr.Services) == 0 {
				return nil, fmt.Errorf("stage %q in stack %q must have at least one service", rsr.Name, rs.Name)
			}
			timeout := DefaultStageTimeout
			if rsr.Timeout != "" {
				d, err := time.ParseDuration(rsr.Timeout)
				if err != nil {
					return nil, fmt.Errorf("invalid timeout %q in stage %q: %w", rsr.Timeout, rsr.Name, err)
				}
				timeout = d
			}
			stack.Stages = append(stack.Stages, Stage{
				Name:     rsr.Name,
				Services: rsr.Services,
				Timeout:  timeout,
			})
		}
		cf.Stacks = append(cf.Stacks, stack)
	}
	return cf, nil
}

// FindStack busca un stack por "primary_group/name" o solo por nombre.
// Si se pasa solo el nombre y hay ambigüedad (mismo nombre en dos
// groups), devuelve error.
func (cf *ComposeFile) FindStack(ref string) (*Stack, error) {
	// Formato compuesto: "group/name"
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		primary := ref[:i]
		name := ref[i+1:]
		for idx := range cf.Stacks {
			if cf.Stacks[idx].PrimaryGroup == primary && cf.Stacks[idx].Name == name {
				return &cf.Stacks[idx], nil
			}
		}
		return nil, fmt.Errorf("stack %q not found in group %q", name, primary)
	}

	// Formato simple: solo nombre (backward compatible)
	var match *Stack
	for idx := range cf.Stacks {
		if cf.Stacks[idx].Name == ref {
			if match != nil {
				return nil, fmt.Errorf("ambiguous stack name %q: exists in groups %q and %q; use group/name", ref, match.PrimaryGroup, cf.Stacks[idx].PrimaryGroup)
			}
			match = &cf.Stacks[idx]
		}
	}
	if match == nil {
		return nil, fmt.Errorf("stack %q not found", ref)
	}
	return match, nil
}
