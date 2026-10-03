package tui

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Lo que bubbletea ejecuta en la TUI pero nunca en un test.
//
// `tea.ExecProcess` devuelve un mensaje cuyo tipo es PRIVADO de la librería: un test
// puede pedir el `Cmd` y ver que hay mensaje, pero no puede ejecutarlo para que la
// TUI receivesa lo que el callback devuelve. La salida de esa pelea es dejar los
// callbacks sin nombre, dentro de una clausura en línea que nadie puede invocar.
//
// Los dos de aquí ya son funciones con nombre, y lo que se comprueba es lo que el
// usuario lee cuando el editor o el agente en línea se cierran con error.
// ---------------------------------------------------------------------------

// TestEditorDoneMsgDistingueCierreDeFallo: el `err == nil`.
//
// Un cierre limpio y un fallo son cosas distintas para el usuario: una es "puedes
// volver a la lista" y la otra es "el editor no arrancó". Con un solo mensaje para
// los dos, un `nvim` mal escrito parecería un cierre normal.
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
	// El motivo va dentro: "editor exited with error" sin el errno no le dice al
	// usuario si es un PATH mal puesto o un editor que no existe.
	if !strings.Contains(msg.message, "no encontrado") {
		t.Errorf("message = %q, want que incluya el motivo del fallo: sin él el usuario no sabe "+
			"si tiene que arreglar el PATH o el editor", msg.message)
	}
}

// TestInlineAgentDoneMsgNombraAlAgenteQueFallo: el mensaje por agente.
//
// El nombre va en el mensaje y no en un campo aparte porque es lo que el usuario lee
// de reojo en la línea de estado: si dos agentes pueden inline, un "exited with
// error" sin nombre no dice cuál fue.
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

// TestStartSessionPropagaElFalloDeAbrirElPty: el PTY que no se puede abrir.
//
// Abrir un PTY falla de verdad —agotamiento de descriptores, `devpts` con el límite
// alcanzado— y cuando falla no hay terminal: hay que decirlo. Un `termSession` a
// medio construir lo daría `Update` por una sesión viva con un PTY que no existe,
// y el usuario vería un modal de terminal que no responde a nada.
//
// El opener va por parámetro (`startSessionWith`) y no por seam global, igual que el
// argv: es una pieza del entorno, no un botón que la producción pueda pulsar.
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
	// Los suelos de 1x1 se aplican ANTES de pedir el PTY: es lo que evita que
	// `xpty.NewPty` reciba un tamaño 0, que no es un error sino un desastre raro.
	if pedidoW != 1 || pedidoH != 1 {
		t.Errorf("se pidió un PTY de %dx%d, want 1x1: el tamaño se corrige antes de abrir", pedidoW, pedidoH)
	}
}
