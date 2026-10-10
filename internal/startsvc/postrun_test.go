package startsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The hook runs only once the start is COMMITTED: reading meta.json from inside it proves it fires after the state is on disk (and, in dynamic mode, after the port was resolved) rather than before the service exists.
func TestStartRunsThePostRunHookAfterTheStartIsCommitted(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	meta := filepath.Join(f.store.ServiceDir(f.dir), "meta.json")
	f.manifest.Commands.Start.Hooks.PostRun = "cp " + meta + " meta_at_post.txt"

	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("start with a post_run hook: %v", err)
	}
	f.cleanup(t, out)
	if len(out.Warnings) != 0 {
		t.Errorf("a hook that succeeded must not warn: %v", out.Warnings)
	}

	seen, err := os.ReadFile(filepath.Join(f.dir, "meta_at_post.txt"))
	if err != nil {
		t.Fatalf("the hook did not run, or ran before meta.json existed: %v", err)
	}
	if !strings.Contains(string(seen), `"state": "running"`) {
		t.Errorf("meta seen by the hook = %q, want the committed state of a resolved start", seen)
	}

	logBody := readLogStr(t, f.store.StdoutLog(f.dir))
	if !strings.Contains(logBody, "── vroom ▶ post_run: cp ") {
		t.Errorf("service log = %q, want the post_run banner", logBody)
	}
	if !strings.Contains(logBody, "── vroom ✓ post_run ok (") {
		t.Errorf("service log = %q, want the ok footer", logBody)
	}
}

// The post_run hook runs synchronously on the start goroutine after the start is committed, so it is the last blocking phase runStart joins on a cancelled start: it must honour the same command lifetime as commands.start.hooks.pre_run, or a Ctrl-C during the route verify sits out the hook's whole unbounded duration before the CLI can exit. Cancelled discovery is the reachable road onto this path — the service is alive with no TCP listener, so the caller walks away and the unwind then reaches the committed post_run.
func TestCancelledStartCutsThePostRunHookAtOnce(t *testing.T) {
	f := newFixture(t)
	f.command(t, "udp-only") // alive, no TCP listener: discovery blocks until the context cuts it
	f.manifest.Commands.Start.Hooks.PostRun = "sleep 8; echo listo > post_ran.txt"
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	defer cancel()

	started := time.Now()
	out, err := f.startWithCtx(t, ctx, 10*time.Second)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("a cancelled start must unwind, not fail: %v", err)
	}
	f.cleanup(t, out)
	if elapsed > 4*time.Second {
		t.Errorf("start took %v: a cancelled start must not sit out the post_run hook", elapsed)
	}
	if _, statErr := os.Stat(filepath.Join(f.dir, "post_ran.txt")); statErr == nil {
		t.Error("the post_run hook ran to completion after cancellation; its process group must be cut")
	}
	if joined := strings.Join(out.Warnings, " | "); !strings.Contains(joined, "post_run") {
		t.Errorf("warnings = %q, want the cut post_run hook reported", joined)
	}
}

// Warn-only by design: the service is already alive, so a failing post hook must not fail the start or kill it — it names itself, in the warnings the CLI and the TUI already surface, and keeps its output in the log.
func TestStartPostRunFailureOnlyWarns(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	f.manifest.Commands.Start.Hooks.PostRun = "echo post antes de fallar; exit 3"

	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("a failing post_run must not fail the start: %v", err)
	}
	f.cleanup(t, out)
	if out.Pid <= 0 {
		t.Fatalf("Pid = %d, want the started service: post_run cannot undo the start", out.Pid)
	}
	joined := strings.Join(out.Warnings, " | ")
	for _, want := range []string{"commands.start.hooks.post_run", "exit status 3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings = %q, want it to contain %q", joined, want)
		}
	}

	if _, err := f.store.LoadMeta(f.dir); err != nil {
		t.Errorf("meta must stay committed after a failed post_run: %v", err)
	}

	logBody := readLogStr(t, f.store.StdoutLog(f.dir))
	for _, want := range []string{"── vroom ▶ post_run: echo post antes de fallar; exit 3 ──", "post antes de fallar", "── vroom ✗ post_run failed (exit 3, "} {
		if !strings.Contains(logBody, want) {
			t.Errorf("service log = %q, want it to contain %q", logBody, want)
		}
	}
}
