package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"vroom/internal/config"
)

// Resolve sin overrides devuelve los 4 built-in.
func TestResolveBuiltins(t *testing.T) {
	got := Resolve(nil)
	if len(got) != 4 {
		t.Fatalf("agents = %d, want 4", len(got))
	}
	if got[0].Name != "opencode" || got[1].Name != "pi" || got[2].Name != "hermes" || got[3].Name != "jcode" {
		t.Errorf("orden/identidad incorrecta: %+v", got)
	}
	if !reflect.DeepEqual(got[2].Cmd, []string{"hermes", "chat", "-q", "{prompt}"}) {
		t.Errorf("cmd hermes = %v", got[2].Cmd)
	}
	// jcode: TUI sin prompt inicial → run one-shot con el prompt.
	if !reflect.DeepEqual(got[3].Cmd, []string{"jcode", "run", "{prompt}"}) {
		t.Errorf("cmd jcode = %v", got[3].Cmd)
	}
}

// Con overrides, la sección reemplaza los built-in (orden alfabético).
func TestResolveOverrides(t *testing.T) {
	got := Resolve(map[string]config.AgentConfig{
		"claude": {Cmd: "claude {prompt}"},
		"codex":  {Cmd: "codex --full-auto {prompt}"},
	})
	if len(got) != 2 {
		t.Fatalf("agents = %d, want 2 (override reemplaza)", len(got))
	}
	if got[0].Name != "claude" || got[1].Name != "codex" {
		t.Errorf("orden alfabético roto: %+v", got)
	}
	if !reflect.DeepEqual(got[1].Cmd, []string{"codex", "--full-auto", "{prompt}"}) {
		t.Errorf("cmd codex = %v", got[1].Cmd)
	}
}

// Available filtra por exec.LookPath del primer token.
func TestAvailable(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "fake-agent"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	list := []Agent{
		{Name: "fake-agent", Cmd: []string{"fake-agent", "{prompt}"}},
		{Name: "ghost", Cmd: []string{"no-existe-xyz", "{prompt}"}},
	}
	got := Available(list)
	if len(got) != 1 || got[0].Name != "fake-agent" {
		t.Errorf("Available = %+v, want solo fake-agent", got)
	}
}

// BuildArgs: {prompt} ocupa un argumento completo; vacío lo elimina.
func TestBuildArgs(t *testing.T) {
	a := Agent{Name: "opencode", Cmd: []string{"opencode", "--prompt", "{prompt}"}}

	got := a.BuildArgs("fix the bug' && rm -rf /")
	want := []string{"opencode", "--prompt", "fix the bug' && rm -rf /"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildArgs = %q, want %q", got, want)
	}

	if got := a.BuildArgs(""); !reflect.DeepEqual(got, []string{"opencode"}) {
		t.Errorf("BuildArgs vacío = %q, want sin placeholder", got)
	}

	// Placeholder en cualquier posición.
	b := Agent{Name: "pi", Cmd: []string{"pi", "{prompt}", "--yolo"}}
	if got := b.BuildArgs("hola"); !reflect.DeepEqual(got, []string{"pi", "hola", "--yolo"}) {
		t.Errorf("BuildArgs = %q", got)
	}
}
