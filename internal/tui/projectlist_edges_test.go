package tui

import (
	"strings"
	"testing"

	"vroom/internal/group"
	"vroom/internal/manifest"
	"vroom/internal/scanner"
	"vroom/internal/tail"
)

func TestFilterMatchMatchesByNameAndByGroup(t *testing.T) {
	tests := []struct {
		name    string
		p       scanner.Project
		q       string
		want    bool
		because string
	}{
		{
			"exact by name", scanner.Project{Name: "tienda-api"}, "tienda-api", true, "",
		},
		{
			"prefix of name", scanner.Project{Name: "tienda-api"}, "tienda", true, "",
		},
		{
			"case insensitive", scanner.Project{Name: "Tienda-Api"}, "tienda", true, "",
		},
		{
			"by primary group", scanner.Project{Name: "api", Manifest: manifestGroup("tienda", "")}, "tienda", true,
			"filtering by group must return a member that is not named after the group",
		},
		{
			"by secondary group", scanner.Project{Name: "api", Manifest: manifestGroup("tienda", "backend")}, "backend", true,
			"the secondary also matches: it is what distinguishes two 'api' from the same primary",
		},
		{
			"other group", scanner.Project{Name: "api", Manifest: manifestGroup("tienda", "backend")}, "blog", false, "",
		},
		{
			"empty string returns all", scanner.Project{Name: "any"}, "", true,
			"an empty filter cannot filter anything: the filter would open and all rows would disappear",
		},
		{
			"project without group or matching name", scanner.Project{Name: "api"}, "web", false, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filterMatch(tt.p, tt.q); got != tt.want {
				t.Errorf("filterMatch(%q, %q) = %v, want %v. %s", tt.p.Name, tt.q, got, tt.want, tt.because)
			}
		})
	}
}

func TestFilterMatchDoesNotMatchWhatItShouldNotAndCountChecksOut(t *testing.T) {
	projects := []scanner.Project{
		{Name: "tienda-api", Manifest: manifestGroup("tienda", "backend")},
		{Name: "tienda-web", Manifest: manifestGroup("tienda", "frontend")},
		{Name: "blog-api", Manifest: manifestGroup("blog", "")},
		{Name: "suelto"},
	}

	for _, q := range []string{"tienda", "api", "backend", "blog", "suelto", "none-of-this", ""} {
		var withMatch []string
		for _, p := range projects {
			if filterMatch(p, q) {
				withMatch = append(withMatch, p.Name)
			}
		}
		// Names in this fixture are unique, so set equality with the filter result is an exact check.
		for _, p := range projects {
			has := false
			for _, n := range withMatch {
				if n == p.Name {
					has = true
				}
			}
			if has != filterMatch(p, q) {
				t.Errorf("q=%q: the list and filterMatch disagree on %q", q, p.Name)
			}
		}
	}

	var tienda []string
	for _, p := range projects {
		if filterMatch(p, "tienda") {
			tienda = append(tienda, p.Name)
		}
	}
	if len(tienda) != 2 || tienda[0] != "tienda-api" || tienda[1] != "tienda-web" {
		t.Errorf("the tienda filter returns %v, want the two from the group and nothing more", tienda)
	}

	var api []string
	for _, p := range projects {
		if filterMatch(p, "api") {
			api = append(api, p.Name)
		}
	}
	if len(api) != 2 {
		t.Errorf("the api filter returns %v, want the two", api)
	}
}

// The example manifest carries no port on purpose: an example port belongs to a twin worktree, so the health probe would hit that other service.
func TestExampleManifestIsValidAndDoesNotCollideWithAnything(t *testing.T) {
	got := exampleManifest("my-project")

	if strings.Contains(got, "port = 80") || strings.Contains(got, "port = 3000") {
		t.Errorf("the example manifest carries an invented port: %q", got)
	}

	// Names with quotes or backslashes must still yield quoted, parseable TOML, hence the odd inputs below.
	for _, name := range []string{"normal", `with "quotes"`, "with\\slash", "accented-λ"} {
		txt := exampleManifest(name)
		if !strings.Contains(txt, `name = "`) {
			t.Errorf("exampleManifest(%q) does not quote the name: %q", name, txt)
		}
		if !strings.Contains(txt, "commands.start.run") {
			t.Errorf("exampleManifest(%q) does not carry commands.start.run, which is the required field: %q", name, txt)
		}
	}
}

func TestTreeDotCoversStatesAndUnknown(t *testing.T) {
	configured := scanner.Project{Path: "/p", Name: "p", Configured: true, Manifest: manifestWithPort(4321)}
	noManifest := scanner.Project{Path: "/p", Name: "p", Configured: false}
	brokenManifest := scanner.Project{Path: "/p", Name: "p", Configured: true, ManifestErr: "missing commands.start.run"}

	tests := []struct {
		name  string
		p     scanner.Project
		sv    *ServiceState
		wants string
	}{
		{"running", configured, &ServiceState{Status: statusRunning}, "●"},
		{"starting", configured, &ServiceState{Status: statusStarting}, "◌"},
		{"stopping", configured, &ServiceState{Status: statusStopping}, "○"},
		{"unknown", configured, &ServiceState{Status: statusUnknown}, "◐"},
		{"stopped", configured, &ServiceState{Status: statusStopped}, "·"},
		// MEASURED (bug): all three fell into the default so a live service rendered with the stopped dot; port_pending also contradicted its own row badge and the "s" action that treats it as live.
		{"port pending", configured, &ServiceState{Status: statusPortPending}, "◌"},
		{"port unresolved", configured, &ServiceState{Status: statusPortUnresolved}, "●"},
		{"no port", configured, &ServiceState{Status: statusNoPort}, "●"},
		{"no manifest", noManifest, &ServiceState{Status: statusRunning}, "⚠"},
		{"invalid manifest", brokenManifest, &ServiceState{Status: statusStopped}, "⚠"},
		{"new process status", configured, &ServiceState{Status: uiStatus("invented")}, "·"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tail.StripANSI(treeDot(tt.p, tt.sv, "◐", "◌"))
			if !strings.Contains(got, tt.wants) {
				t.Errorf("treeDot(%s) = %q, want %q", tt.name, got, tt.wants)
			}
		})
	}

	t.Run("the dot never contradicts its row badge", func(t *testing.T) {
		for _, st := range []uiStatus{
			statusRunning, statusStarting, statusPortPending, statusPortUnresolved, statusNoPort,
		} {
			dot := tail.StripANSI(treeDot(configured, &ServiceState{Status: st}, "◐", "◌"))
			if dot == "·" {
				t.Errorf("the status %q is live but the tree paints it as stopped", st)
			}
		}
	})

	t.Run("no known status", func(t *testing.T) {
		got := tail.StripANSI(treeDot(configured, nil, "◐", "◌"))
		if !strings.Contains(got, "·") {
			t.Errorf("no status = %q, want the stopped dot: it has not been asked, it cannot be said to be alive", got)
		}
	})
}

func TestTreeEmitsComposersHeaderOnlyWithStacksThere(t *testing.T) {
	// The stackTree fixture mixes stacks and plain projects under one primary.
	m := newStackModel(t)

	var withComposers, withStack, withProject bool
	for _, it := range m.tree {
		if it.kind == itemSecondary && it.secondary == composersGroup {
			withComposers = true
		}
		if it.kind == itemStack {
			withStack = true
		}
		if it.kind == itemProject {
			withProject = true
		}
	}
	if !withComposers || !withStack || !withProject {
		t.Errorf("with stacks the tree must emit all three: composers=%v stack=%v project=%v",
			withComposers, withStack, withProject)
	}

	t.Run("the composers header carries the group and the marker", func(t *testing.T) {
		var found bool
		for _, it := range m.tree {
			if it.kind == itemSecondary && it.secondary == composersGroup {
				found = true
				if it.primary == "" {
					t.Error("the composers header without primary: it is not known which group it belongs to")
				}
			}
		}
		if !found {
			t.Error("the composers header was not found")
		}
	})

	t.Run("primary without stacks does not emit composers header", func(t *testing.T) {
		noStacks, _ := newTestModel(t)
		for _, it := range noStacks.tree {
			if it.kind == itemSecondary && it.secondary == composersGroup {
				t.Error("without a compose file the tree cannot emit a composers header: it would be an empty row")
			}
		}
	})
}

func TestGroupPrimaryAndSecondaryOfProjectWithoutManifestDoNotPanic(t *testing.T) {
	various := []scanner.Project{
		{},
		{Name: "no-manifest"},
		{Name: "with-nil-manifest", Manifest: nil},
		{Name: "with-group", Manifest: manifestGroup("tienda", "backend")},
		{Name: "primary-only", Manifest: manifestGroup("tienda", "")},
	}
	for _, p := range various {
		_ = group.PrimaryOf(p)
		_ = group.SecondaryOf(p)
		if got := filterMatch(p, "tienda"); got && p.Manifest == nil {
			t.Errorf("a project without a manifest matched by a group it does not have: %+v", p)
		}
	}
}

func manifestGroup(primary, secondary string) *manifest.Manifest {
	return &manifest.Manifest{
		Name:           "p",
		Commands:       manifest.Commands{Start: manifest.StartCommand{Run: "./p"}},
		PrimaryGroup:   primary,
		SecondaryGroup: secondary,
	}
}
