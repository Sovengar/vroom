// Package agents resolves the agent list for the ask AI action. The argv of a
// builtin agent comes from github.com/sovengar/ai, so this package holds no
// per-agent flag knowledge; only a user override keeps an explicit command.
package agents

import (
	"os/exec"
	"strings"

	"github.com/sovengar/agentbridge"

	"vroom/internal/config"
)

type Agent struct {
	Name string
	// Cmd is a user override. When empty the argv is built from the shared ai
	// capability table.
	Cmd []string
	// Headless marks an agent whose interactive UI takes no initial prompt
	// (hermes chat, jcode), so the prompt goes to the one-shot form instead.
	Headless bool
}

const promptPlaceholder = "{prompt}"

// Builtins returns the agents vroom knows without any config.
func Builtins() []Agent {
	return []Agent{
		{Name: "opencode"},
		{Name: "pi"},
		{Name: "hermes", Headless: true},
		{Name: "jcode", Headless: true},
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

// Binary is the executable the agent runs, from its override or its name.
func (a Agent) Binary() string {
	if len(a.Cmd) > 0 {
		return a.Cmd[0]
	}
	return a.Name
}

func Available(list []Agent) []Agent {
	var out []Agent
	for _, a := range list {
		binary := a.Binary()
		if binary == "" {
			continue
		}
		if _, err := exec.LookPath(binary); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// BuildArgs returns the argv for one run. An override is expanded around
// {prompt}; a builtin is resolved by the ai module, and an empty prompt never
// leaves a dangling flag or an empty argument behind.
func (a Agent) BuildArgs(prompt string) []string {
	if len(a.Cmd) > 0 {
		return expand(a.Cmd, prompt)
	}
	req := agentbridge.Request{
		Agent:  agentbridge.Agent{Name: a.Name, Binary: a.Name},
		Prompt: prompt,
	}
	if a.Headless {
		argv, err := agentbridge.HeadlessArgs(req)
		if err != nil {
			return []string{a.Name}
		}
		return argv
	}
	argv := agentbridge.InteractiveArgs(req)
	if prompt == "" {
		return trimTrailingEmpty(argv)
	}
	return argv
}

// expand fills the {prompt} placeholder. An empty prompt drops the preceding
// flag too, else the agent gets a dangling flag.
func expand(cmd []string, prompt string) []string {
	args := make([]string, 0, len(cmd))
	for _, part := range cmd {
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

// trimTrailingEmpty removes the empty prompt argument the shared builder adds
// for a positional-prompt agent.
func trimTrailingEmpty(argv []string) []string {
	if len(argv) > 0 && argv[len(argv)-1] == "" {
		return argv[:len(argv)-1]
	}
	return argv
}

// Insertion sort instead of sort.Slice, to keep the import list empty.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
