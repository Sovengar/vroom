package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

func wtProjects() []scanner.Project {
	return []scanner.Project{
		{Path: "/repo", Name: "repo", Configured: true, Manifest: &manifest.Manifest{Name: "repo", Command: "echo"}},
		{Path: "/repo-wt/a", Name: "a", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "echo"},
			IsWorktree: true, RepoRoot: "/repo"},
		{Path: "/repo-wt/b", Name: "b", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "echo"},
			IsWorktree: true, RepoRoot: "/repo"},
	}
}

// S7: nombre único resuelve como hoy.
func TestFindProjectUniqueName(t *testing.T) {
	p, err := findProject(wtProjects(), "repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "/repo" {
		t.Errorf("path = %q, want /repo", p.Path)
	}
}

// S7: nombre duplicado sin path falla con paths accionables.
func TestFindProjectDuplicateNameActionable(t *testing.T) {
	_, err := findProject(wtProjects(), "api", "")
	if err == nil {
		t.Fatal("esperaba error de ambigüedad")
	}
	msg := err.Error()
	if !strings.Contains(msg, `ambiguous project name "api"`) {
		t.Errorf("mensaje sin el nombre: %q", msg)
	}
	if !strings.Contains(msg, "/repo-wt/a") || !strings.Contains(msg, "/repo-wt/b") {
		t.Errorf("el mensaje debe listar los paths candidatos: %q", msg)
	}
	if !strings.Contains(msg, "--path") {
		t.Errorf("el mensaje debe sugerir --path: %q", msg)
	}
}

// S7: direccionamiento por path posicional.
func TestFindProjectPositionalPath(t *testing.T) {
	p, err := findProject(wtProjects(), "/repo-wt/a", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "/repo-wt/a" {
		t.Errorf("path = %q, want /repo-wt/a", p.Path)
	}
}

// S7: direccionamiento por flag --path (desambigua el nombre).
func TestFindProjectPathFlag(t *testing.T) {
	p, err := findProject(wtProjects(), "api", "/repo-wt/b")
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "/repo-wt/b" {
		t.Errorf("path = %q, want /repo-wt/b", p.Path)
	}
}

// S7: un path no escaneado no resuelve.
func TestFindProjectUnknownPath(t *testing.T) {
	_, err := findProject(wtProjects(), "/no/existe", "")
	if err == nil || !strings.Contains(err.Error(), "project not found") {
		t.Fatalf("err = %v, want project not found", err)
	}
}

// extractPathFlag separa --path del resto sin perder otros flags.
func TestExtractPathFlag(t *testing.T) {
	rest, path := extractPathFlag([]string{"api", "--path", "/repo-wt/b", "--tail", "50"})
	if path != "/repo-wt/b" {
		t.Errorf("path = %q", path)
	}
	want := []string{"api", "--tail", "50"}
	if strings.Join(rest, " ") != strings.Join(want, " ") {
		t.Errorf("rest = %v, want %v", rest, want)
	}
}

// S7: vroom list expone la relación repo/worktree con array plano.
func TestListExposesRelationFlat(t *testing.T) {
	manager := process.NewManager()
	store := state.NewStoreAt(t.TempDir())
	p := scanner.Project{
		Path: "/repo-wt/a", Name: "a", Configured: true,
		Manifest:   &manifest.Manifest{Name: "api", Command: "echo"},
		IsWorktree: true, RepoRoot: "/repo",
	}
	info := buildProjectInfo(manager, store, map[string]bool{}, p)
	if !info.IsWorktree || info.RepoRoot != "/repo" || info.BareContainer {
		t.Errorf("campos de relación inesperados: %+v", info)
	}

	bare := scanner.Project{Path: "/bare", Name: "bare", IsBareContainer: true}
	binfo := buildProjectInfo(manager, store, map[string]bool{}, bare)
	if !binfo.BareContainer || binfo.Configured {
		t.Errorf("contenedor bare inesperado: %+v", binfo)
	}

	// El array sigue siendo plano y los campos son aditivos.
	data, err := json.Marshal(ListResult{Projects: []ProjectInfo{info}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Projects []map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(decoded.Projects))
	}
	entry := decoded.Projects[0]
	if entry["repo_root"] != "/repo" || entry["is_worktree"] != true {
		t.Errorf("JSON sin campos de relación: %v", entry)
	}
	if _, ok := entry["bare_container"]; ok {
		t.Errorf("bare_container no debe aparecer cuando es false: %v", entry)
	}
}
