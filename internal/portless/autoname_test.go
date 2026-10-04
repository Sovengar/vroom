package portless

import "testing"

// DeriveName never sees the worktree path, only branch and project, so auto's real scope is the branch: two clones on main derive the same name.

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

func TestAutoCannotSeparateSameBranchWorktrees(t *testing.T) {
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

func TestSameBranchCollisionDegradesWithoutEvicting(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("main.miapp")] = 4000

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

// named is the documented answer to the collision: a name that does not depend on the branch.
func TestNamedIsTheEscapeFromBranchScopedNames(t *testing.T) {
	got, err := DeriveName(RouteModeNamed, "mi-api", "main", "miapp")
	if err != nil {
		t.Fatal(err)
	}
	if got != "mi-api" {
		t.Errorf("named ignora la rama a propósito, got %q", got)
	}
	again, _ := DeriveName(RouteModeNamed, "mi-api", "otra-rama", "otro-proyecto")
	if again != got {
		t.Errorf("named debe ser estable entre ramas, %q != %q", got, again)
	}
}
