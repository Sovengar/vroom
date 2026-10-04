package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Must match gitinfo.parseHEAD's criterion, or the same repo gets two branch names and the auto portless route path depends on which one ran.
func TestShortRefAcortaSoloLasRamasDeHeads(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"rama", "refs/heads/main", "main"},
		{"rama con barra", "refs/heads/feature/login", "feature/login"},
		{"tag", "refs/tags/v1.0", "refs/tags/v1.0"},
		{"remoto", "refs/remotes/origin/main", "refs/remotes/origin/main"},
		{"HEAD suelto", "HEAD", "HEAD"},
		{"vacío", "", ""},
		{"prefijo parcial", "refs/head/main", "refs/head/main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortRef(tt.in); got != tt.want {
				t.Errorf("shortRef(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// HEAD, objects and refs without the core.bare = true marker are not enough, since a stray objects/ directory is the likeliest false positive.
func TestIsBareRepoExigeLosTresYElMarcador(t *testing.T) {
	t.Run("los tres pero sin marcador: NO es bare", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{"HEAD", "objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		write(t, filepath.Join(dir, "config"), "[core]\n\tbare = false\n")
		if IsBareRepo(dir) {
			t.Error("sin core.bare = true NO es un bare repo: cualquier carpeta con objects/ lo parecería")
		}
	})

	t.Run("sin config: NO es bare", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{"HEAD", "objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if IsBareRepo(dir) {
			t.Error("sin config no se puede probar nada: declararlo bare sería adivinar")
		}
	})

	t.Run("un repo normal con .git: NO es bare", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, ".git", "config"), "[core]\n\tbare = true\n")
		if IsBareRepo(dir) {
			t.Error("un directorio con .git tiene trabajo dentro: nunca es un repo bare")
		}
	})

	t.Run("un worktree con .git como fichero: NO es bare", func(t *testing.T) {
		// A linked worktree's .git is a file and its main repo IS bare, which makes this the most confusable case.
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /ruta\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if IsBareRepo(dir) {
			t.Error("un worktree no es un bare repo, aunque su repo principal lo sea")
		}
	})

	t.Run("bare completo: sí lo es", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{"HEAD", "objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		write(t, filepath.Join(dir, "config"), "[core]\n\tbare = true\n")
		if !IsBareRepo(dir) {
			t.Error("los tres ficheros más core.bare = true es un bare repo")
		}
	})

	t.Run("le falta uno de los tres", func(t *testing.T) {
		for _, falta := range []string{"HEAD", "objects", "refs"} {
			dir := t.TempDir()
			for _, n := range []string{"HEAD", "objects", "refs"} {
				if n == falta {
					continue
				}
				if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write(t, filepath.Join(dir, "config"), "[core]\n\tbare = true\n")
			if IsBareRepo(dir) {
				t.Errorf("sin %s no es un bare repo", falta)
			}
		}
	})
}

// The key must be looked up inside [core] only, or a [miapp] bare = true build variable makes the project appear twice in the scan.
func TestHasBareMarkerSoloCuentaLaSeccionCore(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   bool
	}{
		{"core con bare", "[core]\n\tbare = true\n", true},
		{"core con bare y otras claves", "[core]\n\tbare = true\n\tlogallrefupdates = false\n", true},
		{"sección [core] con mayúsculas", "[Core]\n\tbare = true\n", true},
		{"clave en minúsculas", "[core]\n\tBare = true\n", true},
		{"clave repetida: gana la primera que sea verdadera", "[core]\n\tbare = false\n\tbare = true\n", true},
		// MEDIDO: with a repeated key the first true wins, unlike git's last-wins; git never writes the marker twice and reimplementing its precedence would cover a config git cannot produce.
		{"clave repetida: la primera true gana igualmente", "[core]\n\tbare = true\n\tbare = false\n", true},
		{"otra sección", "[miapp]\n\tbare = true\n", false},
		{"core sin la clave", "[core]\n\tfilemode = true\n", false},
		{"sin secciones", "bare = true\n", false},
		{"sección core sin igual", "[core]\n\tbare true\n", false},
		{"comentario al final de la línea", "[core]\n\tbare = true # el marcador\n", true},
		{"línea vacía y espacios", "[core]\n\n   \n\tbare = true\n", true},
		// MEDIDO: a single-quoted value does not count because the quotes are not stripped, though git would accept it; the marker is written by git and never quoted.
		{"valor entre comillas", "[core]\n\tbare = 'true'\n", false},
		{"valor no booleano", "[core]\n\tbare = maybe\n", false},
		{"valor vacío", "[core]\n\tbare =\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := write(t, filepath.Join(t.TempDir(), "config"), tt.config)
			if got := hasBareMarker(path); got != tt.want {
				t.Errorf("hasBareMarker(%q) = %v, want %v", tt.config, got, tt.want)
			}
		})
	}
}

// Fail closed: a false positive adds a row the user notices as a mismatched group count, while a false negative only shows up as an orphan worktree.
func TestHasBareMarkerConConfigIlegibleESFalse(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede leer un fichero sin permiso: el caso no se puede provocar")
	}
	path := write(t, filepath.Join(t.TempDir(), "config"), "[core]\n\tbare = true\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	if hasBareMarker(path) {
		t.Error("un config ilegible no puede probar nada: declararlo bare sería adivinar")
	}
}

// This is what separates a config parser from strings.Cut: a value like C:\Users\yo#mi-repo would lose half the path.
func TestStripConfigCommentRespetaLasComillasSimples(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"sin comentario", "[core]\n\tbare = true", "[core]\n\tbare = true"},
		{"comentaje con hash", "\tbare = true # esto", "\tbare = true "},
		{"comentario con punto y coma", "\tbare = true ; esto", "\tbare = true "},
		{"hash dentro de comillas", "\tpath = 'C:/Users/yo#repo'", "\tpath = 'C:/Users/yo#repo'"},
		{"punto y coma dentro de comillas", "\tpath = 'a;b'", "\tpath = 'a;b'"},
		{"comentario después de comillas", "\tpath = 'a' # nota", "\tpath = 'a' "},
		// MEDIDO: an unterminated quote makes everything after it quoted so the hash survives, which is the safe direction for a truncated config.
		{"comilla sin cerrar", "\tpath = 'a#b", "\tpath = 'a#b"},
		{"comentario al principio", "# todo comentario", ""},
		{"vacío", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripConfigComment(tt.in); got != tt.want {
				t.Errorf("stripConfigComment(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// git accepts many truthy spellings, and understanding only "true" would drop a bare = 1 repo from the scan and orphan its worktrees.
func TestIsTrueConfigValueAceptaLasFormasDeGit(t *testing.T) {
	// MEDIDO: no TrimSpace here, because callers already trim it and a second read of the same rule would live in two places.
	for _, v := range []string{"true", "TRUE", "True", "1", "yes", "on"} {
		if !isTrueConfigValue(v) {
			t.Errorf("isTrueConfigValue(%q) = false: git lo acepta como verdadero", v)
		}
	}
	for _, v := range []string{"false", "FALSE", "0", "no", "off", "", "maybe", "2"} {
		if isTrueConfigValue(v) {
			t.Errorf("isTrueConfigValue(%q) = true: no es un valor verdadero de git", v)
		}
	}
	if isTrueConfigValue("  true  ") {
		t.Error("isTrueConfigValueTrimmed")
	}
}

// The hand-made file tests check the conjunction; this one checks that the conjunction describes what git actually writes.
func TestIsBareRepoConUnRepoRealDeVerdad(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git no disponible: la heurística se probaría contra ficheros a mano, no contra un repo real")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "repo.git")
	gitHere(t, base, "init", "--bare", "-q", dir)

	if !IsBareRepo(dir) {
		t.Error("un `git init --bare` real no se reconoce como bare repo: la heurística no describe lo que git escribe")
	}

	normal := filepath.Join(base, "normal")
	gitHere(t, base, "init", "-q", "-b", "main", normal)
	if IsBareRepo(normal) {
		t.Error("un `git init` normal se está tomando por un bare repo")
	}
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func gitHere(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-c", "user.email=test@test", "-c", "user.name=test", "-c", "protocol.file.allow=always"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
