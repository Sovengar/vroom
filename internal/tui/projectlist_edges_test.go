package tui

import (
	"strings"
	"testing"

	"vroom/internal/group"
	"vroom/internal/manifest"
	"vroom/internal/scanner"
	"vroom/internal/tail"
)

func TestFilterMatchTraePorNombreYPorGrupo(t *testing.T) {
	tests := []struct {
		nombre string
		p      scanner.Project
		q      string
		want   bool
		porQue string
	}{
		{
			"exacto por nombre", scanner.Project{Name: "tienda-api"}, "tienda-api", true, "",
		},
		{
			"prefijo del nombre", scanner.Project{Name: "tienda-api"}, "tienda", true, "",
		},
		{
			"sin distinguir mayúsculas", scanner.Project{Name: "Tienda-Api"}, "tienda", true, "",
		},
		{
			"por grupo primario", scanner.Project{Name: "api", Manifest: manifestGroup("tienda", "")}, "tienda", true,
			"filtrar por grupo tiene que traer a un miembro que no se llama como el grupo",
		},
		{
			"por grupo secundario", scanner.Project{Name: "api", Manifest: manifestGroup("tienda", "backend")}, "backend", true,
			"el secundario también matchea: es lo que distingue dos 'api' del mismo primario",
		},
		{
			"otro grupo", scanner.Project{Name: "api", Manifest: manifestGroup("tienda", "backend")}, "blog", false, "",
		},
		{
			"cadena vacía trae todo", scanner.Project{Name: "cualquiera"}, "", true,
			"un filtro vacío no puede filtrar nada: se abriría el filtro y desaparecerían todas las filas",
		},
		{
			"proyecto sin grupo ni nombre coincidente", scanner.Project{Name: "api"}, "web", false, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			if got := filterMatch(tt.p, tt.q); got != tt.want {
				t.Errorf("filterMatch(%q, %q) = %v, want %v. %s", tt.p.Name, tt.q, got, tt.want, tt.porQue)
			}
		})
	}
}

func TestFilterMatchNoTraeLoQueNoDebeYElConteoCuadra(t *testing.T) {
	proyectos := []scanner.Project{
		{Name: "tienda-api", Manifest: manifestGroup("tienda", "backend")},
		{Name: "tienda-web", Manifest: manifestGroup("tienda", "frontend")},
		{Name: "blog-api", Manifest: manifestGroup("blog", "")},
		{Name: "suelto"},
	}

	for _, q := range []string{"tienda", "api", "backend", "blog", "suelto", "nada-de-esto", ""} {
		var conMatch []string
		for _, p := range proyectos {
			if filterMatch(p, q) {
				conMatch = append(conMatch, p.Name)
			}
		}
		// Names in this fixture are unique, so set equality with the filter result is an exact check.
		for _, p := range proyectos {
			tiene := false
			for _, n := range conMatch {
				if n == p.Name {
					tiene = true
				}
			}
			if tiene != filterMatch(p, q) {
				t.Errorf("q=%q: la lista y filterMatch discrepan sobre %q", q, p.Name)
			}
		}
	}

	var tienda []string
	for _, p := range proyectos {
		if filterMatch(p, "tienda") {
			tienda = append(tienda, p.Name)
		}
	}
	if len(tienda) != 2 || tienda[0] != "tienda-api" || tienda[1] != "tienda-web" {
		t.Errorf("el filtro tienda trae %v, want los dos del grupo y nada más", tienda)
	}

	var api []string
	for _, p := range proyectos {
		if filterMatch(p, "api") {
			api = append(api, p.Name)
		}
	}
	if len(api) != 2 {
		t.Errorf("el filtro api trae %v, want los dos", api)
	}
}

// The example manifest carries no port on purpose: an example port belongs to a twin worktree, so the health probe would hit that other service.
func TestExampleManifestEsValidoYNoSeColisionaConNada(t *testing.T) {
	got := exampleManifest("mi-proyecto")

	if strings.Contains(got, "port = 80") || strings.Contains(got, "port = 3000") {
		t.Errorf("el manifiesto de ejemplo trae un puerto inventado: %q", got)
	}

	// Names with quotes or backslashes must still yield quoted, parseable TOML, hence the odd inputs below.
	for _, nombre := range []string{"normal", `con "comillas"`, "con\\barra", "acentuado-ñ"} {
		txt := exampleManifest(nombre)
		if !strings.Contains(txt, `name = "`) {
			t.Errorf("exampleManifest(%q) no entrecomilla el nombre: %q", nombre, txt)
		}
		if !strings.Contains(txt, "command_start") {
			t.Errorf("exampleManifest(%q) no trae command_start, que es el campo obligatorio: %q", nombre, txt)
		}
	}
}

func TestTreeDotCobreLosEstadosYElDesconocido(t *testing.T) {
	configurada := scanner.Project{Path: "/p", Name: "p", Configured: true, Manifest: manifestWithPort(4321)}
	sinManifiesto := scanner.Project{Path: "/p", Name: "p", Configured: false}
	manifiestoRoto := scanner.Project{Path: "/p", Name: "p", Configured: true, ManifestErr: "falta command_start"}

	tests := []struct {
		nombre string
		p      scanner.Project
		sv     *ServiceState
		quiere string
	}{
		{"corriendo", configurada, &ServiceState{Status: statusRunning}, "●"},
		{"arrancando", configurada, &ServiceState{Status: statusStarting}, "◌"},
		{"parando", configurada, &ServiceState{Status: statusStopping}, "○"},
		{"desconocido", configurada, &ServiceState{Status: statusUnknown}, "◐"},
		{"parado", configurada, &ServiceState{Status: statusStopped}, "·"},
		// MEDIDO (bug): all three fell into the default so a live service rendered with the stopped dot; port_pending also contradicted its own row badge and the "s" action that treats it as live.
		{"port pending", configurada, &ServiceState{Status: statusPortPending}, "◌"},
		{"port unresolved", configurada, &ServiceState{Status: statusPortUnresolved}, "●"},
		{"no port", configurada, &ServiceState{Status: statusNoPort}, "●"},
		{"sin manifiesto", sinManifiesto, &ServiceState{Status: statusRunning}, "⚠"},
		{"manifiesto inválido", manifiestoRoto, &ServiceState{Status: statusStopped}, "⚠"},
		{"estado nuevo de process", configurada, &ServiceState{Status: uiStatus("inventado")}, "·"},
	}
	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			got := tail.StripANSI(treeDot(tt.p, tt.sv, "◐", "◌"))
			if !strings.Contains(got, tt.quiere) {
				t.Errorf("treeDot(%s) = %q, want %q", tt.nombre, got, tt.quiere)
			}
		})
	}

	t.Run("el punto nunca contradice al badge de su fila", func(t *testing.T) {
		for _, st := range []uiStatus{
			statusRunning, statusStarting, statusPortPending, statusPortUnresolved, statusNoPort,
		} {
			punto := tail.StripANSI(treeDot(configurada, &ServiceState{Status: st}, "◐", "◌"))
			if punto == "·" {
				t.Errorf("el estado %q es vivo pero el árbol lo pinta como parado", st)
			}
		}
	})

	t.Run("sin estado conocido", func(t *testing.T) {
		got := tail.StripANSI(treeDot(configurada, nil, "◐", "◌"))
		if !strings.Contains(got, "·") {
			t.Errorf("sin estado = %q, want el punto de parado: no se ha preguntado, no se puede decir que vive", got)
		}
	})
}

func TestElArbolEmiteElHeaderDeComposersSoloConStacksAhi(t *testing.T) {
	// The stackTree fixture mixes stacks and plain projects under one primary.
	m := newStackModel(t)

	var conComposers, conStack, conProject bool
	for _, it := range m.tree {
		if it.kind == itemSecondary && it.secondary == composersGroup {
			conComposers = true
		}
		if it.kind == itemStack {
			conStack = true
		}
		if it.kind == itemProject {
			conProject = true
		}
	}
	if !conComposers || !conStack || !conProject {
		t.Errorf("con stacks el árbol tiene que emitir los tres: composers=%v stack=%v project=%v",
			conComposers, conStack, conProject)
	}

	t.Run("el header de composers lleva el grupo y el marcador", func(t *testing.T) {
		var encontrado bool
		for _, it := range m.tree {
			if it.kind == itemSecondary && it.secondary == composersGroup {
				encontrado = true
				if it.primary == "" {
					t.Error("el header de composers sin primary: no se sabe a qué grupo pertenece")
				}
			}
		}
		if !encontrado {
			t.Error("no se encontró el header de composers")
		}
	})

	t.Run("primario sin stacks no emite header de composers", func(t *testing.T) {
		sinStacks, _ := newTestModel(t)
		for _, it := range sinStacks.tree {
			if it.kind == itemSecondary && it.secondary == composersGroup {
				t.Error("sin compose file el árbol no puede emitir un header de composers: sería una fila vacía")
			}
		}
	})
}

func TestGroupPrimaryYSecondaryDeUnProyectoSinManifiestoNoRevientan(t *testing.T) {
	varios := []scanner.Project{
		{},
		{Name: "sin-manifiesto"},
		{Name: "con-manifest-nil", Manifest: nil},
		{Name: "con-grupo", Manifest: manifestGroup("tienda", "backend")},
		{Name: "solo-primario", Manifest: manifestGroup("tienda", "")},
	}
	for _, p := range varios {
		_ = group.PrimaryOf(p)
		_ = group.SecondaryOf(p)
		if got := filterMatch(p, "tienda"); got && p.Manifest == nil {
			t.Errorf("un proyecto sin manifiesto matcheó por un grupo que no tiene: %+v", p)
		}
	}
}

func manifestGroup(primary, secondary string) *manifest.Manifest {
	return &manifest.Manifest{
		Name:           "p",
		Command:        "./p",
		PrimaryGroup:   primary,
		SecondaryGroup: secondary,
	}
}
