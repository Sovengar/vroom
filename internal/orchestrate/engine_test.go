package orchestrate

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// mockManager es un manager mock para tests.
type mockManager struct {
	startFunc func(spec process.StartSpec) (process.StartResult, error)
	stopFunc  func(spec process.StopSpec) error
	evalFunc  func(spec process.EvalSpec) process.Status
}

func (m *mockManager) Start(spec process.StartSpec) (process.StartResult, error) {
	if m.startFunc != nil {
		return m.startFunc(spec)
	}
	return process.StartResult{Pid: 1000, Pgid: 1000, CreationTimeMs: 100}, nil
}

func (m *mockManager) Stop(spec process.StopSpec) error {
	if m.stopFunc != nil {
		return m.stopFunc(spec)
	}
	return nil
}

func (m *mockManager) Evaluate(spec process.EvalSpec) process.Status {
	if m.evalFunc != nil {
		return m.evalFunc(spec)
	}
	return process.StatusRunning
}

func TestResolveServices(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "go run ."}},
		{Path: "/dev/web", Name: "web", Configured: true, Manifest: &manifest.Manifest{Name: "web", Command: "npm start"}},
	}

	resolved, err := engine.ResolveServices([]string{"api", "web"}, projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved = %d, want 2", len(resolved))
	}
	if resolved[0].Name != "api" || resolved[1].Name != "web" {
		t.Errorf("wrong resolution: %+v", resolved)
	}
}

func TestResolveServicesNotFound(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "go run ."}},
	}

	_, err := engine.ResolveServices([]string{"api", "nonexistent"}, projects)
	if err == nil {
		t.Fatal("expected error for unresolved service")
	}
}

func TestDryRun(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{
		Name:         "test",
		PrimaryGroup: "g1",
		Stages: []Stage{
			{Name: "stage1", Services: []string{"api", "web"}, Timeout: 30 * time.Second},
		},
	}
	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "go run ."}},
		{Path: "/dev/web", Name: "web", Configured: true, Manifest: &manifest.Manifest{Name: "web", Command: "npm start"}},
	}

	result, err := engine.DryRun(stack, projects)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Stack != "test" {
		t.Errorf("unexpected result: %+v", result)
	}
	if len(result.Stages) != 1 {
		t.Fatalf("stages = %d, want 1", len(result.Stages))
	}
	if len(result.Stages[0].Services) != 2 {
		t.Errorf("services = %d, want 2", len(result.Stages[0].Services))
	}
}

func TestLaunchSuccess(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStoreAt(dir)
	manager := &mockManager{
		startFunc: func(spec process.StartSpec) (process.StartResult, error) {
			return process.StartResult{Pid: 1000, Pgid: 1000, CreationTimeMs: 100}, nil
		},
		evalFunc: func(spec process.EvalSpec) process.Status {
			return process.StatusRunning
		},
	}
	engine := NewEngine(manager, store)

	stack := &Stack{
		Name:         "test",
		PrimaryGroup: "g1",
		Stages: []Stage{
			{Name: "stage1", Services: []string{"api"}, Timeout: 1 * time.Second},
		},
	}
	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{
			Name:    "api",
			Command: "go run .",
			Port:    0, // sin puerto: health check rápido
		}},
	}

	result, err := engine.Launch(stack, projects)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("launch failed: %s", result.Error)
	}
	if len(result.Stages) != 1 {
		t.Fatalf("stages = %d, want 1", len(result.Stages))
	}
	if result.Stages[0].Services[0].Action != "started" {
		t.Errorf("action = %q, want started", result.Stages[0].Services[0].Action)
	}
}

func TestLaunchAlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStoreAt(dir)

	// Pre-create service dir and meta
	p := scanner.Project{
		Path: "/dev/api", Name: "api", Configured: true,
		Manifest: &manifest.Manifest{Name: "api", Command: "go run .", Port: 0},
	}
	store.EnsureServiceDir(p.Path)
	store.SaveMeta(p.Path, state.Meta{
		Name: "api", Pid: 9999, Pgid: 9999, Port: 0,
		CreationTimeMs: 100, State: state.StateRunning,
	})

	manager := &mockManager{
		evalFunc: func(spec process.EvalSpec) process.Status {
			if spec.Pid == 9999 {
				return process.StatusRunning
			}
			return process.StatusStopped
		},
	}
	engine := NewEngine(manager, store)

	stack := &Stack{
		Name:         "test",
		PrimaryGroup: "g1",
		Stages: []Stage{
			{Name: "stage1", Services: []string{"api"}, Timeout: 1 * time.Second},
		},
	}
	projects := []scanner.Project{p}

	result, err := engine.Launch(stack, projects)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("launch failed: %s", result.Error)
	}
	if result.Stages[0].Services[0].Action != "already_running" {
		t.Errorf("action = %q, want already_running", result.Stages[0].Services[0].Action)
	}
}

func TestLaunchStartFailure(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStoreAt(dir)
	manager := &mockManager{
		startFunc: func(spec process.StartSpec) (process.StartResult, error) {
			return process.StartResult{}, fmt.Errorf("exec: permission denied")
		},
	}
	engine := NewEngine(manager, store)

	stack := &Stack{
		Name:         "test",
		PrimaryGroup: "g1",
		Stages: []Stage{
			{Name: "stage1", Services: []string{"api"}, Timeout: 1 * time.Second},
		},
	}
	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{
			Name: "api", Command: "go run .", Port: 0,
		}},
	}

	result, err := engine.Launch(stack, projects)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Error("expected failure")
	}
}

func TestLaunchSequentialStages(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStoreAt(dir)
	var order []string
	manager := &mockManager{
		startFunc: func(spec process.StartSpec) (process.StartResult, error) {
			order = append(order, spec.WorkDir)
			return process.StartResult{Pid: 1000, Pgid: 1000, CreationTimeMs: 100}, nil
		},
		evalFunc: func(spec process.EvalSpec) process.Status {
			return process.StatusRunning
		},
	}
	engine := NewEngine(manager, store)

	stack := &Stack{
		Name:         "test",
		PrimaryGroup: "g1",
		Stages: []Stage{
			{Name: "stage1", Services: []string{"api"}, Timeout: 1 * time.Second},
			{Name: "stage2", Services: []string{"web"}, Timeout: 1 * time.Second},
		},
	}
	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "go run .", Port: 0}},
		{Path: "/dev/web", Name: "web", Configured: true, Manifest: &manifest.Manifest{Name: "web", Command: "npm start", Port: 0}},
	}

	result, err := engine.Launch(stack, projects)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("launch failed: %s", result.Error)
	}
	if len(result.Stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(result.Stages))
	}
	// Verificar que api se arrancó antes que web
	if len(order) != 2 || order[0] != "/dev/api" || order[1] != "/dev/web" {
		t.Errorf("start order = %v, want [/dev/api /dev/web]", order)
	}
}

func TestStackStatus(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStoreAt(dir)

	p := scanner.Project{
		Path: "/dev/api", Name: "api", Configured: true,
		Manifest: &manifest.Manifest{Name: "api", Command: "go run .", Port: 8080},
	}
	store.EnsureServiceDir(p.Path)
	store.SaveMeta(p.Path, state.Meta{
		Name: "api", Pid: 1000, Pgid: 1000, Port: 8080,
		CreationTimeMs: 100, State: state.StateRunning,
	})

	manager := &mockManager{
		evalFunc: func(spec process.EvalSpec) process.Status {
			if spec.Pid == 1000 {
				return process.StatusRunning
			}
			return process.StatusStopped
		},
	}
	engine := NewEngine(manager, store)

	stack := &Stack{
		Name:         "test",
		PrimaryGroup: "g1",
		Stages: []Stage{
			{Name: "s1", Services: []string{"api"}},
		},
	}

	running, total, err := engine.StackStatus(stack, []scanner.Project{p})
	if err != nil {
		t.Fatal(err)
	}
	if running != 1 || total != 1 {
		t.Errorf("running=%d total=%d, want 1/1", running, total)
	}
}

// ---- Resolución determinista ante Manifest.Name duplicado (0011) ----

func duplicateProjects() []scanner.Project {
	return []scanner.Project{
		{Path: "/repo-wt/b", Name: "b", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "echo"}},
		{Path: "/repo-wt/a", Name: "a", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "echo"}},
	}
}

// S8: un stack con nombre duplicado falla explícitamente, sin last-wins.
func TestResolveServicesDuplicate(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	_, err := engine.ResolveServices([]string{"api"}, duplicateProjects())
	if err == nil {
		t.Fatal("esperaba error por nombre duplicado")
	}
	if !strings.Contains(err.Error(), "ambiguous service") {
		t.Errorf("mensaje inesperado: %v", err)
	}
	// Los paths van en orden estable (a antes que b).
	if !strings.Contains(err.Error(), "/repo-wt/a, /repo-wt/b") {
		t.Errorf("paths no ordenados de forma estable: %v", err)
	}
}

// S8: un nombre único resuelve determinísticamente.
func TestResolveServicesUnique(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "go run ."}},
	}
	resolved, err := engine.ResolveServices([]string{"api"}, projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Project.Path != "/dev/api" {
		t.Fatalf("resolución inesperada: %+v", resolved)
	}
}

// validateServices itera en orden estable (ordenado), independiente del
// orden de las etapas y de la iteración de mapas.
func TestValidateServicesDeterministicOrder(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "echo"}},
		{Path: "/dev/web", Name: "web", Configured: true, Manifest: &manifest.Manifest{Name: "web", Command: "echo"}},
		{Path: "/dev/db", Name: "db", Configured: true, Manifest: &manifest.Manifest{Name: "db", Command: "echo"}},
		{Path: "/dev/cache", Name: "cache", Configured: true, Manifest: &manifest.Manifest{Name: "cache", Command: "echo"}},
	}
	stack := &Stack{
		Name: "s",
		Stages: []Stage{
			{Name: "s1", Services: []string{"web", "api"}},
			{Name: "s2", Services: []string{"cache", "db"}},
		},
	}
	want := []string{"api", "cache", "db", "web"}
	for i := 0; i < 20; i++ {
		resolved, err := engine.validateServices(stack, projects)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, len(resolved))
		for j, r := range resolved {
			got[j] = r.Name
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("orden no determinista: got %v, want %v", got, want)
		}
	}
}

// S8: StackStatus reporta el mismo conflicto que el CLI.
func TestStackStatusConflict(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "s", Stages: []Stage{{Name: "s1", Services: []string{"api"}}}}
	_, _, err := engine.StackStatus(stack, duplicateProjects())
	if err == nil {
		t.Fatal("StackStatus debe reportar el conflicto")
	}
	if !strings.Contains(err.Error(), "ambiguous service") {
		t.Errorf("mensaje inesperado: %v", err)
	}
}

// S8: StopStack no para un proyecto arbitrario ante duplicados.
func TestStopStackDuplicate(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "s", Stages: []Stage{{Name: "s1", Services: []string{"api"}}}}
	if err := engine.StopStack(stack, duplicateProjects(), nil); err == nil {
		t.Fatal("StopStack debe fallar ante un nombre duplicado")
	}
}
