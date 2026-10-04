// Package scanner finds .vroom.toml projects under a root, via fd when it is present and filepath.WalkDir otherwise, and both paths must yield identical results.
package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"vroom/internal/manifest"
	"vroom/internal/worktree"
)

type Project struct {
	Path string
	Name string

	Configured  bool
	Manifest    *manifest.Manifest
	ManifestErr string

	// Repo/worktree facts stay flat annotations on this slice; a nested tree was rejected.
	RepoRoot        string
	IsWorktree      bool
	IsBareContainer bool
	WorktreeErr     string
}

// IsNestedRow marks the rows that group aggregation must skip, so a worktree's own primary_group stays inert (option B).
func (p Project) IsNestedRow() bool {
	return p.IsWorktree || p.IsBareContainer
}

type ScanResult struct {
	Projects []Project
	UsedFD   bool
}

func Scan(root string, depth int) (ScanResult, error) {
	return scanWith(root, depth, fdPath())
}

// scanWith takes the resolved fd ("" forces WalkDir) because resolving the tool inside the scan made half the code unreachable from tests: clearing PATH is not enough, the absolute fallbacks remain.
func scanWith(root string, depth int, fd string) (ScanResult, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return ScanResult{}, fmt.Errorf("could not resolve CWD: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return ScanResult{}, fmt.Errorf("could not access directory %s: %w", absRoot, err)
	}
	if !info.IsDir() {
		return ScanResult{}, fmt.Errorf("%s is not a directory", absRoot)
	}

	if fd != "" {
		projects, bare, err := scanWithFD(fd, absRoot, depth)
		if err != nil {
			return ScanResult{Projects: projects, UsedFD: true}, err
		}
		return ScanResult{Projects: finalize(projects, bare, absRoot), UsedFD: true}, nil
	}
	projects, bare, err := scanWithWalk(absRoot, depth)
	if err != nil {
		return ScanResult{Projects: projects, UsedFD: false}, err
	}
	return ScanResult{Projects: finalize(projects, bare, absRoot), UsedFD: false}, nil
}

func finalize(projects, bare []Project, root string) []Project {
	seen := make(map[string]bool, len(projects)+len(bare))
	merged := make([]Project, 0, len(projects)+len(bare))
	for _, p := range projects {
		if seen[p.Path] {
			continue
		}
		seen[p.Path] = true
		merged = append(merged, p)
	}
	for _, p := range bare {
		if seen[p.Path] {
			continue
		}
		seen[p.Path] = true
		merged = append(merged, p)
	}
	merged = annotateTopology(merged, root)
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Path < merged[j].Path
	})
	return merged
}

// fdPath searches PATH and then the absolute fallbacks (the CI runner, and systems where fd is packaged but off the service PATH); the fallbacks are data in defaultFDFallbacks, not a mutable global, so tests pass nonexistent paths without contaminating parallel runs.
func fdPath() string {
	if p, err := exec.LookPath("fd"); err == nil {
		return p
	}
	return fdInPaths(defaultFDFallbacks)
}

var defaultFDFallbacks = []string{"/usr/bin/fd", "/usr/local/bin/fd"}

func fdInPaths(candidatos []string) string {
	for _, p := range candidatos {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// A var so tests can count walks and assert the scan does no extra traversal.
var walkDir = filepath.WalkDir

func scanWithFD(fd string, root string, depth int) (projects, bare []Project, err error) {
	args := []string{
		"--type", "f",
		"--hidden",
		"--max-depth", strconv.Itoa(depth),
		".vroom.toml",
		root,
	}
	cmd := exec.Command(fd, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil, fmt.Errorf("fd failed: %s\n%s", err, string(output))
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		dir := filepath.Dir(line)
		p := inspectDir(dir)
		if p != nil {
			projects = append(projects, *p)
		}
	}

	// Best-effort: a failed bare-repo enumeration must not fail the scan.
	bare, _ = scanBareReposWithFD(fd, root, depth)

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Path < projects[j].Path
	})
	return projects, bare, nil
}

func scanBareReposWithFD(fd string, root string, depth int) ([]Project, error) {
	args := []string{"--type", "d", "--hidden", "--max-depth", strconv.Itoa(depth)}
	for name := range skipDirs {
		args = append(args, "--exclude", name)
	}
	args = append(args, ".", root)

	cmd := exec.Command(fd, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("fd (dirs) failed: %s\n%s", err, string(output))
	}

	var bare []Project
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		base := filepath.Base(line)
		if isHidden(base) || skipDirs[base] {
			continue
		}
		if worktree.IsBareRepo(line) {
			bare = append(bare, Project{
				Path:            line,
				Name:            base,
				IsBareContainer: true,
			})
		}
	}
	return bare, nil
}

var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
}

// Bare repos are detected in the same walk: one pass, no second traversal.
func scanWithWalk(root string, depth int) (projects, bare []Project, err error) {
	walkErr := walkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		// No error guard on Rel on purpose: path comes from the walk of root, so Rel cannot fail, and a guard there would imply the walk can lose directories.
		rel, _ := filepath.Rel(root, path)
		depthLevel := 0
		if rel != "." {
			depthLevel = strings.Count(rel, string(os.PathSeparator)) + 1
		}

		if path != root {
			if isHidden(d.Name()) || skipDirs[d.Name()] {
				return fs.SkipDir
			}
			// No depthLevel > depth check here by construction: a child is only visited if its parent returned nil, and the parent already cut at depthLevel >= depth (verified over a three-level tree, depth 0 to 6).
		}

		if worktree.IsBareRepo(path) {
			bare = append(bare, Project{
				Path:            path,
				Name:            filepath.Base(path),
				IsBareContainer: true,
			})
			return fs.SkipDir
		}

		if p := inspectDir(path); p != nil {
			projects = append(projects, *p)
		}

		if depthLevel >= depth {
			return fs.SkipDir
		}
		return nil
	})
	if walkErr != nil {
		return nil, nil, fmt.Errorf("error walking %s: %w", root, walkErr)
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Path < projects[j].Path
	})
	return projects, bare, nil
}

func inspectDir(dir string) *Project {
	if !manifest.Exists(dir) {
		return nil
	}

	p := &Project{
		Path: dir,
		Name: filepath.Base(dir),
	}

	m, err := manifest.Parse(filepath.Join(dir, manifest.FileName))
	if err != nil {
		p.ManifestErr = err.Error()
	} else {
		p.Configured = true
		p.Manifest = m
	}
	return p
}

func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

// repoKey is the grouping key that lets git be queried once per repo instead of once per project (N+1); "" means dir is not a consultable git repo.
func repoKey(dir string) string {
	git := filepath.Join(dir, ".git")
	info, err := os.Stat(git)
	if err == nil {
		if info.IsDir() {
			if _, err := os.Stat(filepath.Join(git, "config")); err != nil {
				return "" // a half-built .git is not a repo yet
			}
			return filepath.Clean(git)
		}
		gd, ok := readGitDir(dir, git)
		if !ok {
			return ""
		}
		return commonDir(gd)
	}
	if worktree.IsBareRepo(dir) {
		return filepath.Clean(dir)
	}
	return ""
}

func readGitDir(dir, gitFile string) (string, bool) {
	raw, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false
	}
	gd, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return "", false
	}
	gd = strings.TrimSpace(gd)
	if gd == "" {
		return "", false
	}
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(dir, gd)
	}
	return filepath.Clean(gd), true
}

// For a worktree this is the main checkout's .git, which is what lets a worktree group with its repo.
func commonDir(gitdir string) string {
	raw, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return gitdir
	}
	c := strings.TrimSpace(string(raw))
	if c == "" {
		return gitdir
	}
	if !filepath.IsAbs(c) {
		c = filepath.Join(gitdir, c)
	}
	return filepath.Clean(c)
}

type repoRelation struct {
	main       string
	isWorktree bool
}

// Worktrees inside root that have no manifest of their own are synthesized as unconfigured rows.
func annotateTopology(projects []Project, root string) []Project {
	info := queryWorktreeRelations(projects)

	for i := range projects {
		p := &projects[i]
		if rel, ok := info[p.Path]; ok && rel.isWorktree {
			p.IsWorktree = true
			p.RepoRoot = rel.main
		}
	}

	existing := make(map[string]bool, len(projects))
	for _, p := range projects {
		existing[p.Path] = true
	}
	var synth []Project
	for path, rel := range info {
		if !rel.isWorktree || existing[path] || !withinRoot(root, path) {
			continue
		}
		synth = append(synth, Project{
			Path:       path,
			Name:       filepath.Base(path),
			IsWorktree: true,
			RepoRoot:   rel.main,
		})
	}
	return append(projects, synth...)
}

// Bare repos have no .git, so repoKey falls back to the heuristic: without it their worktrees would be undiscoverable.
func queryWorktreeRelations(projects []Project) map[string]repoRelation {
	byRepo := make(map[string][]*Project)
	for i := range projects {
		p := &projects[i]
		key := repoKey(p.Path)
		if key == "" {
			continue
		}
		byRepo[key] = append(byRepo[key], p)
	}

	keys := make([]string, 0, len(byRepo))
	for key := range byRepo {
		keys = append(keys, key)
	}
	sort.Strings(keys) // stable order keeps the query sequence reproducible

	info := make(map[string]repoRelation)
	for _, key := range keys {
		members := byRepo[key]
		sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })
		wts, err := worktree.List(members[0].Path)
		if err != nil {
			for _, p := range members {
				p.WorktreeErr = err.Error()
			}
			continue
		}
		if len(wts) == 0 {
			continue
		}
		main := wts[0].Path // git lists the main checkout first
		for _, wt := range wts {
			if wt.Prunable {
				// Git marks a worktree prunable but it may still be on disk, so only a missing directory skips it.
				if _, err := os.Stat(wt.Path); err != nil {
					continue
				}
			}
			rel := repoRelation{main: main, isWorktree: wt.Path != main}
			if prev, ok := info[wt.Path]; ok && prev.isWorktree {
				rel = prev // never downgrade a relation already established
			}
			info[wt.Path] = rel
		}
	}
	return info
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
