package startsvc

import (
	"context"
	"testing"
	"time"

	"vroom/internal/state"
)

// A cancelled start must not keep discovering: the port_pending attempt has already been persisted and its pid registered, so it unwinds at once leaving the daemon TRACKED (vroom stop can find it) instead of running the discovery timeout out and dressing an unknown port as resolved.
func TestCancelledDiscoveryStopsAtOnceAndKeepsTheAttemptTracked(t *testing.T) {
	f := newFixture(t)
	f.command(t, "udp-only") // alive, no TCP listener: discovery would otherwise run the whole timeout
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	defer cancel()

	started := time.Now()
	out, err := f.startWithCtx(t, ctx, 10*time.Second)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("a cancelled discovery must unwind, not fail: %v", err)
	}
	f.cleanup(t, out)
	if elapsed > 5*time.Second {
		t.Errorf("start took %v: a cancelled discovery must not run the 10s timeout out", elapsed)
	}
	if out.Meta.State != state.StatePortPending {
		t.Errorf("State = %q, want %q: the port was never discovered", out.Meta.State, state.StatePortPending)
	}
	if out.Pid <= 0 {
		t.Error("the spawned daemon must stay in the result so it can still be stopped")
	}
	if len(out.Warnings) == 0 {
		t.Error("a cancelled discovery must warn about it")
	}
	meta, mErr := f.store.LoadMeta(f.dir)
	if mErr != nil {
		t.Fatalf("the attempt must stay persisted so stop can find the daemon: %v", mErr)
	}
	if meta.State != state.StatePortPending || meta.Pid != out.Pid {
		t.Errorf("persisted meta = %+v, want the port_pending attempt with pid %d", meta, out.Pid)
	}
}

// Ctrl-C during command_pre_start must cut the hook and abort the start without spawning anything: the hook's sh child must not survive as an orphan holding the service log fds.
func TestCancelledPreStartHookAbortsWithoutSpawning(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	f.manifest.Commands.Start.Hooks.PreRun = "sleep 60"
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	defer cancel()

	started := time.Now()
	out, err := f.startWithCtx(t, ctx, 5*time.Second)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a cancelled hook must abort the start")
	}
	if elapsed > 5*time.Second {
		t.Errorf("start took %v after cancelling the hook: it must be cut, not waited out", elapsed)
	}
	if out.Pid != 0 {
		t.Errorf("Pid = %d, want 0: nothing may be spawned after a cancelled hook", out.Pid)
	}
	if meta, mErr := f.store.LoadMeta(f.dir); mErr == nil {
		t.Errorf("meta = %+v, want none: a cancelled hook must leave no state behind", meta)
	}
}
