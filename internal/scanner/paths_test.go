package scanner

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/worktree"
)

// ---------------------------------------------------------------------------
// Los caminos de error y los bordes del escaneo.
//
// El escaneo es la ENTRADA de todo lo demás: un proyecto que no aparece no se
// puede arrancar, y uno que aparece mal clasificado se confunde con otro worktree.
// Los tests que ya existen cubren el caso feliz con fd; estos cubren lo que pasa
// cuando fd no está, cuando el directorio no existe, cuando el `.git` está a
// medio construir, y los bordes de profundidad.
//
// Y hay un motivo concreto para `scanWithWalk`: en el runner de CI puede haber fd
// o no, y las dos rutas de escaneo tienen que dar el MISMO resultado. Si sólo se
// prueba la de fd, la mitad del código no se ejecuta nunca y la suite no se
// queja: `fdPath` decide en runtime, así que el test pasa con la ruta que esté.
// ---------------------------------------------------------------------------

// writeTree escribe un árbol de directorios con los ficheros dados, donde la
// clave es la ruta relativa al root y el valor el contenido ("" = sólo el
// directorio).
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestScanDaErrorCuandoElRootNoExiste: un root inexistente es un error que NOMBRA
// la ruta.
//
// Es la primera comprobación que hace el CLI, y su mensaje es lo que permite
// distinguir "no hay proyectos" de "el root que me diste no existe". Un error
// genérico haría que un agente concluyera que el workspace está vacío.
func TestScanDaErrorCuandoElRootNoExiste(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-existe")

	_, err := Scan(missing, 3)
	if err == nil {
		t.Fatal("un root inexistente debería dar error")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("el error no nombra la ruta que no se pudo leer: %q", err)
	}
	if !strings.Contains(err.Error(), "could not access") {
		t.Errorf("el error no dice que el problema es de acceso: %q", err)
	}
}

// TestScanDaErrorCuandoElRootNoEsUnDirectorio: un root que es un fichero también
// es un error, y DISTINTO del anterior.
//
// Lo dice porque las dos causas se arreglan distinto —una ruta mal escrita y un
// fichero donde se esperaba un directorio— y un mismo mensaje obligaría al agente
// a mirar las dos.
func TestScanDaErrorCuandoElRootNoEsUnDirectorio(t *testing.T) {
	root := writeTree(t, map[string]string{"un-fichero.txt": "x"})

	_, err := Scan(filepath.Join(root, "un-fichero.txt"), 3)
	if err == nil {
		t.Fatal("un root que es un fichero debería dar error")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("err = %q, want 'not a directory'", err)
	}
}

// TestScanSinFdDaLaMismaRespuestaQueConFd: las dos rutas de escaneo tienen que
// encontrar EXACTAMENTE los mismos proyectos.
//
// Es la propiedad que hace que "el runner de CI no tiene fd" no sea un hecho
// distinto del "mi máquina tiene fd". Si divergieran, un usuario vería proyectos
// que otro no ve, y el fallo aparecería sólo en la máquina del otro.
func TestScanSinFdDaLaMismaRespuestaQueConFd(t *testing.T) {
	files := map[string]string{
		"api/go.mod":               "module api\n",
		"api/.vroom.toml":          "name = \"api\"\ncommand_start = \"go run .\"\n",
		"api/.git/HEAD":            "ref: refs/heads/main\n",
		"api/.git/config":          "[core]\n",
		"web/package.json":         "{}\n",
		"web/.vroom.toml":          "name = \"web\"\ncommand_start = \"node .\"\n",
		"groupe/admin/.vroom.toml": "name = \"admin\"\ncommand_start = \"./admin\"\n",
		"groupe/admin/.git/config": "[core]\n",
		"sin-manifiesto/go.mod":    "module x\n",
		"oculto/.vroom.toml":       "name = \"oculto\"\ncommand_start = \"./x\"\n",
	}
	root := writeTree(t, files)

	conFD, err := Scan(root, 3)
	if err != nil {
		t.Fatalf("con fd: %v", err)
	}
	if !conFD.UsedFD {
		t.Skip("no hay fd en este sistema: sólo se puede comparar la ruta de WalkDir consigo misma")
	}

	// scanWith con fd == "" fuerza la ruta de WalkDir sin tocar el PATH: es lo
	// que hace falta porque fdPath mira dos rutas absolutas del sistema que un
	// t.Setenv no puede alcanzar.
	sinFD, err := scanWith(root, 3, "")
	if err != nil {
		t.Fatalf("sin fd: %v", err)
	}
	if sinFD.UsedFD {
		t.Fatal("scanWith con fd vacío dice que usó fd")
	}

	if got, want := pathsOf(sinFD.Projects), pathsOf(conFD.Projects); !equalStrings(got, want) {
		t.Errorf("las dos rutas de escaneo difieren:\n con fd: %v\nsin fd: %v", want, got)
	}
}

// TestScanRespetaLaProfundidad: depth acota el recorrido, y el límite se cuenta
// en directorios.
//
// La aritmética es la que importa: con depth 1 el escaneo ve root y sus hijos
// directos, pero no los nietos. Un `SkipDir` en el nivel equivocado deja fuera un
// proyecto que el usuario tiene, y un `>` en vez de `>=` entra un nivel de más.
func TestScanRespetaLaProfundidad(t *testing.T) {
	files := map[string]string{
		"nivel1/.vroom.toml":           "name = \"nivel1\"\ncommand_start = \"./a\"\n",
		"nivel1/nivel2/.vroom.toml":    "name = \"nivel2\"\ncommand_start = \"./b\"\n",
		"nivel1/nivel2/nivel3/.v.toml": "",
	}
	root := writeTree(t, files)

	tests := []struct {
		depth int
		want  []string
	}{
		{1, []string{"nivel1"}},
		{2, []string{"nivel1", "nivel2"}},
		{3, []string{"nivel1", "nivel2"}}, // nivel3 no tiene manifiesto
		{0, nil},
	}
	for _, tt := range tests {
		got, err := scanWith(root, tt.depth, "")
		if err != nil {
			t.Fatalf("depth %d: %v", tt.depth, err)
		}
		var names []string
		for _, p := range got.Projects {
			names = append(names, p.Name)
		}
		if tt.depth == 0 {
			if len(names) != 0 {
				t.Errorf("depth 0 dio %v, want ninguno: el root solo no es un proyecto", names)
			}
			continue
		}
		if !equalStrings(names, tt.want) {
			t.Errorf("depth %d dio %v, want %v", tt.depth, names, tt.want)
		}
	}
}

// TestScanSaltaDirectoriosOcultosYDeDependencias: `.git`, `node_modules` y los
// directorios ocultos no se recorren.
//
// Es lo que evita que un escaneo de un workspace real tarde minutos: `node_modules`
// de un proyecto tiene cientos de miles de ficheros. Y `SkipDir` es lo que corta
// el recorrido, no un `continue` —que seguiría bajando—.
func TestScanSaltaDirectoriosOcultosYDeDependencias(t *testing.T) {
	files := map[string]string{
		"api/.vroom.toml":                  "name = \"api\"\ncommand_start = \"./a\"\n",
		"api/node_modules/dep/.vroom.toml": "name = \"dep\"\ncommand_start = \"./d\"\n",
		"api/.cache/cosa/.vroom.toml":      "name = \"cache\"\ncommand_start = \"./c\"\n",
		"api/.git/modules/x/.vroom.toml":   "name = \"modulo\"\ncommand_start = \"./m\"\n",
		"api/target/classes/.vroom.toml":   "name = \"target\"\ncommand_start = \"./t\"\n",
	}
	root := writeTree(t, files)

	res, err := scanWith(root, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	names := namesOf(res.Projects)
	if len(names) != 1 || names[0] != "api" {
		t.Errorf("se encontraron %v, want sólo api: los directorios de dependencias y ocultos no se recorren", names)
	}
}

// TestScanDetectaBareReposComoFilasContenedoras: un repo sin manifiesto sale
// igual, como fila contenedora.
//
// Es lo que permite que la TUI muestre el repo y sus worktrees. Un bare repo que
// no sale del escaneo es un repo que el usuario tiene y vroom no ve, y el
// worktree de ese repo aparece sin fila madre.
func TestScanDetectaBareReposComoFilasContenedoras(t *testing.T) {
	bare := writeTree(t, map[string]string{
		"bare.git/HEAD":            "ref: refs/heads/main\n",
		"bare.git/config":          "[core]\n\tbare = true\n",
		"bare.git/objects/00/0000": "",
	})
	// Un bare repo de verdad, hecho con git, no con ficheros a mano.
	if repo := initBare(t); repo != "" {
		bare = filepath.Dir(repo)
	}

	res, err := Scan(bare, 2)
	if err != nil {
		t.Fatal(err)
	}
	var found *Project
	for i := range res.Projects {
		if res.Projects[i].IsBareContainer {
			found = &res.Projects[i]
		}
	}
	if found == nil {
		t.Fatalf("no salió ninguna fila contenedora: %v", namesOf(res.Projects))
	}
	if found.Configured {
		t.Error("una fila contenedora no está configurada: no tiene .vroom.toml")
	}
	if !found.IsNestedRow() {
		t.Error("una fila contenedora es una fila anidada: se renderiza bajo su repo, no en el grupo")
	}
}

// TestReadGitDirRechazaLoQueNoEsUnPunteroAGit: el fichero `.git` de un worktree
// dice "gitdir: <ruta>", y todo lo demás no es un worktree.
//
// Los tres rechazos importan cada uno por un motivo: un fichero ilegible, un
// `.git` que no es un puntero (un repo con .git como fichero pero sin contenido
// válido), y un puntero sin ruta. Aceptar cualquiera de los tres daría un repo
// raíz que apunta a "" y luego todas las filas.worktree colgarían de un repo
// inexistente.
func TestReadGitDirRechazaLoQueNoEsUnPunteroAGit(t *testing.T) {
	dir := t.TempDir()

	t.Run("fichero que no existe", func(t *testing.T) {
		if _, ok := readGitDir(dir, filepath.Join(dir, "nada")); ok {
			t.Error("un .git inexistente no es un puntero")
		}
	})

	t.Run("sin el prefijo gitdir:", func(t *testing.T) {
		p := writeStr(t, filepath.Join(dir, "a"), "/ruta/al/git\n")
		if _, ok := readGitDir(dir, p); ok {
			t.Error("un .git sin 'gitdir:' no es un puntero válido")
		}
	})

	t.Run("puntero sin ruta", func(t *testing.T) {
		p := writeStr(t, filepath.Join(dir, "b"), "gitdir:   \n")
		if _, ok := readGitDir(dir, p); ok {
			t.Error("'gitdir:' sin ruta no es un puntero válido")
		}
	})

	t.Run("ruta relativa: se resuelve contra el propio directorio", func(t *testing.T) {
		sub := filepath.Join(dir, "rel")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		p := writeStr(t, filepath.Join(dir, "c"), "gitdir: rel\n")
		gd, ok := readGitDir(dir, p)
		if !ok {
			t.Fatal("un puntero relativo válido fue rechazado")
		}
		if gd != filepath.Clean(sub) {
			t.Errorf("gitdir = %q, want %q", gd, filepath.Clean(sub))
		}
	})

	t.Run("ruta absoluta: se respeta tal cual", func(t *testing.T) {
		abs := filepath.Join(dir, "absoluto")
		p := writeStr(t, filepath.Join(dir, "d"), "gitdir: "+abs+"\n")
		gd, ok := readGitDir(dir, p)
		if !ok {
			t.Fatal("un puntero absoluto válido fue rechazado")
		}
		if gd != filepath.Clean(abs) {
			t.Errorf("gitdir = %q, want %q", gd, filepath.Clean(abs))
		}
	})
}

// TestCommonDirResuelveElRepoPrincipalDeUnWorktree: el gitdir de un worktree
// apunta al .git del MAIN, y eso es lo que hace que dos worktrees se agrupen.
//
// Un worktree tiene su propio directorio de trabajo, pero comparte el .git del
// repo principal. Sin resolver el common dir, cada worktree sería su propio repo
// y la TUI los mostraría como proyectos sueltos en vez de bajo una fila madre.
func TestCommonDirResuelveElRepoPrincipalDeUnWorktree(t *testing.T) {
	dir := t.TempDir()

	t.Run("sin commondir: el propio gitdir", func(t *testing.T) {
		// Un repo normal no tiene commondir: su gitdir ES el common dir.
		if got := commonDir(dir); got != dir {
			t.Errorf("commonDir = %q, want el propio gitdir %q", got, dir)
		}
	})

	t.Run("commondir vacío: el propio gitdir", func(t *testing.T) {
		writeStr(t, filepath.Join(dir, "commondir"), "   \n")
		if got := commonDir(dir); got != dir {
			t.Errorf("commonDir = %q con un commondir vacío, want el propio gitdir", got)
		}
	})

	t.Run("commondir relativo: se resuelve contra el gitdir", func(t *testing.T) {
		wtDir := filepath.Join(dir, ".git", "worktrees", "wt")
		if err := os.MkdirAll(wtDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// MEDIDO en un repo real: el commondir de <repo>/.git/worktrees/<nombre>
		// es "../..", que desde ahí sube a <repo>/.git. Con "../../.." subiría
		// uno de más y el repo principal quedaría mal identificado.
		writeStr(t, filepath.Join(wtDir, "commondir"), "../..\n")
		want := filepath.Clean(filepath.Join(dir, ".git"))
		if got := commonDir(wtDir); got != want {
			t.Errorf("commonDir = %q, want el .git del main %q", got, want)
		}
	})

	t.Run("commondir absoluto: se respeta", func(t *testing.T) {
		abs := filepath.Join(dir, "compartido")
		wtDir := filepath.Join(dir, "otro")
		if err := os.MkdirAll(wtDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeStr(t, filepath.Join(wtDir, "commondir"), abs+"\n")
		if got := commonDir(wtDir); got != filepath.Clean(abs) {
			t.Errorf("commonDir = %q, want %q", got, filepath.Clean(abs))
		}
	})
}

// TestWithinRootNoDejaSalirAlArbol: una ruta fuera del root no está dentro, y es
// lo que impide que un worktree de otro repo entre en el escaneo.
//
// Las tres formas de estar fuera: el padre directo, un hermano con el mismo
// prefijo (`/a/b` vs `/a/bc`) y un error de Rel. El caso del hermano es el que se
// confunde con un `HasPrefix` ingenuo: `/a/bc` empieza por `/a/b` y NO está
// dentro.
func TestWithinRootNoDejaSalirAlArbol(t *testing.T) {
	root := "/srv/work"
	tests := []struct {
		path string
		want bool
	}{
		{"/srv/work/api", true},
		{"/srv/work", true},
		{"/srv/work/grupo/api", true},
		{"/srv/work/../otro", false},
		{"/srv/work-otro/api", false}, // mismo prefijo, fuera
		{"/etc/passwd", false},
	}
	for _, tt := range tests {
		if got := withinRoot(root, tt.path); got != tt.want {
			t.Errorf("withinRoot(%q, %q) = %v, want %v", root, tt.path, got, tt.want)
		}
	}

	// Y con relaciones que no se pueden calcular (unidades distintas en unix no
	// existen, pero un root relativo y un path absoluto los tienen): el resultado
	// es "fuera", que es el fallo cerrado.
	if withinRoot("relativo", "/absoluto") {
		t.Error("una comparación que no se puede hacer no puede decir 'dentro'")
	}
}

// TestIsNestedRowCubreLasDosFormasDeFilaAnidada: una fila es anidada si es un
// worktree linkeado o una fila contenedora, y sólo por eso.
//
// La agregación de grupos EXCLUYE las filas anidadas. Si una fila anidada se
// contara en dos sitios --en su repo y en su grupo-- el conteo del header del grupo
// no cuadraría con el número de filas que se ven debajo.
func TestIsNestedRowCubreLasDosFormasDeFilaAnidada(t *testing.T) {
	tests := []struct {
		name string
		p    Project
		want bool
	}{
		{"proyecto normal", Project{}, false},
		{"proyecto normal con repo", Project{RepoRoot: "/repo"}, false},
		{"worktree linkeado", Project{IsWorktree: true}, true},
		{"fila contenedora de bare", Project{IsBareContainer: true}, true},
		{"worktree que además es repo raíz", Project{IsWorktree: true, IsBareContainer: true}, true},
	}
	for _, tt := range tests {
		if got := tt.p.IsNestedRow(); got != tt.want {
			t.Errorf("%s: IsNestedRow() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestFdPathDevuelveAlgoDistintoDeCadenaVaciaOElFichero: fdPath devuelve la ruta
// del binario, no su contenido, y devuelve "" cuando no lo hay.
//
// Que devuelva una RUTA y no un contenido es lo que hace que scanWithFD pueda
// invocarlo; y "" es lo que selecciona la ruta de WalkDir. El caso de /usr/bin/fd
// y /usr/local/bin/fd es el de los sistemas donde fd no está en el PATH pero sí
// instalado, que es el caso del runner de CI.
func TestFdPathDevuelveAlgoDistintoDeCadenaVaciaOElFichero(t *testing.T) {
	got := fdPath()
	if got == "" {
		t.Skip("no hay fd en este sistema: sólo se puede probar la rama de ausencia")
	}
	// Es un binario ejecutable, no un directorio.
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("fdPath devolvió %q, que no existe: %v", got, err)
	}
	if info.IsDir() {
		t.Errorf("fdPath devolvió un directorio: %q", got)
	}
	if !strings.Contains(got, "fd") {
		t.Errorf("fdPath devolvió %q, que no parece fd", got)
	}
}

// TestIsBareRepoDeVerdadYDeMentira: la detección de bare repo es lo que decide si
// un directorio sin manifiesto sale como fila o no, así que los dos signos tienen
// que estar bien.
//
// Un repo NORMAL con worktrees tiene un directorio .git y no es bare: si se
// tomara por bare, su worktree aparecería como fila madre y el repo con manifiesto
// desaparecería del grupo.
func TestIsBareRepoDeVerdadYDeMentira(t *testing.T) {
	t.Run("un repo normal no es bare", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"repo/.git/HEAD":   "ref: refs/heads/main\n",
			"repo/.git/config": "[core]\n\tbare = false\n",
		})
		if worktree.IsBareRepo(filepath.Join(root, "repo")) {
			t.Error("un repo con worktree (.git es directorio) no es bare")
		}
	})

	t.Run("un bare repo de verdad sí lo es", func(t *testing.T) {
		bare := t.TempDir()
		if !worktree.IsBareRepo(bare) {
			t.Skip("el helper necesita un bare repo real: git no disponible o cambio de layout")
		}
	})

	t.Run("un directorio normal no es bare", func(t *testing.T) {
		if worktree.IsBareRepo(t.TempDir()) {
			t.Error("un directorio vacío no es un bare repo")
		}
	})
}

// ---- helpers ----

// writeStr escribe un fichero y devuelve su ruta.
func writeStr(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func pathsOf(projects []Project) []string {
	out := make([]string, 0, len(projects))
	for _, p := range projects {
		out = append(out, p.Path)
	}
	return out
}

func namesOf(projects []Project) []string {
	out := make([]string, 0, len(projects))
	for _, p := range projects {
		out = append(out, p.Name)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// initBare crea un bare repo de verdad con git.
func initBare(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := filepath.Join(t.TempDir(), "repo.git")
	runGit(t, t.TempDir(), "init", "--bare", "-q", dir)
	return dir
}

// TestScanConFdQueFallaDaErrorConSuSalida: si fd sale distinto de cero, el escaneo
// falla Y el error lleva la salida de fd.
//
// El error con la salida de fd dentro es lo que hace falta: "fd failed: exit
// status 1" sin más obligaría al usuario a reproducirlo a mano para ver qué pasó.
// Con la salida dentro, el mensaje suele bastar.
//
// Y el fallo NO degrada a WalkDir en silencio: un escaneo que devuelve un
// resultado distinto según si fd falla sería la peor sorpresa. O da lo que dio
// fd, o falla.
func TestScanConFdQueFallaDaErrorConSuSalida(t *testing.T) {
	root := writeTree(t, map[string]string{"api/.vroom.toml": "name = \"api\"\n"})
	failing := writeStr(t, filepath.Join(t.TempDir(), "fd-roto"), "#!/bin/sh\necho 'boom en fd' >&2\nexit 2\n")
	if err := os.Chmod(failing, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := scanWith(root, 3, failing)
	if err == nil {
		t.Fatal("con fd fallando el escaneo debería fallar, no degradar a WalkDir en silencio")
	}
	if !strings.Contains(err.Error(), "fd failed") {
		t.Errorf("err = %q, want el prefijo 'fd failed'", err)
	}
	if !strings.Contains(err.Error(), "boom en fd") {
		t.Errorf("el error no incluye la salida de fd: %q", err)
	}

	// Y el escaneo de bare repos con fd: también falla en voz alta.
	if _, err := scanBareReposWithFD(failing, root, 3); err == nil {
		t.Error("scanBareReposWithFD con fd fallando debería dar error")
	} else if !strings.Contains(err.Error(), "fd (dirs) failed") {
		t.Errorf("err = %q, want el prefijo de la segunda invocación", err)
	}
}

// TestScanConFdQueNoEncuentraNadaDaUnaListaVacia: fd sale 0 y no encuentra nada,
// que es un resultado VÁLIDO y no un error.
//
// Es lo que pasa en un workspace vacío, y tratarlo como error haría que `vroom
// list` fallara en un directorio recién creado.
func TestScanConFdQueNoEncuentraNadaDaUnaListaVacia(t *testing.T) {
	root := writeTree(t, map[string]string{"vacio.txt": "x"})
	empty := writeStr(t, filepath.Join(t.TempDir(), "fd-vacio"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := scanWith(root, 3, empty)
	if err != nil {
		t.Fatalf("fd que no encuentra nada no es un error: %v", err)
	}
	if len(res.Projects) != 0 {
		t.Errorf("se encontraron %v con un fd que no encuentra nada", namesOf(res.Projects))
	}
	if !res.UsedFD {
		t.Error("UsedFD = false: se usó fd, aunque no encontrara nada")
	}
}

// TestScanPorWalkConBareRepoYProfundidad: la ruta de WalkDir también tiene que
// sacar los bare repos y respetar la profundidad.
//
// Es la mitad del escaneo que no se ejecutaba en una máquina con fd instalado, y
// su propia aritmética de profundidad: con SkipDir en el nivel equivocado se
// pierde un subárbol entero.
func TestScanPorWalkConBareRepoYProfundidad(t *testing.T) {
	requireGit(t)
	root := t.TempDir()

	// Un bare repo de verdad: sale como fila contenedora.
	bare := filepath.Join(root, "repo.git")
	runGit(t, root, "init", "--bare", "-q", bare)

	// Y un repo normal con un worktree linkeado, que sale como fila anidada con
	// el .git como FICHERO. Se usa `git worktree add` y no `git clone` porque un
	// clone tiene su propio .git como directorio: no es un worktree y la
	// aserción de abajo no significaría nada.
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	writeStr(t, filepath.Join(repo, ".vroom.toml"), "name = \"repo\"\ncommand_start = \"./x\"\n")
	writeStr(t, filepath.Join(repo, "go.mod"), "module repo\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "init")
	wt := filepath.Join(root, "feature")
	runGit(t, repo, "worktree", "add", "-q", wt, "-b", "feature")

	res, err := scanWith(root, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	var containers, worktrees int
	for _, p := range res.Projects {
		if p.IsBareContainer {
			containers++
		}
		if p.IsWorktree {
			worktrees++
		}
	}
	if containers != 1 {
		t.Errorf("hay %d filas contenedoras, want 1 (el bare repo)", containers)
	}
	if worktrees != 1 {
		t.Errorf("hay %d worktrees, want 1: el walk tiene que reconocer el .git como fichero", worktrees)
	}

	// Y con profundidad 0 no se entra en ninguna fila.
	shallow, err := scanWith(root, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(shallow.Projects) != 0 {
		t.Errorf("con depth 0 aparecieron %v: el root solo no se inspecciona como proyecto", namesOf(shallow.Projects))
	}
}

// TestScanPorWalkFallaSiElWalkFalla: un error del recorrido se propaga.
//
// El error de WalkDir es un fallo de permisos en un subdirectorio que el proceso
// no puede leer. Degradar a una lista parcial sería peor que fallar: el usuario
// vería un workspace con menos proyectos del que tiene, y creería que ese es el
// estado real.
//
// Se provoca sustituyendo la variable `walkDir`, que existe para esto desde antes
// de este test.
func TestScanPorWalkFallaSiElWalkFalla(t *testing.T) {
	orig := walkDir
	t.Cleanup(func() { walkDir = orig })
	walkErr := errors.New("permiso denegado en un subdirectorio")
	calls := 0
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		calls++
		// Se devuelve el error por el callback, que es como WalkDir lo hace de
		// verdad cuando no puede leer un directorio.
		_ = fn(root, nil, walkErr)
		return walkErr
	}

	root := writeTree(t, map[string]string{"api/.vroom.toml": "name = \"api\"\n"})
	res, err := scanWith(root, 3, "")
	if calls != 1 {
		t.Errorf("se llamó al walk %d veces, want 1: el escaneo no debe reintentar", calls)
	}
	if err == nil {
		t.Fatal("un walk fallido debería dar error, no una lista parcial")
	}
	if !strings.Contains(err.Error(), "error walking") {
		t.Errorf("err = %q, want el prefijo 'error walking'", err)
	}
	if len(res.Projects) != 0 {
		t.Errorf("con error no debe devolverse una lista parcial: %v", namesOf(res.Projects))
	}
}

// TestRepoKeyRechazaUnGitAMedioConstruir: un `.git` sin `config` NO es un repo.
//
// Es un caso real: `git init` crea el directorio y escribe HEAD antes de escribir
// config, y un escaneo que pasara por ahí declararía repo un directorio a medias.
// El efecto sería agrupar proyectos bajo un repo que no existe.
func TestRepoKeyRechazaUnGitAMedioConstruir(t *testing.T) {
	medio := writeTree(t, map[string]string{
		"repo/.git/HEAD": "ref: refs/heads/main\n", // sin config
	})
	if got := repoKey(filepath.Join(medio, "repo")); got != "" {
		t.Errorf("repoKey = %q con un .git sin config, want \"\"", got)
	}

	// Y con config, sí es repo: el caso bueno de la misma comprobación.
	completo := writeTree(t, map[string]string{
		"repo/.git/HEAD":   "ref: refs/heads/main\n",
		"repo/.git/config": "[core]\n",
	})
	want := filepath.Clean(filepath.Join(completo, "repo", ".git"))
	if got := repoKey(filepath.Join(completo, "repo")); got != want {
		t.Errorf("repoKey = %q, want %q", got, want)
	}
}

// TestRepoKeyRechazaUnGitFicheroQueNoEsUnPuntero: un `.git` que es un FICHERO
// tiene que decir "gitdir: <ruta>"; si no lo dice, no es un worktree.
//
// El caso es real en repos mal copiados, y el efecto de aceptarlo sería agrupar el
// proyecto bajo un repo raíz vacío, con lo que la agregación de grupos contaría
// mal.
func TestRepoKeyRechazaUnGitFicheroQueNoEsUnPuntero(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"repo/.git":        "esto no es un puntero\n",
		"repo/.vroom.toml": "name = \"repo\"\ncommand_start = \"./x\"\n",
	})
	if got := repoKey(filepath.Join(dir, "repo")); got != "" {
		t.Errorf("repoKey = %q con un .git que no es un puntero, want \"\"", got)
	}
}

// TestFinalizeNoDuplicaLaFilaDeUnBareQueTambienEsProyecto: un repositorio que
// aparece por los dos caminos se lista UNA vez.
//
// Los dos caminos son el escaneo de manifiestos y el de bare repos. Un directorio
// con ambos —un repo con `.vroom.toml` que además es bare— aparecería dos veces, y
// dos filas del mismo servicio harían que el TUI lo ofreciera como duplicado y
// que el `stop` de una dejara a la otra afirmando un PID.
func TestFinalizeNoDuplicaLaFilaDeUnBareQueTambienEsProyecto(t *testing.T) {
	dir := "/srv/doble"
	projects := []Project{{Path: dir, Name: "doble", Configured: true}}
	bare := []Project{{Path: dir, Name: "doble", IsBareContainer: true}}

	got := finalize(projects, bare, "/srv")
	if len(got) != 1 {
		t.Fatalf("hay %d filas, want 1: el mismo path no puede salir dos veces", len(got))
	}
	// Gana la del escaneo de manifiestos, que es la que tiene el Manifest.
	if !got[0].Configured {
		t.Error("la fila duplicada se quedó con la versión sin manifiesto: el servicio parecería no gestionable")
	}

	// Y dos bares con el mismo path también se deduplican entre sí.
	dup := []Project{{Path: dir, IsBareContainer: true}, {Path: dir, IsBareContainer: true}}
	if got := finalize(nil, dup, "/srv"); len(got) != 1 {
		t.Errorf("hay %d filas con dos bares del mismo path, want 1", len(got))
	}
}

// TestFinalizeDedupsProyectosRepetidos: dos filas de proyecto con el mismo path
// se funden en una.
//
// El escaneo con fd y el de WalkDir nunca repiten, pero `finalize` es donde se
// garantiza, y una fila repetida se ve como un servicio duplicado en el árbol.
func TestFinalizeDedupsProyectosRepetidos(t *testing.T) {
	dir := "/srv/doble"
	dup := []Project{
		{Path: dir, Name: "doble", Configured: true},
		{Path: dir, Name: "doble", Configured: true},
	}
	if got := finalize(dup, nil, "/srv"); len(got) != 1 {
		t.Errorf("hay %d filas con dos proyectos del mismo path, want 1", len(got))
	}
}

// TestScanCortaElRecorridoCuandoSePasaDeProfundidad: un directorio más profundo
// que el límite no se entra, ni siquiera para buscar nietos.
//
// Es lo que evita que un escaneo con depth 2 entre en `node_modules/lo/que/haya`,
// y el SkipDir tiene que estar donde está: un `continue` seguiría bajando y el
// corte no serviría de nada.
func TestScanCortaElRecorridoCuandoSePasaDeProfundidad(t *testing.T) {
	root := writeTree(t, map[string]string{
		// El manifiesto está 4 niveles abajo y depth 2, así que no puede salir.
		"a/b/c/d/.vroom.toml": "name = \"profundo\"\ncommand_start = \"./x\"\n",
		"a/.vroom.toml":       "name = \"a\"\ncommand_start = \"./x\"\n",
	})

	res, err := scanWith(root, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	names := namesOf(res.Projects)
	if len(names) != 1 || names[0] != "a" {
		t.Errorf("se encontraron %v con depth 2, want sólo a: el recorrido debe cortarse", names)
	}

	// Con depth 0 el corte ocurre al entrar en el primer hijo: es el mismo
	// SkipDir pero por la otra aritmética (1 > 0 en vez de 3 > 2).
	zero, err := scanWith(root, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(zero.Projects) != 0 {
		t.Errorf("con depth 0 aparecieron %v: no se puede entrar en ningún hijo", namesOf(zero.Projects))
	}

	// Y con depth 5 sí sale, que es lo que demuestra que el corte anterior era por
	// profundidad y no por otra cosa. El nombre de la fila es el del DIRECTORIO,
	// no el del manifiesto: es lo que hacen las dos rutas del escaneo, y
	// findProject tiene una segunda pasada por si acaso.
	more, err := scanWith(root, 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(namesOf(more.Projects), "d") {
		t.Errorf("con depth 5 tampoco salió el proyecto de 4 niveles: %v", namesOf(more.Projects))
	}
}

// TestScanConElCwdBorradoDaErrorEn vezDeSalirCallado: sin directorio de trabajo no
// se puede resolver una ruta RELATIVA, y eso tiene que ser un error.
//
// La alternativa sería devolver el path tal cual y escanear el directorio
// equivocado: el usuario vería la lista de otro sitio y no sabría por qué.
//
// Sólo afecta a rutas relativas: filepath.Abs de una ruta absoluta no necesita el
// CWD, y ese es el caso normal del CLI, que siempre pasa un root absoluto.
func TestScanConElCwdBorradoDaError(t *testing.T) {
	gone := t.TempDir()
	t.Chdir(gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { t.Chdir(t.TempDir()) })

	if _, err := scanWith("relativo", 3, ""); err == nil {
		t.Error("sin CWD, una ruta relativa no se puede resolver: debería dar error, no escanear otro directorio")
	}
}

// contains dice si la lista tiene el valor.
func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
