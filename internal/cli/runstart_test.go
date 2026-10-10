package cli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"vroom/internal/startsvc"
)

// A command cancelled while the start is in flight must JOIN the start, not abandon it: an abandoned goroutine raced the start's own persistOrKill, so the spawned daemon stayed untracked and its route unowned. The start's ctx-aware phases unwind at once, so joining still returns fast; cancellation is reported as an error with the partial result, never a silent success.
func TestRunStartJoinsTheStartBeforeReturningOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var unwound atomic.Bool
	started := time.Now()
	out, err := runStart(ctx, func() (startsvc.Result, error) {
		// Stands in for the start's ctx-aware phases reaching persistOrKill before returning.
		time.Sleep(50 * time.Millisecond)
		unwound.Store(true)
		return startsvc.Result{Port: 7}, nil
	})

	if err == nil {
		t.Fatal("a cancelled command must return an error, never a silent success")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("runStart took %v after cancellation: joining a ctx-aware start must still return at once", elapsed)
	}
	if !unwound.Load() {
		t.Fatal("runStart returned before the start unwound: persistOrKill would have been skipped")
	}
	if out.Port != 7 {
		t.Errorf("Port = %d, want the partial result of the joined start", out.Port)
	}
}

// Without cancellation runStart is transparent: it hands back exactly what the start produced, so the Ctrl-C fast path never alters the normal result.
func TestRunStartReturnsTheStartOutcome(t *testing.T) {
	res, err := runStart(context.Background(), func() (startsvc.Result, error) {
		return startsvc.Result{Port: 4321}, nil
	})
	if err != nil {
		t.Fatalf("a completed start must return its error, got %v", err)
	}
	if res.Port != 4321 {
		t.Errorf("Port = %d, want the start's 4321", res.Port)
	}
}
