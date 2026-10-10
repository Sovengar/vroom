package orchestrate

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// The timeout message must say whether the port was pending an in-flight discovery or already decided and never opened; without it the user reads "it did not open" and never learns a discovery was behind it.
func TestAwaitPortDynamicSondeaLentoCuandoElDiscoveryNoEstaEnVuelo(t *testing.T) {
	port := closedTCPPort(t)
	start := time.Now()
	err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, Port: port}, 600*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a closed port cannot be considered healthy")
	}
	if errors.Is(err, ErrPortPending) {
		t.Errorf("err = %v: without discovery in flight the cause is not pending port", err)
	}
	if !strings.Contains(err.Error(), "not open") {
		t.Errorf("err = %q, want a message saying the port did not open", err)
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("took %s: the 500ms probe did not complete", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("took %s with a 600ms timeout: AwaitPort ignores its budget", elapsed)
	}
}

// The 100ms probe is what buys this: the 500ms one would detect a port opening at 150ms only at 500ms, wasting 350ms of stage budget.
func TestAwaitPortDynamicSondeaFinoConDiscoveryEnVueloYAbre(t *testing.T) {
	port := closedTCPPort(t)
	go func() {
		time.Sleep(150 * time.Millisecond)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return
		}
		time.Sleep(5 * time.Second)
		_ = ln.Close()
	}()

	start := time.Now()
	err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, Port: port, PortPending: true}, 3*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a port that opens at 150ms must pass the health check: %v", err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Errorf("took %s: the fine probe of discovery in flight is not probing every 100ms", elapsed)
	}
}

// Rejected at parse time, not at launch: a file with a nameless stage cannot even be dry-run, and accepting it would publish a plan promising something that does not exist.
func TestParseComposeRechazaEtapaSinNombreYTimeoutInvalido(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			"stage without name",
			`
primary_group = "g1"

[[stack]]
name = "app"

[[stack.stage]]
services = ["api"]
`,
			"name",
		},
		{
			"timeout that is not a duration",
			`
primary_group = "g1"

[[stack]]
name = "app"

[[stack.stage]]
name = "build"
services = ["api"]
timeout = "pronto"
`,
			"timeout",
		},
		{
			"numeric timeout without unit",
			`
primary_group = "g1"

[[stack]]
name = "app"

[[stack.stage]]
name = "build"
services = ["api"]
timeout = 30
`,
			"timeout",
		},
		{
			"stage without services",
			`
primary_group = "g1"

[[stack]]
name = "app"

[[stack.stage]]
name = "build"
`,
			"at least one service",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCompose(t, dir, tt.content)
			_, err := ParseComposeFile(dir)
			if err == nil {
				t.Fatal("a compose with this error must be rejected at parse time, not at launch")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %q, want it to mention %q: the message has to say WHAT is wrong", err, tt.wantErr)
			}
		})
	}
}

// With two projects named api, stopping "api" without asking would stop one of them in scan order, so an ambiguous or missing name must error out.
func TestStopStackConUnNombreQueNoResuelvePropagaElError(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"does-not-exist"}}}}
	err := engine.StopStack(stack, nil)
	if err == nil {
		t.Fatal("stopping a service that does not exist must produce an error")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("err = %q, want it to name the service: it is what the user has to fix", err)
	}

	ambos := []scanner.Project{
		{Path: "/a/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "./a"}}}},
		{Path: "/b/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "./b"}}}},
	}
	dup := &Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"api"}}}}
	if err := engine.StopStack(dup, ambos); err == nil {
		t.Error("with two projects named api you must ask, not stop one at random")
	}
}

// What matters is that it does not try to launch: without the directory there is no log to write, and a process with no log leaves the user nothing to read when it breaks.
func TestStartServiceConStoreNoEscribibleReportaElError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write in a directory without permission: the case cannot be triggered")
	}

	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	store := state.NewStoreAt(locked)
	engine := NewEngine(&mockManager{}, store)

	svc := ResolvedService{Name: "api", Project: scanner.Project{
		Path:       "/dev/api",
		Name:       "api",
		Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "./api"}}, URLGeneration: manifest.URLGenNone,
		},
	}}

	got := engine.startService(svc, time.Second)
	if got.Error == "" {
		t.Errorf("with an unusable store there must be an error, not a started service: %+v", got)
	}
	if got.Pid != 0 {
		t.Errorf("Pid = %d with an unusable store, want 0: nothing started", got.Pid)
	}
	if got.Action == "started" {
		t.Error("Action = started with an unusable store: the service never started")
	}
}

// The real manager, not the mock: the mock Start returns an invented PID without launching anything, so it could not catch a real sleep surviving a failed launch, a port-declaring service that never serves its port, or a rollback that leaves live processes behind.
func TestLaunchConServicioQueArrancaPeroNoAbreElPuertoFallaLaEtapaYLoDejaMuerto(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(process.NewManager(), store)

	port := closedTCPPort(t)
	p := scanner.Project{
		Path:       t.TempDir(),
		Name:       "api",
		Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "sleep 300 # vroom-marker"}}, Port: port, URLGeneration: manifest.URLGenByPort,
		},
	}
	stack := &Stack{Name: "app", Stages: []Stage{
		{Name: "e", Services: []string{"api"}, Timeout: 700 * time.Millisecond},
	}}

	result, err := engine.Launch(stack, []scanner.Project{p})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if result.OK {
		t.Errorf("a service that does not open its port must fail the stage: %+v", result)
	}
	if !strings.Contains(result.Error, "api") {
		t.Errorf("err = %q, want it to name the guilty service", result.Error)
	}
	if len(result.Stages) != 1 || len(result.Stages[0].Services) != 1 {
		t.Fatalf("the stage must report its service even if it fails: %+v", result.Stages)
	}
	if result.Stages[0].Services[0].Error == "" {
		t.Error("the failed service must carry its reason: the stage error does not break it down")
	}

	// The meta must not claim alive, or the next vroom status shows it up and vroom stop goes looking for it.
	meta, err := store.LoadMeta(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 0 {
		t.Errorf("Pid = %d after a failed launch, want 0: the service was stopped but the meta says it is alive", meta.Pid)
	}
	if meta.State == state.StateRunning {
		t.Errorf("State = %q after a failed launch: the service is not running", meta.State)
	}
}
