// Package scanner descubre proyectos desde el CWD con 2 niveles de
// recursividad. Un directorio es proyecto si contiene un manifiesto
// .vroom.toml.
package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// MaxDepth es la profundidad máxima (2 niveles desde CWD).
const MaxDepth = 2

// Scan recorre root hasta MaxDepth niveles y devuelve los proyectos
// que contengan un .vroom.toml, ordenados por ruta.
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

	var projects []Project
	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(absRoot, path)
		if relErr != nil {
			return nil
		}
		depth := 0
		if rel != "." {
			depth = strings.Count(rel, string(os.PathSeparator)) + 1
		}

		if path != absRoot {
			if isHidden(d.Name()) {
				return fs.SkipDir
			}
			if depth > MaxDepth {
				return fs.SkipDir
			}
		}

		if p, ok := inspectDir(path); ok {
			projects = append(projects, p)
		}

		if depth >= MaxDepth {
			return fs.SkipDir
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("error walking %s: %w", absRoot, walkErr)
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

func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}
