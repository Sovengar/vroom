// Package orchestrate turns .vroom-compose.toml stacks into sequential stages of parallel services, resolving every name against the scanned projects and failing on ambiguity instead of guessing.
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

type ComposeFile struct {
	PrimaryGroup string  // file-level fallback, required at one of the two levels
	Stacks       []Stack `toml:"stack"`
}

type Stack struct {
	Name         string  `toml:"name"`
	PrimaryGroup string  `toml:"primary_group"` // overrides the file-level default
	Stages       []Stage `toml:"stage"`
}

type Stage struct {
	Name     string        `toml:"name"`
	Services []string      `toml:"services"`
	Timeout  time.Duration `toml:"timeout"`
}

// StageRaw parses the timeout as a string because TOML durations need an explicit time.ParseDuration.
type StageRaw struct {
	Name     string   `toml:"name"`
	Services []string `toml:"services"`
	Timeout  string   `toml:"timeout"`
}

type StackRaw struct {
	Name         string     `toml:"name"`
	PrimaryGroup string     `toml:"primary_group"`
	Stages       []StageRaw `toml:"stage"`
}

type ComposeFileRaw struct {
	PrimaryGroup string     `toml:"primary_group"`
	Stacks       []StackRaw `toml:"stack"`
}

const DefaultStageTimeout = 30 * time.Second

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

// FindStack accepts "group/name" or a bare name, but a bare name that repeats across groups is an error, never a silent pick of one.
func (cf *ComposeFile) FindStack(ref string) (*Stack, error) {
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
