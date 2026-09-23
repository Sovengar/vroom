// Package scanner descubre proyectos desde un directorio raíz buscando
// ficheros .vroom.toml. Usa fd si está disponible; fallback a WalkDir.
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

// Project es un proyecto detectado en el escaneo.
type Project struct {
	Path     string // ruta absoluta del directorio del proyecto
	Name     string // nombre del directorio

	Configured  bool               // .vroom.toml parseado con éxito
	Manifest    *manifest.Manifest // nil si no configurado
	ManifestErr string             // error de parseo si .vroom.toml malformado

	// Relación repo/worktree (0011): anotaciones aditivas sobre el slice
	// plano. Nunca se construye una estructura anidada.
	RepoRoot        string // ruta del main checkout del repo (solo worktrees)
	IsWorktree      bool   // true si es un worktree linkeado
	IsBareContainer bool   // true si es la fila contenedora de un bare repo
	WorktreeErr     string // error de topología (git ausente/fallo) del repo
}

// ScanResult contiene los proyectos y el método usado para encontrarlos.
type ScanResult struct {
	Projects []Project
	UsedFD   bool // true si se usó fd, false si WalkDir
}

// Scan busca .vroom.toml desde root con la profundidad dada.
// Usa fd si está disponible; si no, WalkDir.
func Scan(root string, depth int) (ScanResult, error) {
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

	if fd := fdPath(); fd != "" {
		projects, err := scanWithFD(fd, absRoot, depth)
		if err != nil {
			return ScanResult{Projects: projects, UsedFD: true}, err
		}
		return ScanResult{Projects: finalize(projects, absRoot, depth), UsedFD: true}, nil
	}
	projects, err := scanWithWalk(absRoot, depth)
	if err != nil {
		return ScanResult{Projects: projects, UsedFD: false}, err
	}
	return ScanResult{Projects: finalize(projects, absRoot, depth), UsedFD: false}, nil
}

// finalize completa el scan: agrega las filas contenedoras de bare repos
// (que no tienen .vroom.toml y por eso no las encuentra el escaneo por
// manifiestos), anota la topología repo/worktree y ordena por ruta.
func finalize(projects []Project, root string, depth int) []Project {
	projects = append(projects, discoverBareRepos(root, depth)...)
	projects = annotateTopology(projects, root)
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Path < projects[j].Path
	})
	return projects
}

// fdPath busca fd en PATH o en ubicaciones conocidas.
func fdPath() string {
	if p, err := exec.LookPath("fd"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/bin/fd", "/usr/local/bin/fd"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// scanWithFD ejecuta fd para encontrar .vroom.toml.
func scanWithFD(fd string, root string, depth int) ([]Project, error) {
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
		return nil, fmt.Errorf("fd failed: %s\n%s", err, string(output))
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var projects []Project
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

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Path < projects[j].Path
	})
	return projects, nil
}

// skipDirs evita descender en directorios pesados.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
}

// scanWithWalk usa filepath.WalkDir como fallback.
func scanWithWalk(root string, depth int) ([]Project, error) {
	var projects []Project
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		depthLevel := 0
		if rel != "." {
			depthLevel = strings.Count(rel, string(os.PathSeparator)) + 1
		}

		if path != root {
			if isHidden(d.Name()) || skipDirs[d.Name()] {
				return fs.SkipDir
			}
			if depthLevel > depth {
				return fs.SkipDir
			}
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
		return nil, fmt.Errorf("error walking %s: %w", root, walkErr)
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Path < projects[j].Path
	})
	return projects, nil
}

// inspectDir devuelve un proyecto si el directorio tiene .vroom.toml, nil si no.
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

// hasGitRepo reporta si dir contiene un repo git real: un .git file
// (worktree/submodule) o un directorio .git con config. Un .git a medio
// construir (fixtures de tests) se ignora para no spawnar git de más.
func hasGitRepo(dir string) bool {
	git := filepath.Join(dir, ".git")
	info, err := os.Stat(git)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return true // .git file: worktree linkeado o submodule
	}
	if _, err := os.Stat(filepath.Join(git, "config")); err != nil {
		return false
	}
	return true
}

// repoRelation es la relación de un worktree con su repo.
type repoRelation struct {
	main       string // ruta del main checkout
	isWorktree bool   // false si el propio path es el main checkout
}

// annotateTopology consulta la topología git de cada proyecto con repo y
// anota las relaciones repo/worktree; además sintetiza filas para
// worktrees in-root que no tienen manifiesto propio (se listan como no
// configurados). Nunca construye una estructura anidada.
func annotateTopology(projects []Project, root string) []Project {
	info := make(map[string]repoRelation)
	queried := make(map[string]bool)
	for i := range projects {
		p := &projects[i]
		if !hasGitRepo(p.Path) || queried[p.Path] {
			continue
		}
		queried[p.Path] = true
		wts, err := worktree.List(p.Path)
		if err != nil {
			p.WorktreeErr = err.Error()
			continue
		}
		if len(wts) == 0 {
			continue
		}
		main := wts[0].Path // git lista el main checkout primero
		for _, wt := range wts {
			if wt.Prunable {
				continue // prunable/ausente: no se renderiza
			}
			rel := repoRelation{main: main, isWorktree: wt.Path != main}
			if prev, ok := info[wt.Path]; ok && prev.isWorktree {
				rel = prev // no degradar una relación ya establecida
			}
			info[wt.Path] = rel
		}
	}

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

// discoverBareRepos busca bare repos bajo root (limitado por depth) y los
// expone como filas contenedoras sin manifiesto.
func discoverBareRepos(root string, depth int) []Project {
	var out []Project
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if path != root {
			if isHidden(d.Name()) || skipDirs[d.Name()] {
				return fs.SkipDir
			}
			if strings.Count(rel, string(os.PathSeparator))+1 > depth {
				return fs.SkipDir
			}
		}
		if worktree.IsBareRepo(path) {
			out = append(out, Project{
				Path:            path,
				Name:            filepath.Base(path),
				IsBareContainer: true,
			})
			return fs.SkipDir
		}
		return nil
	})
	return out
}

// withinRoot reporta si path está dentro de root.
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
