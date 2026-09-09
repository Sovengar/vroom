// Package scanner descubre proyectos desde el CWD que contengan un
// manifiesto .vroom.toml. Solo examina los subdirectorio inmediatos
// del directorio raíz (sin recursión).
package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"vroom/internal/manifest"
)

// Project es un proyecto detectado en el escaneo.
type Project struct {
	Path     string // ruta absoluta
	Name     string // nombre del directorio

	Configured  bool               // .vroom.toml parseado con éxito
	Manifest    *manifest.Manifest // nil si no configurado
	ManifestErr string             // error de parseo si .vroom.toml malformado
}

// Scan lee los subdirectorio inmediatos de root y devuelve aquellos
// que contengan un .vroom.toml válido, ordenados por ruta.
func Scan(root string) ([]Project, error) {
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

	entries, err := os.ReadDir(absRoot)
	if err != nil {
		return nil, fmt.Errorf("could not read directory %s: %w", absRoot, err)
	}

	var projects []Project
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(absRoot, entry.Name())
		if p, ok := inspectDir(dir); ok {
			projects = append(projects, p)
		}
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Path < projects[j].Path
	})
	return projects, nil
}

// inspectDir clasifica un directorio como proyecto si tiene .vroom.toml.
func inspectDir(dir string) (Project, bool) {
	if !manifest.Exists(dir) {
		return Project{}, false
	}

	p := Project{
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
	return p, true
}
