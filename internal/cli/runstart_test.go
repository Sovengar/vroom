package cli

import (
	"context"
	"testing"
	"time"

	"vroom/internal/startsvc"
)

// A command cancelled while the start is in flight must return at once: Ctrl-C cancels the command context, and runStart selects on it, so `vroom start` exits instead of sitting out a discovery or propagation window. The abandoned start is reported as an error, never a silent success.
func TestRunStartReturnsAtOnceWhenTheCommandIsCancelled(t *testing.T) {
	block := make(chan struct{})
	defer close(block)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	_, err := runStart(ctx, func() (startsvc.Result, error) { <-block; return startsvc.Result{}, nil })

	if err == nil {
		t.Fatal("a cancelled command must return an error, never a silent success")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("runStart took %v after cancellation: it must return at once", elapsed)
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
