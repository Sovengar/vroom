package gitinfo

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// readHEAD y parseHEAD: la rama que la TUI enseña en la cabecera de cada
// proyecto.
//
// La rama no es decorativa: en route_mode auto decide el NOMBRE de la ruta de
// portless, así que un error aquí produce direcciones distintas para el mismo
// servicio. Y todo lo que hacen es leer dos ficheros, de modo que los bordes se
// provocan con un árbol de ficheros a mano en vez de con repos reales.
// ---------------------------------------------------------------------------

// TestReadHEADEnUnRepoNormal: .git como DIRECTORIO, que es el caso normal.
func TestReadHEADEnUnRepoNormal(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")

	head, ok := readHEAD(dir)
	if !ok {
		t.Fatal("readHEAD = false en un repo normal con HEAD")
	}
	if head != "ref: refs/heads/main" {
		t.Errorf("head = %q, want el HEAD sin el salto final", head)
	}
}

// TestReadHEADEnUnWorktreeConGitirRelativo: .git como FICHERO con un puntero
// RELATIVO, que es lo que escribe `git worktree add`.
//
// Es el caso que hace que readHEAD tenga dos ramas: si no resolviera el puntero,
// la rama de un worktree saldría vacía y la ruta de portless caería al nombre
// del proyecto — que es justo la colisión que el modo auto existe para evitar.
func TestReadHEADEnUnWorktreeConGitirRelativo(t *testing.T) {
	dir := t.TempDir()
	main := t.TempDir()
	write(t, filepath.Join(main, "HEAD"), "ref: refs/heads/feature/login\n")
	wt := filepath.Join(main, "worktrees", "feature")
	write(t, filepath.Join(wt, "HEAD"), "HEAD del worktree\n")
	write(t, filepath.Join(dir, ".git"), "gitdir: "+relativeTo(dir, wt)+"\n")

	head, ok := readHEAD(dir)
	if !ok {
		t.Fatal("readHEAD = false en un worktree con puntero relativo")
	}
	if head != "HEAD del worktree" {
		t.Errorf("head = %q: se leyó el HEAD equivocado", head)
	}
}

// TestReadHEADEnUnWorktreeConGitirAbsoluto: el puntero también puede ser
// absoluto, y es el caso de un worktree enlazado desde otro directorio.
func TestReadHEADEnUnWorktreeConGitirAbsoluto(t *testing.T) {
	dir := t.TempDir()
	wt := t.TempDir()
	write(t, filepath.Join(wt, "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(dir, ".git"), "gitdir: "+wt+"\n")

	head, ok := readHEAD(dir)
	if !ok {
		t.Fatal("readHEAD = false con un puntero absoluto")
	}
	if head != "ref: refs/heads/main" {
		t.Errorf("head = %q", head)
	}
}

// TestReadHEADRechazaLoQueNoEsUnPunteroAGit: los cuatro rechazos, y cada uno
// importa porque aceptarlo daría una rama inventada.
//
// Y una rama inventada es peor que ninguna: en route_mode auto, la rama se
// concatena al nombre del proyecto para formar el nombre de la ruta. Una rama
// equivocada significa una ruta que colisiona con la de otro worktree.
func TestReadHEADRechazaLoQueNoEsUnPunteroAGit(t *testing.T) {
	t.Run("sin .git", func(t *testing.T) {
		if _, ok := readHEAD(t.TempDir()); ok {
			t.Error("un directorio sin .git no es un repo")
		}
	})

	t.Run(".git sin el prefijo gitdir:", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, ".git"), "/otra/ruta\n")
		if _, ok := readHEAD(dir); ok {
			t.Error("un .git sin 'gitdir:' no es un puntero de worktree")
		}
	})

	t.Run("puntero a un gitdir sin HEAD", func(t *testing.T) {
		dir := t.TempDir()
		// El gitdir existe pero no tiene HEAD: un worktree a medio crear.
		empty := t.TempDir()
		write(t, filepath.Join(dir, ".git"), "gitdir: "+empty+"\n")
		if _, ok := readHEAD(dir); ok {
			t.Error("un gitdir sin HEAD no tiene rama: un worktree a medio crear no puede inventarla")
		}
	})

	t.Run(".git como directorio sin HEAD", func(t *testing.T) {
		// `git init` crea el directorio antes de escribir HEAD. Until entonces no
		// hay rama, y decir que la hay sería inventar.
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, ok := readHEAD(dir); ok {
			t.Error("un .git recién creado sin HEAD no tiene rama")
		}
	})
}

// TestParseHEADInterpretaLasTresFormasYRechazaElResto: ref, sha detached y basura.
//
// La forma detached importa porque `git checkout <sha>` deja el repo ahí, y la
// TUI tiene que distinguir "está en una rama" de "está suelto": son estados de
// trabajo distintos y el usuario necesita verlo.
func TestParseHEADInterpretaLasTresFormasYRechazaElResto(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"rama", "ref: refs/heads/main", "main"},
		{"rama con barra", "ref: refs/heads/feature/login", "feature/login"},
		{"rama con espacios alrededor", "ref:   refs/heads/main  ", "main"},
		{"ref fuera de heads", "ref: refs/tags/v1", "refs/tags/v1"},
		{"sha completo detached", "abc1234def5678901234567890123456789012ab", "abc1234 (detached)"},
		{"sha corto detached", "abc1234", "abc1234 (detached)"},
		{"basura", "esto no es un HEAD", ""},
		{"vacío", "", ""},
		{"hex demasiado corto", "abc", ""},
		{"no hex", "zzzzzzz", ""},
		{"hex con mayusculas", "ABC1234", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseHEAD(tt.in); got != tt.want {
				t.Errorf("parseHEAD(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestBranchConEntradaRaraNoRevienta: la TUI llama a Branch por cada fila del
// escaneo, incluidos proyectos que no son repos.
func TestBranchConEntradaRaraNoRevienta(t *testing.T) {
	// Un directorio que no existe en absoluto: la TUI lo llama igual mientras el
	// árbol cambia.
	if got := Branch(filepath.Join(t.TempDir(), "nada")); got != "" {
		t.Errorf("Branch de un path inexistente = %q, want cadena vacía", got)
	}
	// Un .git que es un directorio vacío.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Branch(dir); got != "" {
		t.Errorf("Branch de un .git sin HEAD = %q, want cadena vacía", got)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// relativeTo devuelve target relativo a base, para escribir el puntero como lo
// escribiría git.
func relativeTo(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}

// TestReadHEADConUnGitFileIlegible: un `.git` que es un FICHERO pero no se puede
// leer no es un repo.
//
// Es la diferencia entre "no hay repo" y "hay algo que no se puede leer", y aquí
// las dos dan lo mismo porque el contrato de readHEAD es un bool: o hay rama, o
// no la hay. Aceptarlo sin leer sería inventar la rama, que es lo que produce la
// colisión de rutas en portless auto.
//
// Se salta como root, que puede leer cualquier fichero.
func TestReadHEADConUnGitFileIlegible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede leer un fichero sin permiso: el caso no se puede provocar")
	}
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /alguna/ruta\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, ok := readHEAD(dir); ok {
		t.Error("un .git ilegible no puede dar una rama: sería inventada")
	}
}
