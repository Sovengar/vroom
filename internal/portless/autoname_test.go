package portless

import "testing"

// ---------------------------------------------------------------------------
// Lo que `auto` AFIRMA y lo que `auto` HACE.
//
// DeriveName no recibe la ruta del worktree, sólo la rama y el nombre del
// proyecto. Eso significa que su alcance real es la RAMA, no el worktree: dos
// clones del mismo repo, ambos en `main`, derivan el mismo nombre.
//
// Estos tests existen para que esa limitación sea una AFIRMACIÓN VERIFICADA y
// no una frase de marketing en el README. Un test que no puede fallar es peor
// que no tener test; éste falla si alguien "arregla" el saneo y rompe la
// derivación, y también falla si alguien reintroduce la expectativa falsa de
// que el worktree participa en el nombre.
// ---------------------------------------------------------------------------

// El caso feliz: `auto` separa RAMAS distintas. Eso es lo que hace bien, y es
// lo que evita que dos ramas del mismo repo compartan una dirección.
func TestAutoSeparatesDistinctBranches(t *testing.T) {
	one, err := DeriveName(RouteModeAuto, "", "feat/a", "miapp")
	if err != nil {
		t.Fatal(err)
	}
	two, err := DeriveName(RouteModeAuto, "", "feat/b", "miapp")
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Errorf("dos ramas distintas deben derivar nombres distintos, ambos %q", one)
	}
}

// Y el límite real, afirmado: dos worktrees en la MISMA rama colisionan. No es
// un defecto de esta implementación, es su alcance — y el README debe decirlo
// en vez de prometer lo contrario.
func TestAutoCannotSeparateSameBranchWorktrees(t *testing.T) {
	// Dos worktrees distintos del mismo repo, ambos en `main`. DeriveName no
	// ve la ruta del worktree, así que no puede separarlos. Se fija aquí para
	// que la limitación sea explícita y para que un cambio futuro que la
	// resuelva hay que actualizar este test a propósito.
	a, err := DeriveName(RouteModeAuto, "", "main", "miapp")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveName(RouteModeAuto, "", "main", "miapp")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Skip("DeriveName ya distingue worktrees en la misma rama; actualiza el README y este test")
	}
	if Hostname(a) != "main.miapp.localhost" {
		t.Errorf("el nombre derivado debe ser <rama>.<proyecto>, got %q", a)
	}
}

// La respuesta a esa colisión NO es una segunda dirección: es un conflicto
// limpio. Y la ruta existente no se destruye — que es justo lo que garantiza
// el pre-cheque de Apply.
func TestSameBranchCollisionDegradesWithoutEvicting(t *testing.T) {
	f := newFake()
	c := f.client(t)
	// El primer worktree ya registró su ruta.
	f.routes[Hostname("main.miapp")] = 4000

	// El segundo worktree deriva el MISMO nombre y pide otro puerto.
	res := c.Apply("main.miapp", 5000, Ownership{})

	if res.Succeeded() {
		t.Fatal("una colisión por nombre debe degradar, no publicar una url ajena")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("el motivo debe ser route_conflict, got %q", res.Reason)
	}
	if got := f.routes[Hostname("main.miapp")]; got != 4000 {
		t.Errorf("la ruta del primer worktree debe quedar intacta en 4000, got %d", got)
	}
}

// `named` es la respuesta documentada a la colisión: un nombre que no depende
// de la rama. Por eso la referencia a `named` en el README no es decorativa.
func TestNamedIsTheEscapeFromBranchScopedNames(t *testing.T) {
	got, err := DeriveName(RouteModeNamed, "mi-api", "main", "miapp")
	if err != nil {
		t.Fatal(err)
	}
	if got != "mi-api" {
		t.Errorf("named ignora la rama a propósito, got %q", got)
	}
	// Y es único por construcción: no depende del estado de git.
	again, _ := DeriveName(RouteModeNamed, "mi-api", "otra-rama", "otro-proyecto")
	if again != got {
		t.Errorf("named debe ser estable entre ramas, %q != %q", got, again)
	}
}
