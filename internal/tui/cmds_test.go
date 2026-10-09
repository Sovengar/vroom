package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// "" maps to unknown, not stopped: a state published without passing through here must not be rendered as stopped.
func TestStateOfMetaTraduceAlVocabularioDeProceso(t *testing.T) {
	tests := []struct {
		in   state.Meta
		want process.Status
	}{
		{state.Meta{}, process.StatusUnknown},
		{state.Meta{State: state.StateRunning}, process.StatusRunning},
		{state.Meta{State: state.StateStopped}, process.StatusStopped},
		{state.Meta{State: state.StatePortPending}, process.StatusPortPending},
		{state.Meta{State: state.StatePortUnresolved}, process.StatusPortUnresolved},
		{state.Meta{State: state.StateNoPort}, process.StatusNoPort},
		{state.Meta{State: "inventado"}, process.Status("inventado")},
	}
	for _, tt := range tests {
		if got := stateOfMeta(tt.in); got != tt.want {
			t.Errorf("stateOfMeta(State=%q) = %q, want %q", tt.in.State, got, tt.want)
		}
	}
}

// Five, not any: the polling and the tail are distinct clocks with distinct intervals (2s state, 400ms console), and losing either leaves stale data with nothing visibly failing.
func TestInitDevuelveLosCincoRelojes(t *testing.T) {
	m, _ := newTestModel(t)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil: without clocks the TUI never updates")
	}

	msgs := collectBatch(t, cmd)
	if len(msgs) < 5 {
		t.Errorf("Init produced %d messages, want at least 5 (polling, console and two spinners)", len(msgs))
	}
	var sawTick, sawConsoleTick bool
	for _, msg := range msgs {
		switch v := msg.(type) {
		case tickMsg:
			sawTick = true
			if time.Time(v).IsZero() {
				t.Error("the state tick does not carry its timestamp")
			}
		case consoleTickMsg:
			sawConsoleTick = true
			if time.Time(v).IsZero() {
				t.Error("the console tick does not carry its timestamp")
			}
		}
	}
	if !sawTick {
		t.Error("Init did not start the state clock: the TUI would show stale states forever")
	}
	if !sawConsoleTick {
		t.Error("Init did not start the console clock: logs would not move without reloading")
	}
}

// Each clock is run for real, waiting out its interval, because only executing it proves it returns its own message.
func TestTickCmdDevuelveSuMsg(t *testing.T) {
	if msg := runCmd(tickCmd()); !isTickOfState(msg) {
		t.Errorf("tickCmd returned %T, want tickMsg", msg)
	}
	if msg := runCmd(consoleTickCmd()); !isConsoleTick(msg) {
		t.Errorf("consoleTickCmd returned %T, want consoleTickMsg", msg)
	}
}

// The process can die between Evaluate and the sample, so an error here is routine and has to arrive in the message, not as a panic.
func TestThreadsCmdMuestreaElPidYPropagaElError(t *testing.T) {
	msg := runCmd(threadsCmd("/tmp/x", 0))
	th, ok := msg.(threadsMsg)
	if !ok {
		t.Fatalf("threadsCmd returned %T", msg)
	}
	if th.path != "/tmp/x" {
		t.Errorf("path = %q, want /tmp/x: without the path the msg cannot be attributed to a service", th.path)
	}
	if th.err == nil {
		t.Error("a non-existent PID should come with an error: otherwise, the UI would show zero threads as a fact")
	}

	msg = runCmd(threadsCmd("/tmp/x", livePID(t)))
	th = msg.(threadsMsg)
	if th.err != nil {
		t.Errorf("a real process should not give a sampling error: %v", th.err)
	}
	if len(th.threads) == 0 {
		t.Error("a live process has at least one thread: an empty list would make one think it is dead")
	}
}

// The store error has four distinct branches (never started, unreadable meta, live service, meta without PID), and conflating them makes the TUI claim "running" on a stopped service.
func TestRefreshCmdRecorreSoloLosConfiguradosYAtribuyeElMotivo(t *testing.T) {
	// isolateConfig is mandatory: New reads config.Load(), so a developer config carrying scanner.root would make the model scan their real workspace.
	isolateConfig(t)
	root := writeTestTree(t, false)
	store := state.NewStoreAt(t.TempDir())

	apiPath := filepath.Join(root, "tienda-api")
	rotoPath := filepath.Join(root, "tienda-web")

	for _, dir := range []string{apiPath, rotoPath} {
		if err := os.MkdirAll(store.ServiceDir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// tienda-web: garbage meta.json, so the store errors instead of reporting it absent.
	if err := os.WriteFile(filepath.Join(store.ServiceDir(rotoPath), "meta.json"), []byte("{no-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := livePID(t)
	if err := store.SaveMeta(apiPath, state.Meta{
		Name: "tienda-api", Pid: live, Pgid: live, Port: 8081, State: state.StateRunning,
	}); err != nil {
		t.Fatal(err)
	}

	m := New(store, &stubManager{}, root)
	m.updateLayout()

	msg := runCmd(refreshCmd(store, &stubManager{}, m.projects))
	ref, ok := msg.(refreshedMsg)
	if !ok {
		t.Fatalf("refreshCmd returned %T", msg)
	}

	// Fail-closed on purpose: an unreadable meta reports stopped plus a warn, and the warn must name the project because the column shows one at a time.
	got := ref.results[rotoPath]
	if got.status != process.StatusStopped {
		t.Errorf("an unreadable meta gave %q, want stopped (fail-closed)", got.status)
	}
	if got.warn == "" {
		t.Fatal("an unreadable meta without warn asserts 'stopped' as if it were a fact")
	}
	if !strings.Contains(got.warn, "tienda-web") || !strings.Contains(got.warn, "unreadable meta.json") {
		t.Errorf("the warning does not say which project and why: %q", got.warn)
	}

	// stubManager's Evaluate reports stopped even for the live PID, so what matters here is that a result exists and carries the git branch.
	if got, ok := ref.results[apiPath]; !ok {
		t.Error("no result for a configured service")
	} else if got.meta.Pid != live {
		t.Errorf("meta.Pid = %d, want the one from the persisted Meta", got.meta.Pid)
	}

	// A service never started is still queried and comes back stopped: a missing meta is a fact, not a failure, or every cold start would fill the TUI with warnings.
	sinMetaPath := filepath.Join(root, "suelto")
	if got := ref.results[sinMetaPath].status; got != process.StatusStopped {
		t.Errorf("a service without meta gave %q, want stopped: a missing meta is a fact, not a failure", got)
	}

	noCfg := filepath.Join(root, "sin-manifiesto")
	if err := os.MkdirAll(noCfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noCfg, "go.mod"), []byte("module sin-manifiesto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m2 := New(store, &stubManager{}, root)
	m2.projects = append(m2.projects, scanner.Project{Path: noCfg, Name: "sin-manifiesto", Configured: false})
	ref2 := runCmd(refreshCmd(store, &stubManager{}, m2.projects)).(refreshedMsg)
	if _, ok := ref2.results[noCfg]; ok {
		t.Error("refreshCmd queried a project without manifest: there is nothing to query")
	}
}

// The branch arrives per service because that name decides the route, and a `git branch -m` vroom never notices is what forces portless reconciliation.
func TestRefreshCmdPropagaLaRamaGitDeCadaProyecto(t *testing.T) {
	isolateConfig(t)
	root := writeTestTree(t, false)
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, root)

	ref := runCmd(refreshCmd(store, &stubManager{}, m.projects)).(refreshedMsg)

	// A configured project that is not a repo has no branch, and the polling must report that without inventing a name.
	for _, p := range m.projects {
		if !p.Configured {
			continue
		}
		r, ok := ref.results[p.Path]
		if !ok {
			t.Errorf("no result for the configured project %s", p.Name)
			continue
		}
		if p.Name == "tienda-api" && !strings.Contains(r.branch, "main") {
			t.Errorf("the branch of %s is %q, want main: the .git/HEAD of the tree points to refs/heads/main", p.Name, r.branch)
		}
	}

	// Never fall back to the directory name for a branch: that invented name is what decides the auto route path.
	for _, p := range m.projects {
		if p.Name == "tienda-api" {
			continue
		}
		if r, ok := ref.results[p.Path]; ok && r.branch != "" {
			t.Errorf("%s is not a repo and yet carries branch %q", p.Name, r.branch)
		}
	}
}

// A command exiting nonzero is not a vroom error (jobMsg carries the exit code with an empty err); only a failed launch is an error, and then there is no exit code to attribute.
func TestJobCmdDistingueElFalloDelComandoDelFalloDeLanzamiento(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")

	t.Run("the command fails with its exit code", func(t *testing.T) {
		jm := runCmd(jobCmd(dir, "build", "echo fuera; exit 7", dir, out, errLog)).(jobMsg)
		if jm.err != nil {
			t.Errorf("an exit 7 is not a vroom error: %v", jm.err)
		}
		if jm.exitCode != 7 {
			t.Errorf("exitCode = %d, want 7", jm.exitCode)
		}
		if jm.kind != "build" {
			t.Errorf("kind = %q", jm.kind)
		}
	})

	t.Run("the command exits successfully", func(t *testing.T) {
		jm := runCmd(jobCmd(dir, "install", "echo dentro", dir, out, errLog)).(jobMsg)
		if jm.err != nil || jm.exitCode != 0 {
			t.Errorf("a correct job gave err=%v exit=%d", jm.err, jm.exitCode)
		}
	})

	t.Run("the command cannot be launched", func(t *testing.T) {
		t.Setenv("PATH", "") // no sh in PATH
		jm := runCmd(jobCmd(dir, "build", "echo hola", dir, out, errLog)).(jobMsg)
		if jm.err == nil {
			t.Error("without an interpreter the job must carry an error: otherwise, it would look like a correct build")
		}
		if jm.exitCode != 0 {
			t.Errorf("exitCode = %d without a process there can be no exit code", jm.exitCode)
		}
	})

	t.Run("the log cannot be written", func(t *testing.T) {
		// stdout.log is a directory, so the banner cannot be written.
		blocked := makeDir(t, filepath.Join(dir, "stdout-es-dir"))
		jm := runCmd(jobCmd(dir, "build", "echo hola", dir, blocked, errLog)).(jobMsg)
		if jm.err == nil {
			t.Error("without a log the command cannot be executed: the agent would see ok without output")
		}
	})
}

// It must append: truncating would lose the service log's history on every start, which is exactly what --tail exists for.
func TestAppendLineCreaElFicheroYAnadeAlFinal(t *testing.T) {
	// MEDIDO: unlike runLogged, appendLine creates no intermediate directories, because callers already created the service dir and an mkdir tree as a side effect of writing one line would hide a path bug.
	dir := t.TempDir()
	path := filepath.Join(dir, "linea.log")

	if err := appendLine(path, "primera"); err != nil {
		t.Fatalf("appendLine on a new path: %v", err)
	}
	if err := appendLine(path, "segunda"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "primera\nsegunda\n" {
		t.Errorf("content = %q, want the two lines in order", got)
	}
}

// It must fail loudly: swallowing the error would drop the stop notice silently and leave the user with a stopped service and no explanation.
func TestAppendLineFallaDondeNoPuedeEscribir(t *testing.T) {
	dir := t.TempDir()
	blocked := makeDir(t, filepath.Join(dir, "bloqueado"))
	if err := appendLine(blocked, "x"); err == nil {
		t.Error("appendLine on a directory should fail")
	}

	if err := appendLine(filepath.Join(dir, "no-existe", "x.log"), "y"); err == nil {
		t.Error("appendLine must not create directories: its contract is to write a line, not a tree")
	}
}

// An error must not advance the offset, or the next tick skips exactly the bytes it could not read.
func TestReadNewStripped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consola.log")
	if err := os.WriteFile(path, []byte("\x1b[31mrojo\x1b[0m\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, off, err := readNewStripped(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("ANSI codes remain in the output: %q", got)
	}
	if !strings.Contains(got, "rojo") {
		t.Errorf("the text was lost when removing ANSI: %q", got)
	}
	if off != int64(len("\x1b[31mrojo\x1b[0m\n")) {
		t.Errorf("offset = %d, want the file size", off)
	}

	if err := os.WriteFile(path, []byte("\x1b[31mrojo\x1b[0m\nmas\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err = readNewStripped(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if got != "mas\n" {
		t.Errorf("second read = %q, want only the new content", got)
	}

	// A log that does not exist yet is not an error, or every cold start would fill the console tick with warnings.
	missing := filepath.Join(t.TempDir(), "no-existe.log")
	got, same, err := readNewStripped(missing, 1234)
	if err != nil {
		t.Errorf("a missing log is not an error: %v", err)
	}
	if got != "" {
		t.Errorf("data = %q from a missing log, want empty", got)
	}
	if same != 1234 {
		t.Errorf("offset = %d with a missing log, want the one passed: advancing would lose bytes", same)
	}

	asDir := makeDir(t, filepath.Join(t.TempDir(), "log-es-dir"))
	_, same, err = readNewStripped(asDir, 1234)
	if err == nil {
		t.Error("an unreadable log must propagate the error: otherwise, the console would show empty content as if it were true")
	}
	if same != 1234 {
		t.Errorf("offset = %d after an error, want 1234: advancing would lose exactly the bytes that were not read", same)
	}

	// A log truncated below the offset is re-read whole: rotation must not make the console go blank.
	short := filepath.Join(t.TempDir(), "corto.log")
	if err := os.WriteFile(short, []byte("nuevo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err = readNewStripped(short, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if got != "nuevo\n" {
		t.Errorf("after a rotation the console showed %q, want the entire content", got)
	}
}

// stdout and stderr offsets are independent, so a command writing to both cannot make one overwrite the other.
func TestConsoleReadDevuelveOffsetIndependientePorFlujo(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.log")
	errLog := filepath.Join(dir, "e.log")
	if err := os.WriteFile(out, []byte("salida\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(errLog, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	msg := runCmd(consoleTailCmd("/tmp/x", 0, 0, out, errLog))
	d, ok := msg.(consoleDeltaMsg)
	if !ok {
		t.Fatalf("consoleTailCmd returned %T", msg)
	}
	if d.path != "/tmp/x" {
		t.Errorf("path = %q, want /tmp/x", d.path)
	}
	if !strings.Contains(d.stdout, "salida") {
		t.Errorf("stdout = %q", d.stdout)
	}
	if d.offS != int64(len("salida\n")) {
		t.Errorf("offS = %d, want the size of stdout", d.offS)
	}
	if d.offE != 0 {
		t.Errorf("offE = %d with an empty stderr, want 0", d.offE)
	}
	if d.errS != nil {
		t.Errorf("errS = %v with a readable log", d.errS)
	}
}

// The intervals differ on purpose: polling the store and evaluating processes is expensive, while a log line is noticed instantly and tail.ReadNew is cheap.
func TestTickCmdEsMasLentoQueElTickDeConsola(t *testing.T) {
	if consoleTick >= pollInterval {
		t.Errorf("consoleTick (%v) should be faster than pollInterval (%v): "+
			"the log is noticed instantly and the store is not", consoleTick, pollInterval)
	}
	if consoleTick <= 0 || pollInterval <= 0 {
		t.Error("a clock at zero is a busy loop")
	}
}

// tea.Batch only exposes its list through a BatchMsg, so the batch has to be decomposed or Init's five clocks stay opaque.
func collectBatch(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := runCmd(cmd)
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		// A lone Cmd is also valid, so it is returned as a one-element list.
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		if c == nil {
			continue
		}
		out = append(out, runCmd(c))
	}
	return out
}

func isTickOfState(msg tea.Msg) bool {
	_, ok := msg.(tickMsg)
	return ok
}

func isConsoleTick(msg tea.Msg) bool {
	_, ok := msg.(consoleTickMsg)
	return ok
}

func makeDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A real process, not a stub: what is measured is the system's own thread sampling, which a double would only echo back.
func livePID(t *testing.T) int {
	t.Helper()
	dir := t.TempDir()
	mgr := process.NewManager()
	res, err := mgr.Start(process.StartSpec{
		Command: "sleep 60", WorkDir: dir,
		StdoutPath: filepath.Join(dir, "o.log"), StderrPath: filepath.Join(dir, "e.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = mgr.Stop(process.StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: process.DefaultStopTimeout})
	})
	return res.Pid
}
