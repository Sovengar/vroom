//go:build unix

package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

// unixManager implementa Manager para plataformas unix.
type unixManager struct{}

// NewManager devuelve el Manager de la plataforma actual.
func NewManager() Manager { return &unixManager{} }

// Start daemoniza el comando:
//  1. sh -c "{command}" para soportar pipes/redirecciones
//  2. setsid() → nuevo session leader (sobrevive al cierre de la TUI)
//  3. stdout/stderr redirigidos a ficheros de log (truncados en cada start)
//  4. Un goroutine reaper hace Wait() para evitar zombies mientras la TUI vive
func (u *unixManager) Start(spec StartSpec) (StartResult, error) {
	if spec.Command == "" {
		return StartResult{}, fmt.Errorf("empty command")
	}
	if err := os.MkdirAll(filepath.Dir(spec.StdoutPath), 0o755); err != nil {
		return StartResult{}, fmt.Errorf("could not create log directory: %w", err)
	}

	// Limpiar logs anteriores antes de abrir en modo append.
	_ = os.Truncate(spec.StdoutPath, 0)
	_ = os.Truncate(spec.StderrPath, 0)

	stdout, err := os.OpenFile(spec.StdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return StartResult{}, fmt.Errorf("could not open stdout.log: %w", err)
	}
	defer func() { _ = stdout.Close() }()

	stderr, err := os.OpenFile(spec.StderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return StartResult{}, fmt.Errorf("could not open stderr.log: %w", err)
	}
	defer func() { _ = stderr.Close() }()

	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return StartResult{}, fmt.Errorf("could not open /dev/null: %w", err)
	}
	defer func() { _ = devNull.Close() }()

	cmd := exec.Command("sh", "-c", spec.Command)
	cmd.Dir = spec.WorkDir
	cmd.Stdin = devNull
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// En Go, cmd.Env == nil hereda os.Environ() y CUALQUIER slice no-nil la
	// reemplaza por completo. Inyectar PORT con append(spec.Env, ...) dejaría
	// al hijo con una sola variable y sin PATH. Se fusiona explícitamente.
	cmd.Env = mergeEnv(os.Environ(), spec.Env)

	if err := cmd.Start(); err != nil {
		return StartResult{}, fmt.Errorf("failed to start %q: %w", spec.Command, err)
	}
	pid := cmd.Process.Pid

	// Reaper: el hijo sigue siendo hijo de este proceso hasta que muere;
	// sin Wait() quedaría zombie mientras la TUI esté abierta.
	go func() { _ = cmd.Wait() }()

	result := StartResult{Pid: pid, Pgid: pid} // con setsid, pgid == pid
	if p, err := gopsprocess.NewProcess(int32(pid)); err == nil {
		if ct, err := p.CreateTime(); err == nil {
			result.CreationTimeMs = ct
		}
	}
	return result, nil
}

// mergeEnv fusiona el entorno del padre con las variables del spec. Una
// variable del spec pisa a la del padre; el resto sobrevive. Devuelve nil si
// no hay nada que inyectar, para que exec aplique la herencia normal.
func mergeEnv(parent, extra []string) []string {
	if len(extra) == 0 {
		return nil
	}
	out := make([]string, 0, len(parent)+len(extra))
	out = append(out, parent...)
	for _, kv := range extra {
		key, _, _ := strings.Cut(kv, "=")
		if key == "" {
			continue
		}
		replaced := false
		for i, existing := range out {
			if k, _, _ := strings.Cut(existing, "="); k == key {
				out[i] = kv
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	return out
}

// Stop termina el servicio con una escalera SIGTERM -> espera -> SIGKILL.
//
// Hay dos raíces creíbles y las dos se atienden, porque el linaje se captura
// igual en ambas:
//
//	PGID conocido -> kill(-pgid) alcanza el grupo entero, más los descendientes
//	                re-sid que viven fuera de él (nohup, pm2, docker run -d).
//	PID conocido  -> no hay grupo al que matar, pero la raíz sí es un objetivo
//	                válido y su linaje se señala pid a pid.
//
// Antes todo el bloque estaba bajo `if spec.Pgid > 0`, así que un spec con
// sólo PID —válido según el propio doc de StopSpec— no mataba nada. Eso es lo
// que dejó procesos vivos reteniendo puertos del rango de reserva.
//
// El linaje se captura de /proc ANTES de señalizar: una vez muerto el root sus
// hijos se reparentan a init y la relación se pierde. Sin ninguna raíz
// creíble, Stop es un no-op y el fallback de puerto NO se dispara: la prueba
// de propiedad no se debilita un milímetro.
func (u *unixManager) Stop(spec StopSpec) error {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}

	root, lineage := captureLineage(spec)

	if root > 0 && !terminate(root, spec.Pgid, lineage, timeout) {
		spec.warnf("stop: quedan procesos vivos tras SIGKILL (%s)", lineageDesc(root, lineage))
	}

	// Último recurso: liberar el puerto, pero sólo con prueba de propiedad.
	if spec.Port > 0 && PortOpen(spec.Port) {
		killPortHolderWith(spec.Port, root, lineage, PortOwnerPIDs, spec.Warn)
	}
	return nil
}

// terminate es la escalera de parada, y es la ÚNICA implementación: la vía del
// grupo y la vía del PID la comparten para que no puedan divergir en semántica.
// pgid es 0 en la vía del PID, y entonces kill(-0, sig) nunca llega a emitirse.
//
// Un cambio de comportamiento deliberado en la vía del grupo: los descendientes
// re-sid ahora reciben SIGTERM antes que SIGKILL. Antes sólo recibían SIGKILL,
// porque kill(-pgid) no los alcanza y el bucle de refuerzo sólo iba a esa
// señal. Es una mejora (parada graciosa en vez de caza) y no cambia el
// contrato observable, que es "tras Stop no queda ningún pid del linaje vivo".
func terminate(root, pgid int, lineage []int, timeout time.Duration) bool {
	signal := func(sig syscall.Signal) {
		if pgid > 0 {
			_ = syscall.Kill(-pgid, sig)
		}
		for _, pid := range lineage {
			_ = syscall.Kill(pid, sig)
		}
	}

	signal(syscall.SIGTERM)
	if waitLineageGone(pgid, lineage, timeout) {
		return true
	}

	signal(syscall.SIGKILL)
	return waitLineageGone(pgid, lineage, 2*time.Second)
}

// lineageDesc describe el linaje para un mensaje de aviso.
func lineageDesc(root int, lineage []int) string {
	if len(lineage) <= 1 {
		return "pgid " + strconv.Itoa(root)
	}
	return "pgid " + strconv.Itoa(root) + " y " + strconv.Itoa(len(lineage)-1) + " descendiente(s)"
}

// Evaluate implementa el orden de confianza:
//  1. PID vivo + creation_time coincide → base de confianza
//  2. Si PID muere, fallback a puerto+pattern para detectar reinicio externo
//  3. Si hay verificaciones configuradas (puerto/pattern) y todas fallan → unknown
//  4. En caso contrario → running
func (u *unixManager) Evaluate(spec EvalSpec) Status {
	pidAlive := spec.Pid > 0 && Alive(spec.Pid, spec.CreationTimeMs)

	checked := false
	ok := false
	portOpen := false
	patternMatch := false
	if spec.Port > 0 {
		checked = true
		portOpen = PortOpen(spec.Port)
		ok = portOpen
	}
	if spec.ProcessPattern != "" {
		checked = true
		patternMatch = PatternMatch(spec.ProcessPattern)
		ok = ok || patternMatch
	}

	if pidAlive {
		if checked && !ok {
			if spec.PortPending && spec.Port > 0 {
				return StatusPortPending
			}
			return StatusUnknown
		}
		if spec.NoPort && spec.Port <= 0 {
			// Vive y no expone puerto TCP: un hecho, no una ausencia de
			// información. Se nombra para que la UI y el JSON no lo
			// confundan con un servicio sano.
			return StatusNoPort
		}
		if spec.PortUnresolved && spec.Port <= 0 {
			// Vive y el puerto nunca se decidió: no es "sano" y no es
			// "no tiene puerto". Se nombra para que la UI lo diga.
			return StatusPortUnresolved
		}
		return StatusRunning
	}

	// PID muerto: fallback externo (servicio reiniciado fuera de vroom).
	// Si pattern matchea → running (el servicio está ahí).
	// Si solo el puerto está abierto → verificar que el proceso escuchando
	// sea el mismo servicio (misma creation_time) para evitar falsos positivos
	// cuando otro servicio usa el mismo puerto.
	if ok {
		if patternMatch {
			return StatusRunning
		}
		if portOpen {
			ownerPID := PortOwnerPID(spec.Port)
			if ownerPID <= 0 {
				// Propietario indeterminado o ambiguo: resolver a
				// "running" sería el veredicto optimista que hace que un
				// twin de otro worktree se reporte vivo. Degrada a
				// indeterminado.
				return StatusUnknown
			}
			ownerAlive := Alive(int(ownerPID), spec.CreationTimeMs)
			if !ownerAlive {
				return StatusStopped
			}
			return StatusRunning
		}
		// No hay `return StatusRunning` para el caso "ok pero el puerto no está
		// abierto": es inalcanzable, y estaba ahí.
		//
		// `ok` se compone como `ok = portOpen` y luego `ok = ok || patternMatch`,
		// así que `ok && !patternMatch` IMPLICA `portOpen`, y el `if` de arriba
		// absorbe todos los caminos. Un segundo sitio para el mismo veredicto
		// invita a mantenerlos sincronizados, y este además era el único `return`
		// del bloque sin condición: leerlo hacía creer que faltaba un caso.
		//
		// MEDIDO: la cobertura lo delató. `258` era la única línea del `Evaluate`
		// que ningún test alcanzaba, y un test que no se puede escribir para una
		// línea es una línea que no hace falta.
	}
	return StatusStopped
}

// PatternMatch verifica si pgrep -f encuentra el patrón (unix).
// Excluye el propio proceso pgrep y sus ancestros para evitar falsos
// positivos (pgrep -f matchea su propio command line).
//
// MEDIDO: con el patrón VACÍO devuelve true, porque `pgrep -f ""` lista todos los
// procesos del sistema y basta con que haya alguno que no sea el propio pgrep. Un
// servicio con `process_pattern = ""` se declararía running siempre.
//
// No lo corrige aquí a propósito: quien llama ya comprueba `spec.ProcessPattern != ""`
// antes de invocar, y ese guard además evita marcar `checked`, que es lo que
// decide el veredicto cuando el PID no está. La guarda aquí dentro pondría un
// segundo sitio donde comprobar la misma regla. Se documenta porque el día que
// alguien llame a PatternMatch desde otro camino sin mirar, el fallo es silencioso.
//
// MEDIDO: el patrón llega a `pgrep -f` SIN COMILLAR, así que pgrep lo trata como una
// EXPRESIÓN REGULAR, no como un literal. Consecuencias medibles:
//
//   - `mi-servicio (web)` — los paréntesis son un grupo, no dos caracteres.
//   - `a.b` matchearía `axb`.
//   - `a b c d e f g` matchea casi cualquier cmdline con esas letras separadas por
//     espacios.
//
// Es el comportamiento de pgrep y el manifiesto llama al campo `process_pattern`, no
// `process_cmdline`, así que la semántica es la que el usuario espera. Se documenta
// porque un patrón con paréntesis es un caso fácil de escribir sin querer, y el
// síntoma es "vroom cree que mi servicio está corriendo".
func PatternMatch(pattern string) bool {
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		return false
	}
	myPid := os.Getpid()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		if pid != myPid {
			return true
		}
	}
	return false
}

// waitLineageGone sondea hasta que el process group y todos los pids del
// linaje capturado han desaparecido, o expira el timeout.
func waitLineageGone(pgid int, lineage []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !pgidAlive(pgid) && !lineageRunning(lineage) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// pgidAlive comprueba si queda algún proceso en el grupo via kill(-pgid, 0).
func pgidAlive(pgid int) bool {
	// kill(0, sig) no significa "no hay grupo": el pid 0 es "mi propio grupo
	// de procesos", así que consultarlo sin guardia señalaría al propio vroom
	// y devolvería true siempre. Sin PGID no hay grupo que preguntarle.
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, syscall.Signal(0))
	switch err {
	case nil:
		return true
	case syscall.ESRCH:
		return false
	case syscall.EPERM:
		return true // existe pero no es nuestro
	default:
		return false
	}
}

// killPortHolderWith libera el puerto MATANDO SÓLO si puede probar que el
// proceso que lo escucha pertenece a su propio linaje. Es el fallo cerrado
// del contrato de propiedad: sin prueba no se mata nada y se avisa.
//
// La prueba exige un único dueño conocido y contenido en el linaje. Cero
// dueños (permisos, /proc ilegible), varios dueños (mismo puerto en IPv4 e
// IPv6, o dos procesos) o un dueño ajeno: los tres son "no se puede probar".
func killPortHolderWith(port, rootPid int, lineage []int, owners func(int) []int32, warn func(string, ...any)) {
	if warn == nil {
		warn = func(string, ...any) {}
	}

	candidates := distinctOwners(owners(port))
	if len(candidates) != 1 {
		warn("puerto %d ocupado pero vroom no puede probar quién lo tiene: no se mata nada", port)
		return
	}
	owner := candidates[0]
	if owner != rootPid && !containsPid(lineage, owner) {
		warn("puerto %d lo tiene el pid %d, fuera del linaje de este servicio: no se mata nada", port, owner)
		return
	}

	_ = exec.Command("fuser", "-k", fmt.Sprintf("%d/tcp", port)).Run()
	// Esperar a que el puerto se libere (max 3s).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !PortOpen(port) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	warn("puerto %d sigue ocupado tras kill: hay un proceso que no se deja matar", port)
}

func distinctOwners(pids []int32) []int {
	seen := make(map[int32]bool, len(pids))
	out := make([]int, 0, len(pids))
	for _, p := range pids {
		if p <= 0 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, int(p))
	}
	return out
}

func containsPid(pids []int, pid int) bool {
	for _, p := range pids {
		if p == pid {
			return true
		}
	}
	return false
}
