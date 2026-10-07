package group

import (
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/scanner"
)

// Both accessors run over every scanned project, including bare directories with no manifest, so a nil must yield "" instead of blanking the TUI.
func TestLosAccesoresNoRevientanConUnProyectoSinManifiesto(t *testing.T) {
	casos := []struct {
		nombre     string
		p          scanner.Project
		primario   string
		secundario string
	}{
		{"no manifest at all", scanner.Project{Name: "loose"}, "", ""},
		{"manifest explicitly nil", scanner.Project{Name: "x", Manifest: nil}, "", ""},
		{"with both groups", proyectoConGrupos("shop", "backend", "api"), "shop", "backend"},
		{"primary only", proyectoConGrupos("shop", "", "api"), "shop", ""},
		{"secondary only", proyectoConGrupos("", "backend", "api"), "", "backend"},
		{"with no groups", proyectoConGrupos("", "", "x"), "", ""},
	}
	for _, tt := range casos {
		t.Run(tt.nombre, func(t *testing.T) {
			if got := PrimaryOf(tt.p); got != tt.primario {
				t.Errorf("PrimaryOf = %q, want %q", got, tt.primario)
			}
			if got := SecondaryOf(tt.p); got != tt.secundario {
				t.Errorf("SecondaryOf = %q, want %q", got, tt.secundario)
			}
		})
	}
}

// Returning "" is what makes the grouping treat the project as inline instead of opening a block literally named "".
func TestElGrupoVacioNoEsUnGrupoNiComoPrimaryNiComoSecundario(t *testing.T) {
	p := proyectoConGrupos("", "", "x")
	if PrimaryOf(p) != "" {
		t.Errorf("an empty primary returned %q: grouping would treat it as a group named %q", PrimaryOf(p), "")
	}
	if SecondaryOf(p) != "" {
		t.Errorf("an empty secondary returned %q", SecondaryOf(p))
	}
}

// Entries arrive already sorted by group, so opening a block is literally "the previous entry is a different group".
func TestIsPrimaryHeaderAbreUnBloqueSoloEnSuPrimeraEntrada(t *testing.T) {
	entradas := []Entry{
		{Primary: "shop"},
		{Primary: "shop"},
		{Primary: "shop"},
		{Primary: "blog"},
		{Primary: "blog"},
		{Primary: ""}, // inline: does not open a block
		{Primary: ""}, // inline: neither does this
		{Primary: "other"},
	}

	want := []bool{
		true,  // first of shop
		false, // second is already inside
		false, // third as well
		true,  // group change
		false, // inside blog
		false, // no group: does not open a block
		false, // no group: neither does this
		true,  // group again
	}

	for i := range entradas {
		if got := IsPrimaryHeader(entradas, i); got != want[i] {
			t.Errorf("IsPrimaryHeader(%d) = %v, want %v (group %q)", i, got, want[i], entradas[i].Primary)
		}
	}
}

// A single-member group has no previous entry of its own yet must still open a header, or its only project appears loose in the tree.
func TestUnBloqueDeUnSoloMiembroSiAbreCabecera(t *testing.T) {
	entradas := []Entry{
		{Primary: "blog"},
		{Primary: "shop"},
	}
	if !IsPrimaryHeader(entradas, 1) {
		t.Error("a group with a single member must open a header: otherwise the project " +
			"appears loose in the tree and its group disappears")
	}
	if !IsPrimaryHeader(entradas, 0) {
		t.Error("the first entry must open a header even without a previous one")
	}
}

// IsPrimaryHeader only works because Arrange keeps the members of a primary contiguous; interleaved groups would get two headers for one group and none for the other.
func TestSecundarioOrdenaElArbolSinMezclarGrupos(t *testing.T) {
	// The input order is interleaved on purpose, because the scan yields projects in directory order, not group order.
	proyectos := []scanner.Project{
		proyectoConGrupos("shop", "", "api"),
		proyectoConGrupos("blog", "", "api"),
		proyectoConGrupos("shop", "", "web"),
	}
	entradas := Arrange(proyectos)

	var cabeceras []string
	var grupoActual string
	for i := range entradas {
		if !IsPrimaryHeader(entradas, i) {
			if entradas[i].Primary != grupoActual {
				t.Errorf("entry %d (%s) says it does not open a block but belongs to group %q and the previous was %q: "+
					"groups are not sorted", i, entradas[i].Project.Name, entradas[i].Primary, grupoActual)
			}
			continue
		}
		if cabeceras != nil && cabeceras[len(cabeceras)-1] == entradas[i].Primary {
			t.Errorf("group %q opens two headers: members are not contiguous", entradas[i].Primary)
		}
		cabeceras = append(cabeceras, entradas[i].Primary)
		grupoActual = entradas[i].Primary
	}

	if len(cabeceras) != 2 {
		t.Errorf("%d headers were opened (%v), want 2: one per group", len(cabeceras), cabeceras)
	}
	// MEASURED: block order is first appearance, not alphabetical, so the tree follows the order in which the user walks their projects.
	if cabeceras[0] != "shop" {
		t.Errorf("the first header is %q, want shop: the order is first appearance", cabeceras[0])
	}
}

func proyectoConGrupos(primary, secondary, name string) scanner.Project {
	return scanner.Project{
		Path: "/" + name, Name: name,
		Manifest: &manifest.Manifest{
			Name: name, Command: "./" + name,
			PrimaryGroup: primary, SecondaryGroup: secondary,
		},
	}
}
