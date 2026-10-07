package orchestrate

import (
	"errors"
	"os"
	"strconv"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// Without the seen guard the second stopService finds the service already stopped and asks the manager to release a port that may by then belong to another service.
func TestStopStackParaCadaServicioUnaSolaVez(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	var paradas []string
	e := NewEngine(&registroManager{paradas: &paradas}, store)

	proyectos := proyectosConNombres("api", "web")
	// Without a meta stopService never reaches Stop, since a service that was already stopped has no pid to kill.
	for i, p := range proyectos {
		if err := store.SaveMeta(p.Path, state.Meta{Pid: 100 + i, Pgid: 100 + i}); err != nil {
			t.Fatal(err)
		}
	}

	stack := &Stack{
		Name: "double",
		Stages: []Stage{
			{Name: "base", Services: []string{"api", "web"}},
			{Name: "extra", Services: []string{"api"}},
		},
	}

	if err := e.StopStack(stack, proyectos); err != nil {
		t.Fatalf("StopStack: %v", err)
	}

	if len(paradas) != 2 {
		t.Fatalf("%d services were stopped (%v), want 2: a service named in two stages is "+
			"the SAME service, and stopping it twice ends up asking the manager to release a port "+
			"that may already belong to someone else", len(paradas), paradas)
	}
	if paradas[0] == paradas[1] {
		t.Errorf("both stops are of the same service (%q): the `seen` is not filtering", paradas[0])
	}
}

// A failed removal leaves the route in place, so the handle must survive for reconciliation: that is why the revocation goes after the if, not before.
func TestLaRetiradaFallidaNoRevocaLaPropiedadDeLaRuta(t *testing.T) {
	fallando := &falloReleaser{}
	t.Cleanup(func() { engineReleaseStub, engineReleaseStubInstalled = nil, false })
	engineReleaseStub = fallando.RemoveAbsent
	engineReleaseStubInstalled = true

	e := NewEngine(&noKillManager{}, state.NewStoreAt(t.TempDir()))

	meta := state.Meta{RouteOwned: true, RouteName: "my-route"}
	e.releaseRouteOnStop(&meta)

	if !meta.RouteOwned {
		t.Error("RouteOwned = false with a removal that failed: the route is still up and now " +
			"nobody has its handle, so nobody can clean it up")
	}
	if !fallando.intentos {
		t.Error("the route removal was not attempted")
	}
}

// The other two branches (stub installed, test binary) are used daily; this one makes the engine talk to the real portless, and an accidental nil here must not read as "this service has no route". MEASURED: IsTestBinary decides on the os.Args[0] suffix, so overriding it reproduces the installed binary's entry without touching the function.
func TestEngineRouteReleaserDevuelveNilFueraDeUnBinarioDeTest(t *testing.T) {
	original := os.Args[0]
	t.Cleanup(func() { os.Args[0] = original })

	os.Args[0] = "/usr/local/bin/vroom"
	if portless.IsTestBinary() {
		t.Fatal("IsTestBinary still says it is a test with a production argv")
	}
	if got := engineRouteReleaser(); got != nil {
		t.Errorf("engineRouteReleaser() = %v with a production binary, want nil", got)
	}
}

type falloReleaser struct{ intentos bool }

func (r *falloReleaser) RemoveAbsent(string) error {
	r.intentos = true
	return errors.New("portless could not remove the route")
}

type registroManager struct{ paradas *[]string }

func (m *registroManager) Stop(spec process.StopSpec) error {
	*m.paradas = append(*m.paradas, strconv.Itoa(spec.Pid))
	return nil
}
func (*registroManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (*registroManager) Evaluate(process.EvalSpec) process.Status { return process.StatusStopped }

func proyectosConNombres(nombres ...string) []scanner.Project {
	out := make([]scanner.Project, 0, len(nombres))
	for i, n := range nombres {
		p := scanner.Project{Path: "/srv/" + n, Name: n, Configured: true}
		p.Manifest = &manifest.Manifest{Name: n, Command: "true", Port: 3000 + i}
		out = append(out, p)
	}
	return out
}
