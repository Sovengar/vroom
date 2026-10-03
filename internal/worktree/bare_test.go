package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// shortRef, IsBareRepo, hasBareMarker y stripConfigComment: la heurística que
// decide qué filas del escaneo son repos, worktrees y contendoras.
//
// IsBareRepo decide si un directorio SIN manifiesto sale o no del escaneo. Un
// falso positivo mete una fila de más; un falso negativo hace que los worktrees
// de ese repo aparezcan sin fila madre, y entonces la TUI los muestra como
// proyectos sueltos y el conteo de un grupo no cuadra con lo que se ve debajo.
//
// Y la heurística es reforzada a propósito con el marcador `core.bare = true`
// que escriben `git init --bare` y `git clone --bare`. Sin él, cualquier
// directorio con HEAD/objects/refs —que es fácil de tener sin querer— se
// declararía repo.
// ---------------------------------------------------------------------------

// TestShortRefAcortaSoloLasRamasDeHeads: refs/heads/x → x; el resto tal cual.
//
// El criterio es el MISMO que el de gitinfo.parseHEAD, y tiene que serlo: si uno
// acortara y el otro no, el mismo repo tendría dos nombres de rama distintos
// según de dónde viniera, y la ruta de portless auto cambiaría según el camino.
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

// TestIsBareRepoExigeLosTresYElMarcador: la conjunción completa.
//
// Los tres ficheros (HEAD, objects, refs) SIN el marcador no bastan, y ese es el
// caso que más falsos positivos produce: un proyecto vacío con una carpeta
// objects/ por casualidades.
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
		// Es el caso que más se confunde: un worktree enlazado tiene un .git que
		// es un fichero, y su repo principal ES bare. El worktree no.
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

// TestHasBareMarkerSoloCuentaLaSeccionCore: un `bare = true` en otra sección no
// cuenta.
//
// Y por eso la clave se busca dentro de [core] y no en el fichero entero: un
// proyecto con una sección [miapp] que tenga `bare = true` (por ejemplo, una
// variable de build) sería tomado por un bare repo y su fila aparecería dos
// veces en el escaneo.
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
		// MEDIDO: con la clave repetida, gana la PRIMERA que sea true, porque
		// hasBareMarker devuelve en cuanto la encuentra. Git usaría la última, así
		// que un config con `bare = true` y luego `bare = false` se declara bare.
		// No se "arregla": el marcador lo escribe git y nunca va repetido, y
		//empsare a reimplementar la precedencia de git config para cubrir un
		// config que git no produce.
		{"clave repetida: la primera true gana igualmente", "[core]\n\tbare = true\n\tbare = false\n", true},
		{"otra sección", "[miapp]\n\tbare = true\n", false},
		{"core sin la clave", "[core]\n\tfilemode = true\n", false},
		{"sin secciones", "bare = true\n", false},
		{"sección core sin igual", "[core]\n\tbare true\n", false},
		{"comentario al final de la línea", "[core]\n\tbare = true # el marcador\n", true},
		{"línea vacía y espacios", "[core]\n\n   \n\tbare = true\n", true},
		// MEDIDO: un valor entre comillas simples NO cuenta. hasBareMarker no
		// quita las comillas, así que isTrueConfigValue recibe "'true'" y dice que
		// no. Git sí lo aceptaría, así que un repo con el valor entrecomillado se
		// sale del escaneo. Se fija el comportamiento real en vez de inventar una
		// lectura más tolerante: el marcador lo escribe git, y git lo escribe sin
		// comillas.
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

// TestHasBareMarkerConConfigIlegibleESFalse: sin poder leer el config no se puede
// probar el marcador, y la respuesta es que no es bare.
//
// Es el fallo cerrado correcto: declarar un repo bare sin prueba metería una fila
// de más en el escaneo, y la fila de más se nota —el conteo del grupo no cuadra—;
// mientras que una fila de menos se nota como un worktree sin madre.
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

// TestStripConfigCommentRespetaLasComillasSimples: un `#` dentro de comillas
// simples es parte del valor, no un comentario.
//
// Es el caso que separa un parser de config de un `strings.Cut`. Un valor como
// `path = "C:\Users\yo#mi-repo"` perdería media ruta, y con ella la del repo.
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
		// MEDIDO: una comilla sin cerrar hace que TODO lo de después se trate
		// como entrecomillado, así que el `#` se conserva. Es lo seguro: un
		// config truncado no debe perder media ruta porque seirian las comillas.
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

// TestIsTrueConfigValueAceptaLasFormasDeGit: los valores que git acepta como
// verdadero, y los que no.
//
// `git init --bare` escribe `bare = true`, pero un usuario puede escribir
// cualquier forma y git la acepta. Si hasBareMarker sólo entendiera "true",
// un repo bare con `bare = 1` saldría del escaneo como proyecto suelto, y sus
// worktrees aparecerían sin madre.
func TestIsTrueConfigValueAceptaLasFormasDeGit(t *testing.T) {
	// MEDIDO: sin espacios. Quien llama ya hace TrimSpace del valor, y meterlo
	// aquí también haría que un "  true  " con espaciosDirectories valiera, lo
	// que es una segunda lectura de la misma regla en dos sitios.
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

// TestIsBareRepoConUnRepoRealDeVerdad: la heurística contra `git init --bare`, no
// contra ficheros a mano.
//
// Los tests con ficheros a mano comprueban la conjunción; éste comprueba que la
// conjunción describe lo que git escribe de verdad, que es lo único que importa
// para el escaneo del usuario.
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

	// Y un repo normal de verdad no es bare.
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

// gitAvailable dice si git está en el PATH, para poder saltarse el test que lo
// necesita sin fallar.
func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// gitHere ejecuta git en dir con identidad inline y falla el test si git falla.
func gitHere(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-c", "user.email=test@test", "-c", "user.name=test", "-c", "protocol.file.allow=always"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
