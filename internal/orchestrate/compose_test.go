package orchestrate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSingleStack(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
[[stack]]
name = "vsocial-full"
primary_group = "vsocial"

  [[stack.stage]]
  name = "infrastructure"
  services = ["db", "camunda-server"]

  [[stack.stage]]
  name = "backend"
  services = ["actuacions-api", "agenda-api"]
`)

	cf, err := ParseComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cf.Stacks) != 1 {
		t.Fatalf("stacks = %d, want 1", len(cf.Stacks))
	}
	s := cf.Stacks[0]
	if s.Name != "vsocial-full" {
		t.Errorf("name = %q", s.Name)
	}
	if s.PrimaryGroup != "vsocial" {
		t.Errorf("primary_group = %q", s.PrimaryGroup)
	}
	if len(s.Stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(s.Stages))
	}
	if s.Stages[0].Name != "infrastructure" {
		t.Errorf("stage 0 name = %q", s.Stages[0].Name)
	}
	if len(s.Stages[0].Services) != 2 {
		t.Errorf("stage 0 services = %d, want 2", len(s.Stages[0].Services))
	}
	if s.Stages[1].Name != "backend" {
		t.Errorf("stage 1 name = %q", s.Stages[1].Name)
	}
}

func TestParseMultipleStacks(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
[[stack]]
name = "full"
primary_group = "vsocial"

  [[stack.stage]]
  name = "all"
  services = ["api", "web"]

[[stack]]
name = "minimal"
primary_group = "vsocial"

  [[stack.stage]]
  name = "all"
  services = ["api"]
`)

	cf, err := ParseComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cf.Stacks) != 2 {
		t.Fatalf("stacks = %d, want 2", len(cf.Stacks))
	}
	if cf.Stacks[0].Name != "full" {
		t.Errorf("stack 0 name = %q", cf.Stacks[0].Name)
	}
	if cf.Stacks[1].Name != "minimal" {
		t.Errorf("stack 1 name = %q", cf.Stacks[1].Name)
	}
}

func TestParseMissingName(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
[[stack]]
primary_group = "vsocial"

  [[stack.stage]]
  name = "all"
  services = ["api"]
`)

	_, err := ParseComposeFile(dir)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestParseMissingPrimaryGroup(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
[[stack]]
name = "test"

  [[stack.stage]]
  name = "all"
  services = ["api"]
`)

	_, err := ParseComposeFile(dir)
	if err == nil {
		t.Fatal("expected error for missing primary_group")
	}
}

func TestParseTopLevelPrimaryGroup(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
primary_group = "vsocial"

[[stack]]
name = "full"

  [[stack.stage]]
  name = "all"
  services = ["api"]

[[stack]]
name = "minimal"

  [[stack.stage]]
  name = "all"
  services = ["web"]
`)

	cf, err := ParseComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cf.PrimaryGroup != "vsocial" {
		t.Errorf("top-level primary_group = %q", cf.PrimaryGroup)
	}
	for _, s := range cf.Stacks {
		if s.PrimaryGroup != "vsocial" {
			t.Errorf("stack %q primary_group = %q, want vsocial", s.Name, s.PrimaryGroup)
		}
	}
}

func TestParseStackOverridesTopLevel(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
primary_group = "vsocial"

[[stack]]
name = "full"
primary_group = "servers"

  [[stack.stage]]
  name = "all"
  services = ["api"]
`)

	cf, err := ParseComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cf.PrimaryGroup != "vsocial" {
		t.Errorf("top-level primary_group = %q", cf.PrimaryGroup)
	}
	if cf.Stacks[0].PrimaryGroup != "servers" {
		t.Errorf("stack primary_group = %q, want servers (override)", cf.Stacks[0].PrimaryGroup)
	}
}

func TestParseStageNoServices(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
[[stack]]
name = "test"
primary_group = "vsocial"

  [[stack.stage]]
  name = "empty"
  services = []
`)

	_, err := ParseComposeFile(dir)
	if err == nil {
		t.Fatal("expected error for empty services")
	}
}

func TestParseTimeoutDefault(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
[[stack]]
name = "test"
primary_group = "vsocial"

  [[stack.stage]]
  name = "all"
  services = ["api"]
`)

	cf, err := ParseComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Stacks[0].Stages[0].Timeout != DefaultStageTimeout {
		t.Errorf("timeout = %v, want %v", cf.Stacks[0].Stages[0].Timeout, DefaultStageTimeout)
	}
}

func TestParseTimeoutCustom(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
[[stack]]
name = "test"
primary_group = "vsocial"

  [[stack.stage]]
  name = "all"
  services = ["api"]
  timeout = "10s"
`)

	cf, err := ParseComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Stacks[0].Stages[0].Timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", cf.Stacks[0].Stages[0].Timeout)
	}
}

func TestParseFileNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := ParseComposeFile(dir)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFindStack(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
[[stack]]
name = "a"
primary_group = "g1"

  [[stack.stage]]
  name = "s1"
  services = ["api"]

[[stack]]
name = "b"
primary_group = "g2"

  [[stack.stage]]
  name = "s1"
  services = ["web"]

[[stack]]
name = "b"
primary_group = "g3"

  [[stack.stage]]
  name = "s1"
  services = ["other"]
`)

	cf, err := ParseComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Formato simple: nombre único
	s, err := cf.FindStack("a")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "a" || s.PrimaryGroup != "g1" {
		t.Errorf("found wrong stack: %+v", s)
	}

	// Formato compuesto: group/name
	s, err = cf.FindStack("g2/b")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "b" || s.PrimaryGroup != "g2" {
		t.Errorf("found wrong stack: %+v", s)
	}

	// Formato simple ambiguo: mismo nombre en dos groups → error
	_, err = cf.FindStack("b")
	if err == nil {
		t.Fatal("expected ambiguity error for stack 'b' in two groups")
	}

	// Formato compuesto inexistente
	_, err = cf.FindStack("g99/x")
	if err == nil {
		t.Fatal("expected error for missing stack")
	}

	// Formato simple inexistente
	_, err = cf.FindStack("c")
	if err == nil {
		t.Fatal("expected error for missing stack")
	}
}

func TestParseInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ComposeFileName), []byte(`[[stack]] invalid toml {{{`), 0o644)
	_, err := ParseComposeFile(dir)
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
}

func TestParseNoStages(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `
[[stack]]
name = "empty"
primary_group = "vsocial"
`)

	_, err := ParseComposeFile(dir)
	if err == nil {
		t.Fatal("expected error for stack with no stages")
	}
}

func writeCompose(t *testing.T, dir, content string) {
	t.Helper()
	_ = os.WriteFile(filepath.Join(dir, ComposeFileName), []byte(content), 0o644)
}
