package group

import (
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/scanner"
)

// proj crea un proyecto configurado con el group dado; group "" = sin manifiesto.
func proj(name, group string) scanner.Project {
	p := scanner.Project{
		Path:     "/home/user/dev/" + name,
		Name:     name,
		Language: "Go",
	}
	if group == "" {
		return p
	}
	p.Configured = true
	p.Manifest = &manifest.Manifest{Name: name, Group: group, Command: "run " + name}
	return p
}

// S10.1: dos "vsocial" juntos bajo separador, otro suelto.
func TestArrangeGroupsTogether(t *testing.T) {
	a := proj("api-java", "tienda")
	b := proj("web-frontend", "tienda")
	c := proj("api-go", "")

	entries := Arrange([]scanner.Project{c, a, b})
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3", len(entries))
	}

	// Orden: api-go (sin grupo), api-java (header tienda), web-frontend (tienda).
	wantOrder := []string{"api-go", "api-java", "web-frontend"}
	for i, want := range wantOrder {
		if entries[i].Project.Name != want {
			t.Errorf("posición %d = %s, want %s", i, entries[i].Project.Name, want)
		}
	}
	if entries[0].Group != "" {
		t.Errorf("api-go no debe tener grupo, got %q", entries[0].Group)
	}
	if entries[1].Group != "tienda" || entries[2].Group != "tienda" {
		t.Errorf("miembros de tienda mal agrupados: %q %q", entries[1].Group, entries[2].Group)
	}

	if !IsGroupHeader(entries, 1) {
		t.Error("api-java debe abrir el bloque tienda")
	}
	if IsGroupHeader(entries, 2) {
		t.Error("web-frontend no es header (mismo grupo que el anterior)")
	}
	if IsGroupHeader(entries, 0) {
		t.Error("api-go sin grupo no debe ser header")
	}
}

// S10.2: grupo con un único miembro también muestra header.
func TestArrangeSingleMemberGroup(t *testing.T) {
	a := proj("backend-vroom", "backend")
	entries := Arrange([]scanner.Project{a})
	if len(entries) != 1 || entries[0].Group != "backend" {
		t.Fatalf("got %+v", entries)
	}
	if !IsGroupHeader(entries, 0) {
		t.Error("grupo único debe mostrar header")
	}
}

func TestArrangeGroupInsertedAfterFirstSeen(t *testing.T) {
	x := proj("x", "")
	a := proj("a", "g1")
	y := proj("y", "")
	b := proj("b", "g1")

	entries := Arrange([]scanner.Project{x, a, y, b})
	var order []string
	for _, e := range entries {
		order = append(order, e.Project.Name)
	}
	got := strings.Join(order, ",")
	// x, a (abre g1), b (junto a a), y
	if got != "x,a,b,y" {
		t.Errorf("orden = %q, want x,a,b,y", got)
	}
}

// Regresión: con grupos intercalados, el índice del último miembro de un
// grupo quedaba obsoleto al insertar otro grupo antes de él; la siguiente
// inserción caía dentro de un bloque ajeno y lo partía en dos (header
// duplicado en la TUI). Cada grupo debe emitirse como un único bloque.
func TestArrangeInterleavedGroupsSingleBlock(t *testing.T) {
	projects := []scanner.Project{
		proj("b1", "backend"), proj("b2", "backend"), proj("f1", "frontend"),
		proj("b3", "backend"), proj("f2", "frontend"), proj("solo", ""),
		proj("infra1", "infra"), proj("solo2", ""), proj("b4", "backend"),
		proj("b5", "backend"), proj("b6", "backend"), proj("f3", "frontend"),
		proj("f4", "frontend"), proj("f5", "frontend"),
	}

	entries := Arrange(projects)
	if len(entries) != len(projects) {
		t.Fatalf("len = %d, want %d", len(entries), len(projects))
	}

	// Un solo header por grupo: ningún grupo reaparece tras haber
	// cerrado su bloque.
	seen := make(map[string]bool)
	for i, e := range entries {
		if !IsGroupHeader(entries, i) {
			continue
		}
		if seen[e.Group] {
			t.Errorf("grupo %q tiene más de un bloque (header en %d)", e.Group, i)
		}
		seen[e.Group] = true
	}
	for _, g := range []string{"backend", "frontend", "infra"} {
		if !seen[g] {
			t.Errorf("grupo %q no tiene header", g)
		}
	}

	// Orden esperado: bloques completos en la posición del primer miembro.
	var order []string
	for _, e := range entries {
		order = append(order, e.Project.Name)
	}
	want := "b1,b2,b3,b4,b5,b6,f1,f2,f3,f4,f5,solo,infra1,solo2"
	if got := strings.Join(order, ","); got != want {
		t.Errorf("orden = %q, want %q", got, want)
	}
}
