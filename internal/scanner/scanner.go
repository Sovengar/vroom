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
)

// Project es un proyecto detectado en el escaneo.
type Project struct {
	Path     string // ruta absoluta del directorio del proyecto
	Name     string // nombre del directorio

	Configured  bool               // .vroom.toml parseado con éxito
	Manifest    *manifest.Manifest // nil si no configurado
	ManifestErr string             // error de parseo si .vroom.toml malformado
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
		return ScanResult{Projects: projects, UsedFD: true}, err
	}
	projects, err := scanWithWalk(absRoot, depth)
	return ScanResult{Projects: projects, UsedFD: false}, err
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
