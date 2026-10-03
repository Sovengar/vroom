package group

import (
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/scanner"
)

// ---------------------------------------------------------------------------
// Los accesores de grupo y el agrupamiento en bloques contiguos.
//
// `PrimaryOf` y `SecondaryOf` se llaman sobre CADA proyecto del escaneo, incluidos
// los directorios sin `.vroom.toml` que el escaneo mete en el árbol. El caso del
// manifiesto nil es el más probable de los dos, y un nil dentro de cualquiera de los
// dos apagaría la TUI en el primer escaneo de un workspace con una carpeta suelta.
//
// Y `IsPrimaryHeader` es la regla que decide qué fila abre un bloque. Get it wrong
// y el árbol enseña cabeceras duplicadas o peor: ninguna.
func TestLosAccesoresNoRevientanConUnProyectoSinManifiesto(t *testing.T) {
	casos := []struct {
		nombre     string
		p          scanner.Project
		primario   string
		secundario string
	}{
		{"sin manifiesto en absoluto", scanner.Project{Name: "suelto"}, "", ""},
		{"manifiesto a nil explícito", scanner.Project{Name: "x", Manifest: nil}, "", ""},
		{"con ambos grupos", proyectoConGrupos("tienda", "backend", "api"), "tienda", "backend"},
		{"sólo primario", proyectoConGrupos("tienda", "", "api"), "tienda", ""},
		{"sólo secundario", proyectoConGrupos("", "backend", "api"), "", "backend"},
		{"sin ningún grupo", proyectoConGrupos("", "", "x"), "", ""},
	}
	for _, tt := range casos {
		t.Run(tt.nombre, func(t *testing.T) {
			if got := PrimaryOf(tt.p); got != tt.primario {
				t.Errorf("PrimaryOf = %q, want %q", got, tt.primario)
			}
			if got := SecondaryOf(tt.p); got != tt.secundario {
				t.Errorf("SecondaryOf = %q, want %q", got, tt.secundario)
			}
		})
	}
}

// TestElGrupoVacioNoEsUnGrupoNiComoPrimaryNiComoSecundario: el invariante de los
// accesores.
//
// Devolver "" es lo que hace que el agrupamiento trate el proyecto como inline en
// lugar de meterlo en un bloque llamado "". Un bloque vacío en el árbol es una fila
// que no lleva a ningún sitio.
func TestElGrupoVacioNoEsUnGrupoNiComoPrimaryNiComoSecundario(t *testing.T) {
	p := proyectoConGrupos("", "", "x")
	if PrimaryOf(p) != "" {
		t.Errorf("un primary vacío devolvió %q: el agrupamiento lo trataría como un grupo llamado %q", PrimaryOf(p), "")
	}
	if SecondaryOf(p) != "" {
		t.Errorf("un secondary vacío devolvió %q", SecondaryOf(p))
	}
}

// TestIsPrimaryHeaderAbreUnBloqueSoloEnSuPrimeraEntrada: la regla del bloque
// contiguo.
//
// Las entradas llegan YA ORDENADAS por grupo, así que "abre el bloque" es
// literalmente "el anterior es de otro grupo". De ahí las dos condiciones: grupo no
// vacío, y ser el primero o tener un vecino distinto.
//
// Cada caso importa por lo que rompería: sin la condición de "no vacío", un proyecto
// inline abriría un bloque; sin la del vecino, cada miembro abriría su propio bloque
// y el árbol tendría cuatro cabeceras en vez de una.
func TestIsPrimaryHeaderAbreUnBloqueSoloEnSuPrimeraEntrada(t *testing.T) {
	entradas := []Entry{
		{Primary: "tienda"},
		{Primary: "tienda"},
		{Primary: "tienda"},
		{Primary: "blog"},
		{Primary: "blog"},
		{Primary: ""}, // inline: no abre bloque
		{Primary: ""}, // inline: tampoco
		{Primary: "otro"},
	}

	want := []bool{
		true,  // la primera de tienda
		false, // la segunda ya está dentro
		false, // la tercera también
		true,  // cambio de grupo
		false, // dentro de blog
		false, // sin grupo: no abre bloque
		false, // sin grupo: tampoco
		true,  // vuelta a haber grupo
	}

	for i := range entradas {
		if got := IsPrimaryHeader(entradas, i); got != want[i] {
			t.Errorf("IsPrimaryHeader(%d) = %v, want %v (grupo %q)", i, got, want[i], entradas[i].Primary)
		}
	}
}

// TestUnBloqueDeUnSoloMiembroSíAbreCabecera: el borde de un bloque de tamaño uno.
//
// Es el caso donde las dos condiciones se contradicen en apariencia: no hay "anterior
// del mismo grupo" y aun así tiene que abrir cabecera. Si fallara, el único proyecto
// de un grupo aparecería sin el grupo encima, que es un proyecto suelto en el árbol.
func TestUnBloqueDeUnSoloMiembroSiAbreCabecera(t *testing.T) {
	entradas := []Entry{
		{Primary: "blog"},
		{Primary: "tienda"},
	}
	if !IsPrimaryHeader(entradas, 1) {
		t.Error("un grupo con un solo miembro tiene que abrir cabecera: si no, el proyecto " +
			"aparece suelto en el árbol y su grupo desaparece")
	}
	// Y el primero de todos también, aunque no tenga anterior.
	if !IsPrimaryHeader(entradas, 0) {
		t.Error("la primera entrada tiene que abrir cabecera aunque no tenga anterior")
	}
}

// TestArrangeOrdenaElArbolSinMezclarGrupos: el orden que hace posible la regla
// de bloque contiguo.
//
// `IsPrimaryHeader` sólo funciona porque `Arrange` deja los miembros de un primario
// juntos. Si dos primarios se intercalaran, la regla de "el anterior es de otro
// grupo" daría dos cabeceras para el mismo grupo y ninguna para el otro.
func TestSecundarioOrdenaElArbolSinMezclarGrupos(t *testing.T) {
	// El orden de ENTRADA va intercalado a propósito, que es lo que el escaneo
	// produce: los proyectos llegan en orden de directorios, no de grupo.
	proyectos := []scanner.Project{
		proyectoConGrupos("tienda", "", "api"),
		proyectoConGrupos("blog", "", "api"),
		proyectoConGrupos("tienda", "", "web"),
	}
	entradas := Arrange(proyectos)

	var cabeceras []string
	var grupoActual string
	for i := range entradas {
		if !IsPrimaryHeader(entradas, i) {
			if entradas[i].Primary != grupoActual {
				t.Errorf("la entrada %d (%s) dice que no abre bloque pero es del grupo %q y el anterior era %q: "+
					"los grupos no están ordenados", i, entradas[i].Project.Name, entradas[i].Primary, grupoActual)
			}
			continue
		}
		if cabeceras != nil && cabeceras[len(cabeceras)-1] == entradas[i].Primary {
			t.Errorf("el grupo %q abre dos cabeceras: los miembros no están contiguos", entradas[i].Primary)
		}
		cabeceras = append(cabeceras, entradas[i].Primary)
		grupoActual = entradas[i].Primary
	}

	if len(cabeceras) != 2 {
		t.Errorf("se abrieron %d cabeceras (%v), want 2: una por grupo", len(cabeceras), cabeceras)
	}
	// MEDIDO: el orden de los bloques es el de PRIMERA APARICIÓN, no el alfabético.
	// Con "tienda" llegando antes que "blog" en la entrada, el árbol empieza por
	// "tienda" — que es lo que quiere el usuario: sus proyectos en el orden en el que
	// los recorre. Ordenar alfabéticamente sería más bonito y menos útil.
	if cabeceras[0] != "tienda" {
		t.Errorf("la primera cabecera es %q, want tienda: el orden es el de primera aparición", cabeceras[0])
	}
}

// proyectoConGrupos construye un proyecto con nombre y los dos grupos.
func proyectoConGrupos(primary, secondary, name string) scanner.Project {
	return scanner.Project{
		Path: "/" + name, Name: name,
		Manifest: &manifest.Manifest{
			Name: name, Command: "./" + name,
			PrimaryGroup: primary, SecondaryGroup: secondary,
		},
	}
}
