package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestFindProjectUniqueName(t *testing.T) {
	p, err := findProject(wtProjects(), "repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "/repo" {
		t.Errorf("path = %q, want /repo", p.Path)
	}
}

func TestFindProjectDuplicateNameActionable(t *testing.T) {
	_, err := findProject(wtProjects(), "api", "")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `ambiguous project name "api"`) {
		t.Errorf("message without the name: %q", msg)
	}
	if !strings.Contains(msg, "/repo-wt/a") || !strings.Contains(msg, "/repo-wt/b") {
		t.Errorf("message must list candidate paths: %q", msg)
	}
	if !strings.Contains(msg, "--path") {
		t.Errorf("message must suggest --path: %q", msg)
	}
}

func TestFindProjectPositionalPath(t *testing.T) {
	p, err := findProject(wtProjects(), "/repo-wt/a", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "/repo-wt/a" {
		t.Errorf("path = %q, want /repo-wt/a", p.Path)
	}
}

func TestFindProjectPathFlag(t *testing.T) {
	p, err := findProject(wtProjects(), "api", "/repo-wt/b")
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "/repo-wt/b" {
		t.Errorf("path = %q, want /repo-wt/b", p.Path)
	}
}

func TestFindProjectUnknownPath(t *testing.T) {
	_, err := findProject(wtProjects(), "/no/existe", "")
	if err == nil || !strings.Contains(err.Error(), "project not found") {
		t.Fatalf("err = %v, want project not found", err)
	}
}

func TestFindProjectResolvesSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "proj")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("could not create symlink: %v", err)
	}

	projects := []scanner.Project{
		{Path: real, Name: "proj", Configured: true, Manifest: &manifest.Manifest{Name: "proj"}},
	}
	if p, err := findByPath(projects, link); err != nil || p.Path != real {
		t.Errorf("findByPath(%q) = (%+v, %v), want path %q", link, p, err, real)
	}

	symProjects := []scanner.Project{{Path: link, Name: "proj", Configured: true}}
	if p, err := findByPath(symProjects, real); err != nil || p.Path != link {
		t.Errorf("findByPath(%q) = (%+v, %v), want path %q", real, p, err, link)
	}
}

func TestExtractPathFlag(t *testing.T) {
	rest, path, err := extractPathFlag([]string{"api", "--path", "/repo-wt/b", "--tail", "50"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/repo-wt/b" {
		t.Errorf("path = %q", path)
	}
	want := []string{"api", "--tail", "50"}
	if strings.Join(rest, " ") != strings.Join(want, " ") {
		t.Errorf("rest = %v, want %v", rest, want)
	}
}

func TestExtractPathFlagEqualsForm(t *testing.T) {
	rest, path, err := extractPathFlag([]string{"--path=/repo-wt/a", "api"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/repo-wt/a" {
		t.Errorf("path = %q, want /repo-wt/a", path)
	}
	if len(rest) != 1 || rest[0] != "api" {
		t.Errorf("rest = %v, want [api]", rest)
	}
}

func TestExtractPathFlagEdgeCases(t *testing.T) {
	cases := [][]string{
		{"api", "--path"},
		{"api", "--path", "--tail"}, // a flag is never taken as the value
		{"api", "--path="},
		{"api", "--path", ""},
		{"api", "--path", "/a", "--path", "/b"},
	}
	for _, args := range cases {
		if _, _, err := extractPathFlag(args); err == nil {
			t.Errorf("extractPathFlag(%v) must return error", args)
		}
	}
}

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
		t.Errorf("unexpected relation fields: %+v", info)
	}

	bare := scanner.Project{Path: "/bare", Name: "bare", IsBareContainer: true}
	binfo := buildProjectInfo(manager, store, map[string]bool{}, bare)
	if !binfo.BareContainer || binfo.Configured {
		t.Errorf("unexpected bare container: %+v", binfo)
	}

	// The array stays flat and relation fields are additive, so existing agent consumers keep parsing it.
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
		t.Errorf("JSON without relation fields: %v", entry)
	}
	if _, ok := entry["bare_container"]; ok {
		t.Errorf("bare_container must not appear when false: %v", entry)
	}
}

func TestListExposesWorktreeError(t *testing.T) {
	manager := process.NewManager()
	store := state.NewStoreAt(t.TempDir())

	p := scanner.Project{
		Path: "/repo", Name: "repo", Configured: true,
		Manifest:    &manifest.Manifest{Name: "repo", Command: "echo"},
		WorktreeErr: "git binary not available",
	}
	info := buildProjectInfo(manager, store, map[string]bool{}, p)
	if info.WorktreeErr != "git binary not available" {
		t.Errorf("WorktreeErr not propagated: %+v", info)
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"worktree_error":"git binary not available"`) {
		t.Errorf("JSON without worktree_error: %s", data)
	}

	clean := buildProjectInfo(manager, store, map[string]bool{}, scanner.Project{
		Path: "/x", Name: "x", Configured: true, Manifest: &manifest.Manifest{Name: "x"},
	})
	cleanData, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cleanData), "worktree_error") {
		t.Errorf("worktree_error must not appear without error: %s", cleanData)
	}
}
