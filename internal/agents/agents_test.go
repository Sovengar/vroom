package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sovengar/agentbridge"

	"vroom/internal/config"
)

func TestResolveBuiltinsComeFromTheSharedCatalog(t *testing.T) {
	got := Resolve(nil)
	catalog := agentbridge.Builtins()
	if len(got) != len(catalog) {
		t.Fatalf("agents = %d, want the %d agentbridge builtins", len(got), len(catalog))
	}
	byName := map[string]Agent{}
	for _, a := range got {
		byName[a.Name] = a
	}
	for _, a := range got {
		// The mode is derived, not declared: an agent with no interactive prompt
		// runs in its one-shot form, and both facts come from the shared table.
		want := agentbridge.CapabilitiesFor(a.Name).NoInteractivePrompt
		if a.Headless != want {
			t.Errorf("%s: headless = %v, want %v", a.Name, a.Headless, want)
		}
		if argv := a.BuildArgs("p"); len(argv) == 0 || argv[0] != a.Name {
			t.Errorf("%s: argv = %v", a.Name, argv)
		}
	}
	// The two agents whose interactive UI takes no initial prompt stay headless.
	for _, name := range []string{"hermes", "jcode"} {
		if a, ok := byName[name]; !ok || !a.Headless {
			t.Errorf("%s must be headless: %+v", name, byName[name])
		}
	}
}

func TestResolveOverrides(t *testing.T) {
	got := Resolve(map[string]config.AgentConfig{
		"claude": {Cmd: "claude {prompt}"},
		"codex":  {Cmd: "codex --full-auto {prompt}"},
	})
	if len(got) != 2 {
		t.Fatalf("agents = %d, want 2 (override replaces)", len(got))
	}
	if got[0].Name != "claude" || got[1].Name != "codex" {
		t.Errorf("alphabetical order broken: %+v", got)
	}
	if !reflect.DeepEqual(got[1].Cmd, []string{"codex", "--full-auto", "{prompt}"}) {
		t.Errorf("cmd codex = %v", got[1].Cmd)
	}
}

func TestAvailable(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "fake-agent"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	list := []Agent{
		{Name: "fake-agent"},
		{Name: "ghost", Cmd: []string{"does-not-exist-xyz", "{prompt}"}},
	}
	got := Available(list)
	if len(got) != 1 || got[0].Name != "fake-agent" {
		t.Errorf("Available = %+v, want only fake-agent", got)
	}
}

func TestBuildArgs(t *testing.T) {
	a := Agent{Name: "opencode", Cmd: []string{"opencode", "--prompt", "{prompt}"}}

	got := a.BuildArgs("fix the bug' && rm -rf /")
	want := []string{"opencode", "--prompt", "fix the bug' && rm -rf /"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildArgs = %q, want %q", got, want)
	}

	if got := a.BuildArgs(""); !reflect.DeepEqual(got, []string{"opencode"}) {
		t.Errorf("empty BuildArgs = %q, want without placeholder", got)
	}

	b := Agent{Name: "pi", Cmd: []string{"pi", "{prompt}", "--yolo"}}
	if got := b.BuildArgs("hello"); !reflect.DeepEqual(got, []string{"pi", "hello", "--yolo"}) {
		t.Errorf("BuildArgs = %q", got)
	}
}
