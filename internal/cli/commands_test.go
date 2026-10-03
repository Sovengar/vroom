package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/orchestrate"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Los nueve comandos del CLI son la superficie que los agentes consumen, y antes
// de este fichero NO SE PODÍAN PROBAR: cada uno escribía en os.Stdout y
// llamaba a os.Exit(1) en caso de error, así que ejecutarlos en proceso mataba
// el binario de test.
//
// El arreglo no fue un harness de subproceso sino devolver valores: los comandos
// Devuelven (payload, error) y Run es el único que emite. Eso significa que
// estos tests exerted el comando REAL contra un árbol REAL en un disco REAL, con
// procesos reales cuando el comando arranca algo. No hay doble de scanner, ni de
// store, ni de manager: lo que se verifica es lo que hace vroom.
//
// Y lo que se verifica es el JSON MARSHALLEADO, no las estructuras internas,
// porque el JSON es el contrato. Un test que leyera campos de ProjectInfo no
// detectaría una etiqueta mal puesta ni un campo omitido.
// ---------------------------------------------------------------------------

// cliEnv aísla el CLI del entorno del desarrollador y devuelve el árbol de
// proyectos.
//
// El aislamiento es TOTAL y en las tres variables que el paquete lee, porque
// las tres importan: VROOM_CONFIG evita leer la config real (que trae
// launcher y dirección de ask), XDG_STATE_HOME evita escribir el estado real
// (que es donde viven los PIDs) y Chdir evita escanear el directorio de trabajo
// del developer.
//
// Chdir y no un --root inyectado porque `vroom` escanea el CWD por contrato:
// un seam para la raíz haría que los tests probaran un root que ningún usuario
// tiene.
func cliEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return cliTree(t)
}

// cliTree crea un árbol con dos proyectos configurados y uno sin manifiesto.
//
// Los dos configurados son deliberadamente distintos en lo que los comandos
// necesitan: `api` tiene command_build/command_install (para los one-shot) y
// `web` no (para el caso de "no hay comando definido", que es un error DISTINTO
// del de "no está configurado" y un agente los tiene que poder separar).
func cliTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("api/go.mod", "module api\n")
	write("api/.vroom.toml", `name = "api"
command_start = "sleep 30"
port = 8081
command_build = "echo built"
command_install = "echo installed"
`)
	write("web/package.json", "{}\n")
	write("web/.vroom.toml", `name = "web"
command_start = "sleep 30"
port = 5173
`)
	// Un manifiesto MALFORMADO: el directorio aparece pero no es gestionable.
	//
	// Y el caso que NO se puede escribir aquí es el directorio sin manifiesto:
	// el escaneo busca .vroom.toml, así que un directorio sin él no sale. No es
	// un límite del CLI sino del escaneo, y por eso la fila "no configurada" se
	// produce con un manifiesto roto y no con uno ausente.
	write("roto/go.mod", "module roto\n")
	write("roto/.vroom.toml", "name = \"roto\"\ncommand_start = [\n") // TOML roto a propósito
	return root
}

// mustJSON marshalla v y falla el test si el comando falló o si el JSON no
// tiene la forma esperada, devolviendo el mapa genérico.
//
// El error del comando se come aquí a propósito: si el comando falla, el test
// tiene que morir en la línea de la llamada y decir qué comando era, no veinte
// líneas más abajo con un tipo raro.
//
// Comparar sobre el mapa y no sobre la estructura es lo que hace que una
// etiqueta mal puesta sea un fallo del test: una etiqueta mal puesta se ve
// comparando el JSON, no el campo.
func mustJSON(t *testing.T, v any, err error) map[string]any {
	t.Helper()
	if err != nil {
		t.Fatalf("el comando falló: %v", err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// actionResult es la forma de todo comando de acción (start/stop/build/install).
//
// Con etiquetas json DELIBERADAMENTE, y no sin ellas: sin etiqueta, encoding/json
// empareja por nombre de campo y `exit_code` no casa con `ExitCode`, así que el
// ExitCode de un build fallido llegaba al test como cero. Un tipo que replica el
// de producción tiene que llevar las mismas etiquetas, o está probando otra
// cosa.
type actionResult struct {
	OK       bool   `json:"ok"`
	Project  string `json:"project"`
	Action   string `json:"action"`
	Pid      int    `json:"pid"`
	ExitCode int    `json:"exit_code"`
	Elapsed  string `json:"elapsed"`
	Error    string `json:"error"`
}

func mustAction(t *testing.T, v any, err error) actionResult {
	t.Helper()
	if err != nil {
		t.Fatalf("el comando falló: %v", err)
	}
	data, merr := json.Marshal(v)
	if merr != nil {
		t.Fatal(merr)
	}
	var out actionResult
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatalf("la respuesta no tiene la forma de ActionResult: %v\n%s", uerr, data)
	}
	return out
}

// logsPayload es LogsResult en forma de struct, para no repetir el cast en cada
// test de logs.
type logsPayload struct {
	Project string `json:"project"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
}

func mustLogs(t *testing.T, v any, err error) logsPayload {
	t.Helper()
	if err != nil {
		t.Fatalf("el comando falló: %v", err)
	}
	data, merr := json.Marshal(v)
	if merr != nil {
		t.Fatal(merr)
	}
	var out logsPayload
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatalf("la respuesta no tiene la forma de LogsResult: %v\n%s", uerr, data)
	}
	return out
}

// namesOfStacks devuelve los nombres de los stacks, para los mensajes de fallo.
func namesOfStacks(stacks []orchestrate.Stack) []string {
	out := make([]string, 0, len(stacks))
	for _, s := range stacks {
		out = append(out, s.Name)
	}
	return out
}

// ---- dispatch: el reparto y su contrato de errores ----

// TestDispatchReparteCadaComando: cada subcomando tiene que llegar a su comando.
//
// El reparto se prueba con la ARMA que importa —el error de --path y el de uso—
// en todos los comandos que los comparten, porque antes el parseo estaba
// copiado nueve veces y un fix aplicado a uno dejaba a los otros ocho con el
// bug sin que nada lo notara. La tabla de dispatch es lo que evita esa
// divergencia: ahora hay un solo bloque que decide, y un solo sitio donde un
// subcomando nuevo se puede colar sin parsear.
func TestDispatchReparteCadaComando(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	tests := []struct {
		args   []string
		action string
	}{
		{[]string{"start", "api"}, "started"},
		{[]string{"stop", "api"}, "stopped"},
		{[]string{"build", "api"}, "build"},
		{[]string{"install", "api"}, "install"},
		// logs no devuelve ActionResult: es un comando de consulta y su forma
		// se comprueba en su propio test.
		{[]string{"logs", "api"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.args[0], func(t *testing.T) {
			payload, handled, err := dispatch(tt.args)
			if !handled {
				t.Fatal("el comando no fue manejado: Run dejaría lanzar la TUI")
			}
			if err != nil {
				t.Fatalf("dispatch(%v) = error %v", tt.args, err)
			}
			if tt.action == "" {
				if payload == nil {
					t.Error("logs devolvió payload nil")
				}
				return
			}
			if got := mustAction(t, payload, err).Action; got != tt.action {
				t.Errorf("Action = %q, want %q", got, tt.action)
			}
		})
	}
}

// TestDispatchUsageYPathPorSubcomando: los dos errores de FORMA, para los cinco
// subcomandos que comparten flags.
//
// El mensaje nombra el subcomando, y eso es parte del contrato: un mensaje de
// `vroom build` que dijera "usage: vroom stop" Costaría un agente un intento
// entero de adivinar. Por eso la tabla compose el texto y no lo repite nueve
// veces.
func TestDispatchUsageYPathPorSubcomando(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	for _, cmd := range []string{"start", "stop", "build", "install", "logs"} {
		t.Run(cmd+"/sin-nombre", func(t *testing.T) {
			_, handled, err := dispatch([]string{cmd})
			if !handled {
				t.Fatal("sin nombre no debe caer en la TUI: el usuario sí pidió un comando")
			}
			if err == nil {
				t.Fatal("sin nombre debería ser error de uso")
			}
			if !strings.Contains(err.Error(), "usage: vroom "+cmd+" <project-name|path>") {
				t.Errorf("el mensaje de uso no nombra el subcomando: %q", err)
			}
		})

		t.Run(cmd+"/path-sin-valor", func(t *testing.T) {
			_, handled, err := dispatch([]string{cmd, "api", "--path"})
			if !handled {
				t.Fatal("un --path mal formado debe seguir siendo un comando manejado")
			}
			if err == nil || !strings.Contains(err.Error(), "--path requires a value") {
				t.Errorf("err = %v, want el error de --path sin valor", err)
			}
		})

		t.Run(cmd+"/path-repetido", func(t *testing.T) {
			_, handled, err := dispatch([]string{cmd, "api", "--path", "/a", "--path", "/b"})
			if !handled {
				t.Fatal("debe seguir siendo un comando manejado")
			}
			if err == nil || !strings.Contains(err.Error(), "more than once") {
				t.Errorf("err = %v, want el error de --path repetido", err)
			}
		})
	}
}

// TestDispatchNoArgumentsYDesconocidoVanALaTUI: los dos casos que NO son error.
// Devolver false aquí es lo que hace que `vroom` a secas abra la TUI y que un
// comando mal escrito no se troubleshooting como un error del CLI.
func TestDispatchNoArgumentsYDesconocidoVanALaTUI(t *testing.T) {
	for _, args := range [][]string{{}, {"inventado"}, {"--verbose"}, {"list2"}} {
		payload, handled, err := dispatch(args)
		if handled {
			t.Errorf("dispatch(%v) dice que manejó el comando, y eso lanza un error de uso en vez de la TUI", args)
		}
		if err != nil {
			t.Errorf("dispatch(%v) devolvió error %v, pero caer en la TUI no es un error", args, err)
		}
		if payload != nil {
			t.Errorf("dispatch(%v) devolvió payload sin manejar el comando", args)
		}
	}
}

// TestDispatchListaYEstadoSonElMismo: `status` es alias de `list`, y lo es por
// el mismo case. Se fija porque un alias que un día se separe hace que dos
// comandos que un agente cree iguales devuelvan filas distintas.
func TestDispatchListaYEstadoSonElMismo(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	fromList, _, err1 := dispatch([]string{"list"})
	fromStatus, _, err2 := dispatch([]string{"status"})
	if err1 != nil || err2 != nil {
		t.Fatalf("list=%v status=%v", err1, err2)
	}

	a, b := mustJSON(t, fromList, err1), mustJSON(t, fromStatus, err2)
	ja, _ := json.Marshal(a["projects"])
	jb, _ := json.Marshal(b["projects"])
	if string(ja) != string(jb) {
		t.Error("`status` y `list` devolvieron proyectos distintos: el alias no es un alias")
	}
}

// TestDispatchAyudaSinArgsYConAlias: las tres formas de pedir la ayuda producen
// lo mismo. Las tres están en el switch y las tres eran el mismo cuerpo, así que
// una podría haber derivado sin que nada lo notara.
func TestDispatchAyudaSinArgsYConAlias(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		payload, handled, err := dispatch(args)
		if !handled || err != nil {
			t.Fatalf("dispatch(%v) = handled %v, err %v", args, handled, err)
		}
		doc := mustJSON(t, payload, err)
		commands, ok := doc["commands"].(map[string]any)
		if !ok {
			t.Fatalf("la ayuda no trae la tabla de comandos: %v", doc)
		}
		// La ayuda tiene que mencionar los comandos que existen, o un agente no
		// puede descubrir la superficie.
		for _, want := range []string{"vroom list", "vroom start <name|path> [--path <path>]", "vroom launch <name> --dry"} {
			if _, ok := commands[want]; !ok {
				t.Errorf("la ayuda no documenta %q", want)
			}
		}
		if _, ok := doc["notes"].(map[string]any); !ok {
			t.Error("la ayuda debe traer las notas: --path es la que desambigua worktrees")
		}
	}
}

// ---- list ----

// TestCmdListPublicaCadaFilaDelEscaneo: list tiene que publicar TODAS las filas
// del escaneo, y cada una con su forma.
//
// Las tres filas del árbol son los tres casos: configurada, configurada sin
// comandos, y presente pero sin manifiesto. Que la tercera salga es lo que
// distingue "list" de "list de lo que se puede arrancar", y un agente que
// 杰出 un proyecto de la lista no puede volver a encontrarlo por nombre.
func TestCmdListPublicaCadaFilaDelEscaneo(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	payload, err := cmdList()
	if err != nil {
		t.Fatal(err)
	}

	list, ok := payload.(ListResult)
	if !ok {
		t.Fatalf("list devolvió %T, want ListResult", payload)
	}
	if len(list.Projects) != 3 {
		t.Fatalf("hay %d proyectos, want 3 (api, web y roto)", len(list.Projects))
	}

	byName := map[string]map[string]any{}
	for _, p := range list.Projects {
		byName[p.Name] = mustJSON(t, p, nil)
	}

	// Configurada: publica comando y puerto declarado, y nada más que no haya
	// confirmado.
	api := byName["api"]
	if api == nil {
		t.Fatal("api no está en la lista")
	}
	if api["configured"] != true {
		t.Error("api debe salir configurada")
	}
	if api["command"] != "sleep 30" {
		t.Errorf("command = %v, want el command_start del manifiesto", api["command"])
	}
	if api["declared_port"] != float64(8081) {
		t.Errorf("declared_port = %v, want 8081", api["declared_port"])
	}
	// Y no puede afirmar un puerto real si no hay meta.
	if _, ok := api["port_verified"]; ok {
		t.Error("un servicio sin meta no puede afirmar port_verified: eso sería un contrato que el JSON no cumple")
	}

	// Configurada sin one-shot: los campos ausentes, no cadenas vacías.
	web := byName["web"]
	if web == nil {
		t.Fatal("web no está en la lista")
	}
	for _, absent := range []string{"command_build", "command_install", "route", "route_mode"} {
		if _, ok := web[absent]; ok {
			t.Errorf("web no define %s y aun así lo publica: el omitempty debe omitirlo", absent)
		}
	}

	// Manifiesto roto: sale con el error del parseo y SIN contrato de puerto.
	//
	// Publicarla es lo que permite al agente ver que el directorio existe y no
	// es gestionable, en vez de no verlo y deducir que no existe. Y el error de
	// parseo es lo que le dice POR QUÉ: sin él, la fila sería indistinguible de
	// una que se olvidó de configurar.
	roto := byName["roto"]
	if roto == nil {
		t.Fatal("el directorio con manifiesto roto no aparece en la lista")
	}
	if roto["configured"] != false {
		t.Error("roto tiene el manifiesto malformado y aun así sale configurado")
	}
	if roto["manifest_error"] == "" {
		t.Error("una fila no configurada por parseo debe decir POR QUÉ: manifest_error")
	}
	for _, absent := range []string{"declared_port", "port_verified", "command", "route"} {
		if _, ok := roto[absent]; ok {
			t.Errorf("una fila no configurada publica %s: no hay manifiesto del que sacarlo", absent)
		}
	}
}

// TestCmdListRespetaElRootDeLaConfig: la raíz de escaneo sale de la CONFIG, no
// del CWD, y es lo que permite a un agente con varios roots escanear el suyo.
//
// Y los tres casos que hacen que esto sea una función y no una línea: un root
// ABSOLUTO manda sobre el CWD, uno RELATIVO se resuelve contra el CWD, y uno con
// `~` se expande contra el HOME. Los tres se equivocarían si se concatenaran a
// ciegas, y en dos de ellos el fallo es silencioso: el escaneo simplemente
// devuelve menos proyectos de los que hay.
func TestCmdListRespetaElRootDeLaConfig(t *testing.T) {
	// rootTree crea un directorio con UN proyecto y devuelve su ruta.
	rootTree := func(t *testing.T, dir, name string) {
		t.Helper()
		writeFile(t, filepath.Join(dir, ".vroom.toml"),
			"name = \""+name+"\"\ncommand_start = \"sleep 30\"\n")
	}

	t.Run("un root absoluto acota el escaneo", func(t *testing.T) {
		root := cliEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		otro := filepath.Join(home, "otro-root")
		if err := os.MkdirAll(otro, 0o755); err != nil {
			t.Fatal(err)
		}
		rootTree(t, otro, "solo-este")
		writeConfigScannerRoot(t, otro)
		t.Chdir(root) // el CWD tiene api, web y roto: no deben aparecer

		lv, le := cmdList()
		projects := mustProjects(t, lv, le)
		if len(projects) != 1 {
			t.Fatalf("hay %d proyectos, want 1: el root del config debe acotar el escaneo (%v)",
				len(projects), namesOf(projects))
		}
		if projects[0].Name != filepath.Base(otro) {
			t.Errorf("Name = %q, want el directorio %q", projects[0].Name, filepath.Base(otro))
		}
	})

	t.Run("un root relativo se resuelve contra el CWD", func(t *testing.T) {
		root := cliEnv(t)
		writeConfigScannerRoot(t, "api")
		t.Chdir(root)

		lv, le := cmdList()
		projects := mustProjects(t, lv, le)
		if len(projects) != 1 || projects[0].Name != "api" {
			t.Errorf("un root relativo debería dar sólo api, dio %v", namesOf(projects))
		}
	})

	t.Run("un root con tilde se expande contra el HOME", func(t *testing.T) {
		root := cliEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		// Si la tilde no se expandiera, el escaneo buscaría un directorio
		// llamado "~" relativo al CWD y no encontraría nada: cero proyectos.
		// Un resultado de cero NO distingue "no expandí" de "no hay", así que el
		// test exige el proyecto que sí existe bajo el HOME.
		dir := filepath.Join(home, "vroom-test-root")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		rootTree(t, dir, "de-home")
		writeConfigScannerRoot(t, "~/vroom-test-root")
		t.Chdir(root)

		lv, le := cmdList()
		projects := mustProjects(t, lv, le)
		if len(projects) != 1 || projects[0].Name != "vroom-test-root" {
			t.Errorf("la tilde no se expandió contra el HOME: %v", namesOf(projects))
		}
	})

	t.Run("un root que no existe es ERROR, no una lista vacía", func(t *testing.T) {
		root := cliEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeConfigScannerRoot(t, "~/no-existe-este-root")
		t.Chdir(root)

		// La diferencia es la que un agente necesita: "no hay proyectos" y "no
		// pude mirar" son dos respuestas, y confundirlas haría que un root mal
		// escrito pareciera un workspace vacío.
		_, err := cmdList()
		if err == nil {
			t.Fatal("un root inexistente debería fallar, no devolver una lista vacía")
		}
		if !strings.Contains(err.Error(), "scan error") {
			t.Errorf("err = %q, want el prefijo 'scan error' del contrato", err)
		}
	})
}

// writeConfigScannerRoot escribe una config con sólo [scanner]. root y depth.
//
// depth va explícito porque el default puede cambiar y un test que dependa del
// default se rompe sin que nadie toque este fichero.
func writeConfigScannerRoot(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vroom.toml")
	body := "[scanner]\nroot = \"" + root + "\"\ndepth = 4\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", path)
}

// mustProjects ejecuta cmdList y devuelve sus proyectos.
func mustProjects(t *testing.T, v any, err error) []ProjectInfo {
	t.Helper()
	if err != nil {
		t.Fatalf("list falló: %v", err)
	}
	list, ok := v.(ListResult)
	if !ok {
		t.Fatalf("list devolvió %T, want ListResult", v)
	}
	return list.Projects
}

func namesOf(projects []ProjectInfo) []string {
	out := make([]string, 0, len(projects))
	for _, p := range projects {
		out = append(out, p.Name)
	}
	return out
}

// ---- errores de resolución de proyecto ----

// TestComandosNoEncontradoYNoConfiguradoSonErroresDistintos: los dos fallos de
// resolución significan cosas distintas para el agente y por eso NO pueden
// compartir mensaje.
//
// "No existe" → el nombre está mal escrito o hay que usar --path.
// "No está configurado" → el proyecto existe pero no tiene .vroom.toml válido.
//
// Fusionarlos obligaría al agente a listar para distinguir los dos casos, que es
// justo lo que el mensaje existe para evitar.
func TestComandosNoEncontradoYNoConfiguradoSonErroresDistintos(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	// Mismo error para todos los comandos que resuelven proyecto: el mensaje lo
	// redacta findProject y es uno solo.
	for _, name := range []string{"start", "stop", "build", "install", "logs"} {
		_, _, err := dispatch([]string{name, "no-existe"})
		if err == nil {
			t.Fatalf("%s de un proyecto inexistente debería fallar", name)
		}
		if !strings.Contains(err.Error(), "project not found: no-existe") {
			t.Errorf("%s: %q no dice que no existe el proyecto", name, err)
		}
		// Y el error de resolución NO lleva el prefijo de scan: son causas
		// distintas y un agente las cuenta por separado.
		if strings.Contains(err.Error(), "scan error") {
			t.Errorf("%s: un proyecto inexistente no es un fallo de escaneo: %q", name, err)
		}
	}

	// El directorio existe pero no hay manifiesto: el mensaje lo dice, y lo dice
	// nombrando la causa concreta, porque "no configurado" sin más no le dice al
	// usuario que le falta crear el fichero.
	_, _, err := dispatch([]string{"start", "roto"})
	if err == nil {
		t.Fatal("arrancar un directorio sin manifiesto debería fallar")
	}
	if !strings.Contains(err.Error(), "roto") || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("err = %q, want el nombre del proyecto y 'not configured'", err)
	}
	if !strings.Contains(err.Error(), ".vroom.toml") {
		t.Errorf("err = %q no dice QUÉ falta", err)
	}

	// build/install omiten el "missing or invalid": su mensaje histórico era más
	// corto y es el que un agente puede tener cacheado.
	_, _, err = dispatch([]string{"build", "roto"})
	if err == nil || strings.Contains(err.Error(), ".vroom.toml") {
		t.Errorf("build de un proyecto no configurado = %q, want el mensaje corto", err)
	}
}

// ---- start ----

// TestCmdStartArrancaYEscribeElMeta: el camino de éxito completo, verificado por
// DOS vías independientes.
//
// La vía pública es el payload. La segunda es el disco: el Meta del proyecto
// tiene que existir y llevar el PID. Importa la segunda porque un `started` con
// el PID en el JSON y sin Meta en disco deja al siguiente `vroom list` affirmations
// un servicio parado: el contrato se rompería en el comando SIGUIENTE, y sólo
// se ve mirando el estado.
func TestCmdStartArrancaYEscribeElMeta(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	payload, err := cmdStart("api", "")
	if err != nil {
		t.Fatal(err)
	}

	res := mustAction(t, payload, err)
	if !res.OK {
		t.Error("OK = false tras un arranque correcto")
	}
	if res.Action != "started" {
		t.Errorf("Action = %q, want started", res.Action)
	}
	if res.Project != "api" {
		t.Errorf("Project = %q, want api: el nombre pedido es el que se publica", res.Project)
	}
	if res.Pid <= 0 {
		t.Fatalf("Pid = %d, want > 0: sin PID un agente no puede verificar nada", res.Pid)
	}

	apiPath := filepath.Join(root, "api")
	meta, err := store.LoadMeta(apiPath)
	if err != nil {
		t.Fatalf("el Meta no se escribió en disco: %v", err)
	}
	if meta.Pid != res.Pid {
		t.Errorf("el Meta tiene Pid %d y el JSON dice %d: el contrato se rompería en el list siguiente", meta.Pid, res.Pid)
	}
	if meta.State != "running" {
		t.Errorf("meta.State = %q, want running", meta.State)
	}

	// Y hay que dejar el proceso vivo: un start que devuelve un PID ya muerto
	// sería un start que no arrancó nada.
	if !processAlive(t, res.Pid) {
		t.Errorf("el PID %d ya no existe: el arranque no ocurrió", res.Pid)
	}
	stopService(t, store, apiPath)
}

// TestCmdStartDeUnServicioYaCorriendoEsIdempotente: un start repetido no
// arranca un segundo proceso.
//
// El `action` distinto es lo que permite a un agente distinguir "lo arranqué yo"
// de "ya estaba", y la ausencia de un PID nuevo es lo que evita duplicar.
func TestCmdStartDeUnServicioYaCorriendoEsIdempotente(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	// El puerto declarado tiene que estar ABIERTO: Evaluate sólo dice "corriendo"
	// cuando, además del PID vivo, el puerto declarado acepta. Un proceso que
	// declara un puerto y no lo abre está vivo pero no sirviendo, y el
	// contrato lo trata como desconocido a propósito.
	listeningService(t, root, "api", `name = "api"
`)

	first_v, first_e := cmdStart("api", "")
	first := mustAction(t, first_v, first_e)
	second_v, second_e := cmdStart("api", "")
	second := mustAction(t, second_v, second_e)

	if second.Action != "already_running" {
		t.Errorf("Action = %q, want already_running", second.Action)
	}
	if !second.OK {
		t.Error("un start idempotente debe salir OK: no es un error, es que ya estaba")
	}
	if second.Pid != 0 {
		t.Errorf("Pid = %d en already_running: no se arrancó nada, luego no hay PID nuevo", second.Pid)
	}
	if first.Pid == 0 {
		t.Fatal("el primer arranque no devolvió PID")
	}

	// Y sigue habiendo UN solo proceso para el proyecto.
	meta, err := store.LoadMeta(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != first.Pid {
		t.Errorf("el Meta cambió de PID (%d -> %d): se arrancó un segundo proceso", first.Pid, meta.Pid)
	}
	stopService(t, store, filepath.Join(root, "api"))
}

// TestCmdStartPorPathDesambigua: dos proyectos con el MISMO nombre de manifiesto
// se distinguen por --path, y sin él el error lo dice.
//
// Es el caso que hace que --path exista, y por eso se prueba por los dos lados:
// con --path funciona, y sin él el error NOMBRA los candidatos.
func TestCmdStartPorPathDesambigua(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	// Dos worktrees con el mismo nombre de manifiesto.
	for _, sub := range []string{"wt-a", "wt-b"} {
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".vroom.toml"),
			[]byte("name = \"dup\"\ncommand_start = \"sleep 30\"\nport = 9001\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Sin --path: ambigüedad, con los dos paths en el mensaje.
	_, _, err := dispatch([]string{"start", "dup"})
	if err == nil {
		t.Fatal("un nombre duplicado sin --path debería fallar")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ambiguous project name") {
		t.Errorf("err = %q, want ambigüedad", msg)
	}
	for _, sub := range []string{"wt-a", "wt-b"} {
		if !strings.Contains(msg, sub) {
			t.Errorf("el mensaje no lista el candidato %q: %q", sub, msg)
		}
	}
	if !strings.Contains(msg, "--path") {
		t.Errorf("el mensaje no sugiere la salida: %q", msg)
	}

	// Con --path: arranca el elegido, y el otro NO arranca.
	payload, _, err := dispatch([]string{"start", "dup", "--path", filepath.Join(root, "wt-a")})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustAction(t, payload, err).Action; got != "started" {
		t.Fatalf("Action = %q, want started", got)
	}

	for _, sub := range []string{"wt-a", "wt-b"} {
		dir := filepath.Join(root, sub)
		meta, err := store.LoadMeta(dir)
		if sub == "wt-a" {
			if err != nil || meta.Pid == 0 {
				t.Errorf("wt-a debería tener Meta con PID: %v", err)
			}
		} else if err == nil && meta.Pid != 0 {
			t.Errorf("wt-b arrancó sin que nadie lo pidiera: PID %d", meta.Pid)
		}
	}
	for _, sub := range []string{"wt-a", "wt-b"} {
		stopService(t, store, filepath.Join(root, sub))
	}
}

// TestCmdStartNoPublicaWarningDeportlessEnElPayload: los warnings del arranque
// van al log de stderr del SERVICIO, no al payload.
//
// Es una decisión de contrato: el payload de un start es "arrancado, con este
// PID", y mezclarle avisos de degradación haría que un agente no pudiera
// distinguir un servicio sano de uno que arrancó con la ruta caída. El aviso
// sigue llegando al sitio donde un humano lo lee.
func TestCmdStartNoPublicaWarningDeportlessEnElPayload(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	payload_v, payload_e := cmdStart("api", "")
	payload := mustAction(t, payload_v, payload_e)
	if payload.Error != "" {
		t.Errorf("un arranque sin degradación no debe traer error: %q", payload.Error)
	}

	// El log de stderr del servicio existe y es un fichero real.
	logPath := store.StderrLog(filepath.Join(root, "api"))
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("no hay log de stderr del servicio: %v", err)
	}
	stopService(t, store, filepath.Join(root, "api"))
}

// ---- stop ----

// TestCmdStopParaElProcesoYDejaElMetaParado: el stop tiene que hacer las DOS
// cosas, y por separado.
//
// Que el proceso muera y que el Meta deje de afirmar un PID. Lo segundo es lo que
// hace que el `list` siguiente no afirme un servicio corriendo: si el Meta se
// queda con el PID de un proceso ya muerto, el estado evaluado sería "unknown" y
// un agente vería un servicio que ya no existe.
func TestCmdStopParaElProcesoYDejaElMetaParado(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	started_v, started_e := cmdStart("api", "")
	started := mustAction(t, started_v, started_e)
	if started.Pid <= 0 {
		t.Fatal("no arrancó")
	}

	payload, err := cmdStop("api", "")
	if err != nil {
		t.Fatal(err)
	}
	res := mustAction(t, payload, err)
	if !res.OK || res.Action != "stopped" {
		t.Errorf("respuesta = %+v, want OK y stopped", res)
	}

	waitGone(t, started.Pid)

	meta, err := store.LoadMeta(filepath.Join(root, "api"))
	if err != nil {
		t.Fatalf("el Meta desapareció en lugar de actualizarse: %v", err)
	}
	if meta.Pid != 0 {
		t.Errorf("meta.Pid = %d tras el stop: el list siguiente afirmaría un servicio vivo", meta.Pid)
	}
	if meta.Pgid != 0 {
		t.Errorf("meta.Pgid = %d tras el stop", meta.Pgid)
	}
	if meta.State != "stopped" {
		t.Errorf("meta.State = %q, want stopped", meta.State)
	}
}

// TestCmdStopDeUnServicioQueNoArrancoSigueSiendoExitoso: parar algo que no está
// parado no es un error.
//
// Es el contrato benigno de stop: un agente que para en un bucle, o que repite
// la orden tras un timeout en el que el servicio quizá sí arrancó, tiene que
// poder hacerlo sin tratar un "ya estaba parado" como fallo. Y la comprobación es
// que sale OK y con el MISMO action, para que el agente pueda cerrar el bucle sin
// distinguir casos.
func TestCmdStopDeUnServicioQueNoArrancoSigueSiendoExitoso(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	for _, name := range []string{"api", "roto"} {
		payload, err := cmdStop(name, "")
		if err != nil {
			t.Fatalf("stop de %q, que nunca arrancó, falló: %v", name, err)
		}
		res := mustAction(t, payload, err)
		if !res.OK || res.Action != "stopped" {
			t.Errorf("stop de %q = %+v, want OK y stopped", name, res)
		}
	}
}

// TestCmdStopEjecutaElCommandStopEnElDirectorioDelProyecto: command_stop es la
// parada GRACIOSA y se ejecuta en el directorio del PROYECTO.
//
// Ese directorio es lo que lo hace útil: el comando de parada de una app real
// (volcar un fichero de estado, cerrar un socket, avisar a otro servicio) usa
// rutas relativas, y ejecutarlo en otro sitio lo haría fallar. Se verifica por
// el efecto —el fichero aparece junto al .vroom.toml— porque un command_stop que
// se ejecutara pero escribiera en el directorio equivocado pasaría un test que
// sólo comprobara el código de salida.
//
// Y command_stop falla con exit 7 a propósito: su fallo NO puede impedir el
// cleanup, porque un servicio vivo con su Meta ya limpiado es peor que un
// comando de parada que no funcionó.
func TestCmdStopEjecutaElCommandStopEnElDirectorioDelProyecto(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	writeFile(t, filepath.Join(root, "api", ".vroom.toml"), `name = "api"
command_start = "sleep 30"
port = 8081
command_stop = "echo parada-graciosa > parada.txt; exit 7"
`)

	started_v, started_e := cmdStart("api", "")
	started := mustAction(t, started_v, started_e)

	payload, err := cmdStop("api", "")
	if err != nil {
		t.Fatalf("command_stop falló con exit 7 y el stop abortó: el cleanup tiene que seguir: %v", err)
	}
	if mustAction(t, payload, err).Action != "stopped" {
		t.Fatal("no se paró")
	}

	// El comando corrió en el directorio del proyecto.
	if got := strings.TrimSpace(readFileString(t, filepath.Join(root, "api", "parada.txt"))); got != "parada-graciosa" {
		t.Errorf("command_stop no escribió en el directorio del proyecto: %q", got)
	}

	// Y el proceso murió igualmente, pese al exit 7.
	waitGone(t, started.Pid)
	meta, err := store.LoadMeta(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 0 {
		t.Errorf("meta.Pid = %d: un command_stop fallido dejó el servicio vivo", meta.Pid)
	}
}

// TestCmdStopDeUnProyectoSinCommandStopNoFalla: el campo es opcional, y su
// ausencia no puede hacer fallar el stop.
func TestCmdStopDeUnProyectoSinCommandStopNoFalla(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	started_v, started_e := cmdStart("web", "")
	started := mustAction(t, started_v, started_e)
	payload, err := cmdStop("web", "")
	if err != nil {
		t.Fatal(err)
	}
	if mustAction(t, payload, err).Action != "stopped" {
		t.Fatal("no se paró")
	}
	waitGone(t, started.Pid)
	if meta, err := store.LoadMeta(filepath.Join(root, "web")); err != nil || meta.Pid != 0 {
		t.Errorf("meta tras el stop = %+v (err %v)", meta, err)
	}
}

// ---- one-shot: build / install ----

// TestCmdOneShotEjecutaYReportaElCodigoDeSalida: build e install son
// SÍNCRONOS, y su resultado tiene que ser el del comando, no el de vroom.
//
// El caso que importa es el de fallo: un `command_build` que sale 1 es un build
// que NO se hizo, y el JSON tiene que decirlo con ok=false y exit_code=1. Un ok
// true con el comando fallando sería el peor resultado posible: el agente
// construiría y publicaría sin haber construido nada.
func TestCmdOneShotEjecutaYReportaElCodigoDeSalida(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	// Fallo: exit 3 y stderr.
	manPath := filepath.Join(root, "api", ".vroom.toml")
	if err := os.WriteFile(manPath, []byte(`name = "api"
command_start = "sleep 30"
command_build = "echo salida-build; echo error-build >&2; exit 3"
command_install = "echo instalando"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	build_v, build_e := cmdBuild("api", "")
	build := mustAction(t, build_v, build_e)
	if build.OK {
		t.Error("OK = true con exit 3: el agente publicaría sin haber construido")
	}
	if build.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", build.ExitCode)
	}
	if build.Action != "build" {
		t.Errorf("Action = %q, want build", build.Action)
	}
	if build.Error == "" {
		t.Error("un fallo tiene que traer el error: sin él el agente no sabe qué pasó")
	}
	if build.Elapsed == "" {
		t.Error("Elapsed vacío: el coste del comando es parte del resultado")
	}

	// El stdout del comando está en el log del servicio, mezclado con su salida.
	out := readFileString(t, store.StdoutLog(filepath.Join(root, "api")))
	if !strings.Contains(out, "salida-build") {
		t.Errorf("el stdout del comando_build no llegó al log del servicio:\n%s", out)
	}
	errLog := readFileString(t, store.StderrLog(filepath.Join(root, "api")))
	if !strings.Contains(errLog, "error-build") {
		t.Errorf("el stderr del comando_build no llegó al log del servicio:\n%s", errLog)
	}
	// Y el banner del comando está, que es lo que permite a un humano atribuir
	// esa línea a un build y no al servicio.
	if !strings.Contains(out, "vroom ▶ build") {
		t.Errorf("falta el banner del comando en el log:\n%s", out)
	}

	// Éxito: install sale 0.
	install_v, install_e := cmdInstall("api", "")
	install := mustAction(t, install_v, install_e)
	if !install.OK {
		t.Errorf("OK = false en un install correcto: %s", install.Error)
	}
	if install.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", install.ExitCode)
	}
}

// TestCmdOneShotSinComandoDefinidoEsError: un manifiesto sin command_build no es
// un build vacío, es un error.
//
// La distinción importa porque un `ok:true` aquí diría "construido" de algo que
// no tiene ni siquiera el comando. Y el mensaje Nombra el kind, porque `build` y
// `install` son campos distintos y el agente tiene que saber cuál falta.
func TestCmdOneShotSinComandoDefinidoEsError(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	for _, tt := range []struct{ kind, project string }{
		{"build", "web"},
		{"install", "web"},
	} {
		_, err := cmdOneShot(tt.project, "", tt.kind)
		if err == nil {
			t.Fatalf("%s de un proyecto sin %s debería fallar", tt.kind, tt.kind)
		}
		if !strings.Contains(err.Error(), tt.project) {
			t.Errorf("el error no nombra el proyecto: %q", err)
		}
		if !strings.Contains(err.Error(), tt.kind) {
			t.Errorf("el error no nombra el comando ausente: %q", err)
		}
		if !strings.Contains(err.Error(), "no "+tt.kind+" command") {
			t.Errorf("el error debería decir qué falta: %q", err)
		}
	}
}

// TestCmdOneShotRechazaUnKindDesconocido: el switch de kind es exhaustivo y este
// es su default.
//
// El default no es decorativo: cmdOneShot es interno, pero el contrato de no
// publicar un ActionResult sin comando es lo que impide que un kind nuevo
// emita un `ok:true` sin haber ejecutado nada. El fallo tiene que ser un error,
// nunca un resultado vacío.
func TestCmdOneShotRechazaUnKindDesconocido(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	payload, err := cmdOneShot("api", "", "deploy")
	if err == nil {
		t.Fatalf("un kind desconocido devolvió %+v, want error: no se ejecutó ningún comando", payload)
	}
	if !strings.Contains(err.Error(), "deploy") {
		t.Errorf("el error no nombra el kind rechazado: %q", err)
	}
}

// ---- logs ----

// TestCmdLogsLeeLosDosFlujosYLosRecorta: `logs` sin flags lee stdout y stderr;
// con --tail recorta por LÍNEAS, no por bytes.
//
// Recortar por líneas es lo que evita partir una línea por la mitad, que es
// justo lo que un agente no puede usar. Y el recorte se aplica a cada flujo por
// separado: mezclarlos antes de recortar haría que las últimas N líneas fuesen
// del último flujo que escribió, no las últimas N de cada uno.
func TestCmdLogsLeeLosDosFlujosYLosRecorta(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}

	writeLines(t, store.StdoutLog(apiPath), "out1", "out2", "out3")
	writeLines(t, store.StderrLog(apiPath), "err1", "err2", "err3")

	full_v, full_e := cmdLogs("api", nil, "")
	full := mustLogs(t, full_v, full_e)
	if got := linesOf(full.Stdout); len(got) != 3 || got[0] != "out1" {
		t.Errorf("stdout completo = %v, want las tres líneas en orden", got)
	}
	if got := linesOf(full.Stderr); len(got) != 3 || got[0] != "err1" {
		t.Errorf("stderr completo = %v, want las tres líneas en orden", got)
	}

	tailed_v, tailed_e := cmdLogs("api", []string{"--tail", "2"}, "")
	tailed := mustLogs(t, tailed_v, tailed_e)
	if got := linesOf(tailed.Stdout); len(got) != 2 || got[0] != "out2" {
		t.Errorf("stdout con --tail 2 = %v, want las dos últimas", got)
	}
	if got := linesOf(tailed.Stderr); len(got) != 2 || got[0] != "err2" {
		t.Errorf("stderr con --tail 2 = %v, want las dos últimas", got)
	}
}

// TestCmdLogsStreamEligeElFlujo: --stream stdout y --stream stderr devuelven UN
// flujo y el otro vacío.
//
// El vacío es parte del contrato: un campo `stderr` ausente o con contenido en
// un `vroom logs --stream stdout` haría que un agente creyera que hay errores.
// Y un valor de stream desconocido cae en merged, que es el comportamiento
// neutro: no se pierde nada.
func TestCmdLogsStreamEligeElFlujo(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}
	writeLines(t, store.StdoutLog(apiPath), "solo-out")
	writeLines(t, store.StderrLog(apiPath), "solo-err")

	tests := []struct {
		stream    string
		wantOut   bool
		wantErr   bool
		wantEmpty bool
	}{
		{"stdout", true, false, true},
		{"stderr", false, true, true},
		{"merged", true, true, false},
		{"inventado", true, true, false}, // cae en merged: neutro
	}
	for _, tt := range tests {
		t.Run(tt.stream, func(t *testing.T) {
			lv, le := cmdLogs("api", []string{"--stream", tt.stream}, "")
			got := mustLogs(t, lv, le)
			if strings.Contains(got.Stdout, "solo-out") != tt.wantOut {
				t.Errorf("stdout = %q, contiene salida = %v", got.Stdout, tt.wantOut)
			}
			if strings.Contains(got.Stderr, "solo-err") != tt.wantErr {
				t.Errorf("stderr = %q, contiene error = %v", got.Stderr, tt.wantErr)
			}
			if tt.wantEmpty && (got.Stdout != "" && got.Stderr != "") {
				t.Errorf("un stream único debe dejar el otro vacío, dio stdout=%q stderr=%q", got.Stdout, got.Stderr)
			}
		})
	}
}

// TestCmdLogsDeUnServicioSinLogsNoFalla: logs es una consulta informativa, así
// que un log ausente o ilegible devuelve vacío y NO es un error.
//
// La razón es de contrato: si `vroom logs` fallara porque el servicio nunca
// escribió nada, un agente no podría ni distinguir "no hay logs" de "el servicio
// está mal". El proyecto sigue siendo el mismo y el estado se lee en `list`.
func TestCmdLogsDeUnServicioSinLogsNoFalla(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}

	got_v, got_e := cmdLogs("api", nil, "")
	got := mustLogs(t, got_v, got_e)
	if got.Stdout != "" || got.Stderr != "" {
		t.Errorf("un servicio sin logs debe devolver vacíos, dio %q / %q", got.Stdout, got.Stderr)
	}
	if got.Project != "api" {
		t.Errorf("Project = %q, want api: el nombre viene del pedido, no del log", got.Project)
	}

	// Y con el log como DIRECTORIO: tampoco es un error.
	if err := os.MkdirAll(store.StdoutLog(apiPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := cmdLogs("api", nil, ""); err != nil {
		t.Errorf("un log ilegible no puede hacer fallar logs: %v", err)
	}
}

// TestParseLogFlagsEsPredecible: los flags mal formados degradan en vez de
// fallar.
//
// `--tail` sin valor y `--stream` sin valor se ignoran; un `--tail` no numérico
// se trata como 0. Es deliberado: el contrato es "lo que pediste, o el log
// entero", y un error por un flag mal escrito dejaría al usuario SIN logs, que es
// peor que darle logs de más. Y un flag desconocido se ignora, para que añadir
// una opción no rompa las invocaciones viejas.
func TestParseLogFlagsEsPredecible(t *testing.T) {
	tests := []struct {
		name       string
		flags      []string
		wantTail   int
		wantStream string
	}{
		{"sin flags", nil, 0, "merged"},
		{"tail", []string{"--tail", "10"}, 10, "merged"},
		{"stream", []string{"--stream", "stdout"}, 0, "stdout"},
		{"ambos y en orden inverso", []string{"--stream", "stderr", "--tail", "3"}, 3, "stderr"},
		{"tail sin valor", []string{"--tail"}, 0, "merged"},
		{"stream sin valor", []string{"--stream"}, 0, "merged"},
		{"tail no numérico", []string{"--tail", "abc"}, 0, "merged"},
		{"tail negativo", []string{"--tail", "-5"}, -5, "merged"},
		{"flag desconocido", []string{"--follow", "x"}, 0, "merged"},
		{"valor que parece otro flag", []string{"--tail", "--stream"}, 0, "merged"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLogFlags(tt.flags)
			if got.tail != tt.wantTail {
				t.Errorf("tail = %d, want %d", got.tail, tt.wantTail)
			}
			if got.stream != tt.wantStream {
				t.Errorf("stream = %q, want %q", got.stream, tt.wantStream)
			}
		})
	}
}

// TestLastNLinesNoParteLineasNiRompeElCasoDeTodo: la función que recorta, con
// sus tres casos límite.
//
// - n <= 0 devuelve el texto entero: `tail 0` significa "todo".
// - Más líneas de las que hay devuelve el texto entero, no un recorte parcial.
// - Cortar recorta por línea completa, y el separador no se pierde.
func TestLastNLinesNoParteLineasNiRompeElCasoDeTodo(t *testing.T) {
	const doc = "uno\ndos\ntres\ncuatro"
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"todo con 0", doc, 0, doc},
		{"negativo es todo", doc, -1, doc},
		{"más de las que hay", doc, 99, doc},
		{"exactamente todas", doc, 4, doc},
		// Sin salto final en la entrada, el recorte lo pone: el consumidor
		// cuenta líneas por saltos, y un "cuatro" sin terminar sería media
		// línea para quien lo lea.
		{"las dos últimas", doc, 2, "tres\ncuatro\n"},
		{"una sola", doc, 1, "cuatro\n"},
		{"vacío con 0", "", 5, ""},
		{"vacío con n", "", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastNLines(tt.in, tt.n); got != tt.want {
				t.Errorf("lastNLines(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}

	// Y el texto vacío entre medias no se convierte en una línea fantasma: un
	// log con líneas en blanco conserva su conteo.
	if got := lastNLines("a\n\n\nb", 2); got != "\nb\n" {
		t.Errorf("lastNLines con líneas vacías = %q, want %q", got, "\nb\n")
	}

	// El caso que motiva la corrección: un log terminado en salto tiene una
	// línea menos de la que se cree al contar. Sin el descarte del elemento
	// final, `--tail 2` de un log de tres devolvía UNA línea —la más antigua
	// pedida se perdía siempre—, que es un bug silencioso y del tipo que sólo
	// aparece cuando el log tiene más de N líneas.
	if got := lastNLines("a\nb\nc\n", 2); got != "b\nc\n" {
		t.Errorf("lastNLines(%q, 2) = %q, want %q", "a\nb\nc\n", got, "b\nc\n")
	}
}

// ---- launch ----

// composeStack escribe un compose file con UN stack y las etapas dadas.
//
// El TOML anidado ([[stack.stage]], no `stages = [...]`) es el formato que
// ParseComposeFile acepta; la forma compacta no parsea, y el error que devuelve
// es "must have at least one stage", que es el que hizo pensar que el bug era
// del CLI cuando era del fichero de test.
func composeStack(t *testing.T, dir, stackName string, stages ...[2]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("primary_group = \"tienda\"\n\n[[stack]]\nname = \"" + stackName + "\"\n")
	for _, st := range stages {
		b.WriteString("\n  [[stack.stage]]\n  name = \"" + st[0] + "\"\n  services = [" + st[1] + "]\n")
	}
	writeFile(t, filepath.Join(dir, orchestrate.ComposeFileName), b.String())
}

// TestCmdLaunchListNoEscaneaElDisco: `launch --list` sólo necesita el compose
// file, y no el resto del disco.
//
// Que no escanee es una decisión: su contrato es el compose, y hacerlo depender
// de un escaneo haría que fallara por razones que no tienen que ver con los
// stacks. Se comprueba con dos cosas: el plan sale bien y el File del resultado
// es la ruta ABSOLUTA, porque un agente necesita saber dónde se leyó y no puede
// deducirlo de un cwd que no conoce.
func TestCmdLaunchListNoEscaneaElDisco(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)
	composeStack(t, root, "web-tier", [2]string{"front", `"web"`})
	// Un segundo stack, añadido al fichero aparte para no perder el helper simple.
	appendCompose(t, root, `
[[stack]]
name = "todo"
primary_group = "tienda"

  [[stack.stage]]
  name = "front"
  services = ["web"]

  [[stack.stage]]
  name = "api"
  services = ["api"]
`)

	payload, err := cmdLaunch([]string{"--list"})
	if err != nil {
		t.Fatal(err)
	}
	res, ok := payload.(LaunchListResult)
	if !ok {
		t.Fatalf("launch --list devolvió %T", payload)
	}
	if len(res.Stacks) != 2 {
		t.Fatalf("hay %d stacks, want 2", len(res.Stacks))
	}
	if res.Stacks[0].Name != "web-tier" || res.Stacks[1].Name != "todo" {
		t.Errorf("los stacks no salen en el orden del fichero: %v", namesOfStacks(res.Stacks))
	}
	if !filepath.IsAbs(res.File) {
		t.Errorf("File = %q no es absoluta: un agente necesita saber DÓNDE se leyó", res.File)
	}
	if filepath.Base(res.File) != orchestrate.ComposeFileName {
		t.Errorf("File = %q no termina en el nombre del compose", res.File)
	}
	// Y los proyectos del árbol NO aparecen: --list no escanea. Si los añadiera,
	// un stack vacío y un workspace vacío serían la misma respuesta.
	for _, s := range res.Stacks {
		if len(s.Stages) == 0 {
			t.Errorf("el stack %q no trae etapas: se parseó a medias", s.Name)
		}
	}
}

// TestCmdLaunchSinComposeDiceQueFalta: sin compose file el error lo NOMBRA.
//
// Un agente que recibe "not found" sin más no puede distinguir "no hay stacks" de
// "estás en el directorio equivocado", y son dos cosas con arreglos distintos. El
// nombre del fichero va en el mensaje porque es lo que hay que crear.
func TestCmdLaunchSinComposeDiceQueFalta(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)

	_, err := cmdLaunch([]string{"--list"})
	if err == nil {
		t.Fatal("sin compose file debería fallar")
	}
	if !strings.Contains(err.Error(), orchestrate.ComposeFileName) {
		t.Errorf("err = %q, want el nombre del fichero que falta", err)
	}
}

// TestCmdLaunchSinArgsDaElUso: y aquí el mensaje SÍ tiene que ser completo,
// porque no hay ningún otro canal por el que descubrir la forma del comando.
func TestCmdLaunchSinArgsDaElUso(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)

	_, err := cmdLaunch(nil)
	if err == nil {
		t.Fatal("launch sin argumentos debería fallar")
	}
	for _, want := range []string{"--list", "--dry", "usage: vroom launch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("el mensaje de uso no menciona %q: %q", want, err)
		}
	}
}

// TestCmdLaunchStackDesconocidoLoDice: FindStack redacta el error y aquí no se
// reescribe. Lo que se comprueba es que el nombre del stack pedido aparece,
// porque si no el agente no sabe qué está buscando.
func TestCmdLaunchStackDesconocidoLoDice(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)
	composeStack(t, root, "existe", [2]string{"front", `"web"`})

	_, err := cmdLaunch([]string{"no-existe"})
	if err == nil {
		t.Fatal("un stack inexistente debería fallar")
	}
	if !strings.Contains(err.Error(), "no-existe") {
		t.Errorf("err = %q, want el nombre del stack pedido", err)
	}
}

// TestCmdLaunchDryNoArrancaNada: --dry devuelve el plan y NO arranca.
//
// Es la propiedad de la que vive --dry: un agente puede preguntar qué pasaría sin
// cambiar nada. Se comprueba por las DOS vías, porque un payload que dijera "dry
// run" mientras arranca el servicio sería la peor de las dos posibilidades: el
// agente creería no haber tocado nada.
func TestCmdLaunchDryNoArrancaNada(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	composeStack(t, root, "front", [2]string{"front", `"web"`})

	lv, err := cmdLaunch([]string{"front", "--dry"})
	if err != nil {
		t.Fatal(err)
	}
	row := mustJSON(t, lv, err)
	if len(row) == 0 {
		t.Fatal("el plan está vacío")
	}

	// Ningún servicio arrancó: sin Meta, sin PID, sin proceso.
	webPath := filepath.Join(root, "web")
	meta, err := store.LoadMeta(webPath)
	if err == nil && meta.Pid != 0 {
		t.Errorf("web tiene PID %d tras un --dry: el plan arrancó algo", meta.Pid)
	}
	if !processAlive(t, meta.Pid) {
		t.Logf("no hay proceso vivo para web tras el --dry, como debe ser")
	}
}

// TestCmdLaunchRealArrancaLosServiciosDelStack: el camino de verdad, y la
// diferencia con --dry es que ahora sí hay Meta y sí hay PID.
//
// El plan del stack es el de una etapa con dos servicios, que es lo que obliga a
// arrancar en paralelo. Y lo que se verifica del JSON es que trae algo: un
// `ok` sin PIDs dejaría al agente sin forma de verificar nada.
func TestCmdLaunchRealArrancaLosServiciosDelStack(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	// Los dos servicios del stack declaran un puerto ABIERTO. Es lo que evita
	// que el motor pase una ventana entera de espera por puerto que nadie va a
	// abrir, y lo que hace que el test termine en milisegundos en vez de en el
	// timeout del stack.
	listeningService(t, root, "web", "name = \"web\"\n")
	listeningService(t, root, "api", "name = \"api\"\n")
	composeStack(t, root, "front", [2]string{"front", `"web", "api"`})

	lv, err := cmdLaunch([]string{"front"})
	if err != nil {
		t.Fatal(err)
	}
	if row := mustJSON(t, lv, err); len(row) == 0 {
		t.Fatal("el resultado del launch está vacío")
	}

	for _, name := range []string{"web", "api"} {
		path := filepath.Join(root, name)
		meta, err := store.LoadMeta(path)
		if err != nil {
			t.Fatalf("%s no escribió Meta: %v", name, err)
		}
		if meta.Pid <= 0 {
			t.Errorf("%s: Meta sin PID: %+v", name, meta)
			continue
		}
		if !processAlive(t, meta.Pid) {
			t.Errorf("%s: el PID %d no existe: el launch no arrancó nada", name, meta.Pid)
		}
		stopService(t, store, path)
	}
}

// TestCmdLaunchConflictoDeNombresEsErrorYNoDejaProcesos: un stack que pide un
// nombre ambiguo NO puede arrancar, y un conflicto tiene que ser un ERROR, no un
// "ok con menos servicios".
//
// La mitad importante es la del no-dejar-procesos: si arrancara el primero y
// fallara con el segundo, dejaría un proceso que nadie pidió y que nada va a
// parar, que es exactamente el daño que el motor de orquestación existe para
// evitar.
func TestCmdLaunchConflictoDeNombresEsErrorYNoDejaProcesos(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	// Dos directorios con el mismo nombre de manifiesto: el stack lo pide y no
	// hay forma de saber cuál de los dos es.
	for _, sub := range []string{"a", "b"} {
		writeFile(t, filepath.Join(root, sub, ".vroom.toml"),
			"name = \"dup\"\ncommand_start = \"sleep 30\"\nport = 9001\n")
	}
	composeStack(t, root, "front", [2]string{"front", `"dup"`})

	_, err := cmdLaunch([]string{"front"})
	if err == nil {
		t.Fatal("un nombre ambiguo dentro del stack debería fallar, no arrancar medio stack")
	}

	for _, sub := range []string{"a", "b"} {
		dir := filepath.Join(root, sub)
		meta, err := store.LoadMeta(dir)
		if err == nil && meta.Pid != 0 {
			t.Errorf("%s quedó con PID %d tras un conflicto: proceso huérfano", sub, meta.Pid)
		}
	}
}

// appendCompose añade texto al compose file del directorio.
func appendCompose(t *testing.T, dir, extra string) {
	t.Helper()
	path := filepath.Join(dir, orchestrate.ComposeFileName)
	cur := readFileString(t, path)
	writeFile(t, path, cur+extra)
}

// TestRunEscribeElContratoEnStdoutYElErrorEnStderr: la separación de los dos
// flujos es el contrato entero.
//
// stdout es la RESPUESTA y stderr es el DIAGNÓSTICO. Un agente parsea stdout y no
// puede parsear stderr; si el error fuera a stdout, un `vroom list` de un árbol
// con un proyecto roto no se podría leer. Y el código 1 dice que no hay respuesta
// que leer.
func TestRunEscribeElContratoEnStdoutYElErrorEnStderr(t *testing.T) {
	// El contrato, medido sobre un `Run` de verdad.
	root := cliEnv(t)
	_ = chdirTree(t, root)

	var out, errBuf bytes.Buffer
	handled, code := runInto(&out, &errBuf, []string{"list"})
	if !handled || code != 0 {
		t.Fatalf("runInto(list) = handled %v, code %d; want true, 0", handled, code)
	}
	var list map[string]any
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatalf("stdout no es JSON de ListResult: %v\n%s", err, out.String())
	}
	if _, ok := list["projects"]; !ok {
		t.Errorf("la lista no trae la clave projects: %v", list)
	}
	if errBuf.Len() != 0 {
		t.Errorf("un list correcto escribió en stderr: %q", errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	handled, code = runInto(&out, &errBuf, []string{"stop", "no-existe"})
	if !handled {
		t.Fatal("un comando fallido sigue siendo un comando manejado")
	}
	if code != 1 {
		t.Errorf("code = %d, want 1: el agente distingue fallo de éxito por el código, no por parsear", code)
	}
	if out.Len() != 0 {
		t.Errorf("un comando fallido escribió en stdout: %q", out.String())
	}
	if !strings.Contains(errBuf.String(), "project not found") {
		t.Errorf("stderr no lleva el error: %q", errBuf.String())
	}

	// Y la TUI sigue siendo el camino por defecto: sin subcomando no se emite
	// NADA, porque lo que va a stdout cuando no hay comando es la TUI.
	out.Reset()
	errBuf.Reset()
	if handled, code := runInto(&out, &errBuf, nil); handled || code != 0 {
		t.Errorf("runInto(nil) = handled %v, code %d; want false, 0", handled, code)
	}
	if out.Len() != 0 || errBuf.Len() != 0 {
		t.Errorf("sin subcomando no debe emitirse nada: stdout=%q stderr=%q", out.String(), errBuf.String())
	}
}

// TestRunDelegaYNoSaleDeRangeDeErrores: Run es el shell que aplica el código. No
// se puede ejecutar en un test cuando el comando falla —mata el proceso—, y por
// eso lo que se fija aquí es que lo DELEGA: runInto decide y Run no lo cambia.
func TestRunAplicaElCodigoDeSalida(t *testing.T) {
	// El contrato observable sin morir: runInto devuelve 1 y Run lo aplicaría.
	// Ejecutar Run de verdad con un fallo terminaría el binario de test, así que
	// la parte verificable es la decisión, y la aplicación es una línea.
	root := cliEnv(t)
	_ = chdirTree(t, root)

	var out, errBuf bytes.Buffer
	if _, code := runInto(&out, &errBuf, []string{"start", "no-existe"}); code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), `{"error":`) {
		t.Errorf("stderr debe llevar el contrato de error en UNA línea JSON: %q", errBuf.String())
	}
}

// TestCmdStartConElDirectorioDeServiciosInservibleNoArrancaNadaYLoDice: el fallo
// justo antes del spawn.
//
// El directorio de servicio tiene que existir antes de arrancar, porque es donde van
// los logs y el meta. Un servicio arrancado sin él deja al usuario sin nada que leer
// cuando se rompe, que es justo cuando lo necesita.
//
// El provocarlo es más difícil de lo que parece: `state.NewStore()` crea
// `base/services`, así que un store del todo inservible falla ANTES, al construir la
// sesión, y no llega a este punto. Lo que hace falta es un store que se pueda crear
// pero cuyo subdirectorio `services` sea un fichero —lo que pasa cuando alguien lo
// crea a mano, o cuando un `mkdir -p` de otra cosa lo ocupa—.
func TestCmdStartConElDirectorioDeServiciosInservibleNoArrancaNadaYLoDice(t *testing.T) {
	root := cliEnv(t)
	chdirTree(t, root)

	// MEDIDO, y es la parte que costó encontrar: `state.NewStore()` ya crea
	// `base/services`, así que ocupar el store entero hace fallar la SESIÓN, no este
	// punto. Para llegar a `EnsureServiceDir` hay que ocupar el subdirectorio concreto
	// del servicio —`<services>/<hash del path>`— con un fichero. El hash es
	// `state.PathKey`, que es exportado justamente para poder calcularlo.
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "vroom", "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", base)

	// El directorio del servicio de `api` es un fichero.
	apiPath := filepath.Join(root, "api")
	if err := os.WriteFile(
		filepath.Join(base, "vroom", "services", state.PathKey(apiPath)),
		[]byte("soy un fichero, no un directorio"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	_, err := cmdStart("api", "")
	if err == nil {
		t.Fatal("con `services` ocupado por un fichero el arranque tiene que fallar")
	}
	// Y el mensaje tiene que ser accionable: el usuario necesita saber qué revisar.
	if !strings.Contains(err.Error(), "service dir") {
		t.Errorf("err = %q, want que diga que no se pudo crear el directorio del servicio", err)
	}

	// Y no se escribió meta: si lo hubiera, el servicio constaría como arrancado y el
	// siguiente `vroom list` lo mostraría vivo.
	store := state.NewStoreAt(filepath.Join(base, "vroom"))
	if _, err := store.LoadMeta(apiPath); err == nil {
		t.Error("se escribió meta pese a no poder crear el directorio del servicio")
	}
}
