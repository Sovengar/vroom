// Package startsvc owns the whole start sequence: it reserves and verifies the real port (docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md) and is the only place on that path that talks to portless, where a missing or broken portless degrades to a warning instead of failing the start (docs/adr/adr-0013-vroom-registers-portless-routes.md).
package startsvc

import (
	"fmt"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

type Request struct {
	Manifest   *manifest.Manifest
	Path       string
	Store      *state.Store
	Manager    process.Manager
	StdoutPath string
	StderrPath string

	DiscoveryTimeout time.Duration

	// Routes is the seam to portless; nil means this service registers no routes (route_mode = "off") and keeps the suite hermetic on a CI runner with no portless, no Node 24 and no proxy.
	Routes RouteRegistrar

	Branch string
}

// RegistrarFor normalises to the interface here because a typed-nil *portless.Client is not a nil RouteRegistrar, which would silently defeat the Routes == nil check.
func RegistrarFor(m *manifest.Manifest) RouteRegistrar {
	c := portless.ClientFor(m)
	if c == nil {
		return nil
	}
	return c
}

type RouteRegistrar interface {
	// Apply registers the service route and reports only what it could prove; it never errors because an absent portless is a degradation, not a start failure.
	Apply(name string, port int, prev portless.Ownership) portless.Result
	// Reconcile returns warnings, never errors; held is an Ownership handle rather than a port, because a live handle does not mean the name is still ours and a raw port would be authority to delete someone else's route.
	Reconcile(prev string, held portless.Ownership, current string) []string
}

type Result struct {
	Meta     state.Meta
	Pid      int
	Port     int
	Warnings []string
}

func Start(req Request) (Result, error) {
	mode := req.Manifest.EffectivePortMode()

	reserved, env := 0, []string(nil)
	if mode == manifest.PortModeDynamic {
		p, err := process.ReservePort()
		if err != nil {
			return Result{}, fmt.Errorf("could not reserve a dynamic port: %w", err)
		}
		reserved = p
		// HOST rides along with PORT so the app does not bind 0.0.0.0 and expose itself to the network by default.
		env = []string{fmt.Sprintf("PORT=%d", reserved), "HOST=127.0.0.1"}
	}

	res, err := req.Manager.Start(process.StartSpec{
		Command:    req.Manifest.Command,
		WorkDir:    req.Path,
		StdoutPath: req.StdoutPath,
		StderrPath: req.StderrPath,
		Env:        env,
	})
	if err != nil {
		process.ReleasePort(reserved) // the child never came into existence
		return Result{}, err
	}

	base := state.Meta{
		Name:           req.Manifest.Name,
		ProjectPath:    req.Path,
		Port:           req.Manifest.Port,
		ProcessPattern: req.Manifest.ProcessPattern,
		Command:        req.Manifest.Command,
		Pid:            res.Pid,
		Pgid:           res.Pgid,
		CreationTimeMs: res.CreationTimeMs,
		StartedAt:      time.Now().Format(time.RFC3339),
		State:          state.StateRunning,
	}

	// All three route facts are inherited or none: copying name and port but not ownership silently loses the grant on every start.
	if prev, err := req.Store.LoadMeta(req.Path); err == nil {
		base.RouteName = prev.RouteName
		base.RoutePort = prev.RoutePort
		base.RouteOwned = prev.RouteOwned
	}

	if mode != manifest.PortModeDynamic {
		out := Result{Meta: base, Pid: res.Pid, Port: req.Manifest.Port}
		if mode == manifest.PortModeFixed {
			applyRoute(req, &base, req.Manifest.Port, &out)
		}
		if err := persistOrKill(req, base, res); err != nil {
			return Result{}, err
		}
		_ = req.Store.RegisterPid(req.Path, res.Pid, res.Pgid)
		return out, nil
	}
	return resolveDynamicPort(req, base, reserved)
}

func resolveDynamicPort(req Request, attempt state.Meta, reserved int) (Result, error) {
	// The attempt lands on disk BEFORE discovery: a concurrent tick then reads this, never the previous run's meta whose CreationTimeMs no longer describes anything.
	attempt.Port = reserved
	attempt.ReservedPort = reserved
	attempt.State = state.StatePortPending
	if err := persistOrKill(req, attempt, process.StartResult{Pid: attempt.Pid, Pgid: attempt.Pgid}); err != nil {
		return Result{}, err
	}
	_ = req.Store.RegisterPid(req.Path, attempt.Pid, attempt.Pgid)

	timeout := req.DiscoveryTimeout
	if timeout <= 0 {
		timeout = process.DefaultDynamicPortTimeout
	}
	d := process.DiscoverPort(attempt.Pid, reserved, req.Manifest.HealthURLPath(), timeout)
	if d.LineageDead {
		// The lineage is gone, so the reserved port is a wasted slot and must go back to the set.
		process.ReleasePort(reserved)
		return Result{}, fmt.Errorf("service exited during startup (no port to resolve)")
	}

	final := attempt
	final.Port = d.Port
	out := Result{Pid: attempt.Pid, Port: d.Port}

	switch {
	case d.Unresolved:
		// Not StateNoPort: this asserts the port is unknown rather than absent, so displayPort cannot fall back to the declared port and probe a twin worktree's.
		final.State = state.StatePortUnresolved
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("service did not bind within %s; its port is unresolved, not absent — restart the service to retry discovery", timeout))
	case d.Port == 0 && len(d.All) == 0:
		final.State = state.StateNoPort
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("service has no TCP port (mode %q); port health checks are disabled",
				req.Manifest.EffectivePortMode()))
	default:
		final.State = state.StateRunning
		final.PortVerified = d.Verified
		if !d.HonoredReserved && reserved != d.Port {
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("service ignored the offered port %d and bound %d instead", reserved, d.Port))
		}
		if !d.Verified {
			// R3: nothing tells us which listener is primary, so the lowest wins and is declared unverified instead of disguised.
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"service opened several ports %v and none answers health_path; picked %d as best guess (unverified port)",
				d.All, d.Port))
		}
	}

	out.Meta = final
	// The route goes in after discovery and before the final SaveMeta: the real port is confirmed, and with an unresolved port nothing is registered, which is what rules out the ADR's 502 window.
	if final.State == state.StateRunning && final.Port > 0 {
		applyRoute(req, &final, final.Port, &out)
		out.Meta = final
	}
	if err := persistOrKill(req, final, process.StartResult{Pid: final.Pid, Pgid: final.Pgid}); err != nil {
		return Result{}, err
	}
	return out, nil
}

func applyRoute(req Request, meta *state.Meta, port int, out *Result) {
	if req.Routes == nil {
		return
	}

	name, err := routeName(req)
	if err != nil {
		out.Warnings = append(out.Warnings, err.Error())
		return
	}

	// Reconcile first: prune never touches alias routes (M5), so a renamed branch would leave the old route pointing at a dead port forever.
	out.Warnings = append(out.Warnings, req.Routes.Reconcile(meta.RouteName, portless.Ownership{Owned: meta.RouteOwned, Port: meta.RoutePort}, name)...)

	res := req.Routes.Apply(name, port, portless.Ownership{Owned: meta.RouteOwned, Port: meta.RoutePort})
	meta.RouteName = res.Name
	meta.RoutePort = port
	meta.RouteStatus = res.Status
	meta.RouteReason = res.Reason
	// Ownership is granted on res.Registered, not res.Succeeded(): a route written with the proxy down is genuinely ours, and dropping its handle would make a restart on a moved port collide with its own route.
	meta.RouteOwned = res.Registered
	// Only a URL seen answering is persisted: an unverified one publishes a false address to whoever reads the meta later.
	if res.Succeeded() {
		meta.RouteURL = res.Url
	} else {
		meta.RouteURL = ""
	}
	if warn := portless.Warn(res); warn != "" {
		out.Warnings = append(out.Warnings, warn)
	}
}

// The reserved port is deliberately not released here: Manager.Start already returned and the child was using it, so returning it to the pool would reopen the H1 race while the child still holds it.
func persistOrKill(req Request, meta state.Meta, res process.StartResult) error {
	if err := req.Store.SaveMeta(req.Path, meta); err != nil {
		stopAfterPersistFailure(req, res)
		return err
	}
	return nil
}

// Best-effort on purpose: if the stop also fails, the persistence error is the one the user has to fix.
func stopAfterPersistFailure(req Request, res process.StartResult) {
	if res.Pid <= 0 {
		return
	}
	_ = req.Manager.Stop(process.StopSpec{
		Pid:     res.Pid,
		Pgid:    res.Pgid,
		Timeout: process.DefaultStopTimeout,
	})
}

func routeName(req Request) (string, error) {
	name, err := portless.DeriveName(
		req.Manifest.EffectiveRouteMode(), req.Manifest.RouteName, req.Branch, req.Manifest.Name)
	if err != nil {
		return "", fmt.Errorf("portless route: %w", err)
	}
	return name, nil
}
