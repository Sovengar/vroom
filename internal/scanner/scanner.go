// Package scanner descubre proyectos desde un directorio raíz usando fd
// para buscar ficheros .vroom.toml.
package scanner

import (
	"fmt"
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

// Scan busca .vroom.toml desde root con la profundidad dada usando fd.
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

	return scanWithFD(absRoot, depth)
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
func scanWithFD(root string, depth int) ([]Project, error) {
	fd := fdPath()
	if fd == "" {
		return nil, fmt.Errorf("fd not found in PATH or /usr/bin")
	}
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
