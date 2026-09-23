// Package cli implementa la interfaz de línea de comandos de vroom para
// consumo por IA: todos los comandos devuelven JSON en stdout y errores
// en stderr con formato {"error":"..."}.
//
// Comandos:
//
//	vroom                    → lanza la TUI (comportamiento por defecto)
//	vroom list               → lista todos los proyectos con estado completo
//	vroom start <name>       → arranca un servicio daemonizado
//	vroom stop <name>        → detiene un servicio
//	vroom build <name>       → ejecuta command_build (one-shot síncrono)
//	vroom install <name>     → ejecuta command_install (one-shot síncrono)
//	vroom logs <name>        → muestra los logs del servicio
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vroom/internal/config"
	"vroom/internal/gitinfo"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
	"vroom/internal/tail"
)

// ---- JSON output types ----

// ListResult es la respuesta de `vroom list`.
type ListResult struct {
	Projects []ProjectInfo `json:"projects"`
}

// ProjectInfo contiene toda la información de un proyecto para consumo
// externo (IA).
type ProjectInfo struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Configured     bool   `json:"configured"`
	Status         string `json:"status"`
	Port           int    `json:"port"`
	Command        string `json:"command,omitempty"`
	CommandStop    string `json:"command_stop,omitempty"`
	CommandBuild   string `json:"command_build,omitempty"`
	CommandInstall string `json:"command_install,omitempty"`
	ProcessPattern string `json:"process_pattern,omitempty"`
	GitBranch      string `json:"git_branch,omitempty"`
	PrimaryGroup   string `json:"primary_group,omitempty"`
	SecondaryGroup string `json:"secondary_group,omitempty"`
	RepoRoot       string `json:"repo_root,omitempty"`
	IsWorktree     bool   `json:"is_worktree,omitempty"`
	BareContainer  bool   `json:"bare_container,omitempty"`
	Collapsed      bool   `json:"collapsed"`
	Pid            int    `json:"pid,omitempty"`
	Pgid           int    `json:"pgid,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	ManifestError  string `json:"manifest_error,omitempty"`
}

// ActionResult es la respuesta de start/stop/build/install.
type ActionResult struct {
	OK       bool   `json:"ok"`
	Project  string `json:"project"`
	Action   string `json:"action"`
	Pid      int    `json:"pid,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Elapsed  string `json:"elapsed,omitempty"`
	Error    string `json:"error,omitempty"`
}

// LogsResult es la respuesta de `vroom logs`.
type LogsResult struct {
	Project string `json:"project"`
	Stdout  string `json:"stdout,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
}

// ErrorResult es el formato estándar de error.
type ErrorResult struct {
	Error string `json:"error"`
}

// ---- Helpers ----

func outputJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func outputError(msg string) {
	enc := json.NewEncoder(os.Stderr)
	_ = enc.Encode(ErrorResult{Error: msg})
	os.Exit(1)
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
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

// findProject busca un proyecto por path (direccionador canónico) o por
// nombre manifest. query puede ser un nombre o una ruta absoluta; path
// (del flag --path) tiene prioridad y desambigua. Primero busca por
// nombre del manifiesto; si no encuentra, intenta por nombre del
// directorio. Devuelve error accionable si hay ambigüedad.
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
	// Fallback: buscar por nombre del directorio
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
		sort.Strings(paths) // orden estable para el mensaje
		return scanner.Project{}, fmt.Errorf(
			"ambiguous project name %q: found in %s; use --path to disambiguate",
			query, strings.Join(paths, ", "))
	}
}

// findByPath resuelve un proyecto por su ruta absoluta exacta.
func findByPath(projects []scanner.Project, path string) (scanner.Project, error) {
	abs := path
	if a, err := filepath.Abs(path); err == nil {
		abs = filepath.Clean(a)
	}
	for _, p := range projects {
		if filepath.Clean(p.Path) == abs {
			return p, nil
		}
	}
	return scanner.Project{}, fmt.Errorf("project not found: %s", path)
}

// extractPathFlag separa el flag --path <valor> de los demás argumentos
// (posicionales y otros flags, p.ej. --tail/--stream de logs).
func extractPathFlag(args []string) (rest []string, path string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--path" && i+1 < len(args) {
			path = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return rest, path
}

// evaluateStatus devuelve el estado evaluado de un proyecto.
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
	})
	return string(status), meta
}

// buildProjectInfo construye ProjectInfo desde un scanner.Project.
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
	}

	if !p.Configured {
		info.Status = state.StateStopped
		if p.ManifestErr != "" {
			info.ManifestError = p.ManifestErr
		}
		return info
	}

	m := p.Manifest
	info.Port = m.Port
	info.Command = m.Command
	info.CommandStop = m.Stop
	info.CommandBuild = m.Build
	info.CommandInstall = m.Install
	info.ProcessPattern = m.ProcessPattern
	info.PrimaryGroup = m.PrimaryGroup
	info.SecondaryGroup = m.SecondaryGroup

	// Colapso: clave = primary o primary/secondary
	if m.PrimaryGroup != "" {
		key := m.PrimaryGroup
		if m.SecondaryGroup != "" {
			key = m.PrimaryGroup + "/" + m.SecondaryGroup
		}
		info.Collapsed = collapsed[key]
	}

	status, meta := evaluateStatus(manager, store, p.Path)
	info.Status = status
	if meta.Pid > 0 {
		info.Pid = meta.Pid
		info.Pgid = meta.Pgid
		info.StartedAt = meta.StartedAt
	}

	return info
}

// ---- Commands ----

// Run es el punto de entrada del CLI. Devuelve true si manejó un
// subcomando (el caller debe salir); false si debe lanzar la TUI.
func Run(args []string) bool {
	if len(args) == 0 {
		return false
	}

	cmd := args[0]

	switch cmd {
	case "list", "status":
		cmdList()
	case "start":
		rest, path := extractPathFlag(args[1:])
		if len(rest) < 1 {
			outputError("usage: vroom start <project-name|path> [--path <path>]")
		}
		cmdStart(rest[0], path)
	case "stop":
		rest, path := extractPathFlag(args[1:])
		if len(rest) < 1 {
			outputError("usage: vroom stop <project-name|path> [--path <path>]")
		}
		cmdStop(rest[0], path)
	case "build":
		rest, path := extractPathFlag(args[1:])
		if len(rest) < 1 {
			outputError("usage: vroom build <project-name|path> [--path <path>]")
		}
		cmdBuild(rest[0], path)
	case "install":
		rest, path := extractPathFlag(args[1:])
		if len(rest) < 1 {
			outputError("usage: vroom install <project-name|path> [--path <path>]")
		}
		cmdInstall(rest[0], path)
	case "logs":
		rest, path := extractPathFlag(args[1:])
		if len(rest) < 1 {
			outputError("usage: vroom logs <project-name|path> [--path <path>] [--tail N --stream merged|stdout|stderr]")
		}
		cmdLogs(rest[0], rest[1:], path)
	case "launch":
		cmdLaunch(args[1:])
	case "help", "--help", "-h":
		cmdHelp()
	default:
		return false // comando desconocido → TUI
	}
	return true
}

// cmdList lista todos los proyectos con estado completo.
func cmdList() {
	cfg := loadConfig()
	root := resolveRoot()
	store, err := state.NewStore()
	if err != nil {
		outputError(err.Error())
	}
	manager := process.NewManager()

	scanResult, err := scanner.Scan(root, cfg.Scanner.Depth)
	if err != nil {
		outputError("scan error: " + err.Error())
	}

	collapsed := store.LoadCollapsed()

	result := ListResult{
		Projects: make([]ProjectInfo, 0, len(scanResult.Projects)),
	}
	for _, p := range scanResult.Projects {
		result.Projects = append(result.Projects, buildProjectInfo(manager, store, collapsed, p))
	}

	outputJSON(result)
}

// cmdStart arranca un servicio daemonizado.
func cmdStart(name, path string) {
	cfg := loadConfig()
	root := resolveRoot()
	store, err := state.NewStore()
	if err != nil {
		outputError(err.Error())
	}
	manager := process.NewManager()

	scanResult, err := scanner.Scan(root, cfg.Scanner.Depth)
	if err != nil {
		outputError("scan error: " + err.Error())
	}

	p, err := findProject(scanResult.Projects, name, path)
	if err != nil {
		outputError(err.Error())
	}

	if !p.Configured {
		outputError(fmt.Sprintf("project %q is not configured (missing or invalid .vroom.toml)", name))
	}

	// Verificar si ya está corriendo
	status, _ := evaluateStatus(manager, store, p.Path)
	if status == state.StateRunning {
		outputJSON(ActionResult{
			OK:      true,
			Project: name,
			Action:  "already_running",
		})
		return
	}

	// Arrancar
	if _, err := store.EnsureServiceDir(p.Path); err != nil {
		outputError("could not create service dir: " + err.Error())
	}

	res, err := manager.Start(process.StartSpec{
		Command:    p.Manifest.Command,
		WorkDir:    p.Path,
		StdoutPath: store.StdoutLog(p.Path),
		StderrPath: store.StderrLog(p.Path),
	})
	if err != nil {
		outputError("start failed: " + err.Error())
	}

	meta := state.Meta{
		Name:           p.Manifest.Name,
		ProjectPath:    p.Path,
		Port:           p.Manifest.Port,
		ProcessPattern: p.Manifest.ProcessPattern,
		Command:        p.Manifest.Command,
		Pid:            res.Pid,
		Pgid:           res.Pgid,
		CreationTimeMs: res.CreationTimeMs,
		StartedAt:      time.Now().Format(time.RFC3339),
		State:          state.StateRunning,
	}
	if err := store.SaveMeta(p.Path, meta); err != nil {
		outputError("could not save meta: " + err.Error())
	}
	if err := store.RegisterPid(p.Path, res.Pid, res.Pgid); err != nil {
		outputError("could not register pid: " + err.Error())
	}

	outputJSON(ActionResult{
		OK:      true,
		Project: name,
		Action:  "started",
		Pid:     res.Pid,
	})
}

// cmdStop detiene un servicio.
func cmdStop(name, path string) {
	cfg := loadConfig()
	root := resolveRoot()
	store, err := state.NewStore()
	if err != nil {
		outputError(err.Error())
	}
	manager := process.NewManager()

	scanResult, err := scanner.Scan(root, cfg.Scanner.Depth)
	if err != nil {
		outputError("scan error: " + err.Error())
	}

	p, err := findProject(scanResult.Projects, name, path)
	if err != nil {
		outputError(err.Error())
	}

	// Parada graciosa si command_stop está definido
	if p.Configured && p.Manifest.Stop != "" {
		_, _, cmdErr := runLogged("stop", p.Manifest.Stop, p.Path, store.StdoutLog(p.Path), store.StderrLog(p.Path))
		if cmdErr != nil {
			// Notificamos pero el cleanup sigue
			_ = cmdErr
		}
	}

	meta, err := store.LoadMeta(p.Path)
	if err == nil && (meta.Pgid > 0 || meta.Port > 0) {
		_ = manager.Stop(process.StopSpec{Pgid: meta.Pgid, Port: meta.Port, Timeout: process.DefaultStopTimeout})
	}

	if err := store.ClearPid(p.Path); err != nil {
		outputError("could not clear pid: " + err.Error())
	}

	if err == nil {
		meta.State = state.StateStopped
		meta.Pid = 0
		meta.Pgid = 0
		_ = store.SaveMeta(p.Path, meta)
	}

	appendLine(store.StderrLog(p.Path), "── vroom ▶ stop: service stopped ──")

	outputJSON(ActionResult{
		OK:      true,
		Project: name,
		Action:  "stopped",
	})
}

// cmdBuild ejecuta command_build de forma síncrona.
func cmdBuild(name, path string) {
	cmdOneShot(name, path, "build")
}

// cmdInstall ejecuta command_install de forma síncrona.
func cmdInstall(name, path string) {
	cmdOneShot(name, path, "install")
}

// cmdOneShot ejecuta un comando one-shot (build/install).
func cmdOneShot(name, path, kind string) {
	cfg := loadConfig()
	root := resolveRoot()
	store, err := state.NewStore()
	if err != nil {
		outputError(err.Error())
	}

	scanResult, err := scanner.Scan(root, cfg.Scanner.Depth)
	if err != nil {
		outputError("scan error: " + err.Error())
	}

	p, err := findProject(scanResult.Projects, name, path)
	if err != nil {
		outputError(err.Error())
	}

	if !p.Configured {
		outputError(fmt.Sprintf("project %q is not configured", name))
	}

	var command string
	switch kind {
	case "build":
		command = p.Manifest.Build
	case "install":
		command = p.Manifest.Install
	}

	if command == "" {
		outputError(fmt.Sprintf("project %q has no %s command defined", name, kind))
	}

	elapsed, exitCode, err := runLogged(kind, command, p.Path, store.StdoutLog(p.Path), store.StderrLog(p.Path))
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
	outputJSON(result)
}

// cmdLogs muestra los logs de un servicio.
func cmdLogs(name string, flags []string, path string) {
	cfg := loadConfig()
	root := resolveRoot()
	store, err := state.NewStore()
	if err != nil {
		outputError(err.Error())
	}

	scanResult, err := scanner.Scan(root, cfg.Scanner.Depth)
	if err != nil {
		outputError("scan error: " + err.Error())
	}

	p, err := findProject(scanResult.Projects, name, path)
	if err != nil {
		outputError(err.Error())
	}

	// Parse flags simples: --tail N, --stream merged|stdout|stderr
	tailLines := 0 // 0 = todo
	stream := "merged"
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--tail":
			if i+1 < len(flags) {
				fmt.Sscanf(flags[i+1], "%d", &tailLines)
				i++
			}
		case "--stream":
			if i+1 < len(flags) {
				stream = flags[i+1]
				i++
			}
		}
	}

	result := LogsResult{Project: name}
	stdoutPath := store.StdoutLog(p.Path)
	stderrPath := store.StderrLog(p.Path)

	readFull := func(path string) string {
		data, _, err := tail.ReadNew(path, 0)
		if err != nil {
			return ""
		}
		return tail.StripANSI(data)
	}

	getLastNLines := func(s string, n int) string {
		if n <= 0 {
			return s
		}
		lines := strings.Split(s, "\n")
		if len(lines) <= n {
			return s
		}
		return strings.Join(lines[len(lines)-n:], "\n")
	}

	switch stream {
	case "stdout":
		result.Stdout = readFull(stdoutPath)
		if tailLines > 0 {
			result.Stdout = getLastNLines(result.Stdout, tailLines)
		}
	case "stderr":
		result.Stderr = readFull(stderrPath)
		if tailLines > 0 {
			result.Stderr = getLastNLines(result.Stderr, tailLines)
		}
	default: // merged
		stdout := readFull(stdoutPath)
		stderr := readFull(stderrPath)
		if tailLines > 0 {
			stdout = getLastNLines(stdout, tailLines)
			stderr = getLastNLines(stderr, tailLines)
		}
		result.Stdout = stdout
		result.Stderr = stderr
	}

	outputJSON(result)
}

// cmdHelp muestra la ayuda.
func cmdHelp() {
	outputJSON(map[string]any{
		"commands": map[string]string{
			"vroom":                                     "launch the TUI (default when no arguments)",
			"vroom list":                                "list all projects with full state (JSON)",
			"vroom status":                              "alias for list",
			"vroom start <name|path> [--path <path>]":   "start a service by project name or path",
			"vroom stop <name|path> [--path <path>]":    "stop a service by project name or path",
			"vroom build <name|path> [--path <path>]":   "run command_build (synchronous)",
			"vroom install <name|path> [--path <path>]": "run command_install (synchronous)",
			"vroom logs <name|path> [--path <path>]":    "show service logs (--tail N --stream merged|stdout|stderr)",
			"vroom launch --list":                       "list all orchestration stacks",
			"vroom launch <name>":                       "launch an orchestration stack",
			"vroom launch <name> --dry":                 "dry run: show plan without executing",
		},
		"notes": map[string]string{
			"--path": "use an explicit project path when a manifest name is ambiguous across worktrees",
		},
	})
}

// LaunchListResult es la respuesta de `vroom launch --list`.
type LaunchListResult struct {
	File   string              `json:"file"`
	Stacks []orchestrate.Stack `json:"stacks"`
}

// cmdLaunch maneja el subcomando launch: --list, <name>, <name> --dry.
func cmdLaunch(args []string) {
	if len(args) == 0 {
		outputError("usage: vroom launch --list | vroom launch <name> [--dry]")
	}

	// Buscar compose file en CWD
	cf, err := orchestrate.ParseComposeFile(".")
	if err != nil {
		outputError(err.Error())
	}

	if args[0] == "--list" {
		cwd, _ := os.Getwd()
		outputJSON(LaunchListResult{
			File:   filepath.Join(cwd, orchestrate.ComposeFileName),
			Stacks: cf.Stacks,
		})
		return
	}

	stackName := args[0]
	stack, err := cf.FindStack(stackName)
	if err != nil {
		outputError(err.Error())
	}

	// Scan projects
	cfg := loadConfig()
	root := resolveRoot()
	store, err := state.NewStore()
	if err != nil {
		outputError(err.Error())
	}
	manager := process.NewManager()

	scanResult, err := scanner.Scan(root, cfg.Scanner.Depth)
	if err != nil {
		outputError("scan error: " + err.Error())
	}

	engine := orchestrate.NewEngine(manager, store)

	// Check for --dry flag
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
			outputError(err.Error())
		}
		outputJSON(result)
		return
	}

	result, err := engine.Launch(stack, scanResult.Projects)
	if err != nil {
		outputError(err.Error())
	}
	outputJSON(result)
}

// ---- Internal helpers (ported from TUI for CLI use) ----

// runLogged ejecuta un comando one-shot con `sh -c` en workDir.
func runLogged(kind, command, workDir, stdoutPath, stderrPath string) (time.Duration, int, error) {
	if dir := filepath.Dir(stdoutPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, 0, err
		}
	}

	appendLine := func(path, line string) error {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(line + "\n")
		return err
	}

	banner := fmt.Sprintf("── vroom ▶ %s: %s ──", kind, command)
	if err := appendLine(stdoutPath, banner); err != nil {
		return 0, 0, err
	}

	start := time.Now()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workDir
	out, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	defer out.Close()
	errF, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	defer errF.Close()
	cmd.Stdout = out
	cmd.Stderr = errF
	runErr := cmd.Run()
	elapsed := time.Since(start).Round(10 * time.Millisecond)
	if runErr != nil {
		exitCode := 0
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		_ = appendLine(stdoutPath, fmt.Sprintf("── vroom ✗ %s failed (exit %d, %s) ──", kind, exitCode, elapsed))
		return elapsed, exitCode, runErr
	}
	_ = appendLine(stdoutPath, fmt.Sprintf("── vroom ✓ %s ok (%s) ──", kind, elapsed))
	return elapsed, 0, nil
}
