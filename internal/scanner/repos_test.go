package scanner

import (
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/manifest"
)

func repoManifest(name string, port int) *manifest.Manifest {
	return &manifest.Manifest{
		Name:     name,
		Port:     port,
		Commands: manifest.Commands{Start: manifest.StartCommand{Run: "echo hi"}},
	}
}

// The config entry REPLACES the file: the name and port the repo commits must lose, not merge.
func TestScanWithRepoEntryReplacesManifestFile(t *testing.T) {
	tr := newTree(t).
		file("api/.vroom.toml", "name = \"from-file\"\nport = 1111\ncommands.start.run = \"echo file\"\n")
	dir := filepath.Join(tr.path(), "api")
	result, err := ScanWith(tr.path(), 4, map[string]*manifest.Manifest{dir: repoManifest("from-config", 2222)})
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "api")
	if p == nil {
		t.Fatalf("api project missing: %+v", result.Projects)
	}
	if !p.Configured || p.Manifest == nil {
		t.Fatalf("project = %+v, want the config manifest", p)
	}
	if p.Manifest.Name != "from-config" || p.Manifest.Port != 2222 {
		t.Errorf("manifest = %+v, want the config one to win", p.Manifest)
	}
	if len(result.Projects) != 1 {
		t.Errorf("got %d projects, want 1 (no duplicate row)", len(result.Projects))
	}
}

// Exactly the case the feature exists for: a repo with no .vroom.toml at all.
func TestScanWithRepoEntryCreatesProjectWithoutFile(t *testing.T) {
	tr := newTree(t).mkdir("private-thing")
	dir := filepath.Join(tr.path(), "private-thing")
	result, err := ScanWith(tr.path(), 4, map[string]*manifest.Manifest{dir: repoManifest("private-thing", 3333)})
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "private-thing")
	if p == nil {
		t.Fatalf("config-only project missing: %+v", result.Projects)
	}
	if !p.Configured || p.Manifest == nil || p.Manifest.Port != 3333 {
		t.Errorf("project = %+v, want configured with the config manifest", p)
	}
	if p.Path != dir {
		t.Errorf("Path = %q, want %q", p.Path, dir)
	}
}

// A config entry makes the file unnecessary, so a malformed one must stop mattering instead of keeping the row unconfigured.
func TestScanWithRepoEntryClearsManifestError(t *testing.T) {
	tr := newTree(t).file("api/.vroom.toml", "name = [broken toml")
	dir := filepath.Join(tr.path(), "api")
	result, err := ScanWith(tr.path(), 4, map[string]*manifest.Manifest{dir: repoManifest("api", 4444)})
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "api")
	if p == nil {
		t.Fatalf("api missing: %+v", result.Projects)
	}
	if p.ManifestErr != "" || !p.Configured || p.Manifest == nil {
		t.Errorf("project = %+v, want the config entry to override the broken file", p)
	}
}

// A typo in the config must not cost a project silently: it travels as a notice.
func TestScanWithRepoEntryMissingDirIsNoticed(t *testing.T) {
	tr := newTree(t)
	missing := filepath.Join(tr.path(), "nope")
	result, err := ScanWith(tr.path(), 4, map[string]*manifest.Manifest{missing: repoManifest("nope", 5555)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 0 {
		t.Errorf("projects = %+v, want none", result.Projects)
	}
	if len(result.Notices) != 1 || !strings.Contains(result.Notices[0], missing) {
		t.Errorf("Notices = %v, want one naming %s", result.Notices, missing)
	}
}

// An entry for a directory that is a file is as unreachable as a missing one.
func TestScanWithRepoEntryFileIsNoticed(t *testing.T) {
	tr := newTree(t).file("api/.vroom.toml", "name = \"api\"\ncommands.start.run = \"echo\"\n")
	notADir := filepath.Join(tr.path(), "api", ".vroom.toml")
	result, err := ScanWith(tr.path(), 4, map[string]*manifest.Manifest{notADir: repoManifest("x", 6666)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Notices) != 1 || !strings.Contains(result.Notices[0], "not a directory") {
		t.Errorf("Notices = %v, want a not-a-directory notice", result.Notices)
	}
}

// The override must survive the topology pass, which re-derives RepoRoot from git: a config-only project keeps its facts.
func TestScanWithRepoEntryKeepsSortedWithWalkedProjects(t *testing.T) {
	tr := newTree(t).
		file("b-file/.vroom.toml", "name = \"b-file\"\ncommands.start.run = \"echo\"\n").
		mkdir("a-config")
	dir := filepath.Join(tr.path(), "a-config")
	result, err := ScanWith(tr.path(), 4, map[string]*manifest.Manifest{dir: repoManifest("a-config", 7777)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(result.Projects), result.Projects)
	}
	if result.Projects[0].Path != dir {
		t.Errorf("first project = %q, want the sorted %q", result.Projects[0].Path, dir)
	}
}
