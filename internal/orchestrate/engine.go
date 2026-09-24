package orchestrate

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// ResolvedService es un servicio resuelto contra los proyectos escaneados.
type ResolvedService struct {
	Name    string
	Project scanner.Project
}

// LaunchResult es el resultado de lanzar un stack.
type LaunchResult struct {
	OK     bool         `json:"ok"`
	Stack  string       `json:"stack"`
	Stages []StageResult `json:"stages"`
	Error  string       `json:"error,omitempty"`
}

// StageResult es el resultado de una etapa.
type StageResult struct {
	Name     string            `json:"name"`
	Services []ServiceResult   `json:"services"`
}

// ServiceResult es el resultado de arrancar un servicio individual.
type ServiceResult struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	Pid    int    `json:"pid,omitempty"`
	Error  string `json:"error,omitempty"`
}

// DryRunResult muestra el plan de ejecución sin ejecutar nada.
type DryRunResult struct {
	OK     bool           `json:"ok"`
	Stack  string         `json:"stack"`
	Stages []DryRunStage  `json:"stages"`
}

// DryRunStage es una etapa en el plan de dry run.
type DryRunStage struct {
	Name     string   `json:"name"`
	Services []string `json:"services"`
}

// Engine orquesta el arranque de stacks de servicios.
type Engine struct {
	manager process.Manager
	store   *state.Store
}

// NewEngine crea un engine con el manager y store dados.
func NewEngine(manager process.Manager, store *state.Store) *Engine {
	return &Engine{manager: manager, store: store}
}

// LookupService resuelve un nombre de servicio a exactamente un proyecto.
// Manifest.Name no es identidad única (worktrees pueden repetirlo): ante
// duplicados devuelve un error explícito con los paths en orden estable,
// nunca un last-wins silencioso.
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
		sort.Strings(paths) // orden estable en el mensaje
		return scanner.Project{}, fmt.Errorf(
			"ambiguous service %q: found in %s; use unique manifest names",
			name, strings.Join(paths, ", "))
	}
}

// ResolveServices resuelve los nombres de servicio contra los proyectos
// escaneados. Devuelve error si algún nombre no se encuentra o si es
// ambiguo (varios proyectos lo declaran).
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

// DryRun muestra el plan de ejecución sin ejecutar nada.
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

// Launch ejecuta la orquestación completa de un stack: etapas secuenciales,
// servicios paralelos dentro de cada etapa, health check, abort on failure.
func (e *Engine) Launch(stack *Stack, projects []scanner.Project) (*LaunchResult, error) {
	services, err := e.validateServices(stack, projects)
	if err != nil {
		return nil, err
	}

	result := &LaunchResult{OK: true, Stack: stack.Name}
	var startedThisSession []string // paths de servicios arrancados por esta sesión
	var mu sync.Mutex

	for _, stage := range stack.Stages {
		sr := StageResult{Name: stage.Name}

		// Resolver servicios de esta etapa
		resolved := make([]ResolvedService, 0, len(stage.Services))
		for _, name := range stage.Services {
			for _, s := range services {
				if s.Name == name {
					resolved = append(resolved, s)
					break
				}
			}
		}

		// Arrancar en paralelo
		var wg sync.WaitGroup
		var stageErr error
		var stageMu sync.Mutex
		startResults := make(map[string]ServiceResult)

		for _, svc := range resolved {
			wg.Add(1)
			go func(svc ResolvedService) {
				defer wg.Done()
				sr := e.startService(svc, stage.Timeout)

				mu.Lock()
				startResults[svc.Name] = sr
				if sr.Error != "" {
					stageMu.Lock()
					if stageErr == nil {
						stageErr = fmt.Errorf("service %q: %s", svc.Name, sr.Error)
					}
					stageMu.Unlock()
				}
				if sr.Action == "started" {
					startedThisSession = append(startedThisSession, svc.Project.Path)
				}
				mu.Unlock()
			}(svc)
		}
		wg.Wait()

		// Recopilar resultados de la etapa
		for _, svc := range resolved {
			if r, ok := startResults[svc.Name]; ok {
				sr.Services = append(sr.Services, r)
			} else {
				sr.Services = append(sr.Services, ServiceResult{Name: svc.Name, Error: "not started"})
			}
		}
		result.Stages = append(result.Stages, sr)

		// Abort on failure
		if stageErr != nil {
			e.abortAndCleanup(startedThisSession)
			result.OK = false
			result.Error = stageErr.Error()
			return result, nil
		}
	}
	return result, nil
}

// LaunchAsync es la versión de Launch para la TUI: ejecuta la orquestación
// en una goroutine y devuelve un canal con el resultado.
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

// StopStack para todos los servicios de un stack. Resuelve cada nombre
// con el mismo criterio explícito que el engine: ante duplicados falla en
// vez de parar un proyecto arbitrario.
func (e *Engine) StopStack(stack *Stack, projects []scanner.Project) error {
	seen := make(map[string]bool)
	for _, stage := range stack.Stages {
		for _, name := range stage.Services {
			if seen[name] {
				continue
			}
			seen[name] = true
			p, err := LookupService(name, projects)
			if err != nil {
				return err
			}
			e.stopService(p)
		}
	}
	return nil
}

// StackStatus evalúa el estado de todos los servicios de un stack con el
// mismo criterio de resolución que el CLI (LookupService): ante un nombre
// ambiguo devuelve error en lugar de elegir arbitrariamente el primero.
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
				})
				if status == process.StatusRunning {
					running++
				}
			}
		}
	}
	return running, total, nil
}

// validateServices resuelve y valida todos los servicios del stack.
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
	sort.Strings(names) // orden estable: mensajes/errores reproducibles
	return e.ResolveServices(names, projects)
}

// startService arranca un servicio: si ya está running, solo verifica health.
func (e *Engine) startService(svc ResolvedService, timeout time.Duration) ServiceResult {
	p := svc.Project

	// Verificar si ya está corriendo
	meta, err := e.store.LoadMeta(p.Path)
	if err == nil && meta.Pid > 0 {
		status := e.manager.Evaluate(process.EvalSpec{
			Pid:            meta.Pid,
			CreationTimeMs: meta.CreationTimeMs,
			Port:           meta.Port,
			ProcessPattern: meta.ProcessPattern,
		})
		if status == process.StatusRunning {
			// Ya corriendo: verificar health y continuar
			if err := WaitForPort(p.Manifest.Port, timeout); err != nil {
				return ServiceResult{Name: svc.Name, Error: fmt.Sprintf("health check failed: %v", err)}
			}
			return ServiceResult{Name: svc.Name, Action: "already_running"}
		}
	}

	// Arrancar
	if _, err := e.store.EnsureServiceDir(p.Path); err != nil {
		return ServiceResult{Name: svc.Name, Error: err.Error()}
	}
	res, err := e.manager.Start(process.StartSpec{
		Command:    p.Manifest.Command,
		WorkDir:    p.Path,
		StdoutPath: e.store.StdoutLog(p.Path),
		StderrPath: e.store.StderrLog(p.Path),
	})
	if err != nil {
		return ServiceResult{Name: svc.Name, Error: err.Error()}
	}

	// Guardar meta
	meta = state.Meta{
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
	_ = e.store.SaveMeta(p.Path, meta)
	_ = e.store.RegisterPid(p.Path, res.Pid, res.Pgid)

	// Health check
	if err := WaitForPort(p.Manifest.Port, timeout); err != nil {
		return ServiceResult{Name: svc.Name, Error: fmt.Sprintf("health check failed: %v", err)}
	}

	return ServiceResult{Name: svc.Name, Action: "started", Pid: res.Pid}
}

// stopService para un servicio individual.
func (e *Engine) stopService(p scanner.Project) {
	if p.Manifest == nil {
		return
	}
	// Parada graciosa
	if p.Manifest.Stop != "" {
		// runLogged omitted for engine (simplified stop)
	}
	meta, err := e.store.LoadMeta(p.Path)
	if err == nil && (meta.Pgid > 0 || meta.Port > 0) {
		_ = e.manager.Stop(process.StopSpec{Pgid: meta.Pgid, Port: meta.Port, Timeout: process.DefaultStopTimeout})
	}
	_ = e.store.ClearPid(p.Path)
	if err == nil {
		meta.State = state.StateStopped
		meta.Pid = 0
		meta.Pgid = 0
		_ = e.store.SaveMeta(p.Path, meta)
	}
}

// abortAndCleanup para todos los servicios arrancados en esta sesión.
func (e *Engine) abortAndCleanup(paths []string) {
	for _, path := range paths {
		meta, err := e.store.LoadMeta(path)
		if err == nil && (meta.Pgid > 0 || meta.Port > 0) {
			_ = e.manager.Stop(process.StopSpec{Pgid: meta.Pgid, Port: meta.Port, Timeout: process.DefaultStopTimeout})
		}
		_ = e.store.ClearPid(path)
		if err == nil {
			meta.State = state.StateStopped
			meta.Pid = 0
			meta.Pgid = 0
			_ = e.store.SaveMeta(path, meta)
		}
	}
}
