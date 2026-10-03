package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/process"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Utilidades del harness de comandos.
//
// Todo aquí existe para poder ejercer un comando REAL contra un árbol REAL en un
// disco REAL. No hay dobles de scanner, store ni manager a propósito: el JSON que
// un agente consume es el efecto de un escaneo de verdad sobre un directorio de
// verdad, y un doble probaría el doble.
//
// Lo que sí se sustituye es el ENTORNO (VROOM_CONFIG, XDG_STATE_HOME, CWD), que
// no es parte de lo que se quiere verificar sino la Isolation para no tocar el
// estado del developer. Y donde se toca algo real (un start), se comprueba el
// efecto real: el proceso existe y el Meta está en disco.
// ---------------------------------------------------------------------------

// errWrite es el fallo de un destino que no acepta escritura.
var errWrite = errors.New("no se pudo escribir")

// chdirTree mueve el proceso al árbol y devuelve el store que leería de
// XDG_STATE_HOME.
//
// Devolver el store es lo que permite comprobar el EFECTO sobre disco —el Meta
// escrito, el log truncado— en vez de sólo lo que el comando afirma. Un comando
// puede afirmar cualquier cosa sobre sí mismo.
func chdirTree(t *testing.T, root string) *state.Store {
	t.Helper()
	t.Chdir(root)
	store, err := state.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// writeFile escribe un fichero, creando los directorios intermedios, y devuelve
// su ruta.
func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// readFileString lee un fichero devolviendo "" si no existe. Los logs de un
// servicio que nunca arrancó no existen, y eso no es un fallo del test.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// writeLines escribe una línea por entrada, con salto de línea final, que es como
// un proceso real escribe en su log.
func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	writeFile(t, path, strings.Join(lines, "\n")+"\n")
}

// linesOf parte un texto en líneas sin contar la vacía final.
func linesOf(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// processAlive mira /proc, que es el mismo mecanismo que usa el resto del repo
// para comprobar liveness. Se dice "mira" y no "pregunta al manager" porque la
// pregunta interesante es si el PID existe de verdad, no qué diría un doble.
func processAlive(t *testing.T, pid int) bool {
	t.Helper()
	if pid <= 0 {
		return false
	}
	if _, err := os.Stat(procDir(pid)); err != nil {
		return false
	}
	// Un zombie tiene /proc/<pid> pero ya no es un proceso vivo: su estado es Z.
	data, err := os.ReadFile(procDir(pid, "stat"))
	if err != nil {
		return false
	}
	return !strings.Contains(string(data), ") Z ")
}

func procDir(pid int, parts ...string) string {
	base := "/proc/" + itoa(pid)
	for _, p := range parts {
		base += "/" + p
	}
	return base
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// waitGone espera a que el proceso desaparezca.
//
// El timeout no es decorativo: process.Stop mata por SIGTERM y luego por SIGKILL,
// y el kernels tarda en recoger al zombie. Sin espera, un test que comprueba "el
// proceso ya no está" falla de forma intermitente y el suite se vuelve ruido.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(t, pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("el proceso %d sigue vivo tras el stop: waitGone agotó su plazo", pid)
}

// stopService baja un servicio con el manager REAL, por el mismo stopCleanup que
// usa el comando. Los tests que arrancan algo tienen que pararlo: un sleep 30
// por test son treinta segundos de procesos zombies acumulados en el runner.
func stopService(t *testing.T, store *state.Store, path string) {
	t.Helper()
	if err := stopCleanup(store, process.NewManager(), path); err != nil {
		t.Fatalf("limpieza del servicio %s: %v", path, err)
	}
}

// aliveManager es un manager que registra los Stop sin tocar procesos. Existe para
// los tests que necesitan el contrato de process.Manager sin pagar un kill real,
// que es un efecto secundario que no tienen nada que ver con lo que verifican.
type aliveManager struct {
	stopped []process.StopSpec
	warned  int
	killErr error
}

func (m *aliveManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}

func (m *aliveManager) Stop(spec process.StopSpec) error {
	m.stopped = append(m.stopped, spec)
	if m.killErr != nil {
		return m.killErr
	}
	// Emite el mismo aviso que emitiría el kill real, para que el camino que
	// escribe los avisos en el log quede ejercitado por quien use este doble.
	if spec.Warn != nil {
		spec.Warn("stopped pid %d", spec.Pid)
		m.warned++
	}
	return nil
}

func (m *aliveManager) Evaluate(process.EvalSpec) process.Status {
	return process.StatusStopped
}

var _ process.Manager = (*aliveManager)(nil)

// listeningService reescribe el manifiesto de un proyecto del árbol para que
// declare un puerto que REALMENTE está escuchando, y devuelve ese puerto.
//
// Por qué hace falta: el veredicto de "corriendo" de process.Evaluate exige que
// el puerto declarado esté abierto cuando hay puerto declarado. Un `sleep` con
// `port = 8081` y nada escuchando NO está corriendo según el contrato —está
// vivo pero no sirve— y el árbol por defecto es exactamente eso.
//
// Poner un listener real de loopback es lo que hace que el test ejercite el
// camino de "ya corriendo" sin escribir un servidor de mentira: lo que se
// comprueba es el dial de verdad, no un doble que diga que sí.
func listeningService(t *testing.T, root, dir string, extraTOML string) int {
	t.Helper()
	port := openPort(t)
	body := "command_start = \"sleep 300\"\nport = " + itoa(port) + "\n" + extraTOML
	writeFile(t, filepath.Join(root, dir, ".vroom.toml"), body)
	return port
}

// failingWriter falla siempre, como un stdout cerrado o un pipe roto.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

// installCLIFailingReleaser apunta la retirada de la CLI a un releaser que
// siempre falla, para poder observar qué hace stopCleanup con un portless roto.
//
// Existe aparte de installCLIReleaser porque los tests que necesitan leer QUÉ
// se retiró necesitan el doble concreto, y los que sólo necesitan que falle no.
func installCLIFailingReleaser(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { cliReleaseStub, cliReleaseStubInstalled = nil, false })
	cliReleaseStub = (&failingReleaser{}).RemoveAbsent
	cliReleaseStubInstalled = true
}

// makeDir crea un directorio y devuelve su ruta. Se usa donde el código espera un
// FICHERO: un OpenFile con O_CREATE sobre un directorio falla con EISDIR, que es
// un fallo que se puede provocar sin depender del uid.
func makeDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
