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
		t.Errorf("two distinct branches must derive distinct names, both %q", one)
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
		t.Skip("DeriveName already distinguishes worktrees on the same branch; update the README and this test")
	}
	if Hostname(a) != "main.miapp.localhost" {
		t.Errorf("the derived name must be <branch>.<project>, got %q", a)
	}
}

func TestSameBranchCollisionDegradesWithoutEvicting(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("main.miapp")] = 4000

	res := c.Apply("main.miapp", 5000, Ownership{})

	if res.Succeeded() {
		t.Fatal("a name collision must degrade, not publish a foreign url")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("the reason must be route_conflict, got %q", res.Reason)
	}
	if got := f.routes[Hostname("main.miapp")]; got != 4000 {
		t.Errorf("the first worktree's route must remain intact at 4000, got %d", got)
	}
}

// named is the documented answer to the collision: a name that does not depend on the branch.
func TestNamedIsTheEscapeFromBranchScopedNames(t *testing.T) {
	got, err := DeriveName(RouteModeNamed, "mi-api", "main", "miapp")
	if err != nil {
		t.Fatal(err)
	}
	if got != "mi-api" {
		t.Errorf("named ignores the branch on purpose, got %q", got)
	}
	again, _ := DeriveName(RouteModeNamed, "mi-api", "otra-rama", "otro-proyecto")
	if again != got {
		t.Errorf("named must be stable across branches, %q != %q", got, again)
	}
}
