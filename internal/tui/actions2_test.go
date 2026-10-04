package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/portless"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Los rechazos que dependen del entorno, no de la tecla.
//
// La tanda anterior cubrió las acciones cuyo rechazo sale de los DATOS del
// modelo —un manifiesto roto, un miembro sin estado—. Ésta es la otra clase:
// rechazos que sólo existen cuando algo del sistema falla, y que sin un fallo
// real ni se ven ni se pueden escribir.
//
// El patrón es el mismo en todos: provocar el fallo de verdad (un directorio sin
// permiso de escritura, un `scanner.root` que no existe, un nombre de argv que no
// existe) y comprobar que la función lo devuelve en vez de tragárselo.
// ---------------------------------------------------------------------------

// TestNewAvisaCuandoElEscaneoFalla: un `scanner.root` inservible.
//
// El escaneo es lo primero que hace `New`, y es lo único que puede fallar ahí
// dentro. La pregunta que responde este test es si el fallo se ve: un `New` que
// avisa y sigue con la lista vacía deja la TUI en pie, que es lo que hace falta
// para que el usuario lea el motivo y corrija la config.
func TestNewAvisaCuandoElEscaneoFalla(t *testing.T) {
	isolateConfig(t)
	// Un `scanner.root` que no existe. `Scan` sobre un root inexistente devuelve
	// error en vez de lista vacía, que es justo lo que distingue "no hay
	// proyectos" de "no pude mirar".
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("[scanner]\nroot = \"/nonexistent-vroom-root-9d2f\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", cfg)

	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, "")
	if m.message == "" {
		t.Fatal("un escaneo fallido tiene que dejar un aviso: si no, el usuario ve una TUI vacía " +
			"sin ninguna pista de por qué")
	}
	// Y el aviso tiene que nombrar el fallo, no decir "algo salió mal".
	if !strings.Contains(m.message, "scanning projects") {
		t.Errorf("message = %q, want que nombre el escaneo", m.message)
	}
	if len(m.projects) != 0 {
		t.Errorf("hay %d proyectos tras un escaneo fallido, want 0", len(m.projects))
	}
}

// TestRunLoggedFallaSiElLogDeSalidaNoSePuedeAbrir: el primer descriptor.
//
// Los logs de un servicio se escriben bajo su directorio en el store. Si ese
// fichero no se puede abrir, el comando NO se lanza: es preferible decir que no se
// pudo escribir el log a ejecutar un build de dos minutos cuyo output no va a
// ninguna parte.
//
// Y el error tiene que ser el del `OpenFile`, sin envolver: es un problema de
// permisos o de disco, y quien lo lee necesita el errno.
func TestRunLoggedFallaSiElLogDeSalidaNoSePuedeAbrir(t *testing.T) {
	dir := t.TempDir()
	// El directorio existe y se puede recorrer, así que el `MkdirAll` del principio
	// pasa y el `OpenFile` del log es el primero que falla de verdad. Es lo que pasa
	// cuando el directorio de logs de un servicio pertenece a otro usuario.
	roto := filepath.Join(dir, "logs")
	if err := os.MkdirAll(roto, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roto, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roto, 0o755) })

	_, code, err := runLogged("build", "echo hola", dir, filepath.Join(roto, "out.log"), "")
	if err == nil {
		t.Fatal("con el log sin permiso de escritura el comando no se puede lanzar: su salida se " +
			"perdería entera y el usuario no vería por qué")
	}
	if code != 0 {
		t.Errorf("exit code = %d con un fallo de lanzamiento, want 0: no llegó a ejecutarse nada", code)
	}
}

// TestRunLoggedFallaSiElBannerNoSePuedeEscribir: el mismo descriptor, sin espacio.
//
// El segundo fallo posible es el que sólo aparece con el fichero YA abierto: se
// abre bien y no se puede escribir en él. Es un disco lleno, una cuota, o una ruta
// que es un dispositivo que siempre dice ENOSPC.
//
// MEDIDO: `/dev/full` es justo eso —abre, escribe y devuelve ENOSPC—, así que la
// rama se provoca de verdad y no con un permiso imposible de reproducir. Lo que
// importa es que el comando NO se lance: media salida de un build en un log cortado
// es peor que un error.
func TestRunLoggedFallaSiElBannerNoSePuedeEscribir(t *testing.T) {
	const lleno = "/dev/full"
	if _, err := os.Stat(lleno); err != nil {
		t.Skipf("esta máquina no tiene %s, y sin él no hay forma de abrir un log que acepte el "+
			"OpenFile y rechace la escritura", lleno)
	}

	_, code, err := runLogged("build", "echo hola", t.TempDir(), lleno, "")
	if err == nil {
		t.Fatal("con un log que no acepta escrituras el comando no se puede lanzar")
	}
	if code != 0 {
		t.Errorf("exit code = %d con un fallo de escritura, want 0: no llegó a ejecutarse nada", code)
	}
}

// TestStopCmdPropagaElFalloDeQuitarElPid: el `ClearPid`.
//
// Cuando el servicio ya está parado, quitar el pid es lo único que queda por
// hacer. Si ese borrado falla, el siguiente arranque se encontraría con un pid
// guardado de un proceso que ya no existe, y la fuente de verdad quedaría mintiendo
// sobre lo que está vivo.
//
// El fallo se provoca con el directorio del servicio sin permiso de escritura,
// que es como se ve de verdad cuando el directorio quedó de otro usuario.
func TestStopCmdPropagaElFalloDeQuitarElPid(t *testing.T) {
	m, store := newTestModel(t)
	p := primerProyectoConfigurado(t, m)

	// `ClearPid` quita el fichero del pid con `os.Remove`, así que se sustituye por
	// un DIRECTORIO NO VACÍO: quitarlo falla con ENOTEMPTY sin depender de
	// permisos, que el usuario del test sí tiene.
	pid := store.PidFile(p.Path)
	if err := os.MkdirAll(filepath.Join(pid, "bloqueo"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := stopCmd(store, &stubManager{}, p.Path, p.Manifest.Stop)
	if cmd == nil {
		t.Fatal("stopCmd tiene que devolver un comando")
	}
	msg, ok := cmd().(stoppedMsg)
	if !ok {
		t.Fatalf("el comando devolvió %T, want stoppedMsg", cmd())
	}
	if msg.err == nil {
		t.Error("stoppedMsg.err = nil tras un ClearPid fallido: el pid guardado queda mintiendo " +
			"sobre lo que está vivo y el usuario no se entera")
	}
	if msg.path != p.Path {
		t.Errorf("path = %q, want %q: el mensaje tiene que decir a qué servicio se refiere", msg.path, p.Path)
	}
}

// TestTuiRouteReleaserDevuelveNilFueraDeUnBinarioDeTest: la rama del cliente real.
//
// `tuiRouteReleaser` tiene tres salidas: el stub si está instalado, un
// reclamador inerte si el binario es de test, y nil —el cliente real— en el
// binario de verdad. Las dos primeras las usa la suite; la tercera es la que
// decide que vroom habla con portless de verdad, y sin probarla nada garantiza
// que un `nil` accidental no se confunda con "no hay contrato de ruta".
//
// MEDIDO: `IsTestBinary` decide por el sufijo de `os.Args[0]`, así que cambiarlo
// reproduce exactamente la entrada del binario instalado, sin tocar la función ni
// su contrato.
func TestTuiRouteReleaserDevuelveNilFueraDeUnBinarioDeTest(t *testing.T) {
	original := os.Args[0]
	t.Cleanup(func() { os.Args[0] = original })

	os.Args[0] = "/usr/local/bin/vroom"
	if portless.IsTestBinary() {
		t.Fatal("IsTestBinary sigue diciendo que es un test con un argv de producción: " +
			"este test no probaría la rama del cliente real")
	}
	if got := tuiRouteReleaser(); got != nil {
		t.Errorf("tuiRouteReleaser() = %v con un binario de producción, want nil: nil es lo que "+
			"le dice a portless que use el cliente real", got)
	}
}

// TestTermKeyIgnoraUnMensajeDeTeclaQueNoEsUnaPulsacion: la guarda de tipo.
//
// El modal de terminal manda a `termKey` todo lo que llega del teclado mientras
// está abierto, y no todo es una pulsación: bubbletea también entrega
// repeticiones, relâjamientos y pulsaciones de ratón. Un `KeyReleaseMsg` no es un
// `KeyPressMsg`, así que el type assertion falla y la tecla se descarta.
//
// Lo que importa aquí es que se DESCARTE, no que se mande al PTY: mandar un
// release al shell lo interpretaría como una tecla más.
func TestTermKeyIgnoraUnMensajeDeTeclaQueNoEsUnaPulsacion(t *testing.T) {
	m, _ := newTestModel(t)
	m.term = &termSession{}
	m.termOpen = true

	nuevo, cmd := m.termKey(tea.KeyReleaseMsg{Code: 'a'})
	if cmd != nil {
		t.Error("una relajación de tecla no puede devolver comando: la sesión sigue viva y su bucle " +
			"de lectura ya está en marcha")
	}
	got, ok := nuevo.(Model)
	if !ok {
		t.Fatalf("termKey devolvió %T, want Model", nuevo)
	}
	if got.termOpen != m.termOpen {
		t.Errorf("termOpen pasó de %v a %v: sólo ctrl+q cierra el modal", m.termOpen, got.termOpen)
	}
	if s := got.term; s != m.term {
		t.Error("la sesión del PTY cambió con una tecla que no es una pulsación")
	}
}
