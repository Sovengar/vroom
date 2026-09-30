package startsvc

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/state"
)

func listen(port int) (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
}

// ---- Escenario: arranque dynamic con la app honrando PORT ----

func TestDynamicStartResolvesRealPort(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("arranque dynamic: %v", err)
	}
	f.cleanup(t, out)

	if out.Port < process.DynamicPortLow || out.Port > process.DynamicPortHigh {
		t.Errorf("el puerto debe caer en %d-%d, got %d", process.DynamicPortLow, process.DynamicPortHigh, out.Port)
	}
	if !process.PortOpen(out.Port) {
		t.Errorf("el proceso no escucha en el puerto resuelto %d", out.Port)
	}
	if got := f.helperEnv(t)["PORT_SEEN"]; got != strconv.Itoa(out.Port) {
		t.Errorf("PORT en el hijo = %q, want %d", got, out.Port)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("una app que honra PORT no debe gerar avisos: %v", out.Warnings)
	}
}

// El puerto real se persiste ANTES de que el arranque devuelva el control.
func TestDynamicStartPersistsBeforeReturning(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("arranque dynamic: %v", err)
	}
	f.cleanup(t, out)

	meta, err := f.store.LoadMeta(f.dir)
	if err != nil {
		t.Fatalf("meta.json debe existir al volver de Start: %v", err)
	}
	if meta.Port != out.Port {
		t.Errorf("meta.Port = %d, want el puerto real %d", meta.Port, out.Port)
	}
	if meta.State != state.StateRunning {
		t.Errorf("meta.State = %q, want running", meta.State)
	}
	if !meta.PortVerified {
		t.Error("un listener confirmado debe marcarse verificado")
	}
	if meta.Pid != out.Pid {
		t.Errorf("meta.Pid = %d, want %d", meta.Pid, out.Pid)
	}
}

// Coherencia: display, JSON y sonda de salud usan el MISMO número. Aquí se
// comprueba la verdad única (meta.Port) y que el declarado no aparece.
func TestDynamicPortIsTheSingleSourceOfTruth(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("arranque dynamic: %v", err)
	}
	f.cleanup(t, out)

	meta, err := f.store.LoadMeta(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Port == f.manifest.Port {
		t.Fatal("el puerto real debe diferir del declarado para que la prueba signifique algo")
	}
	// La sonda de salud gatea contra meta.Port, no contra el manifiesto.
	if !process.PortOpen(meta.Port) {
		t.Errorf("la sonda debe apuntar a %d, que está abierto", meta.Port)
	}
	if process.PortOpen(f.manifest.Port) {
		t.Error("nadie debería estar escuchando en el puerto declarado")
	}
}

// ---- Escenario: el entorno del hijo NO se trunca ----

func TestChildEnvIsNotTruncated(t *testing.T) {
	f := newFixture(t)
	t.Setenv("PATH", os.Getenv("PATH"))
	f.command(t, "honors-port")
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("arranque dynamic: %v", err)
	}
	f.cleanup(t, out)

	env := f.helperEnv(t)
	if env["PATH_SEEN"] == "" {
		t.Error("PATH no llegó al hijo: cmd.Env reemplazó os.Environ()")
	}
	if env["HOME_SEEN"] == "" {
		t.Error("HOME no llegó al hijo")
	}
	if env["PORT_SEEN"] == "" {
		t.Error("PORT no llegó al hijo")
	}
	if env["HOST_SEEN"] != "127.0.0.1" {
		t.Errorf("HOST = %q, want 127.0.0.1", env["HOST_SEEN"])
	}
}

// El hijo resuelve comandos por su PATH: la prueba no puede caer por un PATH
// truncado.
func TestChildResolvesCommandsByPath(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("arranque dynamic: %v", err)
	}
	f.cleanup(t, out)

	if got := f.helperEnv(t)["SH_RESOLVED"]; got != "ok" {
		t.Errorf("el hijo no pudo resolver sh por su PATH: %q", got)
	}
}

// ---- Escenario: la app ignora el puerto reservado (aviso, no error) ----

func TestAppIgnoringPortIsWarningNotError(t *testing.T) {
	f := newFixture(t)
	own := freePort(t)
	f.command(t, "fixed-port", "VROOM_HELPER_PORT="+strconv.Itoa(own))
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("ignorar PORT no debe fallar el arranque: %v", err)
	}
	f.cleanup(t, out)

	if out.Port != own {
		t.Errorf("el puerto real descubierto debe ser el de la app (%d), got %d", own, out.Port)
	}
	if len(out.Warnings) == 0 {
		t.Fatal("debe emitirse un aviso visible de que la app ignoró el puerto")
	}
	if !strings.Contains(out.Warnings[0], strconv.Itoa(own)) {
		t.Errorf("el aviso debe nombrar el puerto real: %q", out.Warnings[0])
	}
	if out.Meta.State != state.StateRunning {
		t.Errorf("el servicio sigue siendo operable, state = %q", out.Meta.State)
	}
}

// ---- Escenario: un servicio lento conserva su puerto ----

func TestSlowBindKeepsItsPort(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port", "VROOM_HELPER_DELAY=3s")
	out, err := f.start(t, 10*time.Second)
	if err != nil {
		t.Fatalf("arranque dynamic: %v", err)
	}
	f.cleanup(t, out)

	if out.Port == 0 {
		t.Fatal("un bind lento no debe reportarse como sin puerto")
	}
	if out.Meta.State != state.StateRunning {
		t.Errorf("state = %q, want running", out.Meta.State)
	}
	if !process.PortOpen(out.Port) {
		t.Errorf("el puerto %d debería estar abierto", out.Port)
	}
}

// ---- Escenario: servicio sin puerto TCP no cuelga el arranque ----

func TestNoTCPPortIsRecordedNotHung(t *testing.T) {
	f := newFixture(t)
	f.command(t, "udp-only")

	start := time.Now()
	out, err := f.start(t, 2*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("un servicio sin puerto TCP no es un fallo de arranque: %v", err)
	}
	f.cleanup(t, out)

	if elapsed > 8*time.Second {
		t.Errorf("el arranque no debe colgarse: tardó %s", elapsed)
	}
	if out.Meta.State != state.StateNoPort {
		t.Errorf("state = %q, want no_port", out.Meta.State)
	}
	if out.Meta.Port != 0 {
		t.Errorf("un servicio sin puerto debe persistir 0, got %d", out.Meta.Port)
	}
	if len(out.Warnings) == 0 {
		t.Error("debe quedar registrado explícitamente que no hay puerto")
	}
	if out.Pid <= 0 {
		t.Error("el servicio debe quedar igualmente operable")
	}
}

// ---- Escenario: un servicio que muere al arrancar se detecta rápido ----

func TestDeadAtStartupFailsFast(t *testing.T) {
	f := newFixture(t)
	f.command(t, "die")

	start := time.Now()
	_, err := f.start(t, 30*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("un servicio que muere debe reportarse como fallo de arranque")
	}
	if elapsed > 5*time.Second {
		t.Errorf("el fallo debe ser del orden de 1s, tardó %s (agotó el timeout del discovery)", elapsed)
	}
}

// ---- Retrocompatibilidad: sin port_mode nada cambia ----

func TestFixedModeBehavesExactlyAsBefore(t *testing.T) {
	f := newFixture(t)
	f.manifest.PortMode = "" // manifiesto existente, sin el campo nuevo
	own := freePort(t)
	f.command(t, "fixed-port", "VROOM_HELPER_PORT="+strconv.Itoa(own))

	out, err := f.start(t, 30*time.Second)
	if err != nil {
		t.Fatalf("arranque fixed: %v", err)
	}
	f.cleanup(t, out)

	meta, _ := f.store.LoadMeta(f.dir)
	if meta.Port != f.manifest.Port {
		t.Errorf("en fixed meta.Port debe ser el declarado (%d), got %d", f.manifest.Port, meta.Port)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("fixed no debe emitir avisos nuevos: %v", out.Warnings)
	}
	if env := f.helperEnv(t); env["PORT_SEEN"] != "" {
		t.Errorf("en fixed no se inyecta PORT, llegó %q", env["PORT_SEEN"])
	}
}

func TestNoneModeStartsWithoutPort(t *testing.T) {
	f := newFixture(t)
	f.manifest.PortMode = manifest.PortModeNone
	f.manifest.Port = 0
	f.command(t, "udp-only")

	out, err := f.start(t, 30*time.Second)
	if err != nil {
		t.Fatalf("arranque none: %v", err)
	}
	f.cleanup(t, out)

	if out.Port != 0 {
		t.Errorf("none no debe tener puerto, got %d", out.Port)
	}
	if env := f.helperEnv(t); env["PORT_SEEN"] != "" {
		t.Errorf("none no inyecta PORT, llegó %q", env["PORT_SEEN"])
	}
	if out.Meta.State == state.StatePortPending {
		t.Error("none nunca está pendiente de puerto")
	}
}

// La ventana de arranque nunca reporta un proceso vivo como detenido: el
// intento queda persistido como pendiente ANTES del discovery.
func TestStartWindowNeverReportsDeadProcessAsStopped(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port", "VROOM_HELPER_DELAY=2s")

	// Arrancamos en paralelo y observamos el disco durante la ventana.
	done := make(chan Result, 1)
	go func() {
		out, err := f.start(t, 10*time.Second)
		if err != nil {
			t.Errorf("arranque: %v", err)
		}
		done <- out
	}()

	deadline := time.Now().Add(2 * time.Second)
	sawPending := false
	for time.Now().Before(deadline) {
		meta, err := f.store.LoadMeta(f.dir)
		if err == nil && meta.Pid > 0 {
			if meta.State != state.StatePortPending {
				t.Fatalf("durante la ventana el estado debe ser port_pending, es %q", meta.State)
			}
			sawPending = true
		}
		time.Sleep(50 * time.Millisecond)
	}
	out := <-done
	f.cleanup(t, out)

	if !sawPending {
		t.Skip("el discovery se resolvió antes de poder observar la ventana")
	}
	if out.Meta.State != state.StateRunning {
		t.Errorf("al resolverse el puerto el estado pasa a vivo, es %q", out.Meta.State)
	}
}

// Dos servicios dynamic del mismo manifiesto base reciben puertos distintos:
// es la premisa del feature (dos worktrees a la vez).
func TestTwoDynamicStartsGetDistinctPorts(t *testing.T) {
	a, b := newFixture(t), newFixture(t)
	a.command(t, "honors-port")
	b.command(t, "honors-port")

	outA, err := a.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("arranque A: %v", err)
	}
	a.cleanup(t, outA)
	outB, err := b.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("arranque B: %v", err)
	}
	b.cleanup(t, outB)

	if outA.Port == outB.Port {
		t.Errorf("dos servicios simultaneouss comparten el puerto %d", outA.Port)
	}
}

// Dynamic necesita un puerto por defecto: es el PORT=${PORT:-N} de la app,
// y sin él el contrato con la app no existe.
func TestDynamicRequiresDefaultPort(t *testing.T) {
	m := &manifest.Manifest{Name: "x", Command: "true", PortMode: manifest.PortModeDynamic}
	if err := m.Validate(); err == nil {
		t.Error("dynamic sin puerto por defecto debe rechazarse en validación")
	}
}

// ---- Slice 3: desambiguación multi-puerto (R2 / R3) ----

// R2 en el arranque completo: la app ignora PORT, abre dos listeners y sólo
// uno responde bien en health_path. Gana ese, y el puerto queda verificado.
func TestR2HealthPathDecidesMainPort(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	f := newFixture(t)
	f.manifest.HealthPath = "/health"
	f.command(t, "two-http-ports",
		"VROOM_HELPER_PORT_A="+strconv.Itoa(freePort(t)),
		"VROOM_HELPER_GOOD="+strconv.Itoa(freePort(t)))

	out, err := f.start(t, 10*time.Second)
	if err != nil {
		t.Fatalf("arranque dynamic: %v", err)
	}
	f.cleanup(t, out)

	env := f.helperEnv(t)
	good := mustAtoiT(t, env["GOOD_PORT"])
	if out.Port != good {
		t.Errorf("R2 debe elegir el listener que responde en health_path (%d), eligió %d", good, out.Port)
	}
	if !out.Meta.PortVerified {
		t.Error("R2 verifica el puerto: alguien respondió")
	}
}

// R3 en el arranque completo: dos listeners que no son HTTP. Gana el menor,
// de forma determinista, y el servicio se marca como puerto NO verificado con
// un aviso que lo dice.
func TestR3NonHTTPPicksLowestAndMarksUnverified(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	f := newFixture(t)
	f.manifest.HealthPath = "/health"
	f.command(t, "two-raw-ports",
		"VROOM_HELPER_PORT_A="+strconv.Itoa(freePort(t)),
		"VROOM_HELPER_PORT_B="+strconv.Itoa(freePort(t)))

	out, err := f.start(t, 10*time.Second)
	if err != nil {
		t.Fatalf("arranque dynamic: %v", err)
	}
	f.cleanup(t, out)

	env := f.helperEnv(t)
	low := minPort(mustAtoiT(t, env["PORT_A"]), mustAtoiT(t, env["PORT_B"]))
	if out.Port != low {
		t.Errorf("R3 debe elegir el menor puerto %d, eligió %d", low, out.Port)
	}
	if out.Meta.PortVerified {
		t.Error("sin respuesta HTTP el puerto no está verificado")
	}
	found := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "unverified") {
			found = true
		}
	}
	if !found {
		t.Errorf("debe declararse que no se puede saber cuál es el principal: %v", out.Warnings)
	}

	// Determinista entre corridas con el mismo conjunto de listeners.
	again := newFixture(t)
	again.manifest.HealthPath = "/health"
	again.command(t, "two-raw-ports",
		"VROOM_HELPER_PORT_A="+strconv.Itoa(freePort(t)),
		"VROOM_HELPER_PORT_B="+strconv.Itoa(freePort(t)))
	out2, err := again.start(t, 10*time.Second)
	if err != nil {
		t.Fatalf("segundo arranque: %v", err)
	}
	again.cleanup(t, out2)
	env2 := again.helperEnv(t)
	low2 := minPort(mustAtoiT(t, env2["PORT_A"]), mustAtoiT(t, env2["PORT_B"]))
	if out2.Port != low2 {
		t.Errorf("R3 debe ser determinista: %d vs %d", out2.Port, low2)
	}
}
