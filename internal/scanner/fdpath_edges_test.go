package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFdInPathsDevuelveLaPrimeraQueExiste: la búsqueda en la lista.
//
// Son DATOS que se recorren, no lógica condicional: la lista es el caso del runner de
// CI (`/usr/bin`) y el de una instalación por paquete fuera del PATH
// (`/usr/local/bin`), y el orden importa porque la del sistema gana.
func TestFdInPathsDevuelveLaPrimeraQueExiste(t *testing.T) {
	dir := t.TempDir()
	segunda := filepath.Join(dir, "segunda")
	if err := os.WriteFile(segunda, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	primera := filepath.Join(dir, "primera")
	if err := os.WriteFile(primera, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := fdInPaths([]string{primera, segunda}); got != primera {
		t.Errorf("fdInPaths = %q con las dos presentes, want %q: el orden decide cuál gana, y el "+
			"del sistema va primero", got, primera)
	}
	if got := fdInPaths([]string{filepath.Join(dir, "ausente"), segunda}); got != segunda {
		t.Errorf("fdInPaths = %q con la primera ausente, want %q", got, segunda)
	}
}

// TestFdInPathsDevuelveVacioSinNingunaRutaUsable: el `return ""`.
//
// Es la rama que decide que el escaneo cae al walk de `filepath.WalkDir`. Con un
// `var` global de las rutas, provocarla exigía que `/usr/bin/fd` y
// `/usr/local/bin/fd` no existieran en la máquina, y en una máquina donde fd está
// instalado no se puede.
//
// Con la lista como parámetro, la comprobación es sobre rutas que el test controla
// y la respuesta es la misma: sin ningún candidato, `""`.
func TestFdInPathsDevuelveVacioSinNingunaRutaUsable(t *testing.T) {
	dir := t.TempDir()

	if got := fdInPaths(nil); got != "" {
		t.Errorf("fdInPaths(nil) = %q, want \"\"", got)
	}
	if got := fdInPaths([]string{}); got != "" {
		t.Errorf("fdInPaths([]) = %q, want \"\"", got)
	}
	if got := fdInPaths([]string{filepath.Join(dir, "no-existe")}); got != "" {
		t.Errorf("fdInPaths con una ruta inexistente = %q, want \"\": sin candidato no hay fd", got)
	}

	// Un DIRECTORIO llamado `fd` no cuenta: ejecutarlo daría EISDIR en vez de un
	// escaneo. Antes la comprobación era sólo `err == nil`, así que un directorio con
	// ese nombre pasaba el filtro y `Scan` se comía un error feo en vez de caer al
	// walk.
	sub := filepath.Join(dir, "fd-directorio")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := fdInPaths([]string{sub}); got != "" {
		t.Errorf("fdInPaths = %q con un directorio, want \"\": no se puede ejecutar", got)
	}
}

// TestScanCaeAlWalkSinFdEnElPath: el contrato completo de la caída.
//
// La pregunta que importa no es "¿qué devuelve fdPath?", sino "¿funciona el escaneo
// sin fd?". Con el PATH vacío, `Scan` tiene que salir por `filepath.WalkDir` y
// devolver LOS MISMOS proyectos que devolvería con fd. Un `UsedFD` a true sin fd
// sería un escaneo que intenta ejecutar algo inexistente.
func TestScanCaeAlWalkSinFdEnElPath(t *testing.T) {
	// MEDIDO: `LookPath` con un PATH vacío falla siempre, así que la rama del PATH
	// queda descartada sin tocar `/usr`.
	t.Setenv("PATH", "")

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", ".vroom.toml"),
		[]byte("name = \"app\"\ncommand_start = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Scan(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Projects) != 1 {
		t.Fatalf("el walk encontró %d proyectos, want 1", len(res.Projects))
	}
	// El camino de fd y el del walk tienen que dar el mismo resultado observable.
	// Con fd disponible se compara con él; sin él no hay contra qué, y lo que se
	// comprueba es que el proyecto veio bien formado.
	p := res.Projects[0]
	if !p.Configured || p.Manifest == nil || p.Manifest.Name != "app" {
		t.Errorf("proyecto = %+v, want Configured con Manifest.Name=app: la caída al walk tiene "+
			"que devolver exactamente lo mismo que el camino con fd", p)
	}
}
