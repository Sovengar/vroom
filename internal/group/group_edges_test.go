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
		{"sin manifiesto en absoluto", scanner.Project{Name: "suelto"}, "", ""},
		{"manifiesto a nil explícito", scanner.Project{Name: "x", Manifest: nil}, "", ""},
		{"con ambos grupos", proyectoConGrupos("tienda", "backend", "api"), "tienda", "backend"},
		{"sólo primario", proyectoConGrupos("tienda", "", "api"), "tienda", ""},
		{"sólo secundario", proyectoConGrupos("", "backend", "api"), "", "backend"},
		{"sin ningún grupo", proyectoConGrupos("", "", "x"), "", ""},
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
		t.Errorf("un primary vacío devolvió %q: el agrupamiento lo trataría como un grupo llamado %q", PrimaryOf(p), "")
	}
	if SecondaryOf(p) != "" {
		t.Errorf("un secondary vacío devolvió %q", SecondaryOf(p))
	}
}

// Entries arrive already sorted by group, so opening a block is literally "the previous entry is a different group".
func TestIsPrimaryHeaderAbreUnBloqueSoloEnSuPrimeraEntrada(t *testing.T) {
	entradas := []Entry{
		{Primary: "tienda"},
		{Primary: "tienda"},
		{Primary: "tienda"},
		{Primary: "blog"},
		{Primary: "blog"},
		{Primary: ""}, // inline: no abre bloque
		{Primary: ""}, // inline: tampoco
		{Primary: "otro"},
	}

	want := []bool{
		true,  // la primera de tienda
		false, // la segunda ya está dentro
		false, // la tercera también
		true,  // cambio de grupo
		false, // dentro de blog
		false, // sin grupo: no abre bloque
		false, // sin grupo: tampoco
		true,  // vuelta a haber grupo
	}

	for i := range entradas {
		if got := IsPrimaryHeader(entradas, i); got != want[i] {
			t.Errorf("IsPrimaryHeader(%d) = %v, want %v (grupo %q)", i, got, want[i], entradas[i].Primary)
		}
	}
}

// A single-member group has no previous entry of its own yet must still open a header, or its only project appears loose in the tree.
func TestUnBloqueDeUnSoloMiembroSiAbreCabecera(t *testing.T) {
	entradas := []Entry{
		{Primary: "blog"},
		{Primary: "tienda"},
	}
	if !IsPrimaryHeader(entradas, 1) {
		t.Error("un grupo con un solo miembro tiene que abrir cabecera: si no, el proyecto " +
			"aparece suelto en el árbol y su grupo desaparece")
	}
	if !IsPrimaryHeader(entradas, 0) {
		t.Error("la primera entrada tiene que abrir cabecera aunque no tenga anterior")
	}
}

// IsPrimaryHeader only works because Arrange keeps the members of a primary contiguous; interleaved groups would get two headers for one group and none for the other.
func TestSecundarioOrdenaElArbolSinMezclarGrupos(t *testing.T) {
	// The input order is interleaved on purpose, because the scan yields projects in directory order, not group order.
	proyectos := []scanner.Project{
		proyectoConGrupos("tienda", "", "api"),
		proyectoConGrupos("blog", "", "api"),
		proyectoConGrupos("tienda", "", "web"),
	}
	entradas := Arrange(proyectos)

	var cabeceras []string
	var grupoActual string
	for i := range entradas {
		if !IsPrimaryHeader(entradas, i) {
			if entradas[i].Primary != grupoActual {
				t.Errorf("la entrada %d (%s) dice que no abre bloque pero es del grupo %q y el anterior era %q: "+
					"los grupos no están ordenados", i, entradas[i].Project.Name, entradas[i].Primary, grupoActual)
			}
			continue
		}
		if cabeceras != nil && cabeceras[len(cabeceras)-1] == entradas[i].Primary {
			t.Errorf("el grupo %q abre dos cabeceras: los miembros no están contiguos", entradas[i].Primary)
		}
		cabeceras = append(cabeceras, entradas[i].Primary)
		grupoActual = entradas[i].Primary
	}

	if len(cabeceras) != 2 {
		t.Errorf("se abrieron %d cabeceras (%v), want 2: una por grupo", len(cabeceras), cabeceras)
	}
	// MEDIDO: block order is first appearance, not alphabetical, so the tree follows the order in which the user walks their projects.
	if cabeceras[0] != "tienda" {
		t.Errorf("la primera cabecera es %q, want tienda: el orden es el de primera aparición", cabeceras[0])
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
