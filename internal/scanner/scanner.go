// Package scanner descubre proyectos desde el CWD con 2 niveles de
// recursividad (spec R2/R3).
//
// Un directorio es proyecto si contiene un marcador de lenguaje
// (pom.xml, go.mod, package.json, ...) o un manifiesto .svc.toml.
// Proyectos con manifiesto están "configurados"; los demás se muestran
// como "sin configurar" (modo descubrimiento).
package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"svc/internal/manifest"
)

// Project es un proyecto detectado en el escaneo.
type Project struct {
	Path     string // ruta absoluta
	Name     string // nombre del directorio
	Language string // Java, Go, JavaScript, Python, Rust u "otro"

	Configured  bool               // .svc.toml parseado con éxito
	Manifest    *manifest.Manifest // nil si no configurado
	ManifestErr string             // error de parseo si .svc.toml malformado (S-T1)
}

// marker asocia un fichero marcador con el lenguaje mostrado.
// El orden define la prioridad (S3.1: primer marcador en orden de tabla).
type marker struct {
	file     string
	language string
}

var markers = []marker{
	{"pom.xml", "Java"},
	{"build.gradle", "Java"},
	{"build.gradle.kts", "Java"},
	{"go.mod", "Go"},
	{"package.json", "JavaScript"},
	{"pyproject.toml", "Python"},
	{"requirements.txt", "Python"},
	{"Cargo.toml", "Rust"},
}

// skipDirs evita descender en directorios ocultos o de artefactos.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
}

// MaxDepth es la profundidad máxima (2 niveles desde CWD; S2.2).
const MaxDepth = 2

// Scan recorre root (ruta absoluta o relativa) hasta MaxDepth niveles y
// devuelve los proyectos detectados ordenados por ruta.
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
			// Directorios ilegibles se saltan sin abortar el escaneo.
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
			if isHidden(d.Name()) || skipDirs[d.Name()] {
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
			// Los marcadores a depth 3+ se ignoran: no descender más.
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

// inspectDir clasifica un directorio como proyecto o no.
func inspectDir(dir string) (Project, bool) {
	language := ""
	for _, m := range markers {
		if fileExists(filepath.Join(dir, m.file)) {
			language = m.language
			break
		}
	}

	hasManifestFile := manifest.Exists(dir)
	if language == "" && !hasManifestFile {
		return Project{}, false
	}
	if language == "" {
		// Proyecto sin marcador conocido pero configurable (ej: Docker).
		language = "otro"
	}

	p := Project{
		Path:     dir,
		Name:     filepath.Base(dir),
		Language: language,
	}
	if hasManifestFile {
		m, err := manifest.Parse(filepath.Join(dir, manifest.FileName))
		if err != nil {
			p.ManifestErr = err.Error() // S-T1: visible como "sin configurar", sin crashear
		} else {
			p.Configured = true
			p.Manifest = m
		}
	}
	return p, true
}

func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
