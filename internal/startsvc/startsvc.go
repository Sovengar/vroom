// Package startsvc owns the whole start sequence: it reserves and verifies the real port (docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md) and is the only place on that path that talks to portless, where a missing or broken portless degrades to a warning instead of failing the start (docs/adr/adr-0013-vroom-registers-portless-routes.md).
package startsvc

import (
	"context"
	"fmt"
	"slices"
	"time"

	"vroom/internal/logrun"
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

	// Ctx is the command-lifetime context: route verification retries for a bounded propagation window, and an abandoned caller (Ctrl-C, a TUI quit) must be able to cut that wait at once. nil means context.Background().
	Ctx context.Context

	DiscoveryTimeout time.Duration

	// Registrar is the seam to portless; nil means this service registers no routes and keeps the suite hermetic on a CI runner with no portless, no Node 24 and no proxy. It is a FACTORY because the generation is not known to the caller: the armed choice and the last recorded one outrank the manifest, so only Start can resolve it and ask for the matching registrar.
	Registrar RegistrarFunc

	Branch string

	// URLGeneration is an explicit per-start override (the TUI's s menu). Empty means "inherit": the recorded generation wins over the manifest's, so a CLI/agent start agrees with the last TUI choice.
	URLGeneration string
	// IsWorktree selects the manifest's [worktrees] policy when no override applies.
	IsWorktree bool
}

// RegistrarFunc maps a resolved generation to a registrar; nil means "publishes no URL" (by_port, none).
type RegistrarFunc func(gen string) RouteRegistrar

// RegistrarFor normalises to the interface here because a typed-nil *portless.Client is not a nil RouteRegistrar, which would silently defeat the nil check.
func RegistrarFor(gen string) RouteRegistrar {
	c := portless.ClientFor(gen)
	if c == nil {
		return nil
	}
	return c
}

type RouteRegistrar interface {
	// Lookup reports the port a name currently points at (the client parses `portless list`, never routes.json), for the by_hostname preflight: only a proven foreign holder may refuse a start.
	Lookup(name string) (port int, found bool, err error)
	// ApplyContext registers the service route and reports only what it could prove; it never errors because an absent portless is a degradation, not a start failure. ctx is the caller's command lifetime, so a cancelled start does not sit out the route propagation window.
	ApplyContext(ctx context.Context, name string, port int, prev portless.Ownership) portless.Result
	// Reconcile returns warnings, never errors; held is an Ownership handle rather than a port, because a live handle does not mean the name is still ours and a raw port would be authority to delete someone else's route. current is every candidate this start may claim: a persisted name still among them is not a rename, and deleting it would remove the route this very start is about to reuse.
	Reconcile(prev string, held portless.Ownership, current ...string) []string
	// Retire drops the previous name after the ladder claimed a different one, and returns warnings, never errors; it is a no-op without an unrevoked ownership lease, so a name taken by another worktree is never deleted.
	Retire(name string, held portless.Ownership) []string
}

type Result struct {
	Meta     state.Meta
	Pid      int
	Port     int
	Warnings []string
}

func Start(req Request) (Result, error) {
	// The recorded generation outranks the manifest because the agent chose it for this service (the s menu); an explicit
	// req.URLGeneration outranks the recorded one. Loading prev first is what makes the recorded generation reachable.
	var prev state.Meta
	if p, err := req.Store.LoadMeta(req.Path); err == nil {
		prev = p
	}
	gen := req.URLGeneration
	if gen == "" {
		gen = prev.URLGeneration
	}
	if gen == "" {
		gen = req.Manifest.EffectiveURLGeneration(req.IsWorktree)
	}

	var routes RouteRegistrar
	if req.Registrar != nil {
		routes = req.Registrar(gen)
	}

	if err := preflight(req, gen, routes, prev); err != nil {
		return Result{}, err
	}

	mode := manifest.PortMode(gen)
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

	// The pre_run hook runs INSIDE Manager.Start (through PreSpawn) rather than here: the manager truncates the logs on every start, so anything written before it would leave no trace of a hook that succeeded.
	var pre func() error
	if req.Manifest.Commands.Start.Hooks.PreRun != "" {
		pre = preRunHook(req.Manifest.Commands.Start.Hooks.PreRun, req.Path, req.StdoutPath, req.StderrPath)
	}

	res, err := req.Manager.Start(process.StartSpec{
		Command:    req.Manifest.Commands.Start.Run,
		WorkDir:    req.Path,
		StdoutPath: req.StdoutPath,
		StderrPath: req.StderrPath,
		Env:        env,
		PreSpawn:   pre,
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
		Command:        req.Manifest.Commands.Start.Run,
		Pid:            res.Pid,
		Pgid:           res.Pgid,
		CreationTimeMs: res.CreationTimeMs,
		StartedAt:      time.Now().Format(time.RFC3339),
		State:          state.StateRunning,
		URLGeneration:  gen,
	}

	// All three route facts are inherited or none: copying name and port but not ownership silently loses the grant on every start.
	base.RouteName = prev.RouteName
	base.RoutePort = prev.RoutePort
	base.RouteOwned = prev.RouteOwned

	if mode != manifest.PortModeDynamic {
		out := Result{Meta: base, Pid: res.Pid, Port: req.Manifest.Port}
		// The fixed generations (by_port, none) publish no URL and every publishing generation is dynamic: this branch has no route to apply and only retires one a previous hostname start may have left behind.
		retireUnpublishedRoute(req, routes, &base, prev, &out)
		// Hand the route result back to the caller: out.Meta was copied BEFORE the route bookkeeping, and the TUI renders that copy (startedMsg.meta) while the store already holds the new one — so without this line a start would show the PREVIOUS start's route, empty on a cold one. The dynamic path below does the same with `out.Meta = final`.
		out.Meta = base
		if err := persistOrKill(req, base, res); err != nil {
			return Result{}, err
		}
		_ = req.Store.RegisterPid(req.Path, res.Pid, res.Pgid)
		return postRun(req, out), nil
	}
	dynamic, err := resolveDynamicPort(req, routes, base, reserved, gen)
	if err != nil {
		return Result{}, err
	}
	return postRun(req, dynamic), nil
}

// preflight refuses a start that could not honour its own address, BEFORE anything spawns: by_hostname on a name a foreign holder keeps (its URL must not move, so there is nothing to fall back to — the ladder generation is the one that exists for falling back), and by_port on an occupied port (the neighbour's listener is not yours, and vroom mistaking it for the service is exactly the bug that made two projects read as running).
func preflight(req Request, gen string, routes RouteRegistrar, prev state.Meta) error {
	switch gen {
	case manifest.URLGenByHostname:
		if routes == nil || req.Manifest.RouteName == "" {
			return nil // no portless: the degradation warning at Apply time is the documented contract, not a start failure
		}
		held, found, err := routes.Lookup(req.Manifest.RouteName)
		if err != nil || !found {
			return nil // the holder cannot be proven (binary broken, name free): Apply reports the degradation, and only a proven foreign holder may refuse a start
		}
		if !((portless.Ownership{Owned: prev.RouteOwned, Port: prev.RoutePort}).Authorises(held)) {
			return fmt.Errorf("the hostname %q is held by port %d; url_generation = %q claims it or nothing — stop the holder, or switch to %q to fall back to the branch hostname",
				portless.Hostname(req.Manifest.RouteName), held, manifest.URLGenByHostname, manifest.URLGenByHostnameOrWorkspace)
		}
	case manifest.URLGenByPort:
		if req.Manifest.Port > 0 && process.PortOpen(req.Manifest.Port) {
			return fmt.Errorf("port %d is already in use and url_generation = %q binds it exactly; stop the occupant or switch generation",
				req.Manifest.Port, manifest.URLGenByPort)
		}
	}
	return nil
}

// retireUnpublishedRoute drops the route a previous start left behind when this start publishes none (a switch to by_port or none): Retire is fail-closed and no-ops without an unrevoked lease, so a name taken by another worktree is never deleted. Only a clean retirement clears the handle — a warning means something is still registered and a later start must be able to reconcile it.
func retireUnpublishedRoute(req Request, routes RouteRegistrar, meta *state.Meta, prev state.Meta, out *Result) {
	if meta.RouteName == "" {
		return
	}
	if routes == nil && req.Registrar != nil {
		// The generation publishes nothing, but the PREVIOUS one did leave a route to revoke; any publishing generation maps to the same real client.
		routes = req.Registrar(staleRouteGeneration(prev))
	}
	if routes == nil {
		return
	}
	warns := routes.Retire(meta.RouteName, portless.Ownership{Owned: meta.RouteOwned, Port: meta.RoutePort})
	out.Warnings = append(out.Warnings, warns...)
	if len(warns) == 0 {
		meta.RouteName = ""
		meta.RoutePort = 0
		meta.RouteOwned = false
		meta.RouteURL = ""
		meta.RouteStatus = ""
		meta.RouteReason = ""
	}
}

// staleRouteGeneration picks a generation that makes the factory return a client: the one that registered the route, or the ladder for metas recorded before generations existed.
func staleRouteGeneration(prev state.Meta) string {
	if manifest.PublishesURL(prev.URLGeneration) {
		return prev.URLGeneration
	}
	return manifest.URLGenByHostnameOrWorkspace
}

func resolveDynamicPort(req Request, routes RouteRegistrar, attempt state.Meta, reserved int, gen string) (Result, error) {
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
			fmt.Sprintf("service has no TCP port (generation %q); port health checks are disabled", gen))
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
		// Only the hostname generations resolve an ephemeral port, and they are exactly the publishing ones: there is no route to retire on this path.
		applyRoute(req, routes, &final, final.Port, gen, &out)
		out.Meta = final
	}
	if err := persistOrKill(req, final, process.StartResult{Pid: final.Pid, Pgid: final.Pgid}); err != nil {
		return Result{}, err
	}
	return out, nil
}

func applyRoute(req Request, routes RouteRegistrar, meta *state.Meta, port int, gen string, out *Result) {
	ctx := req.Ctx
	if routes == nil {
		return
	}

	cands, err := routeCandidates(gen, req)
	if err != nil {
		out.Warnings = append(out.Warnings, err.Error())
		return
	}

	prevName, prev := meta.RouteName, portless.Ownership{Owned: meta.RouteOwned, Port: meta.RoutePort}

	// Reconcile first: prune never touches alias routes (M5), so a renamed branch would leave the old route pointing at a dead port forever.
	out.Warnings = append(out.Warnings, routes.Reconcile(prevName, prev, cands...)...)

	// The ladder: only a pre-write route_conflict advances to the next candidate. Any other degradation is systemic (no binary, no proxy) and would fail identically for every rung, while a post-write conflict has Registered=true — we hold that name already and claiming a second one would leave a route nobody revokes.
	res := routes.ApplyContext(ctx, cands[0], port, prev)
	for i := 1; i < len(cands) && !res.Registered && res.Reason == portless.ReasonRouteConflict; i++ {
		out.Warnings = append(out.Warnings, fallbackWarning(res, cands[i]))
		res = routes.ApplyContext(ctx, cands[i], port, prev)
	}

	// The ladder left a previous name behind: retire it while the lease still proves it is ours, or stop will never revoke it (the handle has moved) and it would block the next worktree falling back to it.
	if res.Registered && prevName != "" && prevName != res.Name && prev.Owned && slices.Contains(cands, prevName) {
		out.Warnings = append(out.Warnings, routes.Retire(prevName, prev)...)
	}

	switch {
	case res.Registered, prevName == "":
		// The handle follows the name this start actually holds — Registered, not Succeeded, because a route written with the proxy down is still ours.
		meta.RouteName = res.Name
	case slices.Contains(cands, prevName):
		// Nothing was claimed and the old name may still be ours: moving the handle to a merely attempted name would orphan a real route.
	default:
		meta.RouteName = res.Name
	}
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

// fallbackWarning names the holder's port so the user learns WHO owns the stable URL, not merely that someone does.
func fallbackWarning(res portless.Result, next string) string {
	if res.HeldPort > 0 {
		return fmt.Sprintf("the portless route %q is taken by port %d; falling back to %q",
			res.Host, res.HeldPort, portless.Hostname(next))
	}
	return fmt.Sprintf("the portless route %q is taken by another service; falling back to %q",
		res.Host, portless.Hostname(next))
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

// preRunHook runs commands.start.hooks.pre_run exactly like any other job (banner, output and footer in the service log) and fails the whole start when it fails: an unmet prerequisite must not be papered over by a service that starts anyway.
func preRunHook(command, workDir, stdoutPath, stderrPath string) func() error {
	return func() error {
		if _, _, err := logrun.Run("pre_run", command, workDir, stdoutPath, stderrPath); err != nil {
			return fmt.Errorf("commands.start.hooks.pre_run %q failed: %w (its output is in the service log)", command, err)
		}
		return nil
	}
}

// postRun runs commands.start.hooks.post_run only once the start is COMMITTED: meta is persisted, the route is resolved and the child exists. That ordering is why a failure can only warn — the service is alive and killing it over a post-hook would surprise more than it protects — and the warning travels the way every other start warning does, through Result.Warnings into the service log.
func postRun(req Request, out Result) Result {
	hook := req.Manifest.Commands.Start.Hooks.PostRun
	if hook == "" {
		return out
	}
	if _, _, err := logrun.Run("post_run", hook, req.Path, req.StdoutPath, req.StderrPath); err != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"commands.start.hooks.post_run %q failed: %v (its output is in the service log)", hook, err))
	}
	return out
}

func routeCandidates(gen string, req Request) ([]string, error) {
	cands, err := portless.RouteCandidates(gen, req.Manifest.RouteName, req.Branch, req.Manifest.Name)
	if err != nil {
		return nil, fmt.Errorf("portless route: %w", err)
	}
	return cands, nil
}
