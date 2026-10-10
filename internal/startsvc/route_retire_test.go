package startsvc

import (
	"slices"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/state"
)

// A start that publishes nothing must retire the route a previous hostname start left behind; only a CLEAN retirement clears the handle, because a warning means something may still be registered and a later start must be able to reconcile it.
func TestSwitchToByPortRetiresThePreviousRoute(t *testing.T) {
	t.Run("a clean retirement clears the handle", func(t *testing.T) {
		fake := &fakeRoutes{}
		out, err := startRetire(t, state.Meta{RouteName: "tienda", RoutePort: 4321, RouteOwned: true, URLGeneration: manifest.URLGenByHostname},
			func(string) RouteRegistrar { return fake })
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(fake.retired, []string{"tienda"}) {
			t.Errorf("the previous route must be retired: %v", fake.retired)
		}
		if out.Meta.RouteName != "" || out.Meta.RouteOwned || out.Meta.RoutePort != 0 {
			t.Errorf("a clean retirement must clear the handle: %+v", out.Meta)
		}
	})

	t.Run("the seam is asked for the generation that registered it", func(t *testing.T) {
		fake := &fakeRoutes{}
		var asked []string
		out, err := startRetire(t, state.Meta{RouteName: "tienda", RoutePort: 4321, RouteOwned: true, URLGeneration: manifest.URLGenByHostname},
			func(gen string) RouteRegistrar {
				asked = append(asked, gen)
				if gen == manifest.URLGenByPort {
					return nil
				}
				return fake
			})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(asked, []string{manifest.URLGenByPort, manifest.URLGenByHostname}) {
			t.Errorf("the factory must be asked for the start generation and then the stale one: %v", asked)
		}
		if !slices.Equal(fake.retired, []string{"tienda"}) {
			t.Errorf("the stale route must be retired through its own generation's client: %v", fake.retired)
		}
		if out.Meta.RouteName != "" {
			t.Errorf("a clean retirement must clear the handle: %+v", out.Meta)
		}
	})

	t.Run("a handle from before generations falls back to the ladder", func(t *testing.T) {
		fake := &fakeRoutes{}
		var asked []string
		_, err := startRetire(t, state.Meta{RouteName: "tienda", RoutePort: 4321, RouteOwned: true},
			func(gen string) RouteRegistrar {
				asked = append(asked, gen)
				if gen == manifest.URLGenByPort {
					return nil
				}
				return fake
			})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(asked, []string{manifest.URLGenByPort, manifest.URLGenByHostnameOrWorkspace}) {
			t.Errorf("a meta without a generation must resolve through the ladder: %v", asked)
		}
		if !slices.Equal(fake.retired, []string{"tienda"}) {
			t.Errorf("the stale route must still be retired: %v", fake.retired)
		}
	})

	t.Run("without any client the handle survives for a later reconcile", func(t *testing.T) {
		out, err := startRetire(t, state.Meta{RouteName: "tienda", RoutePort: 4321, RouteOwned: true, URLGeneration: manifest.URLGenByHostname},
			func(string) RouteRegistrar { return nil })
		if err != nil {
			t.Fatal(err)
		}
		if out.Meta.RouteName != "tienda" || !out.Meta.RouteOwned {
			t.Errorf("a retirement nobody could perform must keep the handle: %+v", out.Meta)
		}
	})

	t.Run("a retirement that warns keeps the handle", func(t *testing.T) {
		fake := &fakeRoutes{retireWarns: []string{"the old route could not be removed"}}
		out, err := startRetire(t, state.Meta{RouteName: "tienda", RoutePort: 4321, RouteOwned: true, URLGeneration: manifest.URLGenByHostname},
			func(string) RouteRegistrar { return fake })
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(fake.retired, []string{"tienda"}) {
			t.Errorf("the retirement must be attempted: %v", fake.retired)
		}
		if out.Meta.RouteName != "tienda" || !out.Meta.RouteOwned {
			t.Errorf("a warned retirement must keep the handle so a later start can reconcile it: %+v", out.Meta)
		}
		if !slices.Contains(out.Warnings, "the old route could not be removed") {
			t.Errorf("the retirement warning must reach the caller: %v", out.Warnings)
		}
	})
}

// startRetire starts a by_port service (an explicit override, like the s menu's p) over a previous meta, with a manager that spawns nothing.
func startRetire(t *testing.T, prev state.Meta, factory RegistrarFunc) (Result, error) {
	t.Helper()
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(dir, prev); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{Name: "api", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "sleep 30"}}, Port: freePort(t), URLGeneration: manifest.URLGenByPort}
	return Start(Request{
		Manifest:         m,
		Path:             dir,
		Store:            store,
		Manager:          &pararAntesDeSalir{},
		StdoutPath:       store.StdoutLog(dir),
		StderrPath:       store.StderrLog(dir),
		DiscoveryTimeout: 2 * time.Second,
		Registrar:        factory,
		URLGeneration:    manifest.URLGenByPort,
	})
}
