package tui

import (
	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/startsvc"
	"vroom/internal/state"
)

// app.go's only two doorways into the portless seam, enforced by TestDiscoveryIsNotInTheTUIRefreshPath because it execs the binary and the 2s refresh tick would spawn once per service (see docs/adr/adr-0013-vroom-registers-portless-routes.md).
var (
	routeStubInstalled bool
	tuiReleaseStub     portless.ReleaserFunc // in production, not _test.go, because releaseRoute must read it: without an injectable seam, deleting the Release call sites left the suite green and ADR 13's removal verified nothing
)

func portlessClient(m *manifest.Manifest) startsvc.RouteRegistrar {
	return startsvc.RegistrarFor(m)
}

// The stub check tests flag AND pointer, because a test that sets only the flag leaves a Releaser wrapping a nil func and the first Remove panics.
func releaseRoute(meta *state.Meta) {
	if !meta.RouteOwned {
		return
	}
	if !portless.Release(tuiRouteReleaser(), meta.RouteName) {
		return
	}
	meta.RouteOwned = false
}

func tuiRouteReleaser() portless.Releaser {
	if routeStubInstalled && tuiReleaseStub != nil {
		return tuiReleaseStub
	}
	if portless.IsTestBinary() {
		return portless.InertReleaser()
	}
	return nil
}
