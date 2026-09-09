package group

import (
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/scanner"
)

// proj crea un proyecto configurado con los grupos dados; primary "" =
// sin manifiesto. Un secondary sin primary se permite (se ignora, S36.3).
func proj(name, primary, secondary string) scanner.Project {
	p := scanner.Project{
		Path: "/home/user/dev/" + name,
		Name: name,
	}
	if primary == "" && secondary == "" {
		return p // sin manifiesto
	}
	p.Configured = true
	p.Manifest = &manifest.Manifest{Name: name, PrimaryGroup: primary, SecondaryGroup: secondary, Command: "run " + name}
	return p
}

// names devuelve los nombres de proyecto en el orden dado.
func names(entries []Entry) string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Project.Name)
	}
	return strings.Join(out, ",")
}

// R10/S10.1 migrado a 0006 (primary sin secondary): dos proyectos con el
// mismo primario juntos bajo su header, otro inline.
func TestArrangeSamePrimaryTogether(t *testing.T) {
	a := proj("api-java", "tienda", "")
	b := proj("web-frontend", "tienda", "")
	c := proj("api-go", "", "")

	entries := Arrange([]scanner.Project{c, a, b})
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3", len(entries))
	}

	// Orden: api-go (inline), api-java (header tienda), web-frontend.
	if got := names(entries); got != "api-go,api-java,web-frontend" {
		t.Errorf("orden = %q", got)
	}
	if entries[0].Primary != "" || entries[0].Secondary != "" {
		t.Errorf("api-go no debe tener grupos, got %+v", entries[0])
	}
	if entries[1].Primary != "tienda" || entries[2].Primary != "tienda" {
		t.Errorf("miembros de tienda mal agrupados: %+v %+v", entries[1], entries[2])
	}
	if entries[1].Secondary != "" || entries[2].Secondary != "" {
		t.Errorf("sin secundario no debe haber Secondary: %+v %+v", entries[1], entries[2])
	}

	if !IsPrimaryHeader(entries, 1) {
		t.Error("api-java debe abrir el bloque tienda")
	}
	if IsPrimaryHeader(entries, 2) {
		t.Error("web-frontend no es header (mismo primario que el anterior)")
	}
	if IsPrimaryHeader(entries, 0) {
		t.Error("api-go sin primario no debe ser header")
	}
	if IsSecondaryHeader(entries, 1) {
		t.Error("sin secundario no debe haber header secundario")
	}
}

// S10.2: primario con un único miembro también muestra header.
func TestArrangeSingleMemberPrimary(t *testing.T) {
	a := proj("backend-vroom", "backend", "")
	entries := Arrange([]scanner.Project{a})
	if len(entries) != 1 || entries[0].Primary != "backend" || entries[0].Secondary != "" {
		t.Fatalf("got %+v", entries)
	}
	if !IsPrimaryHeader(entries, 0) {
		t.Error("primario único debe mostrar header")
	}
}

func TestArrangePrimaryInsertedAfterFirstSeen(t *testing.T) {
	x := proj("x", "", "")
	a := proj("a", "g1", "")
	y := proj("y", "", "")
	b := proj("b", "g1", "")

	entries := Arrange([]scanner.Project{x, a, y, b})
	// x, a (abre g1), b (junto a a), y
	if got := names(entries); got != "x,a,b,y" {
		t.Errorf("orden = %q, want x,a,b,y", got)
	}
}

// Regresión (heredada): con grupos intercalados, cada grupo debe
// emitirse como un único bloque.
func TestArrangeInterleavedPrimariesSingleBlock(t *testing.T) {
	projects := []scanner.Project{
		proj("b1", "backend", ""), proj("b2", "backend", ""), proj("f1", "frontend", ""),
		proj("b3", "backend", ""), proj("f2", "frontend", ""), proj("solo", "", ""),
		proj("infra1", "infra", ""), proj("solo2", "", ""), proj("b4", "backend", ""),
		proj("b5", "backend", ""), proj("b6", "backend", ""), proj("f3", "frontend", ""),
		proj("f4", "frontend", ""), proj("f5", "frontend", ""),
	}

	entries := Arrange(projects)
	if len(entries) != len(projects) {
		t.Fatalf("len = %d, want %d", len(entries), len(projects))
	}

	// Un solo header por primario: ningún primario reaparece tras haber
	// cerrado su bloque.
	seen := make(map[string]bool)
	for i, e := range entries {
		if !IsPrimaryHeader(entries, i) {
			continue
		}
		if seen[e.Primary] {
			t.Errorf("primario %q tiene más de un bloque (header en %d)", e.Primary, i)
		}
		seen[e.Primary] = true
	}
	for _, g := range []string{"backend", "frontend", "infra"} {
		if !seen[g] {
			t.Errorf("primario %q no tiene header", g)
		}
	}

	// Orden esperado: bloques completos en la posición del primer miembro.
	if got := names(entries); got != "b1,b2,b3,b4,b5,b6,f1,f2,f3,f4,f5,solo,infra1,solo2" {
		t.Errorf("orden = %q", got)
	}
}

// S37.1: bloques anidados contiguos; el sin primario queda inline (sin
// sección ungrouped).
func TestArrangeNestedBlocksContiguous(t *testing.T) {
	a := proj("a", "vsocial", "backend")
	b := proj("b", "", "")
	c := proj("c", "vsocial", "infra")
	d := proj("d", "vsocial", "backend")

	entries := Arrange([]scanner.Project{a, b, c, d})
	if got := names(entries); got != "a,d,c,b" {
		t.Errorf("orden = %q, want a,d,c,b", got)
	}
	if entries[0].Secondary != "backend" || entries[1].Secondary != "backend" || entries[2].Secondary != "infra" {
		t.Errorf("secundarios mal asignados: %+v %+v %+v", entries[0], entries[1], entries[2])
	}
	if entries[3].Primary != "" {
		t.Errorf("b debe ir inline sin grupos, got %+v", entries[3])
	}

	// Headers: a abre vsocial y backend; c abre infra; d continúa el
	// bloque backend de a; b es inline.
	if !IsPrimaryHeader(entries, 0) || IsPrimaryHeader(entries, 1) || IsPrimaryHeader(entries, 2) || IsPrimaryHeader(entries, 3) {
		t.Error("solo a debe abrir el bloque vsocial")
	}
	if !IsSecondaryHeader(entries, 0) || IsSecondaryHeader(entries, 1) || !IsSecondaryHeader(entries, 2) || IsSecondaryHeader(entries, 3) {
		t.Errorf("headers secundarios esperados en 0 y 2: %v %v %v %v",
			IsSecondaryHeader(entries, 0), IsSecondaryHeader(entries, 1),
			IsSecondaryHeader(entries, 2), IsSecondaryHeader(entries, 3))
	}
}

// S37.2: el primario se emite en la posición de su primer miembro.
func TestArrangePrimaryAtFirstMember(t *testing.T) {
	x := proj("x", "otros", "")
	y := proj("y", "vsocial", "")
	z := proj("z", "otros", "")

	entries := Arrange([]scanner.Project{x, y, z})
	if got := names(entries); got != "x,z,y" {
		t.Errorf("orden = %q, want x,z,y", got)
	}
}

// S37.3: mezcla con y sin secundario dentro de un primario (sin
// pseudo-header "(general)").
func TestArrangeMixedWithAndWithoutSecondary(t *testing.T) {
	b1 := proj("b1", "vsocial", "backend")
	f1 := proj("f1", "vsocial", "frontend")
	direct := proj("directo", "vsocial", "")

	entries := Arrange([]scanner.Project{b1, f1, direct})
	if got := names(entries); got != "b1,f1,directo" {
		t.Errorf("orden = %q, want b1,f1,directo", got)
	}
	if entries[2].Primary != "vsocial" || entries[2].Secondary != "" {
		t.Errorf("directo debe ir bajo el primario sin secundario, got %+v", entries[2])
	}
	if IsSecondaryHeader(entries, 2) {
		t.Error("el miembro sin secundario no abre header secundario")
	}
}

// S37.4: el secundario se emite contiguo en la posición de su primer
// miembro dentro del primario.
func TestArrangeSecondaryAtFirstMember(t *testing.T) {
	m1 := proj("m1", "vsocial", "b2")
	m2 := proj("m2", "vsocial", "b1")
	m3 := proj("m3", "vsocial", "b2")
	m4 := proj("m4", "vsocial", "")

	entries := Arrange([]scanner.Project{m1, m2, m3, m4})
	if got := names(entries); got != "m1,m3,m2,m4" {
		t.Errorf("orden = %q, want m1,m3,m2,m4", got)
	}
}

// S36.3: secondary sin primary se ignora → inline, sin grupos.
func TestArrangeSecondaryWithoutPrimaryIgnored(t *testing.T) {
	p := proj("huérfano", "", "infra")
	entries := Arrange([]scanner.Project{p})
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if entries[0].Primary != "" || entries[0].Secondary != "" {
		t.Errorf("el secondary sin primary debe descartarse, got %+v", entries[0])
	}
	if IsSecondaryHeader(entries, 0) || IsPrimaryHeader(entries, 0) {
		t.Error("la fila inline no debe ser header de ningún nivel")
	}
}

// S37.5: el cambio de primario también abre header secundario (el
// bloque backend de un primario distinto es otro bloque).
func TestIsSecondaryHeaderAcrossPrimaries(t *testing.T) {
	a := proj("a", "vsocial", "backend")
	b := proj("b", "otros", "backend")

	entries := Arrange([]scanner.Project{a, b})
	if !IsSecondaryHeader(entries, 1) {
		t.Error("cambiar de primario debe abrir header secundario aunque el nombre coincida")
	}
	if !IsPrimaryHeader(entries, 1) {
		t.Error("b abre el bloque del primario otros")
	}
}
