// Package agents resolves the agent list for the ask AI action; {prompt} expands to exactly one argv element so prompts with spaces or quotes need no shell.
package agents

import (
	"os/exec"
	"strings"

	"vroom/internal/config"
)

type Agent struct {
	Name string
	Cmd  []string
}

const promptPlaceholder = "{prompt}"

// jcode uses run because its interactive TUI rejects an initial prompt.
func Builtins() []Agent {
	return []Agent{
		{Name: "opencode", Cmd: []string{"opencode", "--prompt", promptPlaceholder}},
		{Name: "pi", Cmd: []string{"pi", promptPlaceholder}},
		{Name: "hermes", Cmd: []string{"hermes", "chat", "-q", promptPlaceholder}},
		{Name: "jcode", Cmd: []string{"jcode", "run", promptPlaceholder}},
	}
}

func Resolve(overrides map[string]config.AgentConfig) []Agent {
	if len(overrides) == 0 {
		return Builtins()
	}
	// Sorted so the picker order is stable across runs.
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sortStrings(names)
	out := make([]Agent, 0, len(names))
	for _, name := range names {
		out = append(out, Agent{Name: name, Cmd: strings.Fields(overrides[name].Cmd)})
	}
	return out
}

func Available(list []Agent) []Agent {
	var out []Agent
	for _, a := range list {
		if len(a.Cmd) == 0 {
			continue
		}
		if _, err := exec.LookPath(a.Cmd[0]); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// An empty prompt drops the preceding flag too, else the agent gets a dangling flag.
func (a Agent) BuildArgs(prompt string) []string {
	args := make([]string, 0, len(a.Cmd))
	for _, part := range a.Cmd {
		if part == promptPlaceholder {
			if prompt == "" && len(args) > 0 && strings.HasPrefix(args[len(args)-1], "-") {
				args = args[:len(args)-1]
			}
			if prompt != "" {
				args = append(args, prompt)
			}
			continue
		}
		args = append(args, part)
	}
	return args
}

// Insertion sort instead of sort.Slice, to keep the import list empty.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
