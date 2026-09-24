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
	Path string // ruta absoluta del directorio del proyecto
	Name string // nombre del directorio

	Configured  bool               // .vroom.toml parseado con éxito
	Manifest    *manifest.Manifest // nil si no configurado
	ManifestErr string             // error de parseo si .vroom.toml malformado

	// Relación repo/worktree: anotaciones aditivas sobre el slice
	// plano. Nunca se construye una estructura anidada.
	RepoRoot        string // ruta del main checkout del repo (solo worktrees)
	IsWorktree      bool   // true si es un worktree linkeado
	IsBareContainer bool   // true si es la fila contenedora de un bare repo
	WorktreeErr     string // error de topología (git ausente/fallo) del repo
}

// IsNestedRow reporta si el proyecto no es un miembro de grupo de primer
// nivel: o es un worktree linkeado (se renderiza indentado bajo su fila de
// repo) o es una fila contenedora (bare repo / main checkout fuera del
// scan root). La agregación de grupos (conteos, toggle, detalles) debe
// excluirlos para que el primary_group propio de un worktree siga siendo
// inerte (option B).
func (p Project) IsNestedRow() bool {
	return p.IsWorktree || p.IsBareContainer
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

// finalize completa el scan: agrega las filas contenedoras de bare repos
// (que no tienen .vroom.toml y por eso no las encuentra el escaneo por
// manifiestos), anota la topología repo/worktree y ordena por ruta. Los
// bare repos llegan de la propia enumeración del scan (sin walk extra).
// El merge deduplica por ruta: una fila contenedora no se duplica si ya
// existe un proyecto con esa ruta (ni si aparece por más de un camino).
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

// walkDir es filepath.WalkDir; variable para que los tests puedan contar
// las invocaciones y verificar que el scan no hace walks extra.
var walkDir = filepath.WalkDir

// scanWithFD ejecuta fd para encontrar .vroom.toml y, en una segunda
// invocación acotada, enumera directorios para detectar bare repos (que
// no tienen manifiesto). No usa WalkDir.
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

	// Bare repos: best-effort, no rompe el scan si la enumeración falla.
	bare, _ = scanBareReposWithFD(fd, root, depth)

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Path < projects[j].Path
	})
	return projects, bare, nil
}

// scanBareReposWithFD enumera directorios con fd (una sola invocación,
// sin WalkDir) y valida la heurística de bare repo. Respeta depth y salta
// hidden dirs y skipDirs igual que el camino WalkDir.
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

// skipDirs evita descender en directorios pesados.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
}

// scanWithWalk usa filepath.WalkDir como fallback y detecta bare repos en
// el mismo recorrido: un único walk, sin una segunda pasada.
func scanWithWalk(root string, depth int) (projects, bare []Project, err error) {
	walkErr := walkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
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

// repoKey devuelve una clave estable para agrupar proyectos por repo: el
// common git dir de un repo normal o de un worktree, o la ruta de un bare
// repo. Devuelve "" si dir no es un repo git consultable. Permite invocar
// git una sola vez por repo en vez de una por proyecto (N+1).
func repoKey(dir string) string {
	git := filepath.Join(dir, ".git")
	info, err := os.Stat(git)
	if err == nil {
		if info.IsDir() {
			if _, err := os.Stat(filepath.Join(git, "config")); err != nil {
				return "" // .git a medio construir: no es repo
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

// readGitDir resuelve el gitdir apuntado por un fichero .git (worktree o
// submodule): "gitdir: <ruta>", relativa al propio dir si no es absoluta.
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

// commonDir devuelve el common git dir de un gitdir: para un worktree,
// <main>/.git (vía el fichero commondir); si no existe, el propio gitdir.
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

// queryWorktreeRelations consulta `git worktree list` una sola vez por
// repo (agrupando los proyectos por su common git dir / bare path, no una
// vez por proyecto) y devuelve el mapa path→relación, saltando los
// prunable y registrando el error de topología en todos los proyectos del
// repo cuando git falla. Los bare repos no tienen .git, así que se
// detectan con la heurística para poder descubrir sus worktrees.
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
	sort.Strings(keys) // orden estable de consultas

	info := make(map[string]repoRelation)
	for _, key := range keys {
		members := byRepo[key]
		sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })
		wts, err := worktree.List(members[0].Path) // una consulta por repo
		if err != nil {
			for _, p := range members {
				p.WorktreeErr = err.Error() // degradación por repo
			}
			continue
		}
		if len(wts) == 0 {
			continue
		}
		main := wts[0].Path // git lista el main checkout primero
		for _, wt := range wts {
			if wt.Prunable {
				// Prunable con directorio existente: git lo marca
				// obsoleto pero el worktree sigue en disco, así que se
				// trata como normal. Solo se omite si ya no existe.
				if _, err := os.Stat(wt.Path); err != nil {
					continue
				}
			}
			rel := repoRelation{main: main, isWorktree: wt.Path != main}
			if prev, ok := info[wt.Path]; ok && prev.isWorktree {
				rel = prev // no degradar una relación ya establecida
			}
			info[wt.Path] = rel
		}
	}
	return info
}

// withinRoot reporta si path está dentro de root.
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
