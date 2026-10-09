package portless

import (
	"slices"
	"testing"
)

// The ladder is the whole feature: named_with_auto_fallback claims the stable name first and only then the branch name, so whoever starts first keeps the URL a frontend or OAuth callback hardcodes while every other worktree still gets an address.

func TestRouteCandidatesIsTheClaimLadder(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		routeName string
		branch    string
		project   string
		want      []string
	}{
		{
			name:      "the stable name first, the branch name as fallback",
			mode:      RouteModeNamedWithAutoFallback,
			routeName: "api",
			branch:    "feature/x",
			project:   "svc",
			want:      []string{"api", "feature-x.svc"},
		},
		{
			name:    "auto has a single candidate: its value is that the hostname does not depend on who started first",
			mode:    RouteModeAuto,
			branch:  "feature/x",
			project: "svc",
			want:    []string{"feature-x.svc"},
		},
		{
			name:      "a fallback equal to the primary is not a second rung",
			mode:      RouteModeNamedWithAutoFallback,
			routeName: "feature-x.svc",
			branch:    "feature/x",
			project:   "svc",
			want:      []string{"feature-x.svc"},
		},
		{
			name:      "without a branch the fallback is the project name",
			mode:      RouteModeNamedWithAutoFallback,
			routeName: "api",
			project:   "svc",
			want:      []string{"api", "svc"},
		},
		{
			name:      "a branch that sanitizes still yields a usable second rung",
			mode:      RouteModeNamedWithAutoFallback,
			routeName: "api",
			branch:    "Feat/My_Branch",
			project:   "svc",
			want:      []string{"api", "feat-my-branch.svc"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RouteCandidates(tc.mode, tc.routeName, tc.branch, tc.project)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("RouteCandidates = %v, want %v", got, tc.want)
			}
		})
	}
}

// A ladder that starts from garbage must fail the same way DeriveName does: an unknown mode or an unusable route_name is a name nobody would use, never an empty first rung.
func TestRouteCandidatesRejectsWhatDeriveNameRejects(t *testing.T) {
	if _, err := RouteCandidates("inventado", "", "main", "svc"); err == nil {
		t.Error("an unknown route_mode must be rejected")
	}
	if _, err := RouteCandidates(RouteModeNamedWithAutoFallback, "///", "main", "svc"); err == nil {
		t.Error("an unusable route_name must be rejected, not fall back into an empty name")
	}
}

// A fallback worktree persists the branch-derived name. If Reconcile read that as a branch rename it would delete the very route this start is about to reuse, so membership in the candidate set — not equality with one of them — is the no-op test.
func TestReconcileKeepsAPersistedNameThatIsStillACandidate(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("feature-x.api")] = 4321

	warns := c.Reconcile("feature-x.api", Ownership{Owned: true, Port: 4321}, "api", "feature-x.api")

	if len(warns) != 0 {
		t.Errorf("a persisted name that is still a candidate generates no warnings: %v", warns)
	}
	if len(f.removedNames) != 0 {
		t.Errorf("the route the ladder is about to reuse must not be removed: %v", f.removedNames)
	}
	if _, still := f.routes[Hostname("feature-x.api")]; !still {
		t.Error("the candidate route must survive reconciliation untouched")
	}
}
