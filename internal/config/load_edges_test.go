package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/group"
	"vroom/internal/manifest"
	"vroom/internal/scanner"
)

// ---------------------------------------------------------------------------
// Defaults y bordes que no se ven en el uso diario.
//
// Lo que se reúne aquí son los caminos que sólo se alcanzan con una configuración
// rota de una forma concreta: un $VROOM_CONFIG que apunta a un directorio que no
// existe, un manifiesto nil, un TOML que parsea pero no valida.
//
// Y hay una razón de fondo para fijarlos. `Load()` es lo primero que hace el
// programa al arrancar, y su contrato es "arranca igual y avisa": un default mal
// puesto no puede dejar al usuario sin TUI, pero tampoco puede arrancar callado
// haciéndole creer que su configuración se aplica.
// ---------------------------------------------------------------------------

// TestLoadConUnConfigEnUnDirectorioInexistenteArrancaConDefaults: el Stat que
// falla.
//
// Es la diferencia entre "no hay fichero de configuración" y "hay algo donde debería
// haber un fichero". Un Stat de una ruta dentro de un directorio que no existe da
// NotExist, y el resultado tiene que ser defaults limpios —sin error— igual que un
// config ausente.
//
// Lo contrario sería peor: el usuario con un typo en $VROOM_CONFIG vería un aviso de
// "configuración inválida" sobre un fichero que no existe, y no sabría que el
// problema es el path.
func TestLoadConUnConfigEnUnDirectorioInexistenteArrancaConDefaults(t *testing.T) {
	noExiste := filepath.Join(t.TempDir(), "no-existe", "config.toml")
	t.Setenv("VROOM_CONFIG", noExiste)

	cfg := Load()
	if cfg.Err != nil {
		t.Errorf("un config bajo un directorio inexistente dio error %v: es el mismo caso que no tener config", cfg.Err)
	}
	if cfg.Ask.Launcher != "auto" {
		t.Errorf("Ask.Launcher = %q, want auto", cfg.Ask.Launcher)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("sin keybindings la TUI no tendría ninguna tecla asignada")
	}
}

// TestLoadConUnConfigQueEsUnDirectorioArrancaConDefaults: el otro NotExist.
//
// Un $VROOM_CONFIG que apunta a un DIRECTORIO es el error de configuración más
// probable después del typo: se escribe la ruta de la carpeta en vez de la del
// fichero. Stat tiene éxito —la carpeta existe— y el decode falla.
//
// Lo que importa aquí es que arranque IGUAL: defaults más el error. La alternativa,
// no arrancar, dejaría al usuario sin TUI por escribir una ruta mal.
func TestLoadConUnConfigQueEsUnDirectorioArrancaConDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VROOM_CONFIG", dir)

	cfg := Load()
	if cfg.Err == nil {
		t.Error("un config que es un directorio tiene que dar error: el usuario escribió la ruta de la carpeta")
	}
	// Pero con defaults aplicados, para que la TUI funcione igual.
	if cfg.Ask.Launcher != "auto" || cfg.Scanner.Depth != Defaults().Scanner.Depth {
		t.Errorf("con un config ilegible hay que arrancar con defaults, got %+v", cfg)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("sin keybindings la TUI no tendría ninguna tecla asignada")
	}
}

// TestLoadIgnoraLasClavesQueNoExistenEnElSchema: la tolerancia de lo desconocido.
//
// Un `keybindings` de una versión anterior de vroom, o una clave que alguien escribió
// por costumbre, no pueden romper el arranque: se ignoran y el resto se aplica. Es lo
// que permite que un config sobreviva a una versión que quitó un campo.
func TestLoadIgnoraLasClavesQueNoExistenEnElSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`
[ask]
launcher = "inline"
clave_del_futuro = "lo que sea"

[seccion_inventada]
lo_que_sea = 1

[scanner]
depth = 7
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", path)

	cfg := Load()
	if cfg.Err != nil {
		t.Errorf("una clave desconocida no puede invalidar el config: %v", cfg.Err)
	}
	if cfg.Ask.Launcher != "inline" {
		t.Errorf("Ask.Launcher = %q: las claves conocidas tienen que aplicarse aunque haya desconocidas", cfg.Ask.Launcher)
	}
	if cfg.Scanner.Depth != 7 {
		t.Errorf("Scanner.Depth = %d, want 7", cfg.Scanner.Depth)
	}
}

// TestSecondaryOfConManifiestoNilEsCadenaVacia: el borde de los accesores.
//
// SecondaryOf y PrimaryOf se llaman sobre CADA proyecto del escaneo, incluidos los
// directorios sin `.vroom.toml` que el escaneo mete en el árbol. Un nil dentro de uno
// de ellos apagaría la TUI en el primer escaneo de un workspace con una carpeta suelta.
func TestSecondaryOfConManifiestoNilEsCadenaVacia(t *testing.T) {
	// Sin manifiesto: cadena vacía, no panic.
	if got := group.SecondaryOf(scannerProject(nil)); got != "" {
		t.Errorf("SecondaryOf sin manifiesto = %q, want cadena vacía", got)
	}
	if got := group.PrimaryOf(scannerProject(nil)); got != "" {
		t.Errorf("PrimaryOf sin manifiesto = %q, want cadena vacía", got)
	}
	// Con manifiesto: lo que dice.
	if got := group.SecondaryOf(scannerProject(conGrupo("tienda", "backend"))); got != "backend" {
		t.Errorf("SecondaryOf = %q, want backend", got)
	}
	// Con manifiesto sin secundario: vacía, que es lo que significa "directo bajo el
	// primario".
	if got := group.SecondaryOf(scannerProject(conGrupo("tienda", ""))); got != "" {
		t.Errorf("SecondaryOf sin secundario = %q, want cadena vacía", got)
	}
	// Y con grupo vacío: también vacía, y eso NO es un grupo.
	for _, g := range []string{"", "tienda"} {
		if got := group.PrimaryOf(scannerProject(conGrupo(g, ""))); got != g {
			t.Errorf("PrimaryOf(%q) = %q", g, got)
		}
	}
}

// TestKeyByActionEsElInversoRealYNoPierdeLasTeclasPorDefecto: la precalculación.
//
// La TUI precalcula el mapa inverso al arrancar para que resolver una tecla sea
// O(1). Si el mapa se construyera sólo con los bindings del usuario, las teclas por
// defecto dejarían de funcionar en cuanto el usuario tocara cualquier tecla —porque
// validar un config siempre deja el mapa completo— y no habría forma de quejarte.
func TestKeyByActionEsElInversoRealYNoPierdeLasTeclasPorDefecto(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("[keybindings]\nrestart = \"R\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", path)

	cfg := Load()
	if cfg.Err != nil {
		t.Fatal(cfg.Err)
	}

	inv := cfg.KeyByAction()
	defaults := DefaultKeybindings()

	// La tecla del usuario está en el inverso.
	if inv["R"] != "restart" {
		t.Errorf("inv[R] = %q, want restart", inv["R"])
	}
	// Y las teclas por defecto también, porque el mapa del config es completo.
	for accion, tecla := range defaults {
		if accion == "restart" {
			continue
		}
		if inv[tecla] == "" {
			t.Errorf("la tecla %q de la acción %q no está en el inverso: al tocar un binding, "+
				"las demás acciones dejarían de funcionar", tecla, accion)
		}
	}
	// Y el tamaño es el de los defaults: ni más ni menos.
	if len(inv) != len(defaults) {
		t.Errorf("el inverso tiene %d entradas, want %d", len(inv), len(defaults))
	}
}

// TestLoadConUnAgentsSinCmdLoRechazaPeroArrancaConDefaults: la validación
// especular.
//
// Un agente sin `cmd` es un config mal escrito: si se aceptara, el picker de agentes
// lo ofrecería y fallaría al pulsarlo. Pero rechazarlo no puede impedir el arranque.
func TestLoadConUnAgentsSinCmdLoRechazaPeroArrancaConDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`
[ask]
launcher = "inline"

[ask.agents.mi-agente]
prompt = "sin cmd"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", path)

	cfg := Load()
	if cfg.Err == nil {
		t.Fatal("un agente sin cmd tiene que rechazarse: el picker lo ofrecería y fallaría al pulsarlo")
	}
	if !strings.Contains(cfg.Err.Error(), "cmd") {
		t.Errorf("err = %q, want que diga que falta el cmd", cfg.Err)
	}
	// Y con defaults aplicados.
	if len(cfg.Keybindings) == 0 {
		t.Error("un config inválido no puede dejar la TUI sin teclas")
	}
}

// scannerProject construye un proyecto con o sin manifiesto.
func scannerProject(m *manifest.Manifest) scanner.Project {
	return scanner.Project{Path: "/p", Name: "p", Manifest: m}
}

// conGrupo construye el manifiesto mínimo con los dos grupos.
func conGrupo(primary, secondary string) *manifest.Manifest {
	return &manifest.Manifest{
		Name: "p", Command: "./p", PrimaryGroup: primary, SecondaryGroup: secondary,
	}
}

// TestLoadSinHomeDevuelveElErrorYDefaults: el único fallo que puede IMPEDIR leer
// un config.
//
// MEDIDO: hasta ahora el camino estaba probado a través de `Path()`, que es donde se
// ve el error, pero nunca a través de `Load()`, que es quien decide qué hacer con él.
// Y lo que Load hace es devolver defaults CON el error: no hay ningún otro config que
// leer, así que la TUI tiene que arrancar igual.
//
// Sin este camino, un `vroom` arrancado desde un servicio con el entorno vacío
// arrancaría sin saberlo y el usuario vería la TUI con los bindings por defecto sin
// tener forma de saber que su configuración no se ha leído.
func TestLoadSinHomeDevuelveElErrorYDefaults(t *testing.T) {
	t.Setenv("VROOM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	cfg := Load()
	if cfg.Err == nil {
		t.Fatal("sin HOME no hay config que leer, y eso tiene que llegar al usuario")
	}
	if !strings.Contains(cfg.Err.Error(), "home") {
		t.Errorf("err = %q, want que diga que falta el home", cfg.Err)
	}
	// Y con defaults aplicados, porque la TUI tiene que funcionar igual.
	if cfg.Ask.Launcher != "auto" || cfg.Scanner.Depth != Defaults().Scanner.Depth {
		t.Errorf("sin config legible hay que arrancar con defaults, got %+v", cfg)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("sin keybindings la TUI no tendría ninguna tecla asignada")
	}
}
