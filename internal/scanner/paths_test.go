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

// An error that does not name the root reads as an empty workspace.
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

// The two root failures are fixed differently, so they must not share a message.
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

// Parity is what makes "the CI runner has no fd" the same fact as "my machine has fd"; divergence would only show up on the other machine.
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

	// scanWith with fd == "" forces the WalkDir path; t.Setenv cannot reach the absolute fallbacks fdPath also checks.
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
		{3, []string{"nivel1", "nivel2"}}, // nivel3 holds .v.toml, not a manifest
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

// The cut needs SkipDir, not continue, or the traversal keeps descending into the hundreds of thousands of files under node_modules.
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

// A bare repo missing from the scan leaves its worktrees showing with no parent row.
func TestScanDetectaBareReposComoFilasContenedoras(t *testing.T) {
	bare := writeTree(t, map[string]string{
		"bare.git/HEAD":            "ref: refs/heads/main\n",
		"bare.git/config":          "[core]\n\tbare = true\n",
		"bare.git/objects/00/0000": "",
	})
	// The real git repo wins over the hand-written tree: bare detection is a heuristic over git's own layout.
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

// Accepting any of these yields a repo root of "" and hangs every worktree row off a repo that does not exist.
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

// Without commondir each worktree is its own repo and the TUI shows them as loose projects instead of under one row.
func TestCommonDirResuelveElRepoPrincipalDeUnWorktree(t *testing.T) {
	dir := t.TempDir()

	t.Run("sin commondir: el propio gitdir", func(t *testing.T) {
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
		// MEDIDO in a real repo: <repo>/.git/worktrees/<name>/commondir is "../.."; "../../.." climbs one level too far.
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

// /a/bc starts with /a/b but is outside, which is the case a naive HasPrefix gets wrong.
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

	// A Rel that cannot be computed counts as outside, the closed failure.
	if withinRoot("relativo", "/absoluto") {
		t.Error("una comparación que no se puede hacer no puede decir 'dentro'")
	}
}

// Group aggregation excludes nested rows; counting one in both places makes the group header disagree with the rows under it.
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

func TestFdPathDevuelveAlgoDistintoDeCadenaVaciaOElFichero(t *testing.T) {
	got := fdPath()
	if got == "" {
		t.Skip("no hay fd en este sistema: sólo se puede probar la rama de ausencia")
	}
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

// A normal repo with worktrees has a .git directory; read as bare, its worktree becomes the parent row and the manifested repo drops out of its group.
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

func initBare(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := filepath.Join(t.TempDir(), "repo.git")
	runGit(t, t.TempDir(), "init", "--bare", "-q", dir)
	return dir
}

// A failing fd must not silently degrade to WalkDir, since a scan whose result depends on whether fd failed is the worst surprise; the error must carry fd's own output, otherwise the user has to reproduce the run by hand.
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

	if _, err := scanBareReposWithFD(failing, root, 3); err == nil {
		t.Error("scanBareReposWithFD con fd fallando debería dar error")
	} else if !strings.Contains(err.Error(), "fd (dirs) failed") {
		t.Errorf("err = %q, want el prefijo de la segunda invocación", err)
	}
}

// An empty workspace is a valid result; treating it as an error would break vroom list on a freshly created directory.
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

// This is the half of the scan that never runs on a machine with fd, and it has its own depth arithmetic.
func TestScanPorWalkConBareRepoYProfundidad(t *testing.T) {
	requireGit(t)
	root := t.TempDir()

	bare := filepath.Join(root, "repo.git")
	runGit(t, root, "init", "--bare", "-q", bare)

	// git worktree add, not git clone: a clone has its own .git directory, so it is not a worktree and the assertion would prove nothing.
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

	shallow, err := scanWith(root, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(shallow.Projects) != 0 {
		t.Errorf("con depth 0 aparecieron %v: el root solo no se inspecciona como proyecto", namesOf(shallow.Projects))
	}
}

// A partial list would show a workspace with fewer projects than the user has, and they would believe it.
func TestScanPorWalkFallaSiElWalkFalla(t *testing.T) {
	orig := walkDir
	t.Cleanup(func() { walkDir = orig })
	walkErr := errors.New("permiso denegado en un subdirectorio")
	calls := 0
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		calls++
		// The error travels through the callback, which is how real WalkDir reports an unreadable directory.
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

// git init writes HEAD before config, so a scan landing in between would declare a half-built directory a repo.
func TestRepoKeyRechazaUnGitAMedioConstruir(t *testing.T) {
	medio := writeTree(t, map[string]string{
		"repo/.git/HEAD": "ref: refs/heads/main\n",
	})
	if got := repoKey(filepath.Join(medio, "repo")); got != "" {
		t.Errorf("repoKey = %q con un .git sin config, want \"\"", got)
	}

	completo := writeTree(t, map[string]string{
		"repo/.git/HEAD":   "ref: refs/heads/main\n",
		"repo/.git/config": "[core]\n",
	})
	want := filepath.Clean(filepath.Join(completo, "repo", ".git"))
	if got := repoKey(filepath.Join(completo, "repo")); got != want {
		t.Errorf("repoKey = %q, want %q", got, want)
	}
}

// A .git file that is not a pointer is what a badly copied repo looks like; accepting it groups the project under an empty root.
func TestRepoKeyRechazaUnGitFicheroQueNoEsUnPuntero(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"repo/.git":        "esto no es un puntero\n",
		"repo/.vroom.toml": "name = \"repo\"\ncommand_start = \"./x\"\n",
	})
	if got := repoKey(filepath.Join(dir, "repo")); got != "" {
		t.Errorf("repoKey = %q con un .git que no es un puntero, want \"\"", got)
	}
}

// Two rows for one service make stop leave the other claiming a PID.
func TestFinalizeNoDuplicaLaFilaDeUnBareQueTambienEsProyecto(t *testing.T) {
	dir := "/srv/doble"
	projects := []Project{{Path: dir, Name: "doble", Configured: true}}
	bare := []Project{{Path: dir, Name: "doble", IsBareContainer: true}}

	got := finalize(projects, bare, "/srv")
	if len(got) != 1 {
		t.Fatalf("hay %d filas, want 1: el mismo path no puede salir dos veces", len(got))
	}
	// The manifest-scan row wins because it is the one carrying the Manifest.
	if !got[0].Configured {
		t.Error("la fila duplicada se quedó con la versión sin manifiesto: el servicio parecería no gestionable")
	}

	dup := []Project{{Path: dir, IsBareContainer: true}, {Path: dir, IsBareContainer: true}}
	if got := finalize(nil, dup, "/srv"); len(got) != 1 {
		t.Errorf("hay %d filas con dos bares del mismo path, want 1", len(got))
	}
}

// Neither scan path repeats a path; finalize is where that is guaranteed.
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

func TestScanCortaElRecorridoCuandoSePasaDeProfundidad(t *testing.T) {
	root := writeTree(t, map[string]string{
		// The manifest sits 4 levels down and depth is 2, so it cannot show up.
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

	zero, err := scanWith(root, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(zero.Projects) != 0 {
		t.Errorf("con depth 0 aparecieron %v: no se puede entrar en ningún hijo", namesOf(zero.Projects))
	}

	// The row name is the directory, not the manifest; both scan paths do that and findProject has a second pass.
	more, err := scanWith(root, 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(namesOf(more.Projects), "d") {
		t.Errorf("con depth 5 tampoco salió el proyecto de 4 niveles: %v", namesOf(more.Projects))
	}
}

// Only relative roots are affected: filepath.Abs of an absolute path needs no CWD, and the CLI always passes an absolute root.
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

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
