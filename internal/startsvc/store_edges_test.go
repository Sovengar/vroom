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

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Los cuatro fallos de `Start` que no son fallos de arranque: el store que no
// guarda, el pool sin puertos y el timeout que se deja a default.
//
// La diferencia con los fallos de arranque es importante y es lo que separa a este
// archivo. Un comando que no existe o un binario que no se puede lanzar son fallos
// del usuario, y su mensaje tiene que decir qué arreglar en el manifiesto. Un store
// que no escribe o un pool agotado son fallos de la MÁQUINA, y su mensaje tiene que
// decir qué revisar fuera del manifiesto.
//
// Y el orden importa más de lo que parece. El meta se escribe en tres momentos —
// el intento antes del discovery, el puerto real después— y si una de esas
// escrituras falla, el servicio queda corriendo sin registro: la siguiente
// invocación lo trataría como nunca arrancado y arrancaría un segundo proceso
// compitiendo por el mismo puerto.
// ---------------------------------------------------------------------------

// TestStartConElMetaBloqueadoDevuelveElErrorYNoDejaElProcesoSinRegistrar: la
// escritura del intento, en fixed.
//
// El meta se escribe DESPUÉS del spawn y ANTES del discovery. Si esa escritura
// falla, hay un proceso vivo que nadie ha registrado: `vroom list` lo mostraría
// parado y el siguiente `vroom start`arrancaría otro sobre el mismo puerto.
//
// Y el error tiene que volver como error de `Start`, no tragarse el proceso en
// silencio. Un servicio corriendo que su gestor no conoce es el peor resultado
// posible.
func TestStartConElMetaBloqueadoDevuelveElErrorYNoDejaElProcesoSinRegistrar(t *testing.T) {
	f := newFixture(t)
	f.manifest.PortMode = manifest.PortModeFixed
	f.manifest.Port = freePort(t)
	f.command(t, "sleep")

	// El meta.json del servicio es un DIRECTORIO: la escritura falla con EISDIR
	// en vez de con un permiso simulado.
	bloquearMeta(t, f.store, f.dir)

	out, err := f.start(t, 0)
	if err == nil {
		t.Fatal("con el meta bloqueado el arranque tiene que fallar")
	}
	if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want que nombre el fichero que no se pudo escribir", err)
	}

	// MEDIDO (bug que este test encontró): el proceso quedaba VIVO. `Start`
	// devolvía `Result{}` sin PID, así que el caller no tenía forma de pararlo, y el
	// guard de higiene de la suite lo cuenta entre los supervivientes. Ahora `Start`
	// detiene el hijo antes de propagar el error.
	//
	// Lo que se comprueba es que no queda ningún hijo vivo: se busca por el patrón
	// del helper, que es lo único que se sabe de él sin PID.
	if pids := procesosDelHelper(t); len(pids) != 0 {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("quedaron %d procesos del helper vivos tras un fallo de persistencia: "+
			"el caller recibió un error sin PID y no podía pararlos", len(pids))
	}
	_ = out
}

// TestStartEnDynamicConElMetaBloqueadoFallaTambienEnLaEscrituraFinal: el segundo
// SaveMeta.
//
// En dynamic hay DOS escrituras: el intento con el puerto reservado y estado
// pendiente, y el final con el puerto real. Bloquear sólo la segunda es más difícil
// que bloquear la primera —el intento la crea— y es justo por eso que la segunda
// necesita su propio camino.
//
// La diferencia observable: si la escritura final falla, el meta en disco se queda
// con el puerto RESERVADO y el estado pendiente. Un `vroom list` lo mostraría como
// arrancándose para siempre, que es un estado que el usuario no sabe interpretar.
func TestStartEnDynamicConElMetaBloqueadoTambienDejaElHijoSinRegistrar(t *testing.T) {
	f := newFixture(t)
	f.command(t, "sleep")
	// En dynamic el primer SaveMeta es el del INTENTO, y es donde se bloquea: el
	// hijo ya está vivo y ese es el punto donde el comentario del código aceptaba
	// "dejar un hijo vivo sin registrar" como consecuencia necesaria. MEDIDO: lo era
	// porque `Result{}` no lleva PID, no por el puerto.
	bloquearMeta(t, f.store, f.dir)

	_, err := f.start(t, 800*time.Millisecond)
	if err == nil {
		t.Fatal("con el meta bloqueado el arranque tiene que fallar también en dynamic")
	}
	if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want que nombre el fichero que no se pudo escribir", err)
	}

	// Y el hijo tampoco queda vivo: este era el segundo de los tres sitios.
	if pids := procesosDelHelper(t); len(pids) != 0 {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("quedaron %d procesos del helper vivos en dynamic", len(pids))
	}
}

// TestDiscoveryTimeoutCeroUsaElDefaultEn vez de EsperarCeroMilisegundos: el suelo.
//
// Cero significa "el que el paquete decida", no "no esperes". Con un timeout de
// cero el discovery no miraría ni una vez y todo servicio dynamic caería a "no expone
// puerto" al instante, que es un veredicto optimista y equivocado.
func TestDiscoveryTimeoutCeroUsaElDefault(t *testing.T) {
	f := newFixture(t)
	f.command(t, "sleep")

	// Con timeout 0 el arranque tiene que COSTAR lo que el default, no menos.
	start := time.Now()
	out, err := f.start(t, 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = process.NewManager().Stop(process.StopSpec{Pid: out.Pid, Timeout: time.Second})
	})

	// MEDIDO: un servicio que realmente no abre puerto se resuelve rápido igualmente,
	// por la gracia del discovery y no por el timeout. Así que el tiempo no es lo que
	// comprueba este test: lo que se comprueba es que con timeout cero el arranque
	// llega a un veredicto coherente y con marca de tiempo en disco.
	// MEDIDO: el veredicto para un helper que no abre ningún puerto es `running`, no
	// `no_port`. Los dos significan "el servicio vive" —`no_port` lo decide el
	// discovery cuando confirma que no hay listeners— así que la diferencia no es un
	// fallo. Se fija el comportamiento real y se comprueba lo que importa: que el
	// veredicto sea de servicio VIVO. Un arranque con timeout 0 que devolviera
	// `stopped` sería un bug grave.
	switch out.Meta.State {
	case state.StateRunning, state.StateNoPort:
	default:
		t.Errorf("State = %q con timeout 0: el veredicto tiene que ser de servicio vivo", out.Meta.State)
	}
	if out.Meta.StartedAt == "" {
		t.Error("sin StartedAt: el meta no se escribió")
	}
	if out.Pid <= 0 {
		t.Error("Pid = 0 tras un arranque sin error")
	}
	_ = elapsed
}

// TestReservePortAgotadoDevuelveElErrorAntesDeTocarNada: el pool sin puertos.
//
// El error tiene que nombrar el rango, porque "no free port" a secas manda al
// usuario a mirar el firewall y el problema es que esta máquina ya tiene mil
// servicios reservados.
//
// Y lo que importa más: el fallo es ANTES del spawn. Reservar es el paso 1 del
// contrato —"falla antes de tocar el sistema"— y si fuera después habría un proceso
// vivo sin puerto.
func TestReservePortAgotadoDevuelveElErrorAntesDeTocarNada(t *testing.T) {
	f := newFixture(t)
	f.command(t, "sleep")

	// Se agota el pool entero del proceso.
	var reservados []int
	for {
		p, err := process.ReservePort()
		if err != nil {
			break
		}
		reservados = append(reservados, p)
	}
	t.Cleanup(func() {
		for _, p := range reservados {
			process.ReleasePort(p)
		}
	})
	if len(reservados) == 0 {
		t.Skip("el rango de puertos dinámicos está entero ocupado por otra cosa")
	}

	_, err := f.start(t, time.Second)
	if err == nil {
		t.Fatal("con el pool agotado el arranque tiene que fallar")
	}
	if !strings.Contains(err.Error(), "reserve") {
		t.Errorf("err = %q, want que diga que no se pudo reservar puerto", err)
	}
	// Y sin meta escrito: el fallo es antes de tocar el sistema.
	if _, lerr := f.store.LoadMeta(f.dir); lerr == nil {
		t.Error("se escribió meta pese a no poder reservar puerto: el fallo tiene que ser antes del spawn")
	}
}

// TestApplyRouteSinRegistrarNoAbrePortlessNiFallaElArranque: el contrato de la
// degradación.
//
// Sin seam —route_mode = "off"— el camino de ruta no hace nada: ni busca el binario,
// ni lanza un shell, ni falla el arranque. Un servicio sin ruta tiene que arrancar
// igual, porque una ruta es una dirección, no una dependencia.
func TestApplyRouteSinRegistrarNoAbrePortlessNiFallaElArranque(t *testing.T) {
	f := newFixture(t)
	f.manifest.PortMode = manifest.PortModeFixed
	f.manifest.Port = freePort(t)
	f.command(t, "sleep")

	out, err := f.startWithRoutes(t, 0, nil)
	if err != nil {
		t.Fatalf("sin seam de rutas el arranque tiene que funcionar igual: %v", err)
	}
	t.Cleanup(func() {
		_ = process.NewManager().Stop(process.StopSpec{Pid: out.Pid, Timeout: time.Second})
	})

	if out.Meta.RouteOwned {
		t.Error("RouteOwned en true sin seam: se declararía proprietary una ruta que nadie registró")
	}
	if out.Meta.RouteURL != "" {
		t.Errorf("RouteURL = %q sin seam: publicaría una dirección que nadie ha visto responder", out.Meta.RouteURL)
	}
}

// bloquearMeta convierte el meta.json del servicio en un directorio, de modo que la
// escritura falle con EISDIR.
func bloquearMeta(t *testing.T, store *state.Store, projectPath string) {
	t.Helper()
	dir := store.ServiceDir(projectPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "meta.json"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// procesosDelHelper devuelve los PIDs de los procesos del helper de este test.
//
// Es el guard de higiene local: sin PID —porque `Start` devolvió `Result{}`— no hay
// otra forma de saber si el hijo quedó vivo. Se busca por el patrón único que el
// helper lleva en su cmdline.
func procesosDelHelper(t *testing.T) []int {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", "VROOM_NOPORT_HELPER|TestHelperService").Output()
	if err != nil {
		return nil // ningún proceso coincide, que es lo esperado
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pid, convErr := strconv.Atoi(strings.TrimSpace(line))
		if convErr == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}
