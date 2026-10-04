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
		t.Fatalf("editorDoneMsg(nil) devolvió %T, want statusMsg", editorDoneMsg(nil))
	}
	if msg.message != "editor closed" {
		t.Errorf("message = %q, want \"editor closed\"", msg.message)
	}

	fallo := editorDoneMsg(errors.New("exec: nvim: no encontrado"))
	msg, ok = fallo.(statusMsg)
	if !ok {
		t.Fatalf("editorDoneMsg(err) devolvió %T, want statusMsg", fallo)
	}
	if !strings.Contains(msg.message, "exited with error") {
		t.Errorf("message = %q, want que diga que el editor falló", msg.message)
	}
	// The reason must ride inside the message: "exited with error" alone cannot tell a bad PATH from a missing binary.
	if !strings.Contains(msg.message, "no encontrado") {
		t.Errorf("message = %q, want que incluya el motivo del fallo: sin él el usuario no sabe "+
			"si tiene que arreglar el PATH o el editor", msg.message)
	}
}

func TestInlineAgentDoneMsgNombraAlAgenteQueFallo(t *testing.T) {
	cb := inlineAgentDoneMsg("claude")

	msg, ok := cb(nil).(statusMsg)
	if !ok {
		t.Fatalf("cb(nil) devolvió %T, want statusMsg", cb(nil))
	}
	if msg.message != "claude closed" {
		t.Errorf("message = %q, want \"claude closed\"", msg.message)
	}

	msg, ok = cb(errors.New("status 127")).(statusMsg)
	if !ok {
		t.Fatal("cb(err) no devolvió statusMsg")
	}
	if !strings.Contains(msg.message, "claude") {
		t.Errorf("message = %q, want el nombre del agente", msg.message)
	}
	if !strings.Contains(msg.message, "status 127") {
		t.Errorf("message = %q, want el motivo: el código de salida es lo que distingue un "+
			"comando que no existe de uno que falló", msg.message)
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
		t.Fatalf("err = %v, want %v: el fallo del PTY tiene que llegar tal cual", err, querido)
	}
	if s != nil {
		t.Error("con el PTY sin abrir hay que devolver nil: un termSession a medias lo daría " +
			"`Update` por una sesión viva")
	}
	// The 1x1 floor is applied before asking for the PTY, because xpty.NewPty given size 0 does not error, it just misbehaves in a rare way.
	if pedidoW != 1 || pedidoH != 1 {
		t.Errorf("se pidió un PTY de %dx%d, want 1x1: el tamaño se corrige antes de abrir", pedidoW, pedidoH)
	}
}
