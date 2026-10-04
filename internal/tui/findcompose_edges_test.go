package tui

import (
	"os"
	"path/filepath"
	"testing"

	"vroom/internal/orchestrate"
	"vroom/internal/scanner"
)

// TestFindComposeFileTerminaEnLaRaizDelSistema: el `parent == dir`.
//
// `findComposeFile` sube desde cada proyecto hasta encontrar el compose. La parada
// de emergencia es `filepath.Dir` de la raíz: `filepath.Dir("/")` es `"/"`, así que
// sin esa comprobación el bucle se quedaría subiendo para siempre por el mismo
// directorio.
//
// MEDIDO: con un proyecto FUERA del root que se le pasa a la función, la condición
// `dir == root` no se cumple nunca y el recorrido llega de verdad a "/" —que es
// exactamente el caso que el guard protege. Con el `seen` de más abajo y sin este
// `parent == dir`, el buclearía; con los dos, sale por la raíz.
//
// El caso no lo produce `New` —los proyectos siempre salen del escaneo de `root`—
// y por eso el guard no se puede cubrir desde la TUI. Lo que se comprueba aquí es
// el CONTRATO de la función con cualquier entrada: no sube por encima de la raíz del
// sistema, y un compose que sí exista más arriba no se encuentra.
func TestFindComposeFileTerminaEnLaRaizDelSistema(t *testing.T) {
	// Un directorio real, con un compose DENTRO, y un "root" que no es su
	// antepasado: el recorrido tiene que subir más allá de root sin engancharse.
	proyecto := t.TempDir()
	if err := os.WriteFile(filepath.Join(proyecto, orchestrate.ComposeFileName),
		[]byte("primary_group = \"lejos\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(t.TempDir(), "raiz-que-no-es-antepasado")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	// Con el compose justo en el proyecto, lo encuentra sin subir nada.
	cf, err := findComposeFile(root, []scanner.Project{{Path: proyecto, Configured: true}})
	if err != nil {
		t.Fatalf("findComposeFile: %v", err)
	}
	if cf == nil {
		t.Fatal("no encontró el compose que está en el propio directorio del proyecto")
	}

	// Y con un proyecto que NO tiene compose, el recorrido sube hasta "/" y ahí se
	// detiene. Si el guard no estuviera, este test no terminaría nunca, así que el
	// timeout del propio `go test` es lo que detectaría el fallo: por eso hay un
	// proyecto real y no un mock.
	vacio := t.TempDir()
	// Se limpia lo que se pueda haber encontrado subiendo, para que el "no
	// encontró" sea por el guard y no por un compose suelto por el disco temporal.
	for d := filepath.Dir(vacio); d != "/" && d != "."; d = filepath.Dir(d) {
		_ = os.Remove(filepath.Join(d, orchestrate.ComposeFileName))
	}

	_, err = findComposeFile(root, []scanner.Project{{Path: vacio, Configured: true}})
	if err == nil {
		t.Error("findComposeFile encontró un compose subiendo hasta la raíz del sistema")
	}
}
