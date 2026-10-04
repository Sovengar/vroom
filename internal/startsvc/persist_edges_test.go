package startsvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"vroom/internal/process"
	"vroom/internal/state"
)

// TestStopAfterPersistFailureNoHaceNadaSinPid: el res vacío.
//
// `stopAfterPersistFailure` recibe el `StartResult` del hijo que hay que parar. Un
// `Pid` de 0 significa que no hubo hijo —o que el gestor no devolvió cuál—, y en ese
// caso `Stop` no tiene nada que hacer: para `kill`, el grupo 0 es "mi propio grupo de
// procesos".
//
// La rama importa porque `Start` devuelve `Result{}` cuando el spawn falla, y el
// camino de persistencia llama a este helper con ese mismo `Result{}`: sin la guarda,
// un fallo de disco en un arranque fallido intentaría parar el grupo de procesos de
// vroom, que en la TUI es el proceso que el usuario está usando.
func TestStopAfterPersistFailureNoHaceNadaSinPid(t *testing.T) {
	paradas := 0
	req := Request{
		Store:   state.NewStoreAt(t.TempDir()),
		Manager: &contadorManager{paradas: &paradas},
	}

	stopAfterPersistFailure(req, process.StartResult{})
	stopAfterPersistFailure(req, process.StartResult{Pid: -1, Pgid: -1})

	if paradas != 0 {
		t.Errorf("se pararon %d veces sin un pid válido: sin proceso no hay nada que parar, y un "+
			"Pid de 0 es 'mi propio grupo' para kill(-0)", paradas)
	}
}

// TestStopAfterPersistFailureParaElHijoQueHay: el caso normal.
//
// Con un pid, el helper tiene que pararlo. Se usa un `sleep` real para que el efecto
// sea observable —el proceso se va— y no un contador en un doble.
func TestStopAfterPersistFailureParaElHijoQueHay(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	req := Request{Store: store, Manager: process.NewManager()}

	// Un hijo real con su propio grupo, como lo deja `setsid`.
	pid, pgid := lanzarHijo(t, "sleep", "30")
	if !vivo(pid) {
		t.Fatalf("el hijo %d no llegó a arrancar", pid)
	}

	stopAfterPersistFailure(req, process.StartResult{Pid: pid, Pgid: pgid})

	esperarAQueMuera(t, pid)
}

// TestPersistOrKillParaElHijoCuandoElMetaNoSePuedeGuardar: el fallo de disco.
//
// Es el bug que se arregló, y lo que se comprueba es el efecto entero: el servicio no
// llega a existir, así que su proceso no puede quedarse vivo. Un `Start` que devuelve
// un error pero deja un hijo ocupando un puerto es el peor resultado posible, porque
// el siguiente `vroom start` arranca un segundo por encima del mismo puerto.
//
// El fallo se provoca de verdad, sin tocar la persistencia: `meta.json` pasa a ser un
// directorio no vacío, y el `rename` del atómico falla con EISDIR.
func TestPersistOrKillParaElHijoCuandoElMetaNoSePuedeGuardar(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	const proyecto = "/srv/api"

	dir, err := store.EnsureServiceDir(proyecto)
	if err != nil {
		t.Fatal(err)
	}
	if err := mkdirConContenido(filepath.Join(dir, "meta.json")); err != nil {
		t.Fatal(err)
	}

	pid, pgid := lanzarHijo(t, "sleep", "30")
	req := Request{Path: proyecto, Store: store, Manager: process.NewManager()}

	if err := persistOrKill(req, state.Meta{Name: "api"}, process.StartResult{Pid: pid, Pgid: pgid}); err == nil {
		t.Fatal("persistOrKill = nil con un meta que no se puede guardar")
	} else if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want que nombre el fichero: el mensaje es lo que le dice al usuario "+
			"que su directorio de estado está en mal estado", err)
	}

	// El hijo tiene que estar muerto: sin meta no hay nada que gestionar, y un proceso
	// que su gestor no conoce es peor que un arranque fallido.
	esperarAQueMuera(t, pid)
}

// TestPersistOrKillGuardaCuandoPuede: el camino bueno.
//
// Lo contrario del anterior: si el meta se puede escribir, el hijo se deja vivo y no
// se toca. Un `persistOrKill` que parara siempre sería un `Start` que nunca arranca
// nada.
func TestPersistOrKillGuardaCuandoPuede(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	const proyecto = "/srv/api"

	pid, pgid := lanzarHijo(t, "sleep", "30")
	req := Request{Path: proyecto, Store: store, Manager: process.NewManager()}

	if err := persistOrKill(req, state.Meta{Name: "api", Pid: pid, State: state.StateRunning},
		process.StartResult{Pid: pid, Pgid: pgid}); err != nil {
		t.Fatalf("persistOrKill: %v", err)
	}

	meta, err := store.LoadMeta(proyecto)
	if err != nil {
		t.Fatalf("el meta no se guardó: %v", err)
	}
	if meta.Pid != pid {
		t.Errorf("meta.Pid = %d, want %d", meta.Pid, pid)
	}
	if !vivo(pid) {
		t.Error("el hijo fue parado aunque el meta se guardó bien: un Start que nunca arranca " +
			"nada tampoco es un acierto")
	}
}

// ---------------------------------------------------------------------------
// Utilidades
//
// Los hijos son REALES y no dobles: lo que se comprueba en `persistOrKill` es que un
// proceso deja de existir, y un contador en un doble no demuestra nada de eso.
// ---------------------------------------------------------------------------

// lanzarHijo arranca un comando en su propio grupo de procesos, como hace `Start` con
// `setsid`, y devuelve pid y pgid.
func lanzarHijo(t *testing.T, name string, args ...string) (pid, pgid int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid = cmd.Process.Pid
	pgid = pid // con setsid, pgid == pid
	// El reaper en goroutine es lo que hace el `Start` de verdad, y es lo que hace que
	// estos tests sean rápidos: sin recoger, el proceso queda ZOMBIE y `waitLineageGone`
	// lo cuenta como vivo hasta agotar los 5s del timeout, porque un zombie sigue
	// ocupando su grupo. Con el reaper, el grupo se vacía en cuanto el proceso muere,
	// que es lo que pasa en producción.
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { parar(t, pid) })
	return pid, pgid
}

// parar mata el proceso y lo recoge. Se puede llamar aunque ya haya muerto: es el
// `t.Cleanup` de `lanzarHijo` y no debe fallar en ese caso.
func parar(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.Signal(0)) // comprobación de vida, sin efecto
}

// esperarAQueMuera espera a que un pid deje de estar vivo.
func esperarAQueMuera(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !vivo(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("el proceso %d seguía vivo 10s después de pedirle que parara: el helper no lo paró", pid)
}

// vivo dice si el pid está corriendo de verdad.
//
// MEDIDO: mirar sólo si existe `/proc/<pid>` no basta. El hijo es de este mismo proceso
// de test, así que si nadie lo recoge queda ZOMBIE y `/proc/<pid>` sigue ahí durante
// ese rato; un "está vivo" por existencia daría un falso negativo y haría pensar que el
// helper no para nada.
//
// El estado se lee del tercer campo del `stat`, el mismo criterio que usa
// `internal/process`: un zombie no tiene memoria ni puertos y ya no hace nada.
func vivo(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 || i+2 >= len(data) {
		return false
	}
	return string(data[i+2]) != "Z"
}

// mkdirConContenido crea un directorio no vacío, que es lo que hace que un `rename` de
// fichero sobre él falle con EISDIR.
func mkdirConContenido(path string) error {
	return os.MkdirAll(filepath.Join(path, "bloqueo"), 0o755)
}

// contadorManager cuenta las paradas sin tocar nada. Sólo lo usa el test que
// comprueba que no se para cuando no hay pid.
type contadorManager struct {
	process.Manager
	paradas *int
}

func (m *contadorManager) Stop(process.StopSpec) error {
	*m.paradas++
	return nil
}

// TestElMetaFinalDeDynamicSeGuardaConElHijoYaVivoYLoParaSiNo: el tercer guardado.
//
// Hay TRES guardados después del spawn —el intento de dynamic, el final de dynamic y el
// de fixed— y los tres comparten `persistOrKill`. Los otros dos ya tienen test; este
// cubre el que falta, y el que falta es el más caro de cubrir porque su fallo tiene que
// llegar DE UNA VEZ: el intento se escribe con normalidad y el final falla.
//
// La forma limpia de hacerlo es con el retraso del helper. El servicio sespawna, vroom
// escribe el intento, y el helper espera antes de hacer bind. Con el puerto sin abrir,
// el discovery sigue en vuelo, y en esa ventana el test rompe el destino del `meta.json`.
//
// MEDIDO: el fallo real es el `rename` atómico de `meta.json.tmp` sobre un directorio,
// con EISDIR. No se toca la persistencia ni se hace nada especial: es un directorio de
// estado que alguien sustituyó por otra cosa mientras un servicio arrancaba.
func TestElMetaFinalDeDynamicSeGuardaConElHijoYaVivoYLoParaSiNo(t *testing.T) {
	f := newFixture(t)
	// El helper espera antes de hacer bind: es la ventana en la que el discovery está
	// en vuelo y el meta final todavía no se ha escrito.
	f.command(t, "port", "VROOM_HELPER_DELAY=2s")

	// La espera del helper es de 2s y el timeout de discovery de 4s, así que hay
	// margen de sobra para romper el meta con el discovery todavía corriendo.
	const discovery = 4 * time.Second

	// Se espera a que el hijo haya arrancó de verdad —el helper escribe su informe al
	// empezar— antes de romper nada: tocar el destino antes de que exista el intento
	// haría fallar el primer guardado, que es otro camino.
	go func() {
		esperarAQueAparezca(t, f.outFile)
		// Un poco más de margen: el intento se escribe justo después del spawn.
		time.Sleep(300 * time.Millisecond)
		dir, err := f.store.EnsureServiceDir(f.dir)
		if err != nil {
			return
		}
		_ = os.Remove(filepath.Join(dir, "meta.json"))
		_ = mkdirConContenido(filepath.Join(dir, "meta.json"))
	}()

	res, err := f.start(t, discovery)
	if err == nil {
		t.Fatalf("Start = %+v sin error con el meta final inservible: el servicio se daría por "+
			"arrancado sin registro, y el siguiente start abriría un segundo proceso", res)
	}
	if res.Pid != 0 {
		t.Errorf("Result.Pid = %d con un arranque fallido, want 0: con `Result{}` el caller no tiene "+
			"forma de parar al hijo, y por eso `persistOrKill` lo para por su cuenta", res.Pid)
	}
	if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want que nombre el fichero: el mensaje es lo que le dice al usuario "+
			"que su directorio de estado está en mal estado", err)
	}
}

// esperarAQueAparezca espera a que exista un fichero.
func esperarAQueAparezca(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
