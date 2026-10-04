package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/config"
)

// ---------------------------------------------------------------------------
// Resolve, Launch y parsePaneID: los bordes que no son "dentro de herdr".
//
// Resolve decide qué estrategia se usa, y su `warn` es lo único que le dice al
// usuario que su configuración explícita no se está respetando. Por eso el caso
// de "pide herdr y no hay herdr" importa más que el de éxito.
//
// parsePaneID extrae un id de la salida JSON de herdr, y esa salida es de un
// programa externo que no controla este repo. La función tiene que fallar en
// silencio —""— ante cualquier forma inesperada, porque el llamador ya comprueba
// el "" y da un error accionable.
// ---------------------------------------------------------------------------

// TestResolveConConfigExplicitoYSinSesionHerdrAvisa: el caso que produce el
// `warn`, y el aviso tiene que decir DOS cosas.
//
// Qué pasó (no hay sesión herdr) y qué se va a hacer (inline, en primer plano).
// Con sólo una de las dos el usuario tiene que adivinar si su configuración se
// ignora o si algo falló.
func TestResolveConConfigExplicitoYSinSesionHerdrAvisa(t *testing.T) {
	l := &Launcher{
		cfg:  config.AskConfig{Launcher: "herdr"},
		env:  func(string) string { return "" }, // sin HERDR_ENV
		look: func(string) (string, error) { return "/usr/bin/herdr", nil },
		run:  func(string, ...string) (string, error) { return "", nil },
	}

	strategy, warn := l.Resolve()
	if strategy != StrategyInline {
		t.Errorf("estrategia = %q, want %q: sin herdr el fallback es inline", strategy, StrategyInline)
	}
	if warn == "" {
		t.Fatal("sin herdr y con launcher=herdr explícito tiene que haber aviso: si no, el usuario no sabe que su config se ignora")
	}
	if !strings.Contains(warn, "herdr") {
		t.Errorf("el aviso no nombra herdr: %q", warn)
	}
	if !strings.Contains(warn, "inline") {
		t.Errorf("el aviso no dice qué se hace en su lugar: %q", warn)
	}
}

// TestResolveConHerdrExplicitoYSesionHerdrNoAvisa: el camino bueno del herdr
// explícito: estrategia herdr y NINGÚN aviso.
//
// El aviso sólo existe para el fallback. Si también saliera aquí, el usuario que
// pide herdr en una sesión herdr vería un aviso en cada pregunta que no dice
// absolutamente nada.
func TestResolveConHerdrExplicitoYSesionHerdrNoAvisa(t *testing.T) {
	l := &Launcher{
		cfg:  config.AskConfig{Launcher: "herdr"},
		env:  func(k string) string { return "1" },
		look: func(string) (string, error) { return "/usr/bin/herdr", nil },
		run:  func(string, ...string) (string, error) { return "", nil },
	}

	strategy, warn := l.Resolve()
	if strategy != StrategyHerdr {
		t.Errorf("estrategia = %q, want %q: con herdr explícito y sesión herdr se usa herdr", strategy, StrategyHerdr)
	}
	if warn != "" {
		t.Errorf("warn = %q: herdr disponible es exactamente lo pedido, no hay nada que avisar", warn)
	}
}

// TestResolveAutoSinSesionNoAvisa: en `auto` el fallback a inline NO es un aviso.
//
// La diferencia es deliberada: `auto` significa "usa lo que haya", así que inline
// es lo pedido. Avisar ahí sería ruido en cada pregunta de un usuario fuera de
// herdr, y teaches al usuario a ignorar los avisos.
func TestResolveAutoSinSesionNoAvisa(t *testing.T) {
	l := &Launcher{
		cfg:  config.AskConfig{Launcher: "auto"},
		env:  func(string) string { return "" },
		look: func(string) (string, error) { return "", os.ErrNotExist },
		run:  func(string, ...string) (string, error) { return "", nil },
	}

	strategy, warn := l.Resolve()
	if strategy != StrategyInline || warn != "" {
		t.Errorf("auto sin herdr dio %q/%q, want inline y sin aviso: en auto el fallback es lo pedido", strategy, warn)
	}
}

// TestResolveConSesionHerdrPeroSinBinarioCaeAInlineYAvisa: HERDR_ENV=1 sin el
// binario es el caso de " multiplexer dice que sí pero no está".
//
// Y en `auto` no avisa, por el mismo motivo que antes: la comprobación es doble a
// propósito (sesión Y binario) y el resultado es el mismo que sin sesión.
func TestResolveConSesionHerdrPeroSinBinario(t *testing.T) {
	newL := func(strategy string) *Launcher {
		return &Launcher{
			cfg: config.AskConfig{Launcher: strategy},
			env: func(k string) string {
				if k == "HERDR_ENV" {
					return "1"
				}
				return ""
			},
			look: func(string) (string, error) { return "", os.ErrNotExist },
			run:  func(string, ...string) (string, error) { return "", nil },
		}
	}

	if s, w := newL("auto").Resolve(); s != StrategyInline || w != "" {
		t.Errorf("auto con HERDR_ENV pero sin binario dio %q/%q, want inline sin aviso", s, w)
	}
	if s, w := newL("herdr").Resolve(); s != StrategyInline || w == "" {
		t.Errorf("herdr explícito con HERDR_ENV pero sin binario dio %q/%q, want inline con aviso", s, w)
	}
}

// TestResolveConEstrategiaDesconocidaCaeAAuto: un valor que no es ninguno de los
// tresKNOWN no es un error, es el default.
//
// Load ya valida el launcher, así que aquí sólo se fija que la función no
// entre en pánico y que el resultado sea el de `auto`, que es lo que haría
// alguien que escribiera el launcher a mano en un config viejo.
func TestResolveConEstrategiaDesconocidaCaeAAuto(t *testing.T) {
	l := &Launcher{
		cfg:  config.AskConfig{Launcher: "inventada"},
		env:  func(string) string { return "" },
		look: func(string) (string, error) { return "", os.ErrNotExist },
		run:  func(string, ...string) (string, error) { return "", nil },
	}
	if s, w := l.Resolve(); s != StrategyInline || w != "" {
		t.Errorf("una estrategia desconocida dio %q/%q, want el comportamiento de auto", s, w)
	}
}

// TestResolveConInlineYCustomNoPreguntaPorHerdr: inline y custom se devuelven tal
// cual, sin mirar el entorno.
//
// Es lo que hace que `custom` sirva de algo con una plantilla arbitraria: si
// preguntara por herdr, un launcher de shell que menciona "herdr" por casualidad
// dependería del multiplexer.
func TestResolveConInlineYCustomNoPreguntaPorHerdr(t *testing.T) {
	for _, want := range []string{StrategyInline, StrategyCustom} {
		consulted := false
		l := &Launcher{
			cfg:  config.AskConfig{Launcher: want},
			env:  func(string) string { consulted = true; return "1" },
			look: func(string) (string, error) { consulted = true; return "/usr/bin/herdr", nil },
			run:  func(string, ...string) (string, error) { return "", nil },
		}
		got, warn := l.Resolve()
		if got != want {
			t.Errorf("Resolve(%q) = %q, want %q", want, got, want)
		}
		if warn != "" {
			t.Errorf("Resolve(%q) avisó: %q", want, warn)
		}
		if consulted {
			t.Errorf("Resolve(%q) consultó el entorno: no debe, la estrategia está decidida", want)
		}
	}
}

// TestLaunchRechazaInlineYLoDesconocido: inline NO pasa por Launch.
//
// Es lo que impide que un bug en el camino inline lo mande por detrás: inline
// suspende la TUI con tea.ExecProcess y no tiene nada que despachar en segundo
// plano. Devolver un error explícito en vez de un no-op silencioso es lo que hace
// que el fallo se vea si alguien lo llama por error.
func TestLaunchRechazaInlineYLoDesconocido(t *testing.T) {
	l := New(config.AskConfig{})

	for _, strategy := range []string{StrategyInline, "inventada", ""} {
		out, err := l.Launch(strategy, req())
		if err == nil {
			t.Errorf("Launch(%q) = %q sin error: sólo herdr y custom son despachables en background", strategy, out)
		}
		if out != "" {
			t.Errorf("Launch(%q) devolvió %q además del error", strategy, out)
		}
	}
}

// TestLaunchPropagaElErrorDeHerdrConSuSalida: si herdr falla, el mensaje lleva
// su salida.
//
// La salida de herdr es lo que dice por qué falló, y sin ella el usuario tiene
// que reproducir la llamada a mano. Y el prefijo dice en qué paso falló
// (split/create/run), que es lo que determina qué arreglar.
func TestLaunchPropagaElErrorDeHerdrConSuSalida(t *testing.T) {
	tests := []struct {
		name       string
		target     string // pane o tab: decide qué comando se llama primero
		failOn     string // subcomando que falla
		wantPre    string
		wantSalida string
	}{
		{"pane split", "pane", "split", "herdr pane split", "no se puede dividir el pane"},
		{"tab create", "tab", "tab", "herdr tab create", "no se puede crear el tab"},
		{"pane run", "pane", "run", "herdr pane run", "el pane no acepta comandos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Launcher{
				cfg:  config.AskConfig{Target: tt.target, Direction: "right"},
				env:  func(string) string { return "1" },
				look: func(string) (string, error) { return "/usr/bin/herdr", nil },
				run: func(name string, args ...string) (string, error) {
					for _, a := range args {
						if a == tt.failOn {
							return tt.wantSalida, os.ErrNotExist
						}
					}
					if tt.target == "tab" {
						return `{"result":{"root_pane":{"pane_id":"w1:p3"}}}`, nil
					}
					return `{"result":{"pane":{"pane_id":"w1:p9"}}}`, nil
				},
			}

			_, err := l.Launch(StrategyHerdr, req())
			if err == nil {
				t.Fatal("un herdr que falla tiene que dar error")
			}
			if !strings.Contains(err.Error(), tt.wantPre) {
				t.Errorf("err = %q, want que diga en qué paso falló (%q)", err, tt.wantPre)
			}
			if !strings.Contains(err.Error(), tt.wantSalida) {
				t.Errorf("err = %q, want que incluya la salida de herdr (%q)", err, tt.wantSalida)
			}
		})
	}
}

// TestLaunchHerdrSinPaneIdDaErrorAccionable: herdr sale 0 pero sin pane id es un
// fallo, no un acierto.
//
// Es un caso real: una versión distinta de herdr, o un `--json` que cambia. Sin
// pane id no se puede lanzar el comando en ningún sitio, así que seguir como si
// nada dejaría al usuario con un agente que no arrancó y sin aviso.
func TestLaunchHerdrSinPaneIdDaErrorAccionable(t *testing.T) {
	l := &Launcher{
		cfg: config.AskConfig{Target: "pane", Direction: "right"},
		env: func(string) string { return "1" },
		look: func(string) (string, error) {
			return "/usr/bin/herdr", nil
		},
		// Sale 0 y con un JSON válido, pero sin pane_id donde se espera.
		run: func(string, ...string) (string, error) {
			return `{"result":{"pane":{"nombre":"sin-id"}}}`, nil
		},
	}

	out, err := l.Launch(StrategyHerdr, req())
	if err == nil {
		t.Fatalf("sin pane id Launch devolvió %q sin error", out)
	}
	if !strings.Contains(err.Error(), "pane id") {
		t.Errorf("err = %q, want que diga que no se pudo resolver el pane id", err)
	}
}

// TestParsePaneIDAguantaLasFormasQueNoSonLoEsperado: la salida de herdr es de
// un programa externo, así que el parser tiene que devolver "" ante cualquier
// rareza y dejar que el llamador avise.
//
// Cada caso es una forma real de fallo de parseo: JSON que no es un objeto, una ruta
// que no existe, un valor que no es string, un tipo en medio del camino.
func TestParsePaneIDAguentaLasFormasQueNoSonLoEsperado(t *testing.T) {
	tests := []struct {
		name string
		out  string
		path string
		want string
	}{
		{"normal pane", `{"result":{"pane":{"pane_id":"w1:p9"}}}`, "result.pane.pane_id", "w1:p9"},
		{"normal root_pane", `{"result":{"root_pane":{"pane_id":"w1:p3"}}}`, "result.root_pane.pane_id", "w1:p3"},
		{"clave simple", `{"pane_id":"x"}`, "pane_id", "x"},
		{"no es json", "esto no es json", "result.pane.pane_id", ""},
		{"json vacío", "", "result.pane.pane_id", ""},
		{"array", `["result"]`, "result", ""},
		{"ruta inexistente", `{"result":{}}`, "result.pane.pane_id", ""},
		{"clave con valor null", `{"result":{"pane":null}}`, "result.pane.pane_id", ""},
		{"tipo incorrecto en medio", `{"result":"texto"}`, "result.pane.pane_id", ""},
		{"valor no string", `{"result":{"pane":{"pane_id":9}}}`, "result.pane.pane_id", ""},
		{"json truncado", `{"result":{`, "result.pane.pane_id", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePaneID(tt.out, tt.path); got != tt.want {
				t.Errorf("parsePaneID(%q, %q) = %q, want %q", tt.out, tt.path, got, tt.want)
			}
		})
	}
}

// TestParsePaneIDConLaSalidaRealDeHerdrTab: el caso de tab, que usa una clave
// distinta del pane (root_pane) y podría haberse dejado sin probar.
func TestParsePaneIDConLaSalidaRealDeHerdrTab(t *testing.T) {
	l, log := newTestLauncher(t, config.AskConfig{Target: "tab"})
	if out, err := l.Launch(StrategyHerdr, req()); err != nil {
		t.Fatalf("Launch: %v", err)
	} else if !strings.Contains(out, "w1:p3") {
		t.Errorf("salida = %q: el tab tiene que reportar el root_pane, no un pane cualquiera", out)
	}

	calls := readCalls(t, log)
	if len(calls) != 2 {
		t.Fatalf("se llamaron %d veces a herdr, want 2 (create y run): %v", len(calls), calls)
	}
	if !strings.HasPrefix(calls[0], "tab create") {
		t.Errorf("la primera llamada = %q", calls[0])
	}
	if !strings.HasPrefix(calls[1], "pane run w1:p3") {
		t.Errorf("la segunda llamada = %q: el agente tiene que ir al pane que se acaba de crear", calls[1])
	}
}

// TestLaunchCustomExpandeLosPlaceholdersYLosProtege: {dir}, {agent} y {cmd} se
// sustituyen con el valor entrecomillado, y el script CORRE.
//
// Que estén entrecomillados es lo que hace que un directorio con espacios o un
// prompt con punto y coma no rompa la plantilla. Y {cmd} es el argv COMPLETO en
// una sola palabra, que es lo que permite pasárselo a otro comando.
//
// Se usa un `sh` de mentira en el PATH que graba sus argumentos: el objetivo es
// ver lo que LLEGA a la plantilla, no comprobar que sh funciona.
func TestLaunchCustomExpandeLosPlaceholdersYLosProtege(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "out.log")
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' \"$*\" > " + log + "\n"
	if err := os.WriteFile(filepath.Join(bin, "sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	l := &Launcher{
		cfg: config.AskConfig{
			Launcher:    "custom",
			LauncherCmd: "agente --dir {dir} --nombre {agent} -- {cmd}",
		},
		run: func(name string, args ...string) (string, error) {
			return runReal(name, args...)
		},
	}

	r := Request{
		Agent: "open code",
		Args:  []string{"opencode", "--prompt", "arregla el bug; con punto y coma"},
		Dir:   "/srv/mi proyecto",
	}
	out, err := l.Launch(StrategyCustom, r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "custom") {
		t.Errorf("salida = %q, want la confirmación de custom", out)
	}

	got := readFileString(t, log)
	// El dir y el agent llegan entrecomillados, con el espacio intacto.
	if !strings.Contains(got, "'/srv/mi proyecto'") {
		t.Errorf("el directorio no llegó entrecomillado:\n%s", got)
	}
	if !strings.Contains(got, "'open code'") {
		t.Errorf("el agente no llegó entrecomillado:\n%s", got)
	}
	// MEDIDO: {cmd} NO se convierte en una sola palabra — se expande token a
	// token, cada uno entrecomillado. Es lo que hace que el punto y coma del
	// prompt no se ejecute como comando: va dentro de sus comillas.
	if !strings.Contains(got, "'arregla el bug; con punto y coma'") {
		t.Errorf("el prompt no llegó entrecomillado: un ';' suelto ejecutaría el resto\n%s", got)
	}
	if !strings.Contains(got, "'opencode' '--prompt'") {
		t.Errorf("el argv debería llegar token a token entrecomillado:\n%s", got)
	}
}

// TestLaunchCustomPropagaElErrorDeLaPlantilla: si el script de la plantilla sale
// con error, el mensaje lo dice y nombra la plantilla.
//
// Es el mensaje que conecta "el agente no arrancó" con "tu launcher_cmd está
// mal", que es la otra mitad de la que el usuario necesita.
func TestLaunchCustomPropagaElErrorDeLaPlantilla(t *testing.T) {
	l := &Launcher{
		cfg: config.AskConfig{Launcher: "custom", LauncherCmd: "exit 3"},
		run: func(string, ...string) (string, error) {
			return "salida de error", os.ErrNotExist
		},
	}

	_, err := l.Launch(StrategyCustom, req())
	if err == nil {
		t.Fatal("una plantilla que falla tiene que dar error")
	}
	if !strings.Contains(err.Error(), "launcher_cmd") {
		t.Errorf("err = %q, want que nombre launcher_cmd: es lo que el usuario tiene que arreglar", err)
	}
	if !strings.Contains(err.Error(), "salida de error") {
		t.Errorf("err = %q, want que incluya la salida del comando", err)
	}
}

// TestInlineCmdApuntaAlDirectorioDelProyecto: la estrategia inline suspende la
// TUI y corre el agente en el directorio del proyecto.
//
// El cwd es lo que hace que un agente sin --cwd relativo funcione, y es lo que
// hace que "el agente trabaja en este proyecto" sea verdad sin que nadie lo
// escriba en el prompt.
func TestInlineCmdApuntaAlDirectorioDelProyecto(t *testing.T) {
	r := req()
	r.Dir = "/srv/proyecto"

	cmd := New(config.AskConfig{}).InlineCmd(r)
	if cmd.Dir != "/srv/proyecto" {
		t.Errorf("cmd.Dir = %q, want el directorio del proyecto", cmd.Dir)
	}
	// Y el argv es el de la request, sin el nombre del agente por delante: el
	// argv de la request YA incluye el binario.
	if got := strings.Join(cmd.Args, " "); got != "opencode --prompt fix the bug" {
		t.Errorf("cmd.Args = %q", got)
	}
}

// runReal ejecuta un comando de verdad y devuelve su salida combinada.
func runReal(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no se pudo leer %s: %v", path, err)
	}
	return string(data)
}
