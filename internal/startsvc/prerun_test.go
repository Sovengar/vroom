package startsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The hook runs INSIDE the spawn, so a hook that succeeded leaves its trace in the log of the run it prepared instead of being wiped by the truncation that follows it.
func TestStartRunsThePreRunHook(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	f.manifest.Commands.Start.Hooks.PreRun = "echo preparando > preparado.txt"

	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("start with a pre_run hook: %v", err)
	}
	f.cleanup(t, out)

	data, err := os.ReadFile(filepath.Join(f.dir, "preparado.txt"))
	if err != nil {
		t.Fatalf("the hook did not run in the project directory: %v", err)
	}
	if !strings.Contains(string(data), "preparando") {
		t.Errorf("hook file = %q, want the hook's own output", data)
	}

	logBody := readLogStr(t, f.store.StdoutLog(f.dir))
	for _, want := range []string{"── vroom ▶ pre_run: echo preparando > preparado.txt ──", "── vroom ✓ pre_run ok ("} {
		if !strings.Contains(logBody, want) {
			t.Errorf("service log = %q, want it to contain %q", logBody, want)
		}
	}
}

// Fail-fast: the prerequisite was not met, so nothing is spawned and nothing is persisted; the log keeps the evidence of what the hook did before failing.
func TestStartFailsWhenThePreRunHookFails(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	f.manifest.Commands.Start.Hooks.PreRun = "echo antes; exit 3"

	out, err := f.start(t, 5*time.Second)
	if err == nil {
		t.Fatal("a failing commands.start.hooks.pre_run must abort the start")
	}
	if out.Pid != 0 {
		t.Errorf("Pid = %d, want 0: no child may be spawned after a failed hook", out.Pid)
	}
	for _, want := range []string{"commands.start.hooks.pre_run", "exit status 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}

	if meta, mErr := f.store.LoadMeta(f.dir); mErr == nil {
		t.Errorf("meta = %+v, want none: a failed hook must leave no state behind", meta)
	}

	logBody := readLogStr(t, f.store.StdoutLog(f.dir))
	for _, want := range []string{"── vroom ▶ pre_run: echo antes; exit 3 ──", "antes", "── vroom ✗ pre_run failed (exit 3, "} {
		if !strings.Contains(logBody, want) {
			t.Errorf("service log = %q, want it to contain %q", logBody, want)
		}
	}
}

func readLogStr(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
