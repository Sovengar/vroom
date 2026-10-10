package orchestrate

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"vroom/internal/gitinfo"
	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/startsvc"
	"vroom/internal/state"
)

type ResolvedService struct {
	Name    string
	Project scanner.Project
}

type LaunchResult struct {
	OK     bool          `json:"ok"`
	Stack  string        `json:"stack"`
	Stages []StageResult `json:"stages"`
	Error  string        `json:"error,omitempty"`
}

type StageResult struct {
	Name     string          `json:"name"`
	Services []ServiceResult `json:"services"`
}

type ServiceResult struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	Pid    int    `json:"pid,omitempty"`
	// NoPort means alive but no TCP port, so it must not fail the stage nor abort its siblings.
	NoPort bool `json:"no_port,omitempty"`
	// PortUnresolved means alive with an undecidable port after discovery ended: terminal, not a wait.
	PortUnresolved bool   `json:"port_unresolved,omitempty"`
	Error          string `json:"error,omitempty"`
}

type DryRunResult struct {
	OK     bool          `json:"ok"`
	Stack  string        `json:"stack"`
	Stages []DryRunStage `json:"stages"`
}

type DryRunStage struct {
	Name     string   `json:"name"`
	Services []string `json:"services"`
}

type Engine struct {
	manager process.Manager
	store   *state.Store
}

func NewEngine(manager process.Manager, store *state.Store) *Engine {
	return &Engine{manager: manager, store: store}
}

// LookupService never silently last-wins: manifest names are not unique across worktrees, so duplicates fail with the paths sorted for reproducible output.
func LookupService(name string, projects []scanner.Project) (scanner.Project, error) {
	var matches []scanner.Project
	for _, p := range projects {
		if p.Configured && p.Manifest != nil && p.Manifest.Name == name {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return scanner.Project{}, fmt.Errorf("service %q not found in scanned projects", name)
	case 1:
		return matches[0], nil
	default:
		paths := make([]string, len(matches))
		for i, m := range matches {
			paths[i] = m.Path
		}
		sort.Strings(paths)
		return scanner.Project{}, fmt.Errorf(
			"ambiguous service %q: found in %s; use unique manifest names",
			name, strings.Join(paths, ", "))
	}
}

func (e *Engine) ResolveServices(serviceNames []string, projects []scanner.Project) ([]ResolvedService, error) {
	resolved := make([]ResolvedService, 0, len(serviceNames))
	for _, name := range serviceNames {
		p, err := LookupService(name, projects)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, ResolvedService{Name: name, Project: p})
	}
	return resolved, nil
}

func (e *Engine) DryRun(stack *Stack, projects []scanner.Project) (*DryRunResult, error) {
	if _, err := e.validateServices(stack, projects); err != nil {
		return nil, err
	}
	result := &DryRunResult{OK: true, Stack: stack.Name}
	for _, stage := range stack.Stages {
		result.Stages = append(result.Stages, DryRunStage{
			Name:     stage.Name,
			Services: stage.Services,
		})
	}
	return result, nil
}

// Launch returns a Go error only for name resolution; once resolved, a failed stage is a LaunchResult with OK false, never an error.
func (e *Engine) Launch(stack *Stack, projects []scanner.Project) (*LaunchResult, error) {
	services, err := e.validateServices(stack, projects)
	if err != nil {
		return nil, err
	}
	return e.LaunchResolved(stack, services), nil
}

// LaunchResolved skips the second name pass the TUI already did, and therefore cannot fail: a start or health failure is still OK false.
func (e *Engine) LaunchResolved(stack *Stack, services []ResolvedService) *LaunchResult {
	result := &LaunchResult{OK: true, Stack: stack.Name}
	var startedThisSession []string
	var mu sync.Mutex

	for _, stage := range stack.Stages {
		sr := StageResult{Name: stage.Name}

		resolved := make([]ResolvedService, 0, len(stage.Services))
		for _, name := range stage.Services {
			for _, s := range services {
				if s.Name == name {
					resolved = append(resolved, s)
					break
				}
			}
		}

		// Results stay positional, not keyed by name: a service listed twice in one stage would overwrite the same map key and lose a summary line.
		var wg sync.WaitGroup
		var stageErr error
		var stageMu sync.Mutex
		resultados := make([]ServiceResult, len(resolved))

		for i, svc := range resolved {
			wg.Add(1)
			go func(i int, svc ResolvedService) {
				defer wg.Done()
				sr := e.startService(svc, stage.Timeout)

				mu.Lock()
				resultados[i] = sr
				if sr.Error != "" {
					stageMu.Lock()
					if stageErr == nil {
						stageErr = fmt.Errorf("service %q: %s", svc.Name, sr.Error)
					}
					stageMu.Unlock()
				}
				// MEDIDO (bug): rollback keys on the returned PID, not on Action, because Action comes back empty when a started process fails its health check and that orphan stayed alive with meta "running".
				if sr.Pid > 0 {
					startedThisSession = append(startedThisSession, svc.Project.Path)
				}
				mu.Unlock()
			}(i, svc)
		}
		wg.Wait()

		sr.Services = append(sr.Services, resultados...)
		result.Stages = append(result.Stages, sr)

		if stageErr != nil {
			e.abortAndCleanup(startedThisSession)
			result.OK = false
			result.Error = stageErr.Error()
			return result
		}
	}
	return result
}

func (e *Engine) LaunchAsync(stack *Stack, projects []scanner.Project) <-chan LaunchResult {
	ch := make(chan LaunchResult, 1)
	go func() {
		result, err := e.Launch(stack, projects)
		if err != nil {
			ch <- LaunchResult{OK: false, Stack: stack.Name, Error: err.Error()}
		} else {
			ch <- *result
		}
		close(ch)
	}()
	return ch
}

func (e *Engine) StopStack(stack *Stack, projects []scanner.Project) error {
	services, err := e.validateServices(stack, projects)
	if err != nil {
		return err
	}
	e.StopResolved(services)
	return nil
}

func (e *Engine) StopResolved(services []ResolvedService) {
	for _, svc := range services {
		e.stopService(svc.Project)
	}
}

func (e *Engine) StackStatus(stack *Stack, projects []scanner.Project) (running, total int, err error) {
	seen := make(map[string]bool)
	for _, stage := range stack.Stages {
		for _, name := range stage.Services {
			if seen[name] {
				continue
			}
			seen[name] = true
			total++
			p, lookupErr := LookupService(name, projects)
			if lookupErr != nil {
				return running, total, lookupErr
			}
			meta, metaErr := e.store.LoadMeta(p.Path)
			if metaErr == nil && meta.Pid > 0 {
				status := e.manager.Evaluate(process.EvalSpec{
					Pid:            meta.Pid,
					CreationTimeMs: meta.CreationTimeMs,
					Port:           meta.Port,
					ProcessPattern: meta.ProcessPattern,
					PortPending:    meta.State == state.StatePortPending,
					PortUnresolved: meta.State == state.StatePortUnresolved,
					NoPort:         meta.State == state.StateNoPort,
				})
				if status == process.StatusRunning {
					running++
				}
			}
		}
	}
	return running, total, nil
}

func (e *Engine) validateServices(stack *Stack, projects []scanner.Project) ([]ResolvedService, error) {
	allNames := make(map[string]bool)
	for _, stage := range stack.Stages {
		for _, name := range stage.Services {
			allNames[name] = true
		}
	}
	names := make([]string, 0, len(allNames))
	for name := range allNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return e.ResolveServices(names, projects)
}

// processAlive deliberately ignores the TUI's own alive check: it decides whether a service gets restarted, so it needs a single definition.
func processAlive(s process.Status) bool {
	switch s {
	case process.StatusRunning, process.StatusPortPending,
		process.StatusNoPort, process.StatusPortUnresolved:
		return true
	default:
		return false
	}
}

func (e *Engine) startService(svc ResolvedService, timeout time.Duration) ServiceResult {
	p := svc.Project

	meta, err := e.store.LoadMeta(p.Path)
	if err == nil && meta.Pid > 0 {
		status := e.manager.Evaluate(process.EvalSpec{
			Pid:            meta.Pid,
			CreationTimeMs: meta.CreationTimeMs,
			Port:           meta.Port,
			ProcessPattern: meta.ProcessPattern,
			PortPending:    meta.State == state.StatePortPending,
			PortUnresolved: meta.State == state.StatePortUnresolved,
			NoPort:         meta.State == state.StateNoPort,
		})
		if processAlive(status) {
			// pending, no_port and port_unresolved all count as alive: discarding them restarted a healthy service and burned a whole discovery window on every launch.
			outcome, err := e.awaitPortOutcome(p, meta, timeout)
			if err != nil {
				return ServiceResult{Name: svc.Name, Error: fmt.Sprintf("health check failed: %v", err)}
			}
			return portServiceResult(svc.Name, "already_running", 0, outcome)
		}
	}

	if _, err := e.store.EnsureServiceDir(p.Path); err != nil {
		return ServiceResult{Name: svc.Name, Error: err.Error()}
	}
	out, err := startsvc.Start(startsvc.Request{
		Manifest:   p.Manifest,
		Path:       p.Path,
		Store:      e.store,
		Manager:    e.manager,
		StdoutPath: e.store.StdoutLog(p.Path),
		StderrPath: e.store.StderrLog(p.Path),
		Registrar:  startsvc.RegistrarFor,
		Branch:     gitinfo.Branch(p.Path),
		IsWorktree: p.IsWorktree,
	})
	if err != nil {
		return ServiceResult{Name: svc.Name, Error: err.Error()}
	}
	for _, w := range out.Warnings {
		_ = appendLine(e.store.StderrLog(p.Path), "── vroom ▶ start: "+w)
	}

	// The health check probes the REAL port from the Meta, never the manifest default.
	outcome, err := e.awaitPortOutcome(p, out.Meta, timeout)
	if err != nil {
		// MEDIDO (bug): action/pid and error are separate truths, so a health failure still reports both; hiding the PID left a live process outside the rollback.
		r := portServiceResult(svc.Name, "started", out.Pid, outcome)
		r.Error = fmt.Sprintf("health check failed: %v", err)
		return r
	}

	return portServiceResult(svc.Name, "started", out.Pid, outcome)
}

func portServiceResult(name, action string, pid int, outcome PortOutcome) ServiceResult {
	return ServiceResult{
		Name:           name,
		Action:         action,
		Pid:            pid,
		NoPort:         outcome == PortNone,
		PortUnresolved: outcome == PortUnresolved,
	}
}

// PortOutcome has three states, not two: the last two mean a live service that must not fail its stage.
type PortOutcome int

const (
	PortResolved PortOutcome = iota
	PortNone
	PortUnresolved
)

// effectiveGeneration mirrors startsvc's resolution for the health gate: the recorded generation (the last armed s choice) outranks the manifest's, worktree-aware. An empty meta falls back to the manifest, which is also what a start would have resolved.
func effectiveGeneration(p scanner.Project, meta state.Meta) string {
	if meta.URLGeneration != "" {
		return meta.URLGeneration
	}
	return p.Manifest.EffectiveURLGeneration(p.IsWorktree)
}

// no_port and port_unresolved are results, not errors; only port_pending is fatal, because an in-flight discovery means the stage was never actually verified.
func (e *Engine) awaitPortOutcome(p scanner.Project, meta state.Meta, timeout time.Duration) (PortOutcome, error) {
	err := AwaitPort(PortWait{
		Port:        meta.Port,
		Mode:        manifest.PortMode(effectiveGeneration(p, meta)),
		PortPending: meta.State == state.StatePortPending,
		NoPort:      meta.State == state.StateNoPort,
		Unresolved:  meta.State == state.StatePortUnresolved,
	}, timeout)
	switch {
	case errors.Is(err, ErrNoPort):
		return PortNone, nil
	case errors.Is(err, ErrPortUnresolved):
		return PortUnresolved, nil
	default:
		return PortResolved, err
	}
}

func (e *Engine) stopService(p scanner.Project) {
	if p.Manifest == nil {
		return
	}
	// Manifest.Commands.Stop.Run is deliberately ignored: graceful stop is not implemented in the engine.
	meta, err := e.store.LoadMeta(p.Path)
	if err == nil {
		if meta.Pid > 0 || meta.Pgid > 0 || meta.Port > 0 {
			e.stopProcess(p.Path, &meta)
		} else {
			// An already-dead service skips stopProcess but can still own a route, so release it outside the process guard.
			e.releaseRouteOnStop(&meta)
		}
	}
	_ = e.store.ClearPid(p.Path)
	if err == nil {
		meta.State = state.StateStopped
		meta.Pid = 0
		meta.Pgid = 0
		meta.ReservedPort = 0
		_ = e.store.SaveMeta(p.Path, meta)
	}
}

func (e *Engine) abortAndCleanup(paths []string) {
	for _, path := range paths {
		meta, err := e.store.LoadMeta(path)
		if err == nil {
			if meta.Pid > 0 || meta.Pgid > 0 || meta.Port > 0 {
				e.stopProcess(path, &meta)
			} else {
				e.releaseRouteOnStop(&meta)
			}
		}
		_ = e.store.ClearPid(path)
		if err == nil {
			meta.State = state.StateStopped
			meta.Pid = 0
			meta.Pgid = 0
			meta.ReservedPort = 0
			_ = e.store.SaveMeta(path, meta)
		}
	}
}

// Test seam for route release: without it, deleting the three Release call sites kept the suite green, so ADR-0013 decision 13 was verified by nothing (docs/adr/adr-0013-vroom-registers-portless-routes.md).
var (
	engineReleaseStub          portless.ReleaserFunc
	engineReleaseStubInstalled bool
)

func engineRouteReleaser() portless.Releaser {
	if engineReleaseStubInstalled && engineReleaseStub != nil {
		return engineReleaseStub
	}
	if portless.IsTestBinary() {
		return portless.InertReleaser()
	}
	return nil
}

// stopProcess logs the stop warnings because the port-ownership guard fails closed: a silent warning reads as "the port is free" when it is not.
func (e *Engine) stopProcess(path string, meta *state.Meta) {
	var warns []string
	_ = e.manager.Stop(process.StopSpec{
		Pid: meta.Pid, Pgid: meta.Pgid, Port: meta.Port,
		Timeout: process.DefaultStopTimeout,
		Warn:    func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) },
	})
	process.ReleasePort(meta.ReservedPort)
	// A repeated stop is not an error: removing an absent route exits 1 (M10) and that is benign.
	e.releaseRouteOnStop(meta)
	for _, w := range warns {
		_ = appendLine(e.store.StderrLog(path), "── vroom ▶ stop: "+w)
	}
}

// Only revoke when ownership was GRANTED: the handle survives revocation so reconciliation still has something to inspect, which makes it an unsafe delete authority over foreign routes.
func (e *Engine) releaseRouteOnStop(meta *state.Meta) {
	if !meta.RouteOwned {
		return
	}
	if !portless.Release(engineRouteReleaser(), meta.RouteName) {
		return
	}
	meta.RouteOwned = false
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(line + "\n")
	return err
}
