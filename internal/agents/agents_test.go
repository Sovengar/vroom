package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"vroom/internal/config"
)

func TestResolveBuiltins(t *testing.T) {
	got := Resolve(nil)
	if len(got) != 4 {
		t.Fatalf("agents = %d, want 4", len(got))
	}
	if got[0].Name != "opencode" || got[1].Name != "pi" || got[2].Name != "hermes" || got[3].Name != "jcode" {
		t.Errorf("wrong order/identity: %+v", got)
	}
	if !reflect.DeepEqual(got[2].Cmd, []string{"hermes", "chat", "-q", "{prompt}"}) {
		t.Errorf("cmd hermes = %v", got[2].Cmd)
	}
	// jcode is a TUI with no initial prompt, so it needs the one-shot run form to receive it.
	if !reflect.DeepEqual(got[3].Cmd, []string{"jcode", "run", "{prompt}"}) {
		t.Errorf("cmd jcode = %v", got[3].Cmd)
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
		{Name: "fake-agent", Cmd: []string{"fake-agent", "{prompt}"}},
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
