package scanner

import (
	"os"
	"path/filepath"
	"strings"
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

// S2.1
func TestScanDetectsGoProjectByMarker(t *testing.T) {
	tr := newTree(t).file("myapp/go.mod", "module myapp\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	p := find(projects, "myapp")
	if p == nil {
		t.Fatalf("proyecto myapp no detectado: %+v", projects)
	}
	if p.Language != "Go" {
		t.Errorf("language = %q, want Go", p.Language)
	}
	if p.Configured {
		t.Error("proyecto sin manifiesto debe estar sin configurar")
	}
}

// S2.2
func TestScanIgnoresDepth3(t *testing.T) {
	tr := newTree(t).file("a/b/c/go.mod", "module deep\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("proyecto a 3 niveles debe ignorarse, got %+v", projects)
	}
}

func TestScanDetectsDepth2(t *testing.T) {
	tr := newTree(t).file("a/b/go.mod", "module ok\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Language != "Go" {
		t.Errorf("proyecto a 2 niveles debe detectarse, got %+v", projects)
	}
}

// S2.3 + nginx-proxy: manifiesto sin marcador → "otro".
func TestScanManifestOnlyIsOtro(t *testing.T) {
	tr := newTree(t).
		mkdir("nginx-proxy").
		file("nginx-proxy/.vroom.toml", "name = \"nginx-proxy\"\ncommand_start = \"docker run --rm -p 8080:80 nginx:alpine\"\nport = 8080\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("esperaba 1 proyecto, got %+v", projects)
	}
	if projects[0].Language != "otro" {
		t.Errorf("language = %q, want otro", projects[0].Language)
	}
	if !projects[0].Configured {
		t.Error("proyecto con manifiesto válido debe estar configurado")
	}
}

// S3.1: go.mod y package.json en el mismo dir → Go (orden de tabla).
func TestScanDuplicateMarkerPriority(t *testing.T) {
	tr := newTree(t).
		file("mixed/go.mod", "module m\n").
		file("mixed/package.json", "{}")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Language != "Go" {
		t.Errorf("language = %+v, want Go (primer marcador en orden de tabla)", projects)
	}
}

func TestScanAllMarkerLanguages(t *testing.T) {
	tests := []struct {
		marker   string
		language string
	}{
		{"pom.xml", "Java"},
		{"build.gradle", "Java"},
		{"build.gradle.kts", "Java"},
		{"package.json", "JavaScript"},
		{"pyproject.toml", "Python"},
		{"requirements.txt", "Python"},
		{"Cargo.toml", "Rust"},
	}
	for _, tt := range tests {
		t.Run(tt.marker, func(t *testing.T) {
			tr := newTree(t).file("proj/"+tt.marker, "")
			projects, err := Scan(tr.path())
			if err != nil {
				t.Fatal(err)
			}
			if len(projects) != 1 || projects[0].Language != tt.language {
				t.Errorf("marker %s: got %+v, want %s", tt.marker, projects, tt.language)
			}
		})
	}
}

// Directorio sin marcador ni manifiesto NO es proyecto (S18.1 requiere
// que apps/, servers/, infra/ no aparezcan).
func TestScanSkipsMarkerlessDirs(t *testing.T) {
	tr := newTree(t).
		mkdir("apps").
		mkdir("servers").
		mkdir("infra").
		file("apps/api/go.mod", "module api\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "api" {
		t.Errorf("solo apps/api debe aparecer, got %+v", projects)
	}
}

func TestScanSkipsHiddenAndJunkDirs(t *testing.T) {
	tr := newTree(t).
		file(".hidden/go.mod", "module h\n").
		file("node_modules/pkg/package.json", "{}").
		file("real/go.mod", "module r\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "real" {
		t.Errorf("ocultos y node_modules deben saltarse, got %+v", projects)
	}
}

// S-T1
func TestScanMalformedManifest(t *testing.T) {
	tr := newTree(t).
		file("broken/.vroom.toml", "name = [toml roto").
		file("broken/go.mod", "module broken\n")
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

func TestScanIncludesRootItself(t *testing.T) {
	tr := newTree(t).file("go.mod", "module root\n")
	projects, err := Scan(tr.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Path != tr.path() {
		t.Errorf("el CWD con marcador debe listarse, got %+v", projects)
	}
}

// 0006 S36.1: el playground completo se escanea con lenguajes y grupos
// jerárquicos correctos.
func TestScanPlaygroundFixture(t *testing.T) {
	projects, err := Scan(filepath.Join("..", "..", "playground"))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]struct{ language, primary, secondary string }{
		"products-api-java":     {"Java", "tienda", "backend"},
		"orders-api-springboot": {"Java", "tienda", "backend"},
		"billing-api-go":        {"Go", "tienda", "backend"},
		"inventory-api-python":  {"Python", "tienda", ""},
		"web-frontend":          {"JavaScript", "tienda", "frontend"},
		"search-api-python":     {"Python", "servers", "python"},
		"auth-api-go":           {"Go", "servers", "go"},
		"nginx-proxy":           {"otro", "infra", ""},
	}
	if len(projects) != len(want) {
		t.Fatalf("esperaba %d proyectos, got %d: %s", len(want), len(projects), projectNames(projects))
	}
	for _, p := range projects {
		w, ok := want[p.Name]
		if !ok {
			t.Errorf("proyecto inesperado: %s", p.Name)
			continue
		}
		if p.Language != w.language {
			t.Errorf("%s: language = %q, want %q", p.Name, p.Language, w.language)
		}
		if !p.Configured || p.Manifest == nil {
			t.Errorf("%s: debe estar configurado con manifiesto válido", p.Name)
			continue
		}
		if p.Manifest.PrimaryGroup != w.primary || p.Manifest.SecondaryGroup != w.secondary {
			t.Errorf("%s: groups = %q/%q, want %q/%q", p.Name,
				p.Manifest.PrimaryGroup, p.Manifest.SecondaryGroup, w.primary, w.secondary)
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
