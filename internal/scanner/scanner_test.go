package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree construye un árbol en un directorio temporal.
type tree struct {
	t    *testing.T
	root string
}

func newTree(t *testing.T) *tree {
	return &tree{t: t, root: t.TempDir()}
}

func (tr *tree) mkdir(rel string) *tree {
	if err := os.MkdirAll(filepath.Join(tr.root, rel), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	return tr
}

func (tr *tree) file(rel, content string) *tree {
	full := filepath.Join(tr.root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		tr.t.Fatal(err)
	}
	return tr
}

func (tr *tree) path() string { return tr.root }

func find(projects []Project, name string) *Project {
	for i := range projects {
		if projects[i].Name == name {
			return &projects[i]
		}
	}
	return nil
}

// Solo se detectan proyectos con .vroom.toml
func TestScanDetectsVroomTomlProject(t *testing.T) {
	tr := newTree(t).
		mkdir("myapp").
		file("myapp/.vroom.toml", "name = \"myapp\"\ncommand_start = \"go run main.go\"\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	p := find(projects, "myapp")
	if p == nil {
		t.Fatalf("proyecto myapp no detectado: %+v", projects)
	}
	if !p.Configured {
		t.Error("proyecto con manifiesto válido debe estar configurado")
	}
}

// Directorio sin .vroom.toml NO es proyecto
func TestScanIgnoresDirsWithoutManifest(t *testing.T) {
	tr := newTree(t).
		mkdir("no-manifest").
		mkdir("another").
		file("no-manifest/go.mod", "module nope\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("debe ignorar dirs sin .vroom.toml, got %+v", projects)
	}
}

// Depth 2 con .vroom.toml se detecta
func TestScanDepth2(t *testing.T) {
	tr := newTree(t).
		file("a/b/.vroom.toml", "name = \"deep\"\ncommand_start = \"echo hi\"\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "b" {
		t.Errorf("proyecto a depth 2 debe detectarse, got %+v", projects)
	}
}

// Depth 3 se ignora
func TestScanIgnoresDepth3(t *testing.T) {
	tr := newTree(t).
		file("a/b/c/.vroom.toml", "name = \"too-deep\"\ncommand_start = \"echo\"\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("proyecto a depth 3 debe ignorarse, got %+v", projects)
	}
}

// Manifiesto malformado sigue visible pero no marcado como configurado
func TestScanMalformedManifest(t *testing.T) {
	tr := newTree(t).
		file("broken/.vroom.toml", "name = [toml roto")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	p := find(projects, "broken")
	if p == nil {
		t.Fatal("proyecto con manifiesto malformado debe seguir visible")
	}
	if p.Configured {
		t.Error("manifiesto malformado no debe marcarse configurado")
	}
	if p.ManifestErr == "" {
		t.Error("debe reportar el error de parseo")
	}
}

// Directorios ocultos se ignoran
func TestScanSkipsHiddenDirs(t *testing.T) {
	tr := newTree(t).
		file(".hidden/.vroom.toml", "name = \"h\"\ncommand_start = \"echo\"\n").
		file("real/.vroom.toml", "name = \"real\"\ncommand_start = \"echo\"\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "real" {
		t.Errorf("dirs ocultos deben saltarse, got %+v", projects)
	}
}

// Orden por ruta
func TestScanSortsByPath(t *testing.T) {
	tr := newTree(t).
		file("zebra/.vroom.toml", "name = \"zebra\"\ncommand_start = \"echo z\"\n").
		file("alpha/.vroom.toml", "name = \"alpha\"\ncommand_start = \"echo a\"\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("esperaba 2 proyectos, got %d", len(projects))
	}
	if projects[0].Name != "alpha" || projects[1].Name != "zebra" {
		t.Errorf("orden inesperado: %s, %s", projects[0].Name, projects[1].Name)
	}
}

// Manifiesto con grupos
func TestScanManifestWithGroups(t *testing.T) {
	tr := newTree(t).
		file("api/.vroom.toml", "name = \"api\"\nprimary_group = \"tienda\"\nsecondary_group = \"backend\"\ncommand_start = \"go run .\"\nport = 8080\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("esperaba 1 proyecto, got %+v", projects)
	}
	p := projects[0]
	if !p.Configured || p.Manifest == nil {
		t.Error("debe estar configurado")
	}
	if p.Manifest.PrimaryGroup != "tienda" || p.Manifest.SecondaryGroup != "backend" {
		t.Errorf("groups = %q/%q, want tienda/backend", p.Manifest.PrimaryGroup, p.Manifest.SecondaryGroup)
	}
	if p.Manifest.Port != 8080 {
		t.Errorf("port = %d, want 8080", p.Manifest.Port)
	}
}

// El playground completo se escanea
func TestScanPlaygroundFixture(t *testing.T) {
	projects, err := Scan(filepath.Join("..", "..", "playground"))
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 8 {
		t.Fatalf("esperaba 8 proyectos, got %d: %v", len(projects), projectNames(projects))
	}
	for _, p := range projects {
		if !p.Configured || p.Manifest == nil {
			t.Errorf("%s: debe estar configurado", p.Name)
		}
	}
}

func projectNames(projects []Project) string {
	var names []string
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}
