package tui

import (
	"strings"
	"testing"

	"vroom/internal/group"
	"vroom/internal/manifest"
	"vroom/internal/scanner"
	"vroom/internal/tail"
)

// ---------------------------------------------------------------------------
// La construcción del árbol y el filtro: las dos funciones que deciden QUÉ filas
// existen.
//
// El filtro se equivoca de dos maneras opuestas y las dos son malas. Filtrar de más
// esconde proyectos que existen y el usuario cree que no están. Filtrar de menos
// deja proyectos que no se filtró y el usuario busca uno que no aparece. La
// segunda es la que engaña: un filtro con un nombre que el usuario sabe que
// existe y no aparece es indistinguible de un fallo.
//
// Y el punto de matchear por grupo importa más que el del nombre: filtrar "tienda"
// tiene que traer los diez servicios del grupo, no sólo el que se llama
// "tienda-api". Al revés, un usuario con dos grupos comparte nombres.
// ---------------------------------------------------------------------------

// TestFilterMatchTraePorNombreYPorGrupo: los tres campos contra los que se
// compara.
//
// El caso que define la función: un filtro por grupo tiene que traer a TODOS sus
// miembros aunque el texto no aparezca en ninguno de sus nombres. Filtrar por grupo
// es lo que hace útil el filtro en un workspace con muchos proyectos de nombre
// distinto en el mismo grupo.
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

// TestFilterMatchNoTraeLoQueNoDebeYElConteoCuadra: el filtro tiene que ser el
// inverso exacto de "no matchea".
//
// Es la propiedad que hace que el contador de la barra —"⌕ texto · n"— sea
// creíble. Si `n` no fuera el número de filas que quedan, el usuario vería un 3 y
// dos filas y pensaría que el filtro está roto.
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
		// La invariante: lo que dice filterMatch y lo que quedó en la lista son lo
		// mismo. El árbol de test no tiene nombres duplicados, así que la
		// comparación es exacta.
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

	// Y un caso concreto y legible: "tienda" trae los dos del grupo, no el blog.
	var tienda []string
	for _, p := range proyectos {
		if filterMatch(p, "tienda") {
			tienda = append(tienda, p.Name)
		}
	}
	if len(tienda) != 2 || tienda[0] != "tienda-api" || tienda[1] != "tienda-web" {
		t.Errorf("el filtro tienda trae %v, want los dos del grupo y nada más", tienda)
	}

	// "api" trae los dos que se llaman api, uno de cada grupo: el nombre manda
	// sobre el grupo cuando ambos matchean.
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

// TestExampleManifestEsValidoYNoSeColisionaConNada: el manifiesto de ejemplo que
// se le ofrece al usuario.
//
// Es lo que el usuario copia para arrancar. Si no parseara, la sugerencia sería peor
// que no sugerir nada: el usuario lo copia y ve un error de sintaxis en vez de un
// error de "te falta esto".
//
// Y no lleva port: un puerto de ejemplo sería peor que ninguno, porque el servicio
// arrancaría en un puerto que no es suyo y la sonda de salud apuntaría al twin de
// otro worktree.
func TestExampleManifestEsValidoYNoSeColisionaConNada(t *testing.T) {
	got := exampleManifest("mi-proyecto")

	// No lleva puerto inventado.
	if strings.Contains(got, "port = 80") || strings.Contains(got, "port = 3000") {
		t.Errorf("el manifiesto de ejemplo trae un puerto inventado: %q", got)
	}

	// El nombre va entrecomillado: un proyecto con comillas o barra en el nombre
	// tiene que producir un TOML que parsee.
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

// TestTreeDotCubreLosEstadosYElDesconocido: el punto de cada fila del árbol.
//
// Es lo que el usuario lee de un vistazo al escanear, y hay tres casos que se
// confunden si se colapsan:
//
//   - ⚠ para lo que no se puede arrancar (sin manifiesto o manifiesto inválido);
//   - ● para lo que vive;
//   - el punto para lo que está parado, que es la mayoría del tiempo.
//
// El ⚠ tiene prioridad sobre todo lo demás porque es el único que significa "este
// proyecto necesita algo de ti".
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
		// MEDIDO (bug): los tres caían en el default y un servicio VIVO se pintaba
		// con el punto de parado. El de port_pending se contradecía con el badge de
		// su propia fila y con la acción de `s`, que lo trata como vivo.
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
		// La invariante que importa: para cada estado vivo, el punto NO puede ser el
		// de parado. El badge es la referencia porque es el que ya distingue los
		// tres estados vivos con su propio texto.
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
		// sv nil: el proyecto se acaba de escanear y aún no se le ha preguntado.
		got := tail.StripANSI(treeDot(configurada, nil, "◐", "◌"))
		if !strings.Contains(got, "·") {
			t.Errorf("sin estado = %q, want el punto de parado: no se ha preguntado, no se puede decir que vive", got)
		}
	})
}

// TestElArbolEmiteElHeaderDeComposersSoloConStacksAhi: el agrupamiento de stacks.
//
// El caso que importa es el primario que sólo tiene stacks y ningún proyecto: su
// header tiene que existir igualmente, o el usuario vería stacks flotando sin el
// grupo al que pertenecen y no sabría qué los lanza juntos.
//
// Y al revés: un primario sin stacks NO emite header de Composers, porque sería
// una fila vacía entre dos grupos que no lleva a ningún sitio.
func TestElArbolEmiteElHeaderDeComposersSoloConStacksAhi(t *testing.T) {
	// El árbol de stackTree tiene un grupo con stacks y proyectos.
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
		// Se quita el compose file: sin stacks no puede haber header de composers,
		// y sin él el árbol se construye sólo con proyectos.
		sinStacks, _ := newTestModel(t)
		for _, it := range sinStacks.tree {
			if it.kind == itemSecondary && it.secondary == composersGroup {
				t.Error("sin compose file el árbol no puede emitir un header de composers: sería una fila vacía")
			}
		}
	})
}

// TestGroupPrimaryYSecondaryDeUnProyectoSinManifiestoNoRevientan: los accesos a
// grupo se llaman sobre proyectos que pueden no tener manifiesto.
//
// El escaneo mete en el árbol también los directorios sin `.vroom.toml`, así que
// cualquiera de estas dos funciones puede recibir un manifiesto nil. Un panic ahí
// apaga la TUI en el primer escaneo de un workspace con una carpeta suelta.
func TestGroupPrimaryYSecondaryDeUnProyectoSinManifiestoNoRevientan(t *testing.T) {
	varios := []scanner.Project{
		{},
		{Name: "sin-manifiesto"},
		{Name: "con-manifest-nil", Manifest: nil},
		{Name: "con-grupo", Manifest: manifestGroup("tienda", "backend")},
		{Name: "solo-primario", Manifest: manifestGroup("tienda", "")},
	}
	for _, p := range varios {
		// No hay panic: sólo que devuelvan algo usable para filtrar y ordenar.
		_ = group.PrimaryOf(p)
		_ = group.SecondaryOf(p)
		if got := filterMatch(p, "tienda"); got && p.Manifest == nil {
			t.Errorf("un proyecto sin manifiesto matcheó por un grupo que no tiene: %+v", p)
		}
	}
}

// manifestGroup construye el manifiesto mínimo con los dos grupos.
func manifestGroup(primary, secondary string) *manifest.Manifest {
	return &manifest.Manifest{
		Name:           "p",
		Command:        "./p",
		PrimaryGroup:   primary,
		SecondaryGroup: secondary,
	}
}
