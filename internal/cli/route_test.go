package cli

import (
	"encoding/json"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// Marshals for real instead of inspecting the struct, because the contract that matters is the published one.
func marshalInfo(t *testing.T, info ProjectInfo) map[string]any {
	t.Helper()
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Meta is persisted by project path, so the project path and the one the meta was saved under must match or buildProjectInfo reads an empty Meta.
func projectWith(dir string, m *manifest.Manifest) scanner.Project {
	return scanner.Project{Path: dir, Name: "proyecto", Configured: true, Manifest: m}
}

// url_generation is what the manifest asked for and the route object is what vroom achieved: conflating them makes a degraded route look like the one the user requested.
func TestJSONSeparatesIntentFromOutcome(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080,
		URLGeneration: manifest.URLGenByHostnameOrWorkspace, RouteName: "mi-url"}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 42, Port: 8080, State: state.StateRunning,
		RouteName: "mi-url", RoutePort: 8080,
		RouteStatus: portless.StatusDegraded, RouteReason: portless.ReasonProxyNotRunning,
	}); err != nil {
		t.Fatal(err)
	}

	info := buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m))
	out := marshalInfo(t, info)

	if out["url_generation"] != manifest.URLGenByHostnameOrWorkspace {
		t.Errorf("url_generation must publish the INTENT, got %v", out["url_generation"])
	}
	route, ok := out["route"].(map[string]any)
	if !ok {
		t.Fatalf("there must be a route object, got %v", out["route"])
	}
	if route["status"] != portless.StatusDegraded {
		t.Errorf("the route object must reflect the OUTCOME, got %v", route["status"])
	}
	if route["name"] != "mi-url" {
		t.Errorf("the published name is the INTENDED one, got %v", route["name"])
	}
}

// The published name is never a URL, because an agent reading it as a name would try to open it as a host.
func TestPublishedRouteNameIsNeverAURL(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080, URLGeneration: manifest.URLGenByWorkspaceHostname}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 1, Port: 8080, State: state.StateRunning,
		RouteName: "feat.api", RouteStatus: portless.StatusRegistered, RouteURL: "https://feat.api.localhost",
	}); err != nil {
		t.Fatal(err)
	}
	info := buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m))
	route, _ := marshalInfo(t, info)["route"].(map[string]any)
	name, _ := route["name"].(string)
	if name != "feat.api" {
		t.Errorf("the name must be the name, not a url: %q", name)
	}
}

// The ABSENT field is what makes the contract honest: an agent reading it cannot connect to an address nobody checked.
func TestDegradedRoutePublishesNoURL(t *testing.T) {
	for _, reason := range []string{
		portless.ReasonPortlessMissing, portless.ReasonPortlessTimeout,
		portless.ReasonPortlessFailed, portless.ReasonProxyNotRunning,
		portless.ReasonProxyUnreachable, portless.ReasonRouteConflict,
		portless.ReasonPortUnresolved, portless.ReasonRouteNotServed,
	} {
		store := state.NewStoreAt(t.TempDir())
		dir := t.TempDir()
		m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080, URLGeneration: manifest.URLGenByWorkspaceHostname}
		if err := store.SaveMeta(dir, state.Meta{
			Name: "p", Pid: 1, Port: 8080, State: state.StateRunning,
			RouteName: "p", RouteStatus: portless.StatusDegraded, RouteReason: reason,
			RouteURL: "https://p.localhost",
		}); err != nil {
			t.Fatal(err)
		}
		out := marshalInfo(t, buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m)))
		route, ok := out["route"].(map[string]any)
		if !ok {
			t.Fatalf("[%s] there must be a route object", reason)
		}
		if _, hasURL := route["url"]; hasURL {
			t.Errorf("[%s] a degraded route CANNOT publish a url", reason)
		}
		if route["reason"] != reason {
			t.Errorf("[%s] there must be a machine-readable reason, got %v", reason, route["reason"])
		}
		if route["status"] == portless.StatusRegistered {
			t.Errorf("[%s] no field can claim that a route is available", reason)
		}
	}
}

// A registered route does publish its URL: the contract is exact, not timid.
func TestRegisteredRoutePublishesItsURL(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080, URLGeneration: manifest.URLGenByWorkspaceHostname}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 1, Port: 8080, State: state.StateRunning,
		RouteName: "p", RoutePort: 8080,
		RouteStatus: portless.StatusRegistered, RouteURL: "https://p.localhost",
	}); err != nil {
		t.Fatal(err)
	}
	route, ok := marshalInfo(t, buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m)))["route"].(map[string]any)
	if !ok {
		t.Fatal("a registered route must be published")
	}
	if route["url"] != "https://p.localhost" {
		t.Errorf("a registered route publishes its url, got %v", route["url"])
	}
}

// A by_port service neither affirms nor denies a route: the route object stays ABSENT, since claiming "no route" would invent a contract nobody asked for. url_generation still publishes, because it is the one start axis and by_port is a real answer to "how does this start".
func TestNoRouteContractPublishesNoRouteObject(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080}
	out := marshalInfo(t, buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m)))
	if _, has := out["route"]; has {
		t.Error("without a route contract the route object must be ABSENT")
	}
	if out["url_generation"] != manifest.URLGenByPort {
		t.Errorf("url_generation = %v, want by_port: the axis is always answered", out["url_generation"])
	}
}

// The backwards-compatibility gate, in JSON: a manifest that never heard of portless publishes no route object and no dead route_mode/vocabulary keys — only the ONE axis, always answered (url_generation is the deliberate contract change that replaced port_mode + route_mode).
func TestLegacyManifestJSONIsUnchanged(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080}
	if err := store.SaveMeta(dir, state.Meta{Name: "p", Pid: 7, Port: 8080, State: state.StateRunning}); err != nil {
		t.Fatal(err)
	}
	out := marshalInfo(t, buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m)))
	for _, forbidden := range []string{"route", "route_name", "route_url", "port_mode"} {
		if _, has := out[forbidden]; has {
			t.Errorf("a legacy manifest must not publish %q", forbidden)
		}
	}
	if out["url_generation"] != manifest.URLGenByPort {
		t.Errorf("url_generation = %v, want by_port", out["url_generation"])
	}
	if out["port"].(float64) != 8080 {
		t.Errorf("the port must keep being published the same: %v", out["port"])
	}
}

// The JSON does not claim a route exists just because it is WRITTEN: a Meta with a route but no verification does not publish it.
func TestJSONDoesNotAssertRouteJustBecauseItIsWritten(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080, URLGeneration: manifest.URLGenByWorkspaceHostname}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 1, Port: 8080, State: state.StateRunning,
		RouteName: "p", RouteStatus: "", RouteReason: "",
	}); err != nil {
		t.Fatal(err)
	}
	out := marshalInfo(t, buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m)))
	if _, has := out["route"]; has {
		t.Error("a written and unverified route cannot appear as available")
	}
}

// An inert process.Manager for these tests: the JSON surface is built from the Meta, not from the process.
type stubManager struct{}

func (stubManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (stubManager) Stop(process.StopSpec) error { return nil }
func (stubManager) Evaluate(process.EvalSpec) process.Status {
	return process.StatusRunning
}
