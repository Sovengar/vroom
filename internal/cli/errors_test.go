package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Los caminos de ERROR de los comandos.
//
// Cada comando tiene un punto donde el entorno puede fallar antes de que el
// trabajo ocurra: el store que no se puede crear, el escaneo que no puede leer,
// el directorio de servicio que no se puede escribir. Esos caminos son los que
// un agente va a encontrar en su máquina y los que NO estaban cubiertos.
//
// Y hay una razón para fijarlos aquí y no "en algún sitio": cada uno dice algo
// distinto. Un store ilegible y un escaneo fallido se distinguen por el prefijo
// del contrato, y esa distinción es la que permite a un agente decir "tu disco no
// tiene permiso" en vez de "no hay proyectos".
// ---------------------------------------------------------------------------

// brokenStateHome apunta XDG_STATE_HOME a un FICHERO, de modo que el MkdirAll del
// store no puede tener éxito.
//
// Se usa un fichero y no un directorio sin permisos porque no depende del uid:
// el runner de CI puede ir como root, y root puede escribir en un directorio sin
// permiso, con lo que un test así pasaría en local y fallaría en CI. Un fichero
// donde se espera un directorio falla para todos.
func brokenStateHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(writeFile(t, filepath.Join(t.TempDir(), "bloqueado"), "x"), "sub"))
}

// TestStoreIlegibleSeDistingueDeUnProyectoInexistente: el store que no se puede
// crear es un ERROR de todos los comandos, y su mensaje NO lleva el prefijo de
// escaneo.
//
// La separación no es cosmeticidad: `findProject` devuelve "project not found"
// cuando el nombre no existe, y si el store ilegible devolviera algo parecido el
// agente buscaría un error de tipografía en el nombre del proyecto. Y el prefijo
// "scan error" tampoco: el store no participa en el escaneo.
func TestStoreIlegibleSeDistingueDeUnProyectoInexistente(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)
	brokenStateHome(t)

	for _, cmd := range []string{"list", "start", "stop", "build", "install", "logs"} {
		t.Run(cmd, func(t *testing.T) {
			_, _, err := dispatch([]string{cmd, "api"})
			if err == nil {
				t.Fatalf("%s con un state dir inutilizable debería fallar", cmd)
			}
			if strings.Contains(err.Error(), "project not found") {
				t.Errorf("%s: un store ilegible se disfrazo de proyecto inexistente: %q", cmd, err)
			}
			if strings.Contains(err.Error(), "scan error") {
				t.Errorf("%s: un store ilegible no es un fallo de escaneo: %q", cmd, err)
			}
			if !strings.Contains(err.Error(), "state directory") {
				t.Errorf("%s: el error no explica que el problema es el directorio de estado: %q", cmd, err)
			}
		})
	}
}

// TestEscaneoFallidoLlevaSuPropioPrefijo: un root que no es un directorio hace
// fallar el escaneo, y eso SÍ lleva "scan error".
//
// La otra mitad es que el fallo ocurre antes de buscar el proyecto, así que un
// nombre perfectamente válido falla igual: el orden importa y por eso se prueba
// con un nombre que SÍ existe.
func TestEscaneoFallidoLlevaSuPropioPrefijo(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)

	// Root = un fichero. Scan dice "is not a directory" y cmd lo envuelve.
	notADir := filepath.Join(t.TempDir(), "fichero")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeConfigScannerRoot(t, notADir)

	_, _, err := dispatch([]string{"start", "api"})
	if err == nil {
		t.Fatal("un root que no es un directorio debería fallar")
	}
	if !strings.Contains(err.Error(), "scan error") {
		t.Errorf("err = %q, want el prefijo 'scan error'", err)
	}
	// Y el prefijo NO tapa la causa: sin ella el agente no sabe qué arreglar.
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("el error no conserva la causa del fallo de escaneo: %q", err)
	}

	// Y el mismo fallo en list, que es el comando de consulta puro.
	if _, err := cmdList(); err == nil || !strings.Contains(err.Error(), "scan error") {
		t.Errorf("list con un root inválido = %v, want el error de escaneo", err)
	}
}

// TestCmdListConEscaneoFallidoNoDevuelveFilaVacia: el fallo de escaneo tiene que
// PROPAGARSE, no convertirse en una lista vacía.
//
// Es la misma propiedad que en el root inexistente, vista desde el comando: una
// lista vacía es una afirmación ("no hay proyectos"), y un fallo de disco no
// puede convertirse en una afirmación.
func TestCmdListConEscaneoFallidoNoDevuelveFilaVacia(t *testing.T) {
	root := cliEnv(t)
	t.Chdir(root)
	writeConfigScannerRoot(t, filepath.Join(root, "api", ".vroom.toml")) // un fichero

	payload, err := cmdList()
	if err == nil {
		t.Fatalf("list devolvió %+v: un fallo de escaneo no puede ser una lista vacía", payload)
	}
	if payload != nil {
		t.Errorf("con error no debe devolverse payload: %+v", payload)
	}
}

// TestCmdStartPropagaElFalloDeArranque: un manifiesto sin comando de arranque no
// puede arrancar, y el error sale con su prefijo del contrato.
//
// Y lo que importa es que NO se afirme un PID: un `started` con un PID que nunca
// existió es la peor respuesta posible, porque el agente monitoriza un proceso
// imaginario y además lo creería sano.
//
// MEDIDO (alcance): el fallo que se detecta es el de un comando VACÍO, no el de
// un comando inexistente. Un comando que no existe como binario lo descubre el
// `sh -c` del hijo daemonizado, o sea DESPUÉS de que Start devuelva éxito, y vroom
// no espera a que el servicio esté vivo. Así que un start con un binario mal
// escrito responde "started" y el proceso muere enseguida: no es un bug de este
// comando, es la semántica de "daemonizar y devolver", y comprobarla aquí sería
// fijar un defecto como si fuera un contrato.
func TestCmdStartPropagaElFalloDeArranque(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	store2 := mustStore(t)
	apiPath := filepath.Join(root, "api")
	if _, err := store2.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}
	// El log de stdout es un DIRECTORIO: el OpenFile con O_CREATE|O_WRONLY falla
	// con EISDIR, que es el fallo de arranque más externo que se puede provocar
	// sin untrue al motor de procesos.
	if err := os.MkdirAll(store2.StdoutLog(apiPath), 0o755); err != nil {
		t.Fatal(err)
	}

	payload, err := cmdStart("api", "")
	if err == nil {
		t.Fatalf("un manifiesto sin command_start no puede arrancar: devolvió %+v", payload)
	}
	if !strings.Contains(err.Error(), "start failed") {
		t.Errorf("err = %q, want el prefijo 'start failed' del contrato", err)
	}
	// Y no se afirma ningún PID en el Meta, porque no se escribió nada.
	meta, merr := store.LoadMeta(apiPath)
	if merr == nil && meta.Pid != 0 {
		t.Errorf("meta.Pid = %d tras un arranque fallido: se afirmaría un proceso que no existe", meta.Pid)
	}
}

// TestCmdStartConDirectorioDeServicioIlegible: el EnsureServiceDir falla antes de
// arrancar nada, y es un error DISTINTO del fallo del comando.
//
// La razón de separarlos: el primero dice "el disco no me deja", el segundo
// dice "tu comando falló". Un agente que los tratara igual buscaría el problema
// en el manifiesto cuando está en los permisos del HOME.
func TestCmdStartConDirectorioDeServicioIlegible(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	// El hash del path del proyecto tiene que existir como FICHERO para que el
	// MkdirAll de services/<hash> falle.
	apiPath := filepath.Join(root, "api")
	if err := os.WriteFile(store.ServiceDir(apiPath), []byte("bloquea el mkdir"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := cmdStart("api", "")
	if err == nil {
		t.Fatal("con services/<hash> ocupado por un fichero el arranque debería fallar")
	}
	if !strings.Contains(err.Error(), "could not create service dir") {
		t.Errorf("err = %q, want el prefijo del directorio de servicio", err)
	}
	if strings.Contains(err.Error(), "start failed") {
		t.Errorf("un fallo de disco no es un fallo del comando de arranque: %q", err)
	}
}

// TestCmdStopPropagaElFalloDeClearPid: si el Meta no se puede limpiar, el stop
// falla en vez de afirmar que paró.
//
// Es el único punto de stopCleanup cuyo fallo es irrecuperable: sin él el Meta
// sigue afirmando un PID vivo para un servicio ya parado, y el `list` siguiente
// publicaría un servicio corriendo que no existe. La razón de que el error
// EXISTA es esa, y por eso se provoca.
//
// Se provoca quitando permisos al directorio de servicio. Es el único camino real
// para un fallo de escritura aquí, así que el test se salta cuando corre como
// root: en ese caso no hay forma honesta de provocarlo y un skip lo dice mejor
// que un Arrange-trick.
func TestCmdStopPropagaElFalloDeClearPid(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede escribir en un directorio sin permiso: el fallo de ClearPid no se puede provocar")
	}
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(apiPath, state.Meta{Name: "api", Pid: 999999}); err != nil {
		t.Fatal(err)
	}
	// ClearPid hace os.Remove sobre los ficheros de PID y PGID. Un Remove de un
	// fichero que NO existe no es un fallo (os.IsNotExist se ignora a propósito),
	// así que para que el fallo sea real el fichero tiene que existir y el
	// directorio tiene que impedir borrarlo.
	if err := os.WriteFile(store.PidFile(apiPath), []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := store.ServiceDir(apiPath)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := cmdStop("api", "")
	if err == nil {
		t.Fatal("con un Meta que no se puede limpiar, stop debería fallar y no afirmar que paró")
	}
	if !strings.Contains(err.Error(), "could not clear pid") {
		t.Errorf("err = %q, want el prefijo del contrato", err)
	}
	// Y el comando no publica un "stopped": el payload es nil cuando hay error.
	payload, _, derr := dispatch([]string{"stop", "api"})
	if derr == nil {
		t.Fatal("stop debería propagar el fallo")
	}
	if payload != nil {
		t.Errorf("con error no debe devolverse payload: %+v", payload)
	}
}

// TestStopCleanupRegistraLosAvisosDelKillEnElLog: los avisos del manager van al
// log de stderr del servicio, y no a stdout ni al payload.
//
// Es lo que hace que un `vroom stop` no se trague un "no se pudo matar al
// proceso" y que el usuario lo encuentre donde lee los logs. Se comprueba en el
// fichero, porque un aviso que no se escribe en ningún sitio es un aviso
// perdido.
func TestStopCleanupRegistraLosAvisosDelKillEnElLog(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(apiPath, state.Meta{Name: "api", Pid: 424242, Port: 4321}); err != nil {
		t.Fatal(err)
	}

	mgr := &aliveManager{}
	if err := stopCleanup(store, mgr, apiPath); err != nil {
		t.Fatal(err)
	}

	if len(mgr.stopped) != 1 {
		t.Fatalf("Stop se llamó %d veces, want 1", len(mgr.stopped))
	}
	if got := mgr.stopped[0].Pid; got != 424242 {
		t.Errorf("Stop recibió Pid %d, want el del Meta", got)
	}

	log := readFileString(t, store.StderrLog(apiPath))
	if !strings.Contains(log, "stopped pid 424242") {
		t.Errorf("el aviso del kill no llegó al log del servicio:\n%s", log)
	}
	if !strings.Contains(log, "service stopped") {
		t.Errorf("falta la marca de fin de stop en el log:\n%s", log)
	}
}

// TestStopCleanupSinProcesoNoTocaElManager: un servicio que ya estaba parado no
// se "para" otra vez.
//
// Es lo que evita que un stop repetido mande señales a un PID que el sistema ya
// ha reciclado. Y el PID del Meta es 0, luego no hay nada que parar por definición.
func TestStopCleanupSinProcesoNoTocaElManager(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}
	// Pid 0, Pgid 0 y Port 0: no hay nada.
	if err := store.SaveMeta(apiPath, state.Meta{Name: "api"}); err != nil {
		t.Fatal(err)
	}

	mgr := &aliveManager{}
	if err := stopCleanup(store, mgr, apiPath); err != nil {
		t.Fatal(err)
	}
	if len(mgr.stopped) != 0 {
		t.Errorf("Stop se llamó sin proceso que parar: %v", mgr.stopped)
	}
	// Y el Meta queda limpio igualmente: parar algo parado es un no-op que
	// normaliza el estado.
	meta, err := store.LoadMeta(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != state.StateStopped {
		t.Errorf("meta.State = %q, want stopped: parar normaliza aunque no hubiera nada que parar", meta.State)
	}
}

// TestStopCleanupConMetaAusenteIgualNormaliza: sin Meta no hay nada que limpiar,
// y eso tampoco puede ser un error.
//
// Un proyecto que nunca arrancó no tiene Meta, y un `vroom stop` sobre él tiene
// que salir bien: es el caso de "el agente repite la orden por si acaso".
func TestStopCleanupConMetaAusenteIgualNormaliza(t *testing.T) {
	root := cliEnv(t)
	store := mustStore(t)
	_ = chdirTree(t, root)

	apiPath := filepath.Join(root, "api")
	if _, err := store.EnsureServiceDir(apiPath); err != nil {
		t.Fatal(err)
	}

	mgr := &aliveManager{}
	if err := stopCleanup(store, mgr, apiPath); err != nil {
		t.Errorf("sin Meta el cleanup no puede fallar: %v", err)
	}
	if len(mgr.stopped) != 0 {
		t.Errorf("Stop se llamó sin Meta: %v", mgr.stopped)
	}
}

// TestReleaseRouteOnStopSoloRevocaSiLaRetiradaSurtioEfecto: la propiedad se
// revoca cuando la retirada ocurrió, y NO cuando la retirada falló.
//
// Es la diferencia entre "cerrado" y "fallo abierto disfrazado": una propiedad
// revocada sobre una ruta que sigue viva deja la ruta huérfana sin nadie que la
// limpie, porque ya nadie reclama su limpieza.
func TestReleaseRouteOnStopSoloRevocaSiLaRetiradaSurtioEfecto(t *testing.T) {
	installCLIReleaser(t, &recordingReleaser{}) // RemoveAbsent devuelve nil

	meta := state.Meta{Name: "p", RouteName: "ruta", RouteOwned: true}
	releaseRouteOnStop(&meta)

	if meta.RouteOwned {
		t.Error("tras una retirada efectiva la propiedad debe revocarse")
	}
	// El handle sobrevive a la revocación, y tiene que: es donde va a mirar la
	// reconciliación del próximo arranque.
	if meta.RouteName != "ruta" {
		t.Errorf("RouteName = %q: el handle de reconcilización debe conservarse", meta.RouteName)
	}
}

func TestReleaseRouteOnStopConservaLaPropiedadSiRetiradaFalla(t *testing.T) {
	installCLIFailingReleaser(t)

	meta := state.Meta{Name: "p", RouteName: "ruta", RouteOwned: true}
	releaseRouteOnStop(&meta)

	if !meta.RouteOwned {
		t.Error("una retirada fallida no revoca: la ruta puede seguir viva y sin dueño que la limpie")
	}
}

func TestReleaseRouteOnStopNoRetiraLoQueNoEsNuestro(t *testing.T) {
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)

	meta := state.Meta{Name: "p", RouteName: "ajena", RouteOwned: false}
	releaseRouteOnStop(&meta)

	if len(rec.removed) != 0 {
		t.Errorf("una ruta nunca nuestra no se toca: %v", rec.removed)
	}
}

// TestCliRouteReleaserDevuelveNilFueraDeUnBinarioDeTest: en producción el
// releaser es nil, y nil significa "construye el cliente real de portless".
//
// La comprobación depende del NOMBRE del binario, no de una variable, así que se
// puede falsificar en un test: basta con poner os.Args[0] al nombre que tendría
// el binario instalado. Sin esto, el `return nil` de producción se quedaría sin
// cubrir y sin forma de cubrir —porque sólo se llega ahí fuera de un test—.
func TestCliRouteReleaserDevuelveNilFueraDeUnBinarioDeTest(t *testing.T) {
	orig := os.Args[0]
	t.Cleanup(func() { os.Args[0] = orig })

	os.Args[0] = "/usr/local/bin/vroom"
	if got := cliRouteReleaser(); got != nil {
		t.Error("en producción el releaser es nil: nil significa 'construye el cliente real'")
	}

	// Y en un binario de test vuelve al inerte, que es lo que impide que un test
	// que se olvide del seam mute el routes.json del developer.
	os.Args[0] = "/tmp/vroom.test"
	got := cliRouteReleaser()
	if got == nil {
		t.Fatal("en un binario de test el releaser no puede ser nil: construiría el cliente real")
	}
	if err := got.RemoveAbsent("lo-que-sea"); err != nil {
		t.Errorf("el releaser inerte devolvió %v, debe ser nil siempre", err)
	}

	// Con el seam instalado, manda el seam.
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)
	os.Args[0] = "/usr/local/bin/vroom"
	if got := cliRouteReleaser(); got == nil {
		t.Error("con el seam instalado el seam manda sobre el entorno")
	}
}

// failingReleaser falla siempre, como un portless roto o sin Node.
type failingReleaser struct{}

func (*failingReleaser) RemoveAbsent(string) error { return errors.New("requires Node >= 24") }

var _ portless.Releaser = (*failingReleaser)(nil)

// mustStore devuelve el store que el CLI leería del entorno.
//
// Se llama desde un test que ya hizo cliEnv, así que el store del entorno es el
// único que hay. Es un indirección para que el mensaje de fallo diga "store" en
// vez de repetir el NewStore en cada test.
func mustStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestBuildProjectInfoColapsaElGrupoSecundario: la clave de colapso es
// primary/secondary cuando hay grupo secundario, y primary a secas cuando no.
//
// Es lo que hace que el colapso del TUI y el del JSON hablen del MISMO servicio:
// si el JSON usara una clave distinta, un agente vería `collapsed: true` para un
// grupo que en la TUI está desplegado.
func TestBuildProjectInfoColapsaElGrupoSecundario(t *testing.T) {
	store := mustStore(t)

	conSecundario := &fakeProject{
		name: "api", primary: "tienda", secondary: "backend",
	}
	sinSecundario := &fakeProject{
		name: "web", primary: "tienda",
	}

	// La clave con barra secundaria: colapsada.
	collapsed := map[string]bool{"tienda/backend": true, "tienda": false}
	if got := buildProjectInfo(nil, store, collapsed, conSecundario.project(t)); !got.Collapsed {
		t.Error("con grupo secundario la clave es primary/secondary: el collapse tenía que salir true")
	}
	if got := buildProjectInfo(nil, store, collapsed, sinSecundario.project(t)); got.Collapsed {
		t.Error("sin grupo secundario la clave es primary a secas, y aquí no está colapsada")
	}

	// Y al revés: colapsar la clave primary no colapsa al proyecto con
	// secundario. Si los dos compartieran clave, colapsar "tienda" ocultaría
	// también al backend, que es justo lo que el grupo secundario evita.
	if got := buildProjectInfo(nil, store, map[string]bool{"tienda": true}, conSecundario.project(t)); got.Collapsed {
		t.Error("colapsar primary no puede colapsar primary/secondary: son grupos distintos")
	}
}

// fakeProject construye un scanner.Project configurado con los grupos dados.
type fakeProject struct {
	name      string
	primary   string
	secondary string
}

func (f *fakeProject) project(t *testing.T) scanner.Project {
	t.Helper()
	dir := t.TempDir()
	m := &manifest.Manifest{Name: f.name, Command: "sleep 30", PrimaryGroup: f.primary, SecondaryGroup: f.secondary}
	return scanner.Project{Path: dir, Name: f.name, Configured: true, Manifest: m}
}

// TestOutputJSONConFalloDeEscrituraSaleConUno: un stdout que no acepta la
// respuesta tiene que salir con 1 y decirlo, no con 0 y en silencio.
//
// Es el error que más caro sale si se ignora: el agente recibe una respuesta
// vacía, la parsea como "no hay proyectos" y se lo cachea. La diferencia entre
// un list vacío y un list que no se pudo escribir es la diferencia entre un
// proyecto que no existe y un fallo de disco.
func TestOutputJSONConFalloDeEscrituraSaleConUno(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	var errBuf bytes.Buffer
	handled, code := runInto(failingWriter{}, &errBuf, []string{"list"})
	if !handled {
		t.Fatal("el comando sí se manejó")
	}
	if code != 1 {
		t.Errorf("code = %d, want 1: el agente necesita saber que no hay respuesta", code)
	}
	if !strings.Contains(errBuf.String(), "could not write response") {
		t.Errorf("stderr no dice que no se pudo escribir: %q", errBuf.String())
	}
	// Y el mensaje de stderr es el ÚNICO sitio donde se dice: stdout ya falló.
	if strings.Contains(errBuf.String(), "projects") {
		t.Errorf("stderr no debe llevar la respuesta: %q", errBuf.String())
	}
}

// TestRunLoggedPropagaLosFallosDeEscrituraAntesDeEjecutar: runLogged no ejecuta
// nada si no puede escribir, y devuelve el error.
//
// La razón de que el comando no corra es la que importa: si se ejecutara sin logs,
// el agente vería un `build ok` sin salida y sin forma de saber qué pasó. Y el
// `ok` tampoco puede ser true, porque el comando va en el campo Error.
//
// Los tres caminos se provocan con ficheros donde se esperan directorios, que
// fallan para cualquier uid.
func TestRunLoggedPropagaLosFallosDeEscrituraAntesDeEjecutar(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// Marcador que el comando tocaría si llegara a ejecutarse.
	marker := filepath.Join(work, "ejecutado")

	tests := []struct {
		name       string
		stdoutPath string
		stderrPath string
	}{
		{
			// El padre de stdout.log es un FICHERO: el MkdirAll falla.
			name:       "directorio de log bloqueado por un fichero",
			stdoutPath: filepath.Join(writeFile(t, filepath.Join(base, "bloque1"), "x"), "sub", "stdout.log"),
			stderrPath: filepath.Join(base, "caso1", "stderr.log"),
		},
		{
			// stdout.log es un DIRECTORIO: el OpenFile del banner falla.
			name:       "stdout.log es un directorio",
			stdoutPath: makeDir(t, filepath.Join(base, "stdout.log")),
			stderrPath: filepath.Join(base, "caso2", "stderr.log"),
		},
		{
			// El banner sí cabe; el segundo OpenFile de stdout.log falla. No se
			// puede provocar con permisos aquí, así que se hace que stderr.log sea
			// un directorio, que falla en su propio OpenFile.
			name:       "stderr.log es un directorio",
			stdoutPath: filepath.Join(base, "caso3", "stdout.log"),
			stderrPath: makeDir(t, filepath.Join(base, "stderr.log")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Remove(marker)
			elapsed, code, err := runLogged("build", "touch "+marker, work, tt.stdoutPath, tt.stderrPath)
			if err == nil {
				t.Fatalf("runLogged con %s debería fallar", tt.name)
			}
			if elapsed != 0 || code != 0 {
				t.Errorf("un fallo de escritura devuelve elapsed=%v code=%d, want 0,0", elapsed, code)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Error("el comando se ejecutó pese a no poder escribir el log: el agente vería ok sin salida")
			}
		})
	}
}

// TestRunLoggedDaElCodigoDeSalidaRealYLoAnotaEnElLog: el código que sale es el
// del COMANDO, y el log deja escrito por qué.
//
// Las dos mitades: el exit_code del JSON y la línea `✗ build failed (exit N, T)`
// en el log. La segunda es la que permite a un humano entender un fallo sin
// volver a ejecutar el build, y no sale si el comando no se ejecutó.
func TestRunLoggedDaElCodigoDeSalidaRealYLoAnotaEnElLog(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")

	elapsed, code, err := runLogged("build", "echo fuera; echo dentro >&2; exit 42", dir, out, errLog)
	if code != 42 {
		t.Errorf("code = %d, want 42", code)
	}
	if err == nil {
		t.Fatal("un exit 42 debe ser error: el comando NO se hizo")
	}
	// MEDIDO: elapsed puede ser 0 aunque el comando se ejecutara —el redondeo a
	// 10 ms se come un comando de 3 ms—, así que no se puede usar para saber si
	// corrió. Lo que sí se puede es exigir el redondeo, que es lo que lo hace
	// comparable entre ejecuciones.
	if elapsed%10*time.Millisecond != 0 {
		t.Errorf("elapsed = %v no está redondeado a 10ms", elapsed)
	}

	// El stderr del comando llega a su fichero, mezclado con lo que escribe vroom.
	if got := readFileString(t, errLog); !strings.Contains(got, "dentro") {
		t.Errorf("el stderr del comando no llegó a su log: %q", got)
	}
	// Y el fallo queda anotado en stdout, que es donde se lee el banner.
	body := readFileString(t, out)
	if !strings.Contains(body, "vroom ▶ build") {
		t.Errorf("falta el banner del comando:\n%s", body)
	}
	if !strings.Contains(body, "vroom ✗ build failed (exit 42") {
		t.Errorf("el log no dice que el build falló con 42:\n%s", body)
	}
}

// TestRunLoggedRedondeaElTiempoADiezesDeMil: el elapsed que ve el agente está
// redondeado a 10 ms, no en nanos.
//
// No es un detalle: un elapsed con nanosegundos hace que dos ejecuciones del
// mismo build den números distintos y un agente que compare salidas vería un
// cambio donde no lo hay.
func TestRunLoggedRedondeaElTiempoADiezesDeMil(t *testing.T) {
	dir := t.TempDir()
	elapsed, _, err := runLogged("build", "sleep 0.05", dir, filepath.Join(dir, "o.log"), filepath.Join(dir, "e.log"))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed%10*time.Millisecond != 0 {
		t.Errorf("elapsed = %v no está redondeado a 10ms: dos ejecuciones darían números distintos", elapsed)
	}
}

// TestCmdLaunchPropagaElFalloDeSesionYDeEscaneo: `launch` construye store y
// escaneo DESPUÉS de resolver el stack, así que sus dos fallos sólo se ven en un
// launch de verdad (no en --list).
//
// Y esa es justo la razón por la que --list no los tenía antes: compartían
// preámbulo. Separados, --list responde sin disco y un launch real responde con
// el motivo cuando el disco falla.
func TestCmdLaunchPropagaElFalloDeSesionYDeEscaneo(t *testing.T) {
	t.Run("store ilegible", func(t *testing.T) {
		root := cliEnv(t)
		t.Chdir(root)
		composeStack(t, root, "front", [2]string{"front", `"web"`})
		brokenStateHome(t)

		_, err := cmdLaunch([]string{"front"})
		if err == nil {
			t.Fatal("con un state dir inutilizable, launch debería fallar")
		}
		if strings.Contains(err.Error(), "scan error") {
			t.Errorf("un store ilegible no es un fallo de escaneo: %q", err)
		}
	})

	t.Run("escaneo fallido", func(t *testing.T) {
		root := cliEnv(t)
		t.Chdir(root)
		composeStack(t, root, "front", [2]string{"front", `"web"`})
		writeConfigScannerRoot(t, filepath.Join(root, "api", ".vroom.toml")) // un fichero

		_, err := cmdLaunch([]string{"front"})
		if err == nil {
			t.Fatal("con un root que no es un directorio, launch debería fallar")
		}
		if !strings.Contains(err.Error(), "scan error") {
			t.Errorf("err = %q, want el prefijo 'scan error'", err)
		}
	})
}

// TestDispatchReparteLaunchYHelp: los dos subcomandos que no comparten la tabla
// de flags.
//
// Es un test tonto a propósito: la tabla de dispatch grew y un case mal pegado
// (un comando que cae en `default` y por tanto en la TUI) es silencioso — nadie
// ve nada raro, simplemente `vroom help` abre la TUI.
func TestDispatchReparteLaunchYHelp(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)
	composeStack(t, root, "front", [2]string{"front", `"web"`})

	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"launch", "--list"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			payload, handled, err := dispatch(args)
			if !handled || err != nil {
				t.Fatalf("dispatch(%v) = handled %v, err %v; si un comando cae en default abre la TUI en vez de responder", args, handled, err)
			}
			if payload == nil {
				t.Errorf("dispatch(%v) devolvió payload nil", args)
			}
		})
	}
}

// TestCmdLaunchDryConUnServicioInexistenteEsError: --dry NO es una vista previa
// que siempre funciona: valida igual que un launch de verdad.
//
// Es la diferencia entre un dry-run útil y un dry-run decorativo. Si DryRun
// devolviera un plan para un stack cuyo servicio no existe, el agente leería
// "OK" y luego el launch real fallaría, con lo que el dry-run no le habría
// ahorrado nada. El mensaje lo redacta validateServices y aquí no se reescribe.
func TestCmdLaunchDryConUnServicioInexistenteEsError(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)
	composeStack(t, root, "front", [2]string{"front", `"no-existe"`})

	payload, err := cmdLaunch([]string{"front", "--dry"})
	if err == nil {
		t.Fatalf("un --dry con un servicio inexistente debería fallar, devolvió %+v", payload)
	}
	if !strings.Contains(err.Error(), "no-existe") {
		t.Errorf("err = %q, want el nombre del servicio que no se encuentra", err)
	}
	if payload != nil {
		t.Errorf("con error no debe devolverse plan: %+v", payload)
	}

	// Y el launch real del mismo stack falla con el mismo motivo: el dry-run no
	// es más tolerante que el launch, y por eso el test de --dry tiene sentido.
	if _, err := cmdLaunch([]string{"front"}); err == nil {
		t.Error("el launch real del mismo stack debería fallar con el mismo servicio inexistente")
	}
}

// TestRunLoggedSinShDevuelveElErrorDeEjecucion: si no hay intérprete, el error no
// es un ExitError y el código se queda en 0.
//
// La consecuencia es de contrato: `build ok, exit_code 0` con un comando que
// nunca corrió sería un build fantasma. El código 0 con Error presente es
// distinguible por el agente, y ese es el motivo de que el campo Error exista
// junto al ExitCode.
//
// Se provoca vaciando el PATH para que exec no encuentre `sh`.
func TestRunLoggedSinShDevuelveElErrorDeEjecucion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")

	_, code, err := runLogged("build", "echo hola", dir,
		filepath.Join(dir, "o.log"), filepath.Join(dir, "e.log"))
	if err == nil {
		t.Fatal("sin intérprete el comando no puede ejecutarse")
	}
	if code != 0 {
		t.Errorf("code = %d, want 0: no hubo exit code porque no hubo proceso", code)
	}
	// Y el log deja constancia, porque el fallo ocurrió después del banner.
	if body := readFileString(t, filepath.Join(dir, "o.log")); !strings.Contains(body, "build failed") {
		t.Errorf("el log no anota el fallo:\n%s", body)
	}
}

// TestRunConUnComandoQueNoFallaDevuelveQueLoManejo: Run con éxito devuelve true
// y NO mata el proceso, así que es ejecutable en un test.
//
// Es toda la parte de Run que se puede ejercitar en proceso: la otra es el
// os.Exit del código de error, y esa sale por definición del proceso. Por eso
// la cobertura de Run se queda en su rama de éxito, y por eso el contrato de
// error se verifica con runInto, que recibe los writers y devuelve el código.
//
// El JSON sale por el stdout REAL del binario de test, que `go test` se come
// salvo con -v. Es el precio de probar la función de verdad en vez de su copia.
func TestRunConUnComandoQueNoFallaDevuelveQueLoManejo(t *testing.T) {
	root := cliEnv(t)
	_ = chdirTree(t, root)

	if !Run([]string{"help"}) {
		t.Fatal("Run(help) debería manejar el comando")
	}
	// Y sin subcomando devuelve false, que es lo que hace que main lance la TUI.
	if Run(nil) {
		t.Error("Run sin argumentos no debe manejar nada: main lanzaría la TUI dos veces")
	}
}

// TestNormalizePathConUnDirectorioBorradoNoFalla: filepath.Abs necesita el CWD, y
// si el CWD ya no existe devuelve error.
//
// Es alcanzable de verdad —basta con borrar el directorio en el que se está— y
// la respuesta correcta es CAER al path tal cual en vez de propagar el error: el
// llamador de findByPath compara normalizados, y un path sin normalizar sólo
// emparejará con otro sin normalizar, que es el mejor caso posible cuando el
// directorio desapareció. Devolver error convertiría un nombre mal escrito en
// "no se pudo resolver el directorio de trabajo".
func TestNormalizePathConUnDirectorioBorradoNoFalla(t *testing.T) {
	gone := t.TempDir()
	t.Chdir(gone)
	// Borrar el directorio en el que estamos deja al proceso sin CWD.
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	// MEDIDO: sin CWD, filepath.Abs falla y el resultado es el path TAL CUAL, no
	// uno absoluto. Es lo único que se puede hacer sin CWD, y es coherente con lo
	// que hace el llamador: findByPath normaliza ambos lados con la misma
	// función, así que un path sin normalizar sólo empareja con otro sin
	// normalizar. Lo que NO puede pasar es propagar el error, porque eso
	// convertiría un nombre mal escrito en "no se pudo resolver el directorio de
	// trabajo".
	got := normalizePath("api")
	if got != "api" {
		t.Errorf("normalizePath = %q sin CWD, want el path tal cual", got)
	}

	// Y con CWD válido vuelve a hacer su trabajo.
	t.Chdir(t.TempDir())
	full := filepath.Join(mustGetwd(t), "api")
	if got := normalizePath("api"); got != full {
		t.Errorf("normalizePath = %q con CWD válido, want %q", got, full)
	}
	// Un path que existe se normaliza con symlinks resueltos, que es lo que hace
	// que --path con un symlink apunte al proyecto correcto.
	real := t.TempDir()
	link := filepath.Join(filepath.Dir(real), "enlace")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := normalizePath(link); got != real {
		t.Errorf("normalizePath(symlink) = %q, want el destino %q", got, real)
	}

}

// mustGetwd devuelve el CWD actual o falla el test.
func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
