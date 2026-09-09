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

// Scan busca .vroom.toml desde root con la profundidad dada.
// Usa fd si está disponible; si no, WalkDir.
func Scan(root string, depth int) ([]Project, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("could not resolve CWD: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("could not access directory %s: %w", absRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", absRoot)
	}

	if fdAvailable() {
		return scanWithFD(absRoot, depth)
	}
	return scanWithWalk(absRoot, depth)
}

// fdAvailable comprueba si fd está en PATH.
func fdAvailable() bool {
	_, err := exec.LookPath("fd")
	return err == nil
}

// scanWithFD ejecuta fd para encontrar .vroom.toml.
func scanWithFD(root string, depth int) ([]Project, error) {
	args := []string{
		"--type", "f",
		"--name", ".vroom.toml",
		"--max-depth", strconv.Itoa(depth),
		"--absolute-path",
		root,
	}
	cmd := exec.Command("fd", args...)
	output, err := cmd.Output()
	if err != nil {
		// fd falló, fallback a WalkDir
		return scanWithWalk(root, depth)
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
			if isHidden(d.Name()) {
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
