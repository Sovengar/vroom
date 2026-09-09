package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

// scannerTree construye un árbol en un directorio temporal y devuelve la raíz.
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

// No recursión: subdirectorio con .vroom.toml a profundidad > 1 se ignora
func TestScanNoRecursion(t *testing.T) {
	tr := newTree(t).
		file("a/b/.vroom.toml", "name = \"deep\"\ncommand_start = \"echo hi\"\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("subdirectorio profundo sin recursión debe ignorarse, got %+v", projects)
	}
}

// Solo subdirectorio inmediato: un proyecto a depth 1 se detecta
func TestScanImmediateChildOnly(t *testing.T) {
	tr := newTree(t).
		file("proj/.vroom.toml", "name = \"proj\"\ncommand_start = \"echo ok\"\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "proj" {
		t.Errorf("esperaba proyecto proj, got %+v", projects)
	}
}

// El root mismo sin .vroom.toml no se lista como proyecto
func TestScanRootWithoutManifest(t *testing.T) {
	tr := newTree(t)
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("CWD sin .vroom.toml no debe listarse, got %+v", projects)
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
