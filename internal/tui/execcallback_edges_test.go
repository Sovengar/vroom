package tui

import (
	"errors"
	"strings"
	"testing"
)

// tea.ExecProcess's result message type is library-private, so a test can see the Cmd but never run it; hence these callbacks are named functions instead of inline closures.
func TestEditorDoneMsgDistingueCierreDeFallo(t *testing.T) {
	msg, ok := editorDoneMsg(nil).(statusMsg)
	if !ok {
		t.Fatalf("editorDoneMsg(nil) returned %T, want statusMsg", editorDoneMsg(nil))
	}
	if msg.message != "editor closed" {
		t.Errorf("message = %q, want \"editor closed\"", msg.message)
	}

	fallo := editorDoneMsg(errors.New("exec: nvim: no encontrado"))
	msg, ok = fallo.(statusMsg)
	if !ok {
		t.Fatalf("editorDoneMsg(err) returned %T, want statusMsg", fallo)
	}
	if !strings.Contains(msg.message, "exited with error") {
		t.Errorf("message = %q, want it to say the editor failed", msg.message)
	}
	// The reason must ride inside the message: "exited with error" alone cannot tell a bad PATH from a missing binary.
	if !strings.Contains(msg.message, "no encontrado") {
		t.Errorf("message = %q, want it to include the reason for the failure: without it the user doesn't know "+
			"whether to fix the PATH or the editor", msg.message)
	}
}

func TestInlineAgentDoneMsgNombraAlAgenteQueFallo(t *testing.T) {
	cb := inlineAgentDoneMsg("claude")

	msg, ok := cb(nil).(statusMsg)
	if !ok {
		t.Fatalf("cb(nil) returned %T, want statusMsg", cb(nil))
	}
	if msg.message != "claude closed" {
		t.Errorf("message = %q, want \"claude closed\"", msg.message)
	}

	msg, ok = cb(errors.New("status 127")).(statusMsg)
	if !ok {
		t.Fatal("cb(err) did not return statusMsg")
	}
	if !strings.Contains(msg.message, "claude") {
		t.Errorf("message = %q, want the agent name", msg.message)
	}
	if !strings.Contains(msg.message, "status 127") {
		t.Errorf("message = %q, want the reason: the exit code is what distinguishes a "+
			"nonexistent command from one that failed", msg.message)
	}
}

// A PTY open failure must yield a nil session plus the error, never a half-built termSession that Update would treat as live with no terminal behind it; the opener is a parameter like argv, not a global seam, because it is a piece of the environment, not a button production can press.
func TestStartSessionPropagaElFalloDeAbrirElPty(t *testing.T) {
	querido := errors.New("pty: no free descriptors")

	var pedidoW, pedidoH int
	abrir := func(w, h int) (ptyIface, error) {
		pedidoW, pedidoH = w, h
		return nil, querido
	}

	s, err := startSessionWith(0, 0, t.TempDir(), []string{"/bin/sh"}, abrir)
	if !errors.Is(err, querido) {
		t.Fatalf("err = %v, want %v: the PTY failure must arrive as-is", err, querido)
	}
	if s != nil {
		t.Error("with the PTY not open, nil must be returned: a half-built termSession would make " +
			"Update treat it as a live session")
	}
	// The 1x1 floor is applied before asking for the PTY, because xpty.NewPty given size 0 does not error, it just misbehaves in a rare way.
	if pedidoW != 1 || pedidoH != 1 {
		t.Errorf("a PTY of %dx%d was requested, want 1x1: the size is corrected before opening", pedidoW, pedidoH)
	}
}
