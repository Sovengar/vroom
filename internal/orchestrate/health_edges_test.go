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
		t.Fatal("un puerto cerrado no puede darse por sano")
	}
	if errors.Is(err, ErrPortPending) {
		t.Errorf("err = %v: sin discovery en vuelo la causa no es puerto pendiente", err)
	}
	if !strings.Contains(err.Error(), "not open") {
		t.Errorf("err = %q, want un mensaje que diga que el puerto no abrió", err)
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("tardó %s: el sondeo de 500ms no llegó a completarse", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("tardó %s con un timeout de 600ms: AwaitPort ignora su presupuesto", elapsed)
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
		t.Fatalf("un puerto que abre a los 150ms tiene que pasar el health check: %v", err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Errorf("tardó %s: el sondeo fino de discovery en vuelo no está sondando cada 100ms", elapsed)
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
			"etapa sin nombre",
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
			"timeout que no es una duración",
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
			"timeout numérico sin unidad",
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
			"etapa sin servicios",
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
				t.Fatal("un compose con este error tiene que rechazarse en el parseo, no en el arranque")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %q, want que mencione %q: el mensaje tiene que decir QUÉ está mal", err, tt.wantErr)
			}
		})
	}
}

// With two projects named api, stopping "api" without asking would stop one of them in scan order, so an ambiguous or missing name must error out.
func TestStopStackConUnNombreQueNoResuelvePropagaElError(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"no-existe"}}}}
	err := engine.StopStack(stack, nil)
	if err == nil {
		t.Fatal("parar un servicio que no existe tiene que dar error")
	}
	if !strings.Contains(err.Error(), "no-existe") {
		t.Errorf("err = %q, want que nombre el servicio: es lo que el usuario tiene que corregir", err)
	}

	ambos := []scanner.Project{
		{Path: "/a/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./a"}},
		{Path: "/b/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./b"}},
	}
	dup := &Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"api"}}}}
	if err := engine.StopStack(dup, ambos); err == nil {
		t.Error("con dos proyectos llamados api hay que preguntar, no parar uno al azar")
	}
}

// What matters is that it does not try to launch: without the directory there is no log to write, and a process with no log leaves the user nothing to read when it breaks.
func TestStartServiceConStoreNoEscribibleReportaElError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede escribir en un directorio sin permiso: el caso no se puede provocar")
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
			Name: "api", Command: "./api", PortMode: manifest.PortModeNone,
		},
	}}

	got := engine.startService(svc, time.Second)
	if got.Error == "" {
		t.Errorf("con el store inservible tiene que haber error, no un servicio arrancado: %+v", got)
	}
	if got.Pid != 0 {
		t.Errorf("Pid = %d con el store inservible, want 0: nada arrancó", got.Pid)
	}
	if got.Action == "started" {
		t.Error("Action = started con el store inservible: el servicio no llegó a arrancar")
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
			Name: "api", Command: "sleep 300 # marcador-vroom", Port: port, PortMode: manifest.PortModeFixed,
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
		t.Errorf("un servicio que no abre su puerto tiene que fallar la etapa: %+v", result)
	}
	if !strings.Contains(result.Error, "api") {
		t.Errorf("err = %q, want que nombre el servicio culpable", result.Error)
	}
	if len(result.Stages) != 1 || len(result.Stages[0].Services) != 1 {
		t.Fatalf("la etapa tiene que reportar su servicio aunque falle: %+v", result.Stages)
	}
	if result.Stages[0].Services[0].Error == "" {
		t.Error("el servicio fallido tiene que traer su motivo: el error de la etapa no lo desglosa")
	}

	// The meta must not claim alive, or the next vroom status shows it up and vroom stop goes looking for it.
	meta, err := store.LoadMeta(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 0 {
		t.Errorf("Pid = %d tras un launch fallido, want 0: el servicio se paró pero el meta lo dice vivo", meta.Pid)
	}
	if meta.State == state.StateRunning {
		t.Errorf("State = %q tras un launch fallido: el servicio no está corriendo", meta.State)
	}
}
