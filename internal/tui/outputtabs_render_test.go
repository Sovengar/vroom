package tui

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"vroom/internal/gitinfo"
	"vroom/internal/state"
	"vroom/internal/tail"
)

// ---------------------------------------------------------------------------
// Los cuatro paneles de muestreo del Output: métricas, git, entorno y salud.
//
// Cada uno tiene la misma estructura de cuatro rechazos —sin selección, sin
// manifiesto, parado, y "todavía no ha llegado"— y cada rechazo dice una cosa
// distinta. La razón por la que el primero importa más que los otros: sin
// selección hay que VACIAR el panel en vez de dejar el del servicio anterior, que
// es como el usuario acaba creyendo que el servicio que acaba de seleccionar es el
// que está escribiendo.
//
// Y los cuatro se ejercitaban sólo en su camino feliz, con el resto en negro. Un
// panel en negro no es un panel probado: un error de sonda y un panel vacío se
// ven igual.
// ---------------------------------------------------------------------------

// ComoPanel ejecuta una función de render sobre el servicio indicado y devuelve el
// texto plano del panel.
func comoPanel(t *testing.T, m Model, f func(Model, int) []string) string {
	t.Helper()
	return tail.StripANSI(strings.Join(f(m, m.rightW), "\n"))
}

// Los cuatro rechazos de los cuatro paneles: sin selección y sin manifiesto.
//
// El de sin manifiesto es el más importante porque NO vacía: los cuatro dicen qué
// falta, y ninguno puede mostrar los datos del servicio anterior.
func TestPanelesDeMuestreoDicenPorQueNoTraenDatos(t *testing.T) {
	paneles := []struct {
		nombre string
		render func(Model, int) []string
		quiere string
	}{
		{"metrics", Model.metricsLines, "select a service"},
		{"git", Model.gitLines, "select a project"},
		{"env", Model.envLines, "select a service"},
		{"timeline", Model.timelineLines, "select a service"},
		{"health", Model.healthLines, "select a service"},
	}
	for _, p := range paneles {
		t.Run(p.nombre+"/sin selección", func(t *testing.T) {
			m := sinSeleccion(t)
			got := comoPanel(t, m, p.render)
			if !strings.Contains(got, p.quiere) {
				t.Errorf("%s sin selección = %q, want una invitación a elegir", p.nombre, got)
			}
		})
	}

	// Sin manifiesto: cada panel tiene su propio texto y NINGUNO puede ser el de
	// "sin selección", porque el proyecto SÍ está seleccionado.
	for _, p := range []struct {
		nombre string
		render func(Model, int) []string
		quiere string
	}{
		{"metrics", Model.metricsLines, "no manifest"},
		{"env", Model.envLines, "no manifest"},
		{"health", Model.healthLines, "no manifest"},
	} {
		t.Run(p.nombre+"/sin manifiesto", func(t *testing.T) {
			m := sinManifiesto(t)
			got := comoPanel(t, m, p.render)
			if !strings.Contains(got, p.quiere) {
				t.Errorf("%s sin manifiesto = %q", p.nombre, got)
			}
			if strings.Contains(got, "select a service") {
				t.Errorf("%s = %q: el proyecto está seleccionado, el motivo es otro", p.nombre, got)
			}
		})
	}
}

// TestPanelesDeMuestreoDicenQueElServicioNoCorre: parado es un estado, no un error.
//
// Y con un texto que empuja a ARRANCAR, no a esperar: si el panel dijera "no data",
// el usuario pensaría que la sonda falló y esperaría a que volviera sola.
func TestPanelesDeMuestreoDicenQueElServicioNoCorre(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusStopped

	for _, p := range []struct {
		nombre string
		render func(Model, int) []string
		quiere string
	}{
		{"metrics", Model.metricsLines, "not running"},
		{"env", Model.envLines, "start it"},
		{"health", Model.healthLines, "not running"},
	} {
		t.Run(p.nombre, func(t *testing.T) {
			if got := comoPanel(t, m, p.render); !strings.Contains(got, p.quiere) {
				t.Errorf("%s con el servicio parado = %q, want %q", p.nombre, got, p.quiere)
			}
		})
	}
}

// TestPanelesDeMuestreoDicenQueEstanLeyendo: servicio vivo, panel aún vacío.
//
// Son tres estados distintos —"sampling metrics…", "reading git status…",
// "reading environment…", "probing…" y "no events yet"— y todos significan que la
// respuesta está de camino. Lo que no puede pasar es un panel vacío: eso se lee
// como "no hay nada" y el usuario se va a mirar el log en otra parte.
func TestPanelesDeMuestreoDicenQueEstanLeyendo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	setManifestPort(&m, "tienda-api", 4321)

	for _, p := range []struct {
		nombre string
		render func(Model, int) []string
		quiere string
	}{
		{"metrics", Model.metricsLines, "sampling"},
		{"env", Model.envLines, "reading"},
		{"health", Model.healthLines, "probing"},
		{"timeline", Model.timelineLines, "no events"},
	} {
		t.Run(p.nombre, func(t *testing.T) {
			if got := comoPanel(t, m, p.render); !strings.Contains(got, p.quiere) {
				t.Errorf("%s = %q, want %q", p.nombre, got, p.quiere)
			}
		})
	}

	// Git es distinto: lee por proyecto, no por proceso vivo, así que no depende
	// del estado. Se comprueba aparte.
	t.Run("git", func(t *testing.T) {
		if got := comoPanel(t, m, Model.gitLines); !strings.Contains(got, "reading") {
			t.Errorf("git = %q, want que diga que está leyendo", got)
		}
	})
}

// TestGitLinesDistingueRepoDeNoRepoYLimpioDeSucio: los tres contenidos.
//
// "(no git repo)" importa: un proyecto sin .git no es un error, es un proyecto
// normal fuera de git. Mostrarlo como inválido haría que el usuario creyera que
// tiene un problema.
//
// Y "N changed" es un número, no un "dirty": el usuario quiere saber si es un
// fichero o veinte antes de abrir el editor.
func TestGitLinesDistingueRepoDeNoRepoYLimpioDeSucio(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	t.Run("repo limpio", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{Branch: "main", Commits: []string{"abc1234 primero"}}
		got := comoPanel(t, m, Model.gitLines)
		for _, want := range []string{"branch:", "main", "status:", "clean", "commits:", "abc1234"} {
			if !strings.Contains(got, want) {
				t.Errorf("un repo limpio no trae %q:\n%s", want, got)
			}
		}
		// Y sin bloque de cambios: no hay nada que listar.
		if strings.Contains(got, "changes:") {
			t.Errorf("un repo limpio no puede traer sección de cambios:\n%s", got)
		}
	})

	t.Run("repo sucio", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{
			Branch:  "feature/login",
			Changed: []string{" M app.go", "?? nuevo.go"},
		}
		got := comoPanel(t, m, Model.gitLines)
		if !strings.Contains(got, "2 changed") {
			t.Errorf("el recuento de cambios tiene que ser el número, no un \"dirty\":\n%s", got)
		}
		if !strings.Contains(got, "changes:") || !strings.Contains(got, "nuevo.go") {
			t.Errorf("el panel tiene que listar los ficheros:\n%s", got)
		}
	})

	t.Run("sin repo", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{}
		if got := comoPanel(t, m, Model.gitLines); !strings.Contains(got, "no git repo") {
			t.Errorf("un proyecto fuera de git = %q, want la etiqueta de proyecto fuera de git, porque no es un error", got)
		}
	})

	t.Run("git falla", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{Err: "no such repository"}
		got := comoPanel(t, m, Model.gitLines)
		if !strings.Contains(got, "no such repository") {
			t.Errorf("un fallo de git tiene que aparecer: %q", got)
		}
		if strings.Contains(got, "clean") {
			t.Errorf("un fallo de git no puede salir como clean, porque sería mentir sobre el estado:\n%s", got)
		}
	})
}

// TestHealthLinesMuestraElCuerpoCuandoElProbeTraeUno: el snippet es lo que hace
// útil el panel.
//
// Un 500 sin cuerpo deja al usuario con un código y nada que interpretar. El
// snippet son los primeros bytes de la respuesta, que es justo lo que decide si el
// fallo es del servicio o del proxy.
func TestHealthLinesMuestraElCuerpoCuandoElProbeTraeUno(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	setManifestPort(&m, "tienda-api", 4321)
	m.healthRes[path] = &healthResult{
		StatusCode:  500,
		Latency:     350 * time.Millisecond,
		ContentType: "application/json",
		Snippet:     `{"error":"boom"}`,
	}

	got := comoPanel(t, m, Model.healthLines)
	for _, want := range []string{"status:", "HTTP 500", "latency:", "type:", "application/json", `{"error":"boom"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("el panel de salud no trae %q:\n%s", want, got)
		}
	}

	// Sin snippet no hay una línea de cuerpo: se distingue de un cuerpo vacío.
	m.healthRes[path].Snippet = ""
	if got := comoPanel(t, m, Model.healthLines); strings.Contains(got, "snippet") {
		t.Errorf("sin snippet no hay nada que enseñar:\n%s", got)
	}
}

// TestHealthLinesDistingueUn200DeUn404PorElCodigo: el código decide el estilo, y
// con él la lectura del usuario.
//
// Un 3xx también se marca: una redirección en una sonda de salud significa que la
// ruta no es la que el manifiesto dice, que es justo el bug que el panel existe
// para hacer visible.
func TestHealthLinesDistingueUn200DeUn404PorElCodigo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	setManifestPort(&m, "tienda-api", 4321)

	for _, code := range []int{200, 204, 301, 302, 400, 404, 500, 503} {
		m.healthRes[path] = &healthResult{StatusCode: code, ContentType: "text/plain"}
		got := comoPanel(t, m, Model.healthLines)
		if !strings.Contains(got, "HTTP "+strconv.Itoa(code)) {
			t.Errorf("código %d no aparece: %q", code, got)
		}
	}
}

// TestHealthLinesNoSondeaConElManifestoSinPuertoYLoDice: el mensaje tiene que
// decir el campo.
//
// Es la mitad de "no hay nada que mirar" y la otra es "qué escribir". Con el texto
// completo el usuario abre el manifiesto y lo arregla en un minuto; con un "no port"
// seco tiene que buscar el nombre del campo.
func TestHealthLinesNoSondeaConElManifestoSinPuertoYLoDice(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Meta = state.Meta{State: state.StateStopped, Port: 0}
	if p := m.projectByPath(path); p != nil && p.Manifest != nil {
		p.Manifest.Port = 0
	}

	got := comoPanel(t, m, Model.healthLines)
	if !strings.Contains(got, "port = N") {
		t.Errorf("= %q, want que diga QUÉ escribir en el manifiesto", got)
	}
}

// TestEnvLinesCuentaLasVariablesYLasLista: el encabezado con el recuento.
//
// El recuento es lo que hace útil el panel: un entorno con 3 variables se lee de
// un vistazo y uno con 300 dice que hay algo raro sin abrir la lista.
func TestEnvLinesCuentaLasVariablesYLasLista(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	m.envVars[path] = []string{"A=1", "B=2", "C=3"}

	got := comoPanel(t, m, Model.envLines)
	if !strings.Contains(got, "3 variables") {
		t.Errorf("= %q, want el recuento", got)
	}
	for _, want := range []string{"A=1", "B=2", "C=3"} {
		if !strings.Contains(got, want) {
			t.Errorf("no lista %q:\n%s", want, got)
		}
	}
}

// TestTimelineLinesLosOrdenaDelMasRecienteAlMasAntiguo: el primero es el último
// que pasó.
//
// Es lo contrario del orden de un log, y a propósito: en un timeline el usuario
// viene a ver "qué acabo de pasar". Un timeline en orden cronológico obliga a
// llegar al final para ver lo reciente, que es donde ya está el cursor.
func TestTimelineLinesLosOrdenaDelMasRecienteAlMasAntiguo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	m.events[path] = []timelineEvent{
		{At: base, Kind: "start", Detail: "arrancado", Elapsed: 0, OK: true},
		{At: base.Add(time.Minute), Kind: "build", Detail: "falló", Elapsed: 2 * time.Second, OK: false},
		{At: base.Add(2 * time.Minute), Kind: "stop", Detail: "", Elapsed: time.Millisecond, OK: true},
	}

	got := comoPanel(t, m, Model.timelineLines)
	iStop := strings.Index(got, "stop")
	iBuild := strings.Index(got, "build")
	iStart := strings.Index(got, "start")

	if iStop < 0 || iBuild < 0 || iStart < 0 {
		t.Fatalf("faltan eventos:\n%s", got)
	}
	if iStop >= iBuild || iBuild >= iStart {
		t.Errorf("el orden es cronológico, want el más reciente primero:\n%s", got)
	}
	// Y cada fila trae su marca de ok/fallo y su duración si la hubo.
	if !strings.Contains(got, "✓") || !strings.Contains(got, "✗") {
		t.Errorf("cada evento tiene que traer su marca de resultado:\n%s", got)
	}
	if !strings.Contains(got, "(2s)") {
		t.Errorf("un evento con duración la enseña:\n%s", got)
	}
	// El de duración cero NO enseña paréntesis vacío.
	if strings.Contains(got, "(0s)") {
		t.Errorf("un evento instantáneo no enseña duración:\n%s", got)
	}
}

// TestMetricsLinesMuestreaYAnotaElMomento: la tabla y su marca de tiempo.
//
// El "sampled at" importa: una métrica de hace un minuto es un dato distinto del
// de hace un segundo, y sin el sello el usuario compara la de ahora con la de hace
// medio minuto creyendo que bajó.
func TestMetricsLinesMuestreaYAnotaElMomento(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))

	ahora := time.Date(2026, 10, 3, 15, 4, 5, 0, time.Local)
	m.metrics[path] = &metricsView{CPU: 12.5, RSSKB: 1536, FDs: 42, Threads: 8, At: ahora}

	got := comoPanel(t, m, Model.metricsLines)
	for _, want := range []string{"CPU%", "RSS", "FDs", "Threads", "12.5%", "1.5 MB", "42", "8", "sampled at"} {
		if !strings.Contains(got, want) {
			t.Errorf("la tabla no trae %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, ahora.Format("15:04:05")) {
		t.Errorf("falta la hora del muestreo: %q", got)
	}
}

// TestProbeHealthTraeElCuerpoYElContentTypeDeVerdad: la sonda es HTTP de verdad,
// no un stub.
//
// Se levanta un httptest real porque lo que importa es que el panel muestre lo que
// el servicio responde de verdad: un Content-Type inventado y un snippet que no
// viene de la respuesta son los dos modos de fallo de una sonda.
func TestProbeHealthTraeElCuerpoYElContentTypeDeVerdad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	setManifestPort(&m, "tienda-api", serverPort(t, srv.URL))
	setHealthPath(&m, "tienda-api", "/api/health")

	// La sonda de verdad contra el servidor de verdad.
	p := m.projectByPath(path)
	res := probeHealth(healthURL(p, m.services[path]))
	if res.Err != "" {
		t.Fatalf("la sonda contra un servidor real falló: %v", res.Err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(res.ContentType, "json") {
		t.Errorf("content-type = %q, want el que devuelve el servidor", res.ContentType)
	}
	if !strings.Contains(res.Snippet, `"ok":true`) {
		t.Errorf("snippet = %q, want el cuerpo real", res.Snippet)
	}

	// Y el panel lo muestra todo.
	m.healthRes[path] = res
	got := comoPanel(t, m, Model.healthLines)
	for _, want := range []string{"HTTP 200", "json", `/api/health`, `"ok":true`} {
		if !strings.Contains(got, want) {
			t.Errorf("el panel no trae %q:\n%s", want, got)
		}
	}
}
