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
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
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
	write("roto/.vroom.toml", "name = \"roto\"\ncommand_start = [\n") // Intentionally broken TOML
	return root
}

// Comparing the marshalled map instead of the struct is what makes a mis-set json tag fail the test, because a tag bug shows up in the JSON and not in the field.
func mustJSON(t *testing.T, v any, err error) map[string]any {
	t.Helper()
	if err != nil {
		t.Fatalf("command failed: %v", err)
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
		t.Fatalf("command failed: %v", err)
	}
	data, merr := json.Marshal(v)
	if merr != nil {
		t.Fatal(merr)
	}
	var out actionResult
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatalf("response is not shaped like ActionResult: %v\n%s", uerr, data)
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
		t.Fatalf("command failed: %v", err)
	}
	data, merr := json.Marshal(v)
	if merr != nil {
		t.Fatal(merr)
	}
	var out logsPayload
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatalf("response is not shaped like LogsResult: %v\n%s", uerr, data)
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

func TestDispatchRoutesEachCommand(t *testing.T) {
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
				t.Fatal("command was not handled: Run would launch the TUI")
			}
			if err != nil {
				t.Fatalf("dispatch(%v) = error %v", tt.args, err)
			}
			if tt.action == "" {
				if payload == nil {
					t.Error("logs returned nil payload")
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
func TestDispatchUsageAndPathBySubcommand(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	for _, cmd := range []string{"start", "stop", "build", "install", "logs"} {
		t.Run(cmd+"/no-name", func(t *testing.T) {
			_, handled, err := dispatch([]string{cmd})
			if !handled {
				t.Fatal("no name must not fall through to the TUI: the user did request a command")
			}
			if err == nil {
				t.Fatal("no name should be a usage error")
			}
			if !strings.Contains(err.Error(), "usage: vroom "+cmd+" <project-name|path>") {
				t.Errorf("usage message does not name the subcommand: %q", err)
			}
		})

		t.Run(cmd+"/path-without-value", func(t *testing.T) {
			_, handled, err := dispatch([]string{cmd, "api", "--path"})
			if !handled {
				t.Fatal("a malformed --path must still be a handled command")
			}
			if err == nil || !strings.Contains(err.Error(), "--path requires a value") {
				t.Errorf("err = %v, want the --path missing value error", err)
			}
		})

		t.Run(cmd+"/repeated-path", func(t *testing.T) {
			_, handled, err := dispatch([]string{cmd, "api", "--path", "/a", "--path", "/b"})
			if !handled {
				t.Fatal("must still be a handled command")
			}
			if err == nil || !strings.Contains(err.Error(), "more than once") {
				t.Errorf("err = %v, want the repeated --path error", err)
			}
		})
	}
}

func TestDispatchNoArgumentsAndUnknownGoToTUI(t *testing.T) {
	for _, args := range [][]string{{}, {"invented"}, {"--verbose"}, {"list2"}} {
		payload, handled, err := dispatch(args)
		if handled {
			t.Errorf("dispatch(%v) says it handled the command, and that raises a usage error instead of the TUI", args)
		}
		if err != nil {
			t.Errorf("dispatch(%v) returned error %v, but falling through to the TUI is not an error", args, err)
		}
		if payload != nil {
			t.Errorf("dispatch(%v) returned payload without handling the command", args)
		}
	}
}

func TestDispatchListAndStatusAreTheSame(t *testing.T) {
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
		t.Error("`status` and `list` returned different projects: the alias is not an alias")
	}
}

func TestDispatchHelpWithNoArgsAndWithAlias(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		payload, handled, err := dispatch(args)
		if !handled || err != nil {
			t.Fatalf("dispatch(%v) = handled %v, err %v", args, handled, err)
		}
		doc := mustJSON(t, payload, err)
		commands, ok := doc["commands"].(map[string]any)
		if !ok {
			t.Fatalf("help does not include the command table: %v", doc)
		}
		for _, want := range []string{"vroom list", "vroom start <name|path> [--path <path>]", "vroom launch <name> --dry"} {
			if _, ok := commands[want]; !ok {
				t.Errorf("help does not document %q", want)
			}
		}
		if _, ok := doc["notes"].(map[string]any); !ok {
			t.Error("help must include the notes: --path is what disambiguates worktrees")
		}
	}
}

func TestCmdListPublishesEachScanRow(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	payload, err := cmdList()
	if err != nil {
		t.Fatal(err)
	}

	list, ok := payload.(ListResult)
	if !ok {
		t.Fatalf("list returned %T, want ListResult", payload)
	}
	if len(list.Projects) != 3 {
		t.Fatalf("got %d projects, want 3 (api, web and roto)", len(list.Projects))
	}

	byName := map[string]map[string]any{}
	for _, p := range list.Projects {
		byName[p.Name] = mustJSON(t, p, nil)
	}

	api := byName["api"]
	if api == nil {
		t.Fatal("api is not in the list")
	}
	if api["configured"] != true {
		t.Error("api must appear configured")
	}
	if api["command"] != "sleep 30" {
		t.Errorf("command = %v, want the manifest's command_start", api["command"])
	}
	if api["declared_port"] != float64(8081) {
		t.Errorf("declared_port = %v, want 8081", api["declared_port"])
	}
	if _, ok := api["port_verified"]; ok {
		t.Error("a service without meta cannot claim port_verified: that would be a contract the JSON does not meet")
	}

	web := byName["web"]
	if web == nil {
		t.Fatal("web is not in the list")
	}
	for _, absent := range []string{"command_build", "command_install", "route", "route_mode"} {
		if _, ok := web[absent]; ok {
			t.Errorf("web does not define %s yet publishes it: omitempty must omit it", absent)
		}
	}

	// Publishing the unmanageable row is the point: without manifest_error it would be indistinguishable from a project nobody configured.
	roto := byName["roto"]
	if roto == nil {
		t.Fatal("the directory with a broken manifest does not appear in the list")
	}
	if roto["configured"] != false {
		t.Error("roto has a malformed manifest yet appears configured")
	}
	if roto["manifest_error"] == "" {
		t.Error("an unconfigured row due to parsing must say WHY: manifest_error")
	}
	for _, absent := range []string{"declared_port", "port_verified", "command", "route"} {
		if _, ok := roto[absent]; ok {
			t.Errorf("an unconfigured row publishes %s: there is no manifest to get it from", absent)
		}
	}
}

// Two of the three root forms fail silently when mishandled: the scan just returns fewer projects, so an agent reads a broken root as an empty workspace.
func TestCmdListRespectsConfigRoot(t *testing.T) {
	rootTree := func(t *testing.T, dir, name string) {
		t.Helper()
		writeFile(t, filepath.Join(dir, ".vroom.toml"),
			"name = \""+name+"\"\ncommand_start = \"sleep 30\"\n")
	}

	t.Run("an absolute root bounds the scan", func(t *testing.T) {
		root := cliEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		other := filepath.Join(home, "other-root")
		if err := os.MkdirAll(other, 0o755); err != nil {
			t.Fatal(err)
		}
		rootTree(t, other, "only-this")
		writeConfigScannerRoot(t, other)
		t.Chdir(root) // CWD has api, web and roto: they must not appear

		lv, le := cmdList()
		projects := mustProjects(t, lv, le)
		if len(projects) != 1 {
			t.Fatalf("got %d projects, want 1: the config root must bound the scan (%v)",
				len(projects), namesOf(projects))
		}
		if projects[0].Name != filepath.Base(other) {
			t.Errorf("Name = %q, want the directory %q", projects[0].Name, filepath.Base(other))
		}
	})

	t.Run("a relative root resolves against the CWD", func(t *testing.T) {
		root := cliEnv(t)
		writeConfigScannerRoot(t, "api")
		t.Chdir(root)

		lv, le := cmdList()
		projects := mustProjects(t, lv, le)
		if len(projects) != 1 || projects[0].Name != "api" {
			t.Errorf("a relative root should yield only api, got %v", namesOf(projects))
		}
	})

	t.Run("a root with tilde expands against HOME", func(t *testing.T) {
		root := cliEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		// Assert the project that exists, not a zero count: zero cannot distinguish a missing tilde expansion from an empty home.
		dir := filepath.Join(home, "vroom-test-root")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		rootTree(t, dir, "from-home")
		writeConfigScannerRoot(t, "~/vroom-test-root")
		t.Chdir(root)

		lv, le := cmdList()
		projects := mustProjects(t, lv, le)
		if len(projects) != 1 || projects[0].Name != "vroom-test-root" {
			t.Errorf("tilde was not expanded against HOME: %v", namesOf(projects))
		}
	})

	t.Run("a non-existent root is an ERROR, not an empty list", func(t *testing.T) {
		root := cliEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeConfigScannerRoot(t, "~/no-such-root")
		t.Chdir(root)

		// A missing root must be an error, not an empty list: "no projects" and "could not look" are different answers for an agent.
		_, err := cmdList()
		if err == nil {
			t.Fatal("a nonexistent root should fail, not return an empty list")
		}
		if !strings.Contains(err.Error(), "scan error") {
			t.Errorf("err = %q, want the contract's 'scan error' prefix", err)
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
		t.Fatalf("list failed: %v", err)
	}
	list, ok := v.(ListResult)
	if !ok {
		t.Fatalf("list returned %T, want ListResult", v)
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

func TestCommandsNotFoundAndUnconfiguredAreDistinctErrors(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	for _, name := range []string{"start", "stop", "build", "install", "logs"} {
		_, _, err := dispatch([]string{name, "no-existe"})
		if err == nil {
			t.Fatalf("%s of a nonexistent project should fail", name)
		}
		if !strings.Contains(err.Error(), "project not found: no-existe") {
			t.Errorf("%s: %q does not say the project does not exist", name, err)
		}
		// Resolution failures must not carry the scan prefix: an agent counts the two causes separately.
		if strings.Contains(err.Error(), "scan error") {
			t.Errorf("%s: a nonexistent project is not a scan failure: %q", name, err)
		}
	}

	// The message must name .vroom.toml: "not configured" alone does not tell the user which file to create.
	_, _, err := dispatch([]string{"start", "roto"})
	if err == nil {
		t.Fatal("starting a directory without a manifest should fail")
	}
	if !strings.Contains(err.Error(), "roto") || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("err = %q, want the project name and 'not configured'", err)
	}
	if !strings.Contains(err.Error(), ".vroom.toml") {
		t.Errorf("err = %q does not say WHAT is missing", err)
	}

	// build/install keep the shorter message on purpose: agents may already match on it, so unifying the wording would break them.
	_, _, err = dispatch([]string{"build", "roto"})
	if err == nil || strings.Contains(err.Error(), ".vroom.toml") {
		t.Errorf("build of an unconfigured project = %q, want the short message", err)
	}
}

func TestCmdStartStartsAndWritesMeta(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	payload, err := cmdStart("api", "")
	if err != nil {
		t.Fatal(err)
	}

	res := mustAction(t, payload, err)
	if !res.OK {
		t.Error("OK = false after a successful start")
	}
	if res.Action != "started" {
		t.Errorf("Action = %q, want started", res.Action)
	}
	if res.Project != "api" {
		t.Errorf("Project = %q, want api: the requested name is what gets published", res.Project)
	}
	if res.Pid <= 0 {
		t.Fatalf("Pid = %d, want > 0: without a PID an agent cannot verify anything", res.Pid)
	}

	apiPath := filepath.Join(root, "api")
	meta, err := store.LoadMeta(apiPath)
	if err != nil {
		t.Fatalf("Meta was not written to disk: %v", err)
	}
	if meta.Pid != res.Pid {
		t.Errorf("Meta has Pid %d and JSON says %d: the contract would break on the next list", meta.Pid, res.Pid)
	}
	if meta.State != "running" {
		t.Errorf("meta.State = %q, want running", meta.State)
	}

	if !processAlive(t, res.Pid) {
		t.Errorf("PID %d no longer exists: the start did not happen", res.Pid)
	}
	stopService(t, store, apiPath)
}

func TestCmdStartOfAlreadyRunningServiceIsIdempotent(t *testing.T) {
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
		t.Error("an idempotent start must return OK: it is not an error, it is that it was already running")
	}
	if second.Pid != 0 {
		t.Errorf("Pid = %d on already_running: nothing was started, so there is no new PID", second.Pid)
	}
	if first.Pid == 0 {
		t.Fatal("the first start did not return a PID")
	}

	meta, err := store.LoadMeta(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != first.Pid {
		t.Errorf("Meta changed PID (%d -> %d): a second process was started", first.Pid, meta.Pid)
	}
	stopService(t, store, filepath.Join(root, "api"))
}

func TestCmdStartByPathDisambiguates(t *testing.T) {
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
		t.Fatal("a duplicate name without --path should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ambiguous project name") {
		t.Errorf("err = %q, want ambiguity", msg)
	}
	for _, sub := range []string{"wt-a", "wt-b"} {
		if !strings.Contains(msg, sub) {
			t.Errorf("message does not list candidate %q: %q", sub, msg)
		}
	}
	if !strings.Contains(msg, "--path") {
		t.Errorf("message does not suggest the way out: %q", msg)
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
				t.Errorf("wt-a should have Meta with PID: %v", err)
			}
		} else if err == nil && meta.Pid != 0 {
			t.Errorf("wt-b started without anyone asking: PID %d", meta.Pid)
		}
	}
	for _, sub := range []string{"wt-a", "wt-b"} {
		stopService(t, store, filepath.Join(root, sub))
	}
}

// Start warnings go to the service stderr log, never to the payload: an agent must be able to tell a healthy start from a degraded one.
func TestCmdStartDoesNotPublishPortlessWarningInPayload(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	payload_v, payload_e := cmdStart("api", "")
	payload := mustAction(t, payload_v, payload_e)
	if payload.Error != "" {
		t.Errorf("a start without degradation must not carry an error: %q", payload.Error)
	}

	logPath := store.StderrLog(filepath.Join(root, "api"))
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("no stderr log for the service: %v", err)
	}
	stopService(t, store, filepath.Join(root, "api"))
}

func TestCmdStopStopsProcessAndLeavesMetaStopped(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	started_v, started_e := cmdStart("api", "")
	started := mustAction(t, started_v, started_e)
	if started.Pid <= 0 {
		t.Fatal("did not start")
	}

	payload, err := cmdStop("api", "")
	if err != nil {
		t.Fatal(err)
	}
	res := mustAction(t, payload, err)
	if !res.OK || res.Action != "stopped" {
		t.Errorf("response = %+v, want OK and stopped", res)
	}

	waitGone(t, started.Pid)

	meta, err := store.LoadMeta(filepath.Join(root, "api"))
	if err != nil {
		t.Fatalf("Meta disappeared instead of being updated: %v", err)
	}
	if meta.Pid != 0 {
		t.Errorf("meta.Pid = %d after stop: the next list would claim a live service", meta.Pid)
	}
	if meta.Pgid != 0 {
		t.Errorf("meta.Pgid = %d after stop", meta.Pgid)
	}
	if meta.State != "stopped" {
		t.Errorf("meta.State = %q, want stopped", meta.State)
	}
}

// Stop is deliberately idempotent: an agent retrying after a timeout must not read "was already stopped" as a failure.
func TestCmdStopOfServiceThatNeverStartedStillSucceeds(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	for _, name := range []string{"api", "roto"} {
		payload, err := cmdStop(name, "")
		if err != nil {
			t.Fatalf("stop of %q, which never started, failed: %v", name, err)
		}
		res := mustAction(t, payload, err)
		if !res.OK || res.Action != "stopped" {
			t.Errorf("stop of %q = %+v, want OK and stopped", name, res)
		}
	}
}

// Verified by the file that appears next to .vroom.toml, not by exit code: a stop command run in the wrong directory would still exit 0.
func TestCmdStopExecutesCommandStopInProjectDirectory(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	writeFile(t, filepath.Join(root, "api", ".vroom.toml"), `name = "api"
command_start = "sleep 30"
port = 8081
command_stop = "echo graceful-stop > stop.txt; exit 7"
`)

	started_v, started_e := cmdStart("api", "")
	started := mustAction(t, started_v, started_e)

	payload, err := cmdStop("api", "")
	if err != nil {
		t.Fatalf("command_stop failed with exit 7 and stop aborted: cleanup must continue: %v", err)
	}
	if mustAction(t, payload, err).Action != "stopped" {
		t.Fatal("did not stop")
	}

	if got := strings.TrimSpace(readFileString(t, filepath.Join(root, "api", "stop.txt"))); got != "graceful-stop" {
		t.Errorf("command_stop did not write to the project directory: %q", got)
	}

	waitGone(t, started.Pid)
	meta, err := store.LoadMeta(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 0 {
		t.Errorf("meta.Pid = %d: a failed command_stop left the service alive", meta.Pid)
	}
}

func TestCmdStopOfProjectWithoutCommandStopDoesNotFail(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	started_v, started_e := cmdStart("web", "")
	started := mustAction(t, started_v, started_e)
	payload, err := cmdStop("web", "")
	if err != nil {
		t.Fatal(err)
	}
	if mustAction(t, payload, err).Action != "stopped" {
		t.Fatal("did not stop")
	}
	waitGone(t, started.Pid)
	if meta, err := store.LoadMeta(filepath.Join(root, "web")); err != nil || meta.Pid != 0 {
		t.Errorf("meta after stop = %+v (err %v)", meta, err)
	}
}

func TestCmdOneShotExecutesAndReportsExitCode(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	manPath := filepath.Join(root, "api", ".vroom.toml")
	if err := os.WriteFile(manPath, []byte(`name = "api"
command_start = "sleep 30"
command_build = "echo build-output; echo build-error >&2; exit 3"
command_install = "echo installing"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	build_v, build_e := cmdBuild("api", "")
	build := mustAction(t, build_v, build_e)
	if build.OK {
		t.Error("OK = true with exit 3: the agent would publish without having built")
	}
	if build.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", build.ExitCode)
	}
	if build.Action != "build" {
		t.Errorf("Action = %q, want build", build.Action)
	}
	if build.Error == "" {
		t.Error("a failure must carry the error: without it the agent does not know what happened")
	}
	if build.Elapsed == "" {
		t.Error("Empty Elapsed: the command cost is part of the result")
	}

	out := readFileString(t, store.StdoutLog(filepath.Join(root, "api")))
	if !strings.Contains(out, "build-output") {
		t.Errorf("command_build stdout did not reach the service log:\n%s", out)
	}
	errLog := readFileString(t, store.StderrLog(filepath.Join(root, "api")))
	if !strings.Contains(errLog, "build-error") {
		t.Errorf("command_build stderr did not reach the service log:\n%s", errLog)
	}
	if !strings.Contains(out, "vroom ▶ build") {
		t.Errorf("command banner missing from the log:\n%s", out)
	}

	install_v, install_e := cmdInstall("api", "")
	install := mustAction(t, install_v, install_e)
	if !install.OK {
		t.Errorf("OK = false on a correct install: %s", install.Error)
	}
	if install.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", install.ExitCode)
	}
}

func TestCmdOneShotWithoutDefinedCommandIsError(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	for _, tt := range []struct{ kind, project string }{
		{"build", "web"},
		{"install", "web"},
	} {
		_, err := cmdOneShot(tt.project, "", tt.kind)
		if err == nil {
			t.Fatalf("%s of a project without %s should fail", tt.kind, tt.kind)
		}
		if !strings.Contains(err.Error(), tt.project) {
			t.Errorf("error does not name the project: %q", err)
		}
		if !strings.Contains(err.Error(), tt.kind) {
			t.Errorf("error does not name the missing command: %q", err)
		}
		if !strings.Contains(err.Error(), "no "+tt.kind+" command") {
			t.Errorf("error should say what is missing: %q", err)
		}
	}
}

// An unknown kind must be an error, never an empty ActionResult: a new kind must not be able to emit ok:true without running a command.
func TestCmdOneShotRejectsUnknownKind(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	payload, err := cmdOneShot("api", "", "deploy")
	if err == nil {
		t.Fatalf("an unknown kind returned %+v, want error: no command was executed", payload)
	}
	if !strings.Contains(err.Error(), "deploy") {
		t.Errorf("error does not name the rejected kind: %q", err)
	}
}

// Tail is applied per stream: merging first would make the last N lines come from whichever stream wrote last.
func TestCmdLogsReadsBothStreamsAndTrims(t *testing.T) {
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
		t.Errorf("full stdout = %v, want the three lines in order", got)
	}
	if got := linesOf(full.Stderr); len(got) != 3 || got[0] != "err1" {
		t.Errorf("full stderr = %v, want the three lines in order", got)
	}

	tailed_v, tailed_e := cmdLogs("api", []string{"--tail", "2"}, "")
	tailed := mustLogs(t, tailed_v, tailed_e)
	if got := linesOf(tailed.Stdout); len(got) != 2 || got[0] != "out2" {
		t.Errorf("stdout with --tail 2 = %v, want the last two", got)
	}
	if got := linesOf(tailed.Stderr); len(got) != 2 || got[0] != "err2" {
		t.Errorf("stderr with --tail 2 = %v, want the last two", got)
	}
}

// An unknown --stream value falls back to merged, the neutral choice that loses nothing.
func TestCmdLogsStreamSelectsStream(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}
	writeLines(t, store.StdoutLog(apiPath), "only-out")
	writeLines(t, store.StderrLog(apiPath), "only-err")

	tests := []struct {
		stream    string
		wantOut   bool
		wantErr   bool
		wantEmpty bool
	}{
		{"stdout", true, false, true},
		{"stderr", false, true, true},
		{"merged", true, true, false},
		{"invented", true, true, false}, // falls back to merged: neutral
	}
	for _, tt := range tests {
		t.Run(tt.stream, func(t *testing.T) {
			lv, le := cmdLogs("api", []string{"--stream", tt.stream}, "")
			got := mustLogs(t, lv, le)
			if strings.Contains(got.Stdout, "only-out") != tt.wantOut {
				t.Errorf("stdout = %q, contains output = %v", got.Stdout, tt.wantOut)
			}
			if strings.Contains(got.Stderr, "only-err") != tt.wantErr {
				t.Errorf("stderr = %q, contains error = %v", got.Stderr, tt.wantErr)
			}
			if tt.wantEmpty && (got.Stdout != "" && got.Stderr != "") {
				t.Errorf("a single stream must leave the other empty, got stdout=%q stderr=%q", got.Stdout, got.Stderr)
			}
		})
	}
}

// A missing or unreadable log is not an error: an agent must be able to tell "no logs" from "the service is broken".
func TestCmdLogsOfServiceWithoutLogsDoesNotFail(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}

	got_v, got_e := cmdLogs("api", nil, "")
	got := mustLogs(t, got_v, got_e)
	if got.Stdout != "" || got.Stderr != "" {
		t.Errorf("a service without logs must return empty, got %q / %q", got.Stdout, got.Stderr)
	}
	if got.Project != "api" {
		t.Errorf("Project = %q, want api: the name comes from the request, not the log", got.Project)
	}

	if err := os.MkdirAll(store.StdoutLog(apiPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := cmdLogs("api", nil, ""); err != nil {
		t.Errorf("an unreadable log cannot make logs fail: %v", err)
	}
}

// Malformed flags degrade to the whole log instead of erroring: no logs at all is worse than too many, and ignoring unknown flags keeps old invocations working.
func TestParseLogFlagsIsPredictable(t *testing.T) {
	tests := []struct {
		name       string
		flags      []string
		wantTail   int
		wantStream string
	}{
		{"no flags", nil, 0, "merged"},
		{"tail", []string{"--tail", "10"}, 10, "merged"},
		{"stream", []string{"--stream", "stdout"}, 0, "stdout"},
		{"both and in reverse order", []string{"--stream", "stderr", "--tail", "3"}, 3, "stderr"},
		{"tail without value", []string{"--tail"}, 0, "merged"},
		{"stream without value", []string{"--stream"}, 0, "merged"},
		{"non-numeric tail", []string{"--tail", "abc"}, 0, "merged"},
		{"negative tail", []string{"--tail", "-5"}, -5, "merged"},
		{"unknown flag", []string{"--follow", "x"}, 0, "merged"},
		{"value that looks like another flag", []string{"--tail", "--stream"}, 0, "merged"},
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

func TestLastNLinesDoesNotSplitLinesNorBreakAllCase(t *testing.T) {
	const doc = "one\ntwo\nthree\nfour"
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"all with 0", doc, 0, doc},
		{"negative is all", doc, -1, doc},
		{"more than available", doc, 99, doc},
		{"exactly all", doc, 4, doc},
		// The trailing newline is added on purpose: consumers count lines by newlines, so an unterminated last line reads as half a line.
		{"the last two", doc, 2, "three\nfour\n"},
		{"just one", doc, 1, "four\n"},
		{"empty with 0", "", 5, ""},
		{"empty with n", "", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastNLines(tt.in, tt.n); got != tt.want {
				t.Errorf("lastNLines(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}

	if got := lastNLines("a\n\n\nb", 2); got != "\nb\n" {
		t.Errorf("lastNLines with empty lines = %q, want %q", got, "\nb\n")
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
	b.WriteString("primary_group = \"shop\"\n\n[[stack]]\nname = \"" + stackName + "\"\n")
	for _, st := range stages {
		b.WriteString("\n  [[stack.stage]]\n  name = \"" + st[0] + "\"\n  services = [" + st[1] + "]\n")
	}
	writeFile(t, filepath.Join(dir, orchestrate.ComposeFileName), b.String())
}

// File must be absolute: an agent cannot deduce where the compose was read from a cwd it never set.
func TestCmdLaunchListDoesNotScanDisk(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)
	composeStack(t, root, "web-tier", [2]string{"front", `"web"`})
	appendCompose(t, root, `
[[stack]]
name = "all"
primary_group = "shop"

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
		t.Fatalf("launch --list returned %T", payload)
	}
	if len(res.Stacks) != 2 {
		t.Fatalf("got %d stacks, want 2", len(res.Stacks))
	}
	if res.Stacks[0].Name != "web-tier" || res.Stacks[1].Name != "all" {
		t.Errorf("stacks are not in file order: %v", namesOfStacks(res.Stacks))
	}
	if !filepath.IsAbs(res.File) {
		t.Errorf("File = %q is not absolute: an agent needs to know WHERE it was read", res.File)
	}
	if filepath.Base(res.File) != orchestrate.ComposeFileName {
		t.Errorf("File = %q does not end with the compose file name", res.File)
	}
	for _, s := range res.Stacks {
		if len(s.Stages) == 0 {
			t.Errorf("stack %q has no stages: it was half-parsed", s.Name)
		}
	}
}

func TestCmdLaunchWithoutComposeSaysMissing(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)

	_, err := cmdLaunch([]string{"--list"})
	if err == nil {
		t.Fatal("without compose file should fail")
	}
	if !strings.Contains(err.Error(), orchestrate.ComposeFileName) {
		t.Errorf("err = %q, want the name of the missing file", err)
	}
}

// Here the usage message must be complete because it is the only channel an agent has to learn the command's shape.
func TestCmdLaunchWithoutArgsShowsUsage(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)

	_, err := cmdLaunch(nil)
	if err == nil {
		t.Fatal("launch without arguments should fail")
	}
	for _, want := range []string{"--list", "--dry", "usage: vroom launch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("usage message does not mention %q: %q", want, err)
		}
	}
}

func TestCmdLaunchUnknownStackSaysSo(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)
	composeStack(t, root, "exists", [2]string{"front", `"web"`})

	_, err := cmdLaunch([]string{"no-existe"})
	if err == nil {
		t.Fatal("a nonexistent stack should fail")
	}
	if !strings.Contains(err.Error(), "no-existe") {
		t.Errorf("err = %q, want the requested stack name", err)
	}
}

func TestCmdLaunchDryStartsNothing(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)
	composeStack(t, root, "front", [2]string{"front", `"web"`})

	lv, err := cmdLaunch([]string{"front", "--dry"})
	if err != nil {
		t.Fatal(err)
	}
	row := mustJSON(t, lv, err)
	if len(row) == 0 {
		t.Fatal("the plan is empty")
	}

	webPath := filepath.Join(root, "web")
	meta, err := store.LoadMeta(webPath)
	if err == nil && meta.Pid != 0 {
		t.Errorf("web has PID %d after a --dry: the plan started something", meta.Pid)
	}
	if !processAlive(t, meta.Pid) {
		t.Logf("no live process for web after --dry, as expected")
	}
}

func TestCmdLaunchRealStartsStackServices(t *testing.T) {
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
		t.Fatal("launch result is empty")
	}

	for _, name := range []string{"web", "api"} {
		path := filepath.Join(root, name)
		meta, err := store.LoadMeta(path)
		if err != nil {
			t.Fatalf("%s did not write Meta: %v", name, err)
		}
		if meta.Pid <= 0 {
			t.Errorf("%s: Meta without PID: %+v", name, meta)
			continue
		}
		if !processAlive(t, meta.Pid) {
			t.Errorf("%s: PID %d does not exist: launch did not start anything", name, meta.Pid)
		}
		stopService(t, store, path)
	}
}

// A name conflict must be an error, never an "ok with fewer services": a partial start would leave a process nobody asked for and nothing will stop it.
func TestCmdLaunchNameConflictIsErrorAndLeavesNoProcesses(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	for _, sub := range []string{"a", "b"} {
		writeFile(t, filepath.Join(root, sub, ".vroom.toml"),
			"name = \"dup\"\ncommand_start = \"sleep 30\"\nport = 9001\n")
	}
	composeStack(t, root, "front", [2]string{"front", `"dup"`})

	_, err := cmdLaunch([]string{"front"})
	if err == nil {
		t.Fatal("an ambiguous name within the stack should fail, not start half a stack")
	}

	for _, sub := range []string{"a", "b"} {
		dir := filepath.Join(root, sub)
		meta, err := store.LoadMeta(dir)
		if err == nil && meta.Pid != 0 {
			t.Errorf("%s left with PID %d after a conflict: orphan process", sub, meta.Pid)
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
func TestRunWritesContractToStdoutAndErrorToStderr(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	var out, errBuf bytes.Buffer
	handled, code := runInto(&out, &errBuf, []string{"list"})
	if !handled || code != 0 {
		t.Fatalf("runInto(list) = handled %v, code %d; want true, 0", handled, code)
	}
	var list map[string]any
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatalf("stdout is not ListResult JSON: %v\n%s", err, out.String())
	}
	if _, ok := list["projects"]; !ok {
		t.Errorf("list does not include the projects key: %v", list)
	}
	if errBuf.Len() != 0 {
		t.Errorf("a correct list wrote to stderr: %q", errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	handled, code = runInto(&out, &errBuf, []string{"stop", "no-existe"})
	if !handled {
		t.Fatal("a failed command is still a handled command")
	}
	if code != 1 {
		t.Errorf("code = %d, want 1: the agent distinguishes failure from success by code, not by parsing", code)
	}
	if out.Len() != 0 {
		t.Errorf("a failed command wrote to stdout: %q", out.String())
	}
	if !strings.Contains(errBuf.String(), "project not found") {
		t.Errorf("stderr does not carry the error: %q", errBuf.String())
	}

	// With no subcommand nothing is emitted: stdout belongs to the TUI in that case.
	out.Reset()
	errBuf.Reset()
	if handled, code := runInto(&out, &errBuf, nil); handled || code != 0 {
		t.Errorf("runInto(nil) = handled %v, code %d; want false, 0", handled, code)
	}
	if out.Len() != 0 || errBuf.Len() != 0 {
		t.Errorf("with no subcommand nothing should be emitted: stdout=%q stderr=%q", out.String(), errBuf.String())
	}
}

// Run cannot be exercised here because it exits the process on failure and would kill the test binary, so only its delegation to runInto is pinned.
func TestRunAppliesExitCode(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	var out, errBuf bytes.Buffer
	if _, code := runInto(&out, &errBuf, []string{"start", "no-existe"}); code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), `{"error":`) {
		t.Errorf("stderr must carry the error contract in ONE JSON line: %q", errBuf.String())
	}
}

func TestCmdStartWithUnusableServiceDirStartsNothingAndSaysSo(t *testing.T) {
	root := cliEnv(t)
	chdirTree(t, root)

	// MEASURED: state.NewStore() already creates base/services, so occupying the whole store fails the session and not this point; reaching EnsureServiceDir needs a file at <services>/<hash of the path>, whose hash is state.PathKey.
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "vroom", "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", base)

	apiPath := filepath.Join(root, "api")
	if err := os.WriteFile(
		filepath.Join(base, "vroom", "services", state.PathKey(apiPath)),
		[]byte("I am a file, not a directory"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	_, err := cmdStart("api", "")
	if err == nil {
		t.Fatal("with `services` occupied by a file the start must fail")
	}
	if !strings.Contains(err.Error(), "service dir") {
		t.Errorf("err = %q, want it to say the service directory could not be created", err)
	}

	store := state.NewStoreAt(filepath.Join(base, "vroom"))
	if _, err := store.LoadMeta(apiPath); err == nil {
		t.Error("meta was written despite not being able to create the service directory")
	}
}
