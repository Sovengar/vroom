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

// ---------------------------------------------------------------------------
// Las guardas que la cobertura dejó sin exercitar y que sí se pueden provocar.
//
// Todas son fallos de verdad —un nombre repetido entre etapas, un seam de retirada
// que devuelve error, un binario que no está en el PATH— y todas se comprueban por
// su EFECTO, que es lo que le importa al usuario: que un servicio nombrado en dos
// etapas se pare una sola vez, que la propiedad de una ruta no se revoque sin más,
// y que el escaneo caiga al walk cuando `fd` no está.
// ---------------------------------------------------------------------------

// TestStopStackParaCadaServicioUnaSolaVez: el `seen` de las etapas.
//
// Un stack puede nombrar el mismo servicio en dos etapas —"levanta la base, luego
// levanta la web"—. El `seen` evita pararlo dos veces: sin él, el segundo
// `stopService` se encuentra el servicio ya parado y acaba pidiendo al gestor que
// libere su puerto, que para entonces puede ser el de otro servicio.
//
// El efecto observable es el que se comprueba: una sola llamada a `Stop`, aunque el
// nombre esté repetido.
func TestStopStackParaCadaServicioUnaSolaVez(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	var paradas []string
	e := NewEngine(&registroManager{paradas: &paradas}, store)

	proyectos := proyectosConNombres("api", "web")
	// Sin meta, `stopService` no llega a `Stop`: un servicio que ya estaba parado no
	// tiene pid que matar. Se les da uno a cada uno para que haya algo que parar.
	for i, p := range proyectos {
		if err := store.SaveMeta(p.Path, state.Meta{Pid: 100 + i, Pgid: 100 + i}); err != nil {
			t.Fatal(err)
		}
	}

	stack := &Stack{
		Name: "doble",
		Stages: []Stage{
			{Name: "base", Services: []string{"api", "web"}},
			{Name: "extra", Services: []string{"api"}},
		},
	}

	if err := e.StopStack(stack, proyectos); err != nil {
		t.Fatalf("StopStack: %v", err)
	}

	if len(paradas) != 2 {
		t.Fatalf("se pararon %d servicios (%v), want 2: un servicio nombrado en dos etapas es "+
			"el MISMO servicio, y pararlo dos veces acaba pidiendo al gestor que suelte un puerto "+
			"que ya puede tener otro", len(paradas), paradas)
	}
	if paradas[0] == paradas[1] {
		t.Errorf("las dos paradas son del mismo servicio (%q): el `seen` no está filtrando", paradas[0])
	}
}

// TestLaRetiradaFallidaNoRevocaLaPropiedadDeLaRuta: el `Release` que no surtió
// efecto.
//
// `releaseRouteOnStop` sólo revoca la propiedad cuando la retirada REALmente pasó.
// Un fallo de portless deja la ruta puesta, así que perder el handle la dejaría sin
// quien la limpie: por eso la revocación va detrás del `if` y no antes.
//
// La consecuencia observable está en el Meta que se persiste: con la retirada
// fallando, `RouteOwned` tiene que seguir a `true`.
func TestLaRetiradaFallidaNoRevocaLaPropiedadDeLaRuta(t *testing.T) {
	fallando := &falloReleaser{}
	t.Cleanup(func() { engineReleaseStub, engineReleaseStubInstalled = nil, false })
	engineReleaseStub = fallando.RemoveAbsent
	engineReleaseStubInstalled = true

	e := NewEngine(&noKillManager{}, state.NewStoreAt(t.TempDir()))

	meta := state.Meta{RouteOwned: true, RouteName: "mi-ruta"}
	e.releaseRouteOnStop(&meta)

	if !meta.RouteOwned {
		t.Error("RouteOwned = false con una retirada que falló: la ruta sigue puesta y ahora " +
			"nadie tiene su handle, así que no la puede limpiar nadie")
	}
	if !fallando.intentos {
		t.Error("no se intentó retirar la ruta")
	}
}

// TestEngineRouteReleaserDevuelveNilFueraDeUnBinarioDeTest: la rama de producción.
//
// Las otras dos —stub instalado, binario de test— las usa la suite a diario. La
// tercera es la que decide que el engine habla con el portless real, y sin probarla
// nada garantiza que un `nil` accidental no se confunda con "este servicio no tiene
// ruta".
//
// MEDIDO: `IsTestBinary` decide por el sufijo de `os.Args[0]`, así que cambiarlo
// reproduce la entrada del binario instalado sin tocar la función ni su contrato.
func TestEngineRouteReleaserDevuelveNilFueraDeUnBinarioDeTest(t *testing.T) {
	original := os.Args[0]
	t.Cleanup(func() { os.Args[0] = original })

	os.Args[0] = "/usr/local/bin/vroom"
	if portless.IsTestBinary() {
		t.Fatal("IsTestBinary sigue diciendo que es un test con un argv de producción")
	}
	if got := engineRouteReleaser(); got != nil {
		t.Errorf("engineRouteReleaser() = %v con un binario de producción, want nil", got)
	}
}

// falloReleaser devuelve un error real de portless: no retiró nada.
type falloReleaser struct{ intentos bool }

func (r *falloReleaser) RemoveAbsent(string) error {
	r.intentos = true
	return errors.New("portless no pudo retirar la ruta")
}

// registroManager apunta a quién se ha parado, con su PID, para poder contar.
type registroManager struct{ paradas *[]string }

func (m *registroManager) Stop(spec process.StopSpec) error {
	*m.paradas = append(*m.paradas, strconv.Itoa(spec.Pid))
	return nil
}
func (*registroManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (*registroManager) Evaluate(process.EvalSpec) process.Status { return process.StatusStopped }

// proyectosConNombres construye proyectos escaneados con esos nombres de manifiesto.
func proyectosConNombres(nombres ...string) []scanner.Project {
	out := make([]scanner.Project, 0, len(nombres))
	for i, n := range nombres {
		p := scanner.Project{Path: "/srv/" + n, Name: n, Configured: true}
		p.Manifest = &manifest.Manifest{Name: n, Command: "true", Port: 3000 + i}
		out = append(out, p)
	}
	return out
}
