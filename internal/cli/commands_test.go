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

// Commands return (payload, error) with Run as the only emitter, so these tests drive the real command in-process against a real tree: no subprocess harness and no scanner, store or manager doubles.

// Chdir rather than an injected --root because vroom scans the CWD by contract: a root seam would make the tests exercise a root no user has.
func cliEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return cliTree(t)
}

// web declares no command_build/command_install so tests get the "no command defined" error, which is distinct from "not configured".
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
	// A broken manifest is the only way to produce an unconfigured row: the scan only reports directories that hold a .vroom.toml, so a manifest-less directory never shows up.
	write("roto/go.mod", "module roto\n")
	write("roto/.vroom.toml", "name = \"roto\"\ncommand_start = [\n") // TOML roto a propósito
	return root
}

// Comparing the marshalled map instead of the struct is what makes a mis-set json tag fail the test, because a tag bug shows up in the JSON and not in the field.
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

// The json tags are deliberate: untagged, encoding/json matches by field name, exit_code misses ExitCode, and a failed build's exit code reaches the test as zero.
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

func namesOfStacks(stacks []orchestrate.Stack) []string {
	out := make([]string, 0, len(stacks))
	for _, s := range stacks {
		out = append(out, s.Name)
	}
	return out
}

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
		// logs is a query command with no ActionResult, hence the empty action.
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

// The usage message must name the real subcommand: an agent reading "usage: vroom stop" out of a build would burn a whole guess attempt.
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
	if _, ok := api["port_verified"]; ok {
		t.Error("un servicio sin meta no puede afirmar port_verified: eso sería un contrato que el JSON no cumple")
	}

	web := byName["web"]
	if web == nil {
		t.Fatal("web no está en la lista")
	}
	for _, absent := range []string{"command_build", "command_install", "route", "route_mode"} {
		if _, ok := web[absent]; ok {
			t.Errorf("web no define %s y aun así lo publica: el omitempty debe omitirlo", absent)
		}
	}

	// Publishing the unmanageable row is the point: without manifest_error it would be indistinguishable from a project nobody configured.
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

// Two of the three root forms fail silently when mishandled: the scan just returns fewer projects, so an agent reads a broken root as an empty workspace.
func TestCmdListRespetaElRootDeLaConfig(t *testing.T) {
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
		// Assert the project that exists, not a zero count: zero cannot distinguish a missing tilde expansion from an empty home.
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

		// A missing root must be an error, not an empty list: "no projects" and "could not look" are different answers for an agent.
		_, err := cmdList()
		if err == nil {
			t.Fatal("un root inexistente debería fallar, no devolver una lista vacía")
		}
		if !strings.Contains(err.Error(), "scan error") {
			t.Errorf("err = %q, want el prefijo 'scan error' del contrato", err)
		}
	})
}

// depth is explicit on purpose: a test depending on the shipped default breaks when the default changes without anyone touching this file.
func writeConfigScannerRoot(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vroom.toml")
	body := "[scanner]\nroot = \"" + root + "\"\ndepth = 4\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", path)
}

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

func TestComandosNoEncontradoYNoConfiguradoSonErroresDistintos(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	for _, name := range []string{"start", "stop", "build", "install", "logs"} {
		_, _, err := dispatch([]string{name, "no-existe"})
		if err == nil {
			t.Fatalf("%s de un proyecto inexistente debería fallar", name)
		}
		if !strings.Contains(err.Error(), "project not found: no-existe") {
			t.Errorf("%s: %q no dice que no existe el proyecto", name, err)
		}
		// Resolution failures must not carry the scan prefix: an agent counts the two causes separately.
		if strings.Contains(err.Error(), "scan error") {
			t.Errorf("%s: un proyecto inexistente no es un fallo de escaneo: %q", name, err)
		}
	}

	// The message must name .vroom.toml: "not configured" alone does not tell the user which file to create.
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

	// build/install keep the shorter message on purpose: agents may already match on it, so unifying the wording would break them.
	_, _, err = dispatch([]string{"build", "roto"})
	if err == nil || strings.Contains(err.Error(), ".vroom.toml") {
		t.Errorf("build de un proyecto no configurado = %q, want el mensaje corto", err)
	}
}

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

	if !processAlive(t, res.Pid) {
		t.Errorf("el PID %d ya no existe: el arranque no ocurrió", res.Pid)
	}
	stopService(t, store, apiPath)
}

func TestCmdStartDeUnServicioYaCorriendoEsIdempotente(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	// The declared port must be really listening: a live PID with a closed port evaluates as unknown, not running.
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

	meta, err := store.LoadMeta(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != first.Pid {
		t.Errorf("el Meta cambió de PID (%d -> %d): se arrancó un segundo proceso", first.Pid, meta.Pid)
	}
	stopService(t, store, filepath.Join(root, "api"))
}

func TestCmdStartPorPathDesambigua(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

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

// Start warnings go to the service stderr log, never to the payload: an agent must be able to tell a healthy start from a degraded one.
func TestCmdStartNoPublicaWarningDeportlessEnElPayload(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	payload_v, payload_e := cmdStart("api", "")
	payload := mustAction(t, payload_v, payload_e)
	if payload.Error != "" {
		t.Errorf("un arranque sin degradación no debe traer error: %q", payload.Error)
	}

	logPath := store.StderrLog(filepath.Join(root, "api"))
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("no hay log de stderr del servicio: %v", err)
	}
	stopService(t, store, filepath.Join(root, "api"))
}

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

// Stop is deliberately idempotent: an agent retrying after a timeout must not read "was already stopped" as a failure.
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

// Verified by the file that appears next to .vroom.toml, not by exit code: a stop command run in the wrong directory would still exit 0.
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

	if got := strings.TrimSpace(readFileString(t, filepath.Join(root, "api", "parada.txt"))); got != "parada-graciosa" {
		t.Errorf("command_stop no escribió en el directorio del proyecto: %q", got)
	}

	waitGone(t, started.Pid)
	meta, err := store.LoadMeta(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 0 {
		t.Errorf("meta.Pid = %d: un command_stop fallido dejó el servicio vivo", meta.Pid)
	}
}

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

func TestCmdOneShotEjecutaYReportaElCodigoDeSalida(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

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

	out := readFileString(t, store.StdoutLog(filepath.Join(root, "api")))
	if !strings.Contains(out, "salida-build") {
		t.Errorf("el stdout del comando_build no llegó al log del servicio:\n%s", out)
	}
	errLog := readFileString(t, store.StderrLog(filepath.Join(root, "api")))
	if !strings.Contains(errLog, "error-build") {
		t.Errorf("el stderr del comando_build no llegó al log del servicio:\n%s", errLog)
	}
	if !strings.Contains(out, "vroom ▶ build") {
		t.Errorf("falta el banner del comando en el log:\n%s", out)
	}

	install_v, install_e := cmdInstall("api", "")
	install := mustAction(t, install_v, install_e)
	if !install.OK {
		t.Errorf("OK = false en un install correcto: %s", install.Error)
	}
	if install.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", install.ExitCode)
	}
}

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

// An unknown kind must be an error, never an empty ActionResult: a new kind must not be able to emit ok:true without running a command.
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

// Tail is applied per stream: merging first would make the last N lines come from whichever stream wrote last.
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

// An unknown --stream value falls back to merged, the neutral choice that loses nothing.
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

// A missing or unreadable log is not an error: an agent must be able to tell "no logs" from "the service is broken".
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

	if err := os.MkdirAll(store.StdoutLog(apiPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := cmdLogs("api", nil, ""); err != nil {
		t.Errorf("un log ilegible no puede hacer fallar logs: %v", err)
	}
}

// Malformed flags degrade to the whole log instead of erroring: no logs at all is worse than too many, and ignoring unknown flags keeps old invocations working.
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
		// The trailing newline is added on purpose: consumers count lines by newlines, so an unterminated last line reads as half a line.
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

	if got := lastNLines("a\n\n\nb", 2); got != "\nb\n" {
		t.Errorf("lastNLines con líneas vacías = %q, want %q", got, "\nb\n")
	}

	// Regression: a log ending in a newline has one line fewer than a naive count, so --tail 2 of three lines returned a single line.
	if got := lastNLines("a\nb\nc\n", 2); got != "b\nc\n" {
		t.Errorf("lastNLines(%q, 2) = %q, want %q", "a\nb\nc\n", got, "b\nc\n")
	}
}

// The nested [[stack.stage]] TOML is what ParseComposeFile accepts; the compact stages = [...] form fails with "must have at least one stage".
func composeStack(t *testing.T, dir, stackName string, stages ...[2]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("primary_group = \"tienda\"\n\n[[stack]]\nname = \"" + stackName + "\"\n")
	for _, st := range stages {
		b.WriteString("\n  [[stack.stage]]\n  name = \"" + st[0] + "\"\n  services = [" + st[1] + "]\n")
	}
	writeFile(t, filepath.Join(dir, orchestrate.ComposeFileName), b.String())
}

// File must be absolute: an agent cannot deduce where the compose was read from a cwd it never set.
func TestCmdLaunchListNoEscaneaElDisco(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)
	composeStack(t, root, "web-tier", [2]string{"front", `"web"`})
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
	for _, s := range res.Stacks {
		if len(s.Stages) == 0 {
			t.Errorf("el stack %q no trae etapas: se parseó a medias", s.Name)
		}
	}
}

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

// Here the usage message must be complete because it is the only channel an agent has to learn the command's shape.
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

	webPath := filepath.Join(root, "web")
	meta, err := store.LoadMeta(webPath)
	if err == nil && meta.Pid != 0 {
		t.Errorf("web tiene PID %d tras un --dry: el plan arrancó algo", meta.Pid)
	}
	if !processAlive(t, meta.Pid) {
		t.Logf("no hay proceso vivo para web tras el --dry, como debe ser")
	}
}

func TestCmdLaunchRealArrancaLosServiciosDelStack(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	// Both services hold a really listening port, otherwise the engine burns a whole port-wait window and the test ends in the stack timeout.
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

// A name conflict must be an error, never an "ok with fewer services": a partial start would leave a process nobody asked for and nothing will stop it.
func TestCmdLaunchConflictoDeNombresEsErrorYNoDejaProcesos(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

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

func appendCompose(t *testing.T, dir, extra string) {
	t.Helper()
	path := filepath.Join(dir, orchestrate.ComposeFileName)
	cur := readFileString(t, path)
	writeFile(t, path, cur+extra)
}

// stdout is the response and stderr the diagnostic: an agent parses stdout only, so an error there would make a list of a tree with a broken project unreadable.
func TestRunEscribeElContratoEnStdoutYElErrorEnStderr(t *testing.T) {
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

	// With no subcommand nothing is emitted: stdout belongs to the TUI in that case.
	out.Reset()
	errBuf.Reset()
	if handled, code := runInto(&out, &errBuf, nil); handled || code != 0 {
		t.Errorf("runInto(nil) = handled %v, code %d; want false, 0", handled, code)
	}
	if out.Len() != 0 || errBuf.Len() != 0 {
		t.Errorf("sin subcomando no debe emitirse nada: stdout=%q stderr=%q", out.String(), errBuf.String())
	}
}

// Run cannot be exercised here because it exits the process on failure and would kill the test binary, so only its delegation to runInto is pinned.
func TestRunAplicaElCodigoDeSalida(t *testing.T) {
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

func TestCmdStartConElDirectorioDeServiciosInservibleNoArrancaNadaYLoDice(t *testing.T) {
	root := cliEnv(t)
	chdirTree(t, root)

	// MEDIDO: state.NewStore() already creates base/services, so occupying the whole store fails the session and not this point; reaching EnsureServiceDir needs a file at <services>/<hash of the path>, whose hash is state.PathKey.
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "vroom", "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", base)

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
	if !strings.Contains(err.Error(), "service dir") {
		t.Errorf("err = %q, want que diga que no se pudo crear el directorio del servicio", err)
	}

	store := state.NewStoreAt(filepath.Join(base, "vroom"))
	if _, err := store.LoadMeta(apiPath); err == nil {
		t.Error("se escribió meta pese a no poder crear el directorio del servicio")
	}
}
