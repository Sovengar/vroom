// Package cli is vroom's AI-facing contract: every subcommand emits JSON on stdout and errors as {"error":"..."} on stderr.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vroom/internal/config"
	"vroom/internal/gitinfo"
	"vroom/internal/logrun"
	"vroom/internal/manifest"
	"vroom/internal/orchestrate"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/startsvc"
	"vroom/internal/state"
	"vroom/internal/tail"
)

type ListResult struct {
	Projects []ProjectInfo `json:"projects"`
}

type ProjectInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Configured bool   `json:"configured"`
	Status     string `json:"status"`
	// Never the declared port, which may belong to a twin worktree; 0 means vroom confirmed no listener.
	Port int `json:"port"`
	// Published apart so "no real port" is not read as "no port in the manifest".
	DeclaredPort int    `json:"declared_port,omitempty"`
	PortMode     string `json:"port_mode,omitempty"`
	// *bool on purpose: absent = no port contract at all, false = contract exists but nothing was confirmed, which bool+omitempty cannot emit.
	PortVerified *bool `json:"port_verified,omitempty"`
	// Intent, not outcome, and omitted when the manifest has no route contract so a legacy manifest still emits byte-identical JSON.
	RouteMode string `json:"route_mode,omitempty"`
	// Pointer because absence is itself a state: a manifest with no route contract neither claims nor denies one.
	Route *RouteInfo `json:"route,omitempty"`
	// Pointer because an unconfigured project has no commands at all: emitting an empty section would claim it does.
	Commands       *CommandsInfo `json:"commands,omitempty"`
	ProcessPattern string        `json:"process_pattern,omitempty"`
	GitBranch      string        `json:"git_branch,omitempty"`
	PrimaryGroup   string        `json:"primary_group,omitempty"`
	SecondaryGroup string        `json:"secondary_group,omitempty"`
	RepoRoot       string        `json:"repo_root,omitempty"`
	IsWorktree     bool          `json:"is_worktree,omitempty"`
	BareContainer  bool          `json:"bare_container,omitempty"`
	WorktreeErr    string        `json:"worktree_error,omitempty"`
	Collapsed      bool          `json:"collapsed"`
	Pid            int           `json:"pid,omitempty"`
	Pgid           int           `json:"pgid,omitempty"`
	StartedAt      string        `json:"started_at,omitempty"`
	ManifestError  string        `json:"manifest_error,omitempty"`
}

type RouteInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Url    string `json:"url,omitempty"`
	Reason string `json:"reason,omitempty"`
	Port   int    `json:"port,omitempty"`
}

// CommandsInfo mirrors the manifest's [commands] section verbatim — same structure, same names, no omission — so an agent reads one vocabulary across .vroom.toml, the Go fields and this JSON. There is no legacy alias: commands.start.run/commands.stop.run/... stopped existing with the hard rename.
type CommandsInfo struct {
	Start   StartCommandInfo `json:"start"`
	Build   RunnableInfo     `json:"build"`
	Install RunnableInfo     `json:"install"`
	Stop    RunnableInfo     `json:"stop"`
}

type StartCommandInfo struct {
	Run   string    `json:"run"`
	Hooks HooksInfo `json:"hooks"`
}

// HooksInfo is always present and its keys always emitted: an agent must be able to tell "no hook" from "field not supported by this vroom version".
type HooksInfo struct {
	PreRun string `json:"pre_run"`
}

type RunnableInfo struct {
	Run string `json:"run"`
}

type ActionResult struct {
	OK       bool   `json:"ok"`
	Project  string `json:"project"`
	Action   string `json:"action"`
	Pid      int    `json:"pid,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Elapsed  string `json:"elapsed,omitempty"`
	Error    string `json:"error,omitempty"`
}

type LogsResult struct {
	Project string `json:"project"`
	Stdout  string `json:"stdout,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
}

type ErrorResult struct {
	Error string `json:"error"`
}

// The writer is a parameter, not a global: this JSON is the agent contract, so it must be exercisable without swapping the whole process's file descriptors.
func outputJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Returns the exit code instead of calling os.Exit, so the caller decides the process outcome and every error path stays testable.
func outputError(w io.Writer, msg string) int {
	_ = json.NewEncoder(w).Encode(ErrorResult{Error: msg})
	return 1
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

func resolveRoot() string {
	cfg := config.Load()
	root, _ := os.Getwd()
	if cfg.Scanner.Root != "" {
		scanRoot := cfg.Scanner.Root
		if strings.HasPrefix(scanRoot, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				scanRoot = filepath.Join(home, scanRoot[1:])
			}
		}
		if !filepath.IsAbs(scanRoot) {
			scanRoot = filepath.Join(root, scanRoot)
		}
		return scanRoot
	}
	return root
}

func loadConfig() config.Config {
	return config.Load()
}

// --path wins over the name because the same manifest name can exist in several worktrees.
func findProject(projects []scanner.Project, query, path string) (scanner.Project, error) {
	if path != "" {
		return findByPath(projects, path)
	}
	if filepath.IsAbs(query) {
		return findByPath(projects, query)
	}

	var matches []scanner.Project
	for _, p := range projects {
		if p.Configured && p.Manifest != nil && p.Manifest.Name == query {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		for _, p := range projects {
			if p.Name == query {
				matches = append(matches, p)
			}
		}
	}
	switch len(matches) {
	case 0:
		return scanner.Project{}, fmt.Errorf("project not found: %s", query)
	case 1:
		return matches[0], nil
	default:
		paths := make([]string, len(matches))
		for i, m := range matches {
			paths[i] = m.Path
		}
		sort.Strings(paths) // sorted so the ambiguity message is stable across runs
		return scanner.Project{}, fmt.Errorf(
			"ambiguous project name %q: found in %s; use --path to disambiguate",
			query, strings.Join(paths, ", "))
	}
}

// Both sides are normalized (abs, clean, symlinks resolved) so a symlinked path still resolves to the right project.
func findByPath(projects []scanner.Project, path string) (scanner.Project, error) {
	abs := normalizePath(path)
	for _, p := range projects {
		if normalizePath(p.Path) == abs {
			return p, nil
		}
	}
	return scanner.Project{}, fmt.Errorf("project not found: %s", path)
}

func normalizePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(abs)
}

// A bad --path is an error, not silence: a silently dropped flag would change which project a command acts on.
func extractPathFlag(args []string) (rest []string, path string, err error) {
	rest = make([]string, 0, len(args))
	seen := false
	set := func(val string) error {
		if seen {
			return fmt.Errorf("--path specified more than once")
		}
		if val == "" {
			return fmt.Errorf("--path requires a non-empty value")
		}
		seen = true
		path = val
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--path" {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return nil, "", fmt.Errorf("--path requires a value")
			}
			if err := set(args[i+1]); err != nil {
				return nil, "", err
			}
			i++
			continue
		}
		if val, ok := strings.CutPrefix(a, "--path="); ok {
			if err := set(val); err != nil {
				return nil, "", err
			}
			continue
		}
		rest = append(rest, a)
	}
	return rest, path, nil
}

func evaluateStatus(manager process.Manager, store *state.Store, path string) (string, state.Meta) {
	meta, err := store.LoadMeta(path)
	if err != nil {
		return state.StateStopped, state.Meta{}
	}
	status := manager.Evaluate(process.EvalSpec{
		Pid:            meta.Pid,
		CreationTimeMs: meta.CreationTimeMs,
		Port:           meta.Port,
		ProcessPattern: meta.ProcessPattern,
		// The three port states come from the persisted meta: recomputing them here made the TUI and the JSON tell different stories about the same service.
		PortPending:    meta.State == state.StatePortPending,
		PortUnresolved: meta.State == state.StatePortUnresolved,
		NoPort:         meta.State == state.StateNoPort,
	})
	return string(status), meta
}

func buildProjectInfo(manager process.Manager, store *state.Store, collapsed map[string]bool, p scanner.Project) ProjectInfo {
	info := ProjectInfo{
		Name:          p.Name,
		Path:          p.Path,
		Configured:    p.Configured,
		Status:        state.StateStopped,
		GitBranch:     gitinfo.Branch(p.Path),
		RepoRoot:      p.RepoRoot,
		IsWorktree:    p.IsWorktree,
		BareContainer: p.IsBareContainer,
		WorktreeErr:   p.WorktreeErr,
	}

	if !p.Configured {
		info.Status = state.StateStopped
		if p.ManifestErr != "" {
			info.ManifestError = p.ManifestErr
		}
		return info
	}

	m := p.Manifest
	info.Commands = &CommandsInfo{
		Start: StartCommandInfo{
			Run:   m.Commands.Start.Run,
			Hooks: HooksInfo{PreRun: m.Commands.Start.Hooks.PreRun},
		},
		Build:   RunnableInfo{Run: m.Commands.Build.Run},
		Install: RunnableInfo{Run: m.Commands.Install.Run},
		Stop:    RunnableInfo{Run: m.Commands.Stop.Run},
	}
	info.ProcessPattern = m.ProcessPattern
	info.PrimaryGroup = m.PrimaryGroup
	info.SecondaryGroup = m.SecondaryGroup
	if m.EffectiveRouteMode() != manifest.RouteModeOff {
		info.RouteMode = m.EffectiveRouteMode()
	}

	if m.PrimaryGroup != "" {
		key := m.PrimaryGroup
		if m.SecondaryGroup != "" {
			key = m.PrimaryGroup + "/" + m.SecondaryGroup
		}
		info.Collapsed = collapsed[key]
	}

	status, meta := evaluateStatus(manager, store, p.Path)
	info.Status = status
	// The recorded mode (the agent's ss/sd choice) outranks the manifest's, so the JSON shows how the service actually starts.
	if meta.PortMode != "" {
		info.PortMode = meta.PortMode
	} else {
		info.PortMode = m.EffectivePortMode()
	}
	if meta.Pid > 0 {
		info.Pid = meta.Pid
		info.Pgid = meta.Pgid
		info.StartedAt = meta.StartedAt
	}

	// Resolved after evaluateStatus because meta is the service's real state; assigning earlier is exactly what made the JSON always emit the declared port.
	info.DeclaredPort = m.Port
	if meta.Pid > 0 {
		info.Port = meta.Port
		info.PortVerified = boolPtr(meta.PortVerified)
	} else {
		info.Port = m.Port
	}

	// Taken from the persisted meta, never a live portless call, so `vroom list` cannot shell out and a degraded route carries no url.
	if info.RouteMode != "" {
		r := RouteInfo{Name: meta.RouteName, Port: meta.RoutePort}
		switch meta.RouteStatus {
		case portless.StatusRegistered:
			r.Status = portless.StatusRegistered
			r.Url = meta.RouteURL
		case portless.StatusDegraded:
			r.Status = portless.StatusDegraded
			r.Reason = meta.RouteReason
		}
		// An empty RouteStatus means never attempted, and emitting an object with an empty status would be a contract the JSON does not keep.
		if r.Status != "" {
			info.Route = &r
		}
	}

	return info
}

func boolPtr(b bool) *bool { return &b }

// The seam exists because deleting the three Release call sites kept the suite green, leaving ADR-0013's "retired on all three paths" decision unverified; in production both vars are zero and the real path runs.
var (
	cliReleaseStub          portless.ReleaserFunc
	cliReleaseStubInstalled bool
)

func cliRouteReleaser() portless.Releaser {
	if cliReleaseStubInstalled && cliReleaseStub != nil {
		return cliReleaseStub
	}
	// LOW-3: in a test binary nil would build the real client and mutate the developer's routes.json.
	if portless.IsTestBinary() {
		return portless.InertReleaser()
	}
	return nil
}

// Deletes only when ownership was GRANTED: the handle survives revocation, so treating it as delete authority once removed another worktree's route (reproduced against real portless).
func releaseRouteOnStop(meta *state.Meta) {
	if !meta.RouteOwned {
		return
	}
	if !portless.Release(cliRouteReleaser(), meta.RouteName) {
		return // the release did not take effect, so nothing is revoked
	}
	meta.RouteOwned = false
}

// exit is a parameter, not a package var: a global swapped by a test would contaminate every other test running in the same process.
func Run(args []string, exit func(int)) bool {
	handled, code := runInto(os.Stdout, os.Stderr, args)
	if handled && code != 0 {
		exit(code)
	}
	return handled
}

// stdout is the response and stderr the diagnostic, so a test can demand that an error never lands on stdout.
func runInto(stdout, stderr io.Writer, args []string) (handled bool, code int) {
	payload, handled, err := dispatch(args)
	if !handled {
		return false, 0
	}
	if err != nil {
		return true, outputError(stderr, err.Error())
	}
	if err := outputJSON(stdout, payload); err != nil {
		// A stdout that refuses the JSON is not a command that did nothing: the agent would read the empty answer as "no projects".
		return true, outputError(stderr, "could not write response: "+err.Error())
	}
	return true, 0
}

// --path and usage errors are checked once here because they depend on the invocation's shape, not on any command.
func dispatch(args []string) (payload any, handled bool, err error) {
	if len(args) == 0 {
		return nil, false, nil
	}

	cmd := args[0]

	switch cmd {
	case "start", "stop", "build", "install", "logs":
		usage := "usage: vroom " + cmd + " <project-name|path> [--path <path>]"
		if cmd == "logs" {
			usage += " [--tail N --stream merged|stdout|stderr]"
		}
		rest, path, perr := extractPathFlag(args[1:])
		if perr != nil {
			return nil, true, perr
		}
		if len(rest) < 1 {
			return nil, true, errors.New(usage)
		}
		switch cmd {
		case "start":
			payload, err = cmdStart(rest[0], path)
		case "stop":
			payload, err = cmdStop(rest[0], path)
		case "build":
			payload, err = cmdBuild(rest[0], path)
		case "install":
			payload, err = cmdInstall(rest[0], path)
		case "logs":
			payload, err = cmdLogs(rest[0], rest[1:], path)
		}
		return payload, true, err

	case "list", "status":
		payload, err = cmdList()
		return payload, true, err
	case "launch":
		payload, err = cmdLaunch(args[1:])
		return payload, true, err
	case "help", "--help", "-h":
		payload, err = cmdHelp()
		return payload, true, err
	default:
		return nil, false, nil // an unknown subcommand is not an error: it falls through to the TUI
	}
}

// Error prefixes are part of the JSON contract: an agent must tell "project not found" from "scan failed" without reading the stack.
type cliSession struct {
	cfg     config.Config
	root    string
	store   *state.Store
	manager process.Manager
}

// The "scan error: " prefix is applied only by the scanner, because store/config failures are not scan errors and must not claim to be.
func newCliSession() (cliSession, error) {
	cfg := loadConfig()
	store, err := state.NewStore()
	if err != nil {
		return cliSession{}, err
	}
	return cliSession{
		cfg:     cfg,
		root:    resolveRoot(),
		store:   store,
		manager: process.NewManager(),
	}, nil
}

// Separate from newCliSession because `launch --list` needs the store but not the scan: a disk error must not stop it from listing stacks.
func (s cliSession) scan() (scanner.ScanResult, error) {
	res, err := scanner.Scan(s.root, s.cfg.Scanner.Depth)
	if err != nil {
		return res, fmt.Errorf("scan error: %w", err)
	}
	return res, nil
}

// Returned unwrapped because findProject already names the ambiguous paths and a wrap would bury that.
func (s cliSession) resolve(query, path string) (scanner.Project, error) {
	res, err := s.scan()
	if err != nil {
		return scanner.Project{}, err
	}
	return findProject(res.Projects, query, path)
}

func cmdList() (any, error) {
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}

	scanResult, err := s.scan()
	if err != nil {
		return nil, err
	}

	collapsed := s.store.LoadCollapsed()

	result := ListResult{
		Projects: make([]ProjectInfo, 0, len(scanResult.Projects)),
	}
	for _, p := range scanResult.Projects {
		result.Projects = append(result.Projects, buildProjectInfo(s.manager, s.store, collapsed, p))
	}

	return result, nil
}

func cmdStart(name, path string) (any, error) {
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}

	p, err := s.resolve(name, path)
	if err != nil {
		return nil, err
	}

	if !p.Configured {
		return nil, fmt.Errorf("project %q is not configured (missing or invalid .vroom.toml)", name)
	}

	status, _ := evaluateStatus(s.manager, s.store, p.Path)
	if status == state.StateRunning {
		return ActionResult{
			OK:      true,
			Project: name,
			Action:  "already_running",
		}, nil
	}

	if _, err := s.store.EnsureServiceDir(p.Path); err != nil {
		return nil, fmt.Errorf("could not create service dir: %w", err)
	}

	out, err := startsvc.Start(startsvc.Request{
		Manifest:   p.Manifest,
		Path:       p.Path,
		Store:      s.store,
		Manager:    s.manager,
		StdoutPath: s.store.StdoutLog(p.Path),
		StderrPath: s.store.StderrLog(p.Path),
		Routes:     startsvc.RegistrarFor(p.Manifest),
		Branch:     gitinfo.Branch(p.Path),
	})
	if err != nil {
		return nil, fmt.Errorf("start failed: %w", err)
	}
	for _, w := range out.Warnings {
		_ = appendLine(s.store.StderrLog(p.Path), "── vroom ▶ start: "+w)
	}

	return ActionResult{
		OK:      true,
		Project: name,
		Action:  "started",
		Pid:     out.Pid,
	}, nil
}

func cmdStop(name, path string) (any, error) {
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}

	p, err := s.resolve(name, path)
	if err != nil {
		return nil, err
	}

	if p.Configured && p.Manifest.Commands.Stop.Run != "" {
		// A failing commands.stop.run must not skip cleanup: dropping the PID without killing the process leaves an unowned orphan.
		_, _, _ = runLogged("stop", p.Manifest.Commands.Stop.Run, p.Path, s.store.StdoutLog(p.Path), s.store.StderrLog(p.Path))
	}

	if err := stopCleanup(s.store, s.manager, p.Path); err != nil {
		return nil, err
	}

	return ActionResult{
		OK:      true,
		Project: name,
		Action:  "stopped",
	}, nil
}

// Only ClearPid returns an error: the other failures are logged and cleanup continues, but a stale PID would make the next `vroom list` report a service that is not running.
func stopCleanup(store *state.Store, manager process.Manager, path string) error {
	meta, err := store.LoadMeta(path)
	if err == nil && (meta.Pid > 0 || meta.Pgid > 0 || meta.Port > 0) {
		var warns []string
		_ = manager.Stop(process.StopSpec{
			Pid: meta.Pid, Pgid: meta.Pgid, Port: meta.Port,
			Timeout: process.DefaultStopTimeout,
			Warn:    func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) },
		})
		for _, w := range warns {
			_ = appendLine(store.StderrLog(path), "── vroom ▶ stop: "+w)
		}
		process.ReleasePort(meta.ReservedPort)
	}
	if err == nil {
		// Outside the process guard on purpose: a service already dead at stop time (Pid 0) still leaves a route behind.
		releaseRouteOnStop(&meta)
	}

	if err := store.ClearPid(path); err != nil {
		return fmt.Errorf("could not clear pid: %w", err)
	}
	if err == nil {
		meta.State = state.StateStopped
		meta.Pid = 0
		meta.Pgid = 0
		meta.ReservedPort = 0
		_ = store.SaveMeta(path, meta)
	}
	_ = appendLine(store.StderrLog(path), "── vroom ▶ stop: service stopped ──")
	return nil
}

func cmdBuild(name, path string) (any, error) {
	return cmdOneShot(name, path, "build")
}

func cmdInstall(name, path string) (any, error) {
	return cmdOneShot(name, path, "install")
}

// The kind switch must refuse a new one-shot kind rather than publish an empty ActionResult, which an agent reads as "ran and succeeded".
func cmdOneShot(name, path, kind string) (any, error) {
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}

	p, err := s.resolve(name, path)
	if err != nil {
		return nil, err
	}

	if !p.Configured {
		return nil, fmt.Errorf("project %q is not configured", name)
	}

	var command string
	switch kind {
	case "build":
		command = p.Manifest.Commands.Build.Run
	case "install":
		command = p.Manifest.Commands.Install.Run
	default:
		return nil, fmt.Errorf("unknown one-shot command %q", kind)
	}

	if command == "" {
		return nil, fmt.Errorf("project %q has no %s command defined", name, kind)
	}

	elapsed, exitCode, err := runLogged(kind, command, p.Path, s.store.StdoutLog(p.Path), s.store.StderrLog(p.Path))
	result := ActionResult{
		OK:       err == nil,
		Project:  name,
		Action:   kind,
		ExitCode: exitCode,
		Elapsed:  elapsed.String(),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result, nil
}

// One type, not two ints, because both flags travel together and jointly decide which stream is read.
type logFilter struct {
	// 0 means the whole log, not zero lines.
	tail   int
	stream string
}

// A malformed --tail/--stream degrades to the whole log on purpose: failing instead would leave the caller with no logs at all.
func parseLogFlags(flags []string) logFilter {
	f := logFilter{stream: "merged"}
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--tail":
			if i+1 < len(flags) {
				_, _ = fmt.Sscanf(flags[i+1], "%d", &f.tail)
				i++
			}
		case "--stream":
			if i+1 < len(flags) {
				f.stream = flags[i+1]
				i++
			}
		}
	}
	return f
}

// An unreadable log yields "" and no error: logs are an extra on the row, and a query command must still answer.
func readLog(path string) string {
	data, _, err := tail.ReadNew(path, 0)
	if err != nil {
		return ""
	}
	return tail.StripANSI(data)
}

// MEDIDO (bug): a trailing newline is a terminator, not a line, and counting it made `--tail N` return N-1 lines, dropping the oldest.
func lastNLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n") + "\n"
}

func cmdLogs(name string, flags []string, path string) (any, error) {
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}

	p, err := s.resolve(name, path)
	if err != nil {
		return nil, err
	}

	f := parseLogFlags(flags)

	result := LogsResult{Project: name}
	stdoutPath := s.store.StdoutLog(p.Path)
	stderrPath := s.store.StderrLog(p.Path)

	switch f.stream {
	case "stdout":
		result.Stdout = lastNLines(readLog(stdoutPath), f.tail)
	case "stderr":
		result.Stderr = lastNLines(readLog(stderrPath), f.tail)
	default: // any stream value other than stdout|stderr degrades to merged
		result.Stdout = lastNLines(readLog(stdoutPath), f.tail)
		result.Stderr = lastNLines(readLog(stderrPath), f.tail)
	}

	return result, nil
}

// Help is JSON too: this package has one output contract, and plain text would break what an agent can assume.
func cmdHelp() (any, error) {
	return map[string]any{
		"commands": map[string]string{
			"vroom":        "launch the TUI (default when no arguments)",
			"vroom list":   "list all projects with full state (JSON)",
			"vroom status": "alias for list",
			"vroom start <name|path> [--path <path>]":   "start a service by project name or path",
			"vroom stop <name|path> [--path <path>]":    "stop a service by project name or path",
			"vroom build <name|path> [--path <path>]":   "run commands.build.run (synchronous)",
			"vroom install <name|path> [--path <path>]": "run commands.install.run (synchronous)",
			"vroom logs <name|path> [--path <path>]":    "show service logs (--tail N --stream merged|stdout|stderr)",
			"vroom launch --list":                       "list all orchestration stacks",
			"vroom launch <name>":                       "launch an orchestration stack",
			"vroom launch <name> --dry":                 "dry run: show plan without executing",
		},
		"notes": map[string]string{
			"--path": "use an explicit project path when a manifest name is ambiguous across worktrees",
		},
	}, nil
}

type LaunchListResult struct {
	File   string              `json:"file"`
	Stacks []orchestrate.Stack `json:"stacks"`
}

// --list never builds the store nor scans: its contract is the compose file, and touching disk only added ways to fail for reasons unrelated to the stacks.
func cmdLaunch(args []string) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: vroom launch --list | vroom launch <name> [--dry]")
	}

	cf, err := orchestrate.ParseComposeFile(".")
	if err != nil {
		return nil, err
	}

	if args[0] == "--list" {
		cwd, _ := os.Getwd()
		return LaunchListResult{
			File:   filepath.Join(cwd, orchestrate.ComposeFileName),
			Stacks: cf.Stacks,
		}, nil
	}

	stackName := args[0]
	stack, err := cf.FindStack(stackName)
	if err != nil {
		return nil, err
	}

	s, err := newCliSession()
	if err != nil {
		return nil, err
	}
	scanResult, err := s.scan()
	if err != nil {
		return nil, err
	}

	engine := orchestrate.NewEngine(s.manager, s.store)

	dryRun := false
	for _, a := range args[1:] {
		if a == "--dry" {
			dryRun = true
			break
		}
	}

	if dryRun {
		result, err := engine.DryRun(stack, scanResult.Projects)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	result, err := engine.Launch(stack, scanResult.Projects)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// runLogged keeps the name this package's tests speak; the job itself is shared with the TUI and with the pre_run hook (internal/logrun), because a banner, an exit code and a footer mean the same thing in all three.
func runLogged(kind, command, workDir, stdoutPath, stderrPath string) (time.Duration, int, error) {
	return logrun.Run(kind, command, workDir, stdoutPath, stderrPath)
}
