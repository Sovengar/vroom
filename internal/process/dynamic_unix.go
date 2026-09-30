//go:build unix

package process

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DiscoveryResult es el resultado de resolver el puerto real de un linaje.
type DiscoveryResult struct {
	// Port es el puerto TCP principal que la app escucha. 0 = la app no
	// abrió ningún puerto TCP (servicio solo-UDP, worker, etc.).
	Port int
	// All son todos los listeners del linaje, ascendente. Permite que el
	// llamador distinga "no hay ninguno" de "hay varios y elegí uno".
	All []int
	// HonoredReserved es true cuando había puerto reservado (R1) y la app
	// lo tomó: descubrimiento determinista, sin heurística.
	HonoredReserved bool
	// Verified dice si el puerto elegido está CONFIRMADO. R1, un único
	// listener y R2 lo verifican; R3 no puede, y por eso se declara.
	Verified bool
	// LineageDead significa que el linaje ya no corría: se abandona el
	// discovery en vez de agotar el timeout.
	LineageDead bool
	// Unresolved significa que se agotó el plazo ANTES de poder decidir un
	// puerto principal. No es lo mismo que no tener puerto: el proceso vive,
	// puede que aún no haya hecho bind, y puede que sí lo haga más tarde.
	// Confundir ambos deja un servicio lento etiquetado "sin puerto" para
	// siempre, porque nadie vuelve a descubrir.
	Unresolved bool
}

// DiscoverPort resuelve el puerto real de un linaje acotando a los listeners
// que pertenecen a sus procesos.
//
// La ambigüedad sólo existe cuando vroom ADIVINA, y aquí está el orden:
//
//	R1  el puerto reservado está entre los listeners → ese es. Determinista,
//	    sin heurística, y es el caso normal: si la app honra PORT, no hay
//	    ambigüedad que desambiguar.
//	R2  hay varios y el reservado no está → gana el que mejor responde en
//	    health_path (200 > 2xx/3xx > 5xx > 404).
//	R3  empate o protocolo no-HTTP → gana el de menor número, determinista,
//	    y Verified queda false: el servicio se marca "puerto no verificado".
//
// "Sin puertos" y "aún no ha hecho bind" NO se pueden distinguir por
// ausencia: un servicio que tarda 12 s en levantar y uno solo-UDP lucen
// igual durante 12 s. Por eso el vencimiento del plazo NO se toma como
// prueba de ausencia: se concede una segunda ventana acotada
// (DefaultDynamicUnresolvedGrace) y sólo si tampoco aparece nada ahí se
// afirma "no tiene puerto TCP". Si hay listeners pero no se puede decidir
// cuál es el principal, el resultado es Unresolved y el llamador lo nombra
// como tal en vez de inventar un número.
func DiscoverPort(rootPid int, reserved int, healthPath string, timeout time.Duration) DiscoveryResult {
	deadline := time.Now().Add(timeout)
	graceDeadline := deadline.Add(DefaultDynamicUnresolvedGrace)
	var prev []int
	changedAt := time.Now()

	for {
		if rootPid <= 0 || !lineageRunning([]int{rootPid}) {
			return DiscoveryResult{LineageDead: true}
		}

		listeners := lineageListenersAt(procRoot, rootPid)

		// R1 no espera a la ventana de estabilización: si el puerto
		// reservado está escuchando, la app lo tomó y no hay nada que
		// adivinar, por mucho que abra después otro listener.
		if reserved > 0 && containsPid(listeners, reserved) {
			return DiscoveryResult{Port: reserved, All: listeners, HonoredReserved: true, Verified: true}
		}

		// Una app puede abrir listeners por etapas (metrics primero, main
		// después). Aceptar la primera muestra elegiría el equivocado: se
		// espera a que el conjunto deje de crecer durante la ventana de
		// estabilización.
		if !samePorts(listeners, prev) {
			prev = append([]int(nil), listeners...)
			changedAt = time.Now()
		}

		now := time.Now()
		if len(listeners) > 0 {
			if time.Since(changedAt) >= discoverSettle {
				return decidePort(listeners, reserved, healthPath)
			}
			if now.After(deadline) {
				// Hay listeners pero el conjunto no se estabiliza y no se
				// puede decidir cuál es el principal. Se declara sin
				// decidir; el llamador lo nombra en vez de inventar un número.
				return DiscoveryResult{Unresolved: true}
			}
		} else if now.After(graceDeadline) {
			// Ni el plazo ni la gracia aportaron un solo listener. Ahora sí
			// se puede afirmar que no expone puerto TCP.
			return DiscoveryResult{}
		}
		// Sin puertos y sin agotar la ventana aún puede significar "todavía
		// no ha hecho bind": la gracia es la vía de recuperación.
		time.Sleep(discoverInterval)
	}
}

// discoverInterval separa dos muestras consecutivas del linaje.
const discoverInterval = 100 * time.Millisecond

// discoverSettle es cuánto debe llevar el conjunto de listeners sin cambiar
// antes de aceptarlo. Cubre la apertura escalonada típica (metrics en una
// goroutine, main en otra). Más listeners escalonados que esto ya caen en la
// limitación documentada: ver adr-0012.
const discoverSettle = 500 * time.Millisecond

func samePorts(a, b []int) bool {
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

// decidePort es la decisión sobre un conjunto de listeners ya observado y
// estable. Separado del bucle para poder ejercitar R1/R2/R3 sin /proc.
func decidePort(listeners []int, reserved int, healthPath string) DiscoveryResult {
	if reserved > 0 && containsPid(listeners, reserved) {
		return DiscoveryResult{Port: reserved, All: listeners, HonoredReserved: true, Verified: true}
	}
	if len(listeners) == 1 {
		return DiscoveryResult{Port: listeners[0], All: listeners, Verified: true}
	}
	return pickMainPort(listeners, healthPath)
}

// pickMainPort desambigua varios listeners sin poder usar el reservado.
func pickMainPort(listeners []int, healthPath string) DiscoveryResult {
	best, bestRank := 0, -1
	for _, port := range listeners { // ya viene ascendente: empate gana el menor
		if rank := healthRank(probeStatus(port, healthPath)); rank > bestRank {
			best, bestRank = port, rank
		}
	}
	if best > 0 && bestRank > 0 {
		return DiscoveryResult{Port: best, All: listeners, Verified: true}
	}
	// Ninguno respondió como HTTP: protocolo desconocido o sockets crudos.
	// No hay forma de saber cuál es el principal, y se dice.
	return DiscoveryResult{Port: listeners[0], All: listeners, Verified: false}
}

// healthRank ordena las respuestas: 200 > resto de 2xx/3xx > 5xx > 404.
// 0 significa "no respondió como HTTP", que no gana nunca.
func healthRank(status int) int {
	switch {
	case status == 200:
		return 4
	case status >= 200 && status < 400:
		return 3
	case status >= 500:
		return 2
	case status == 404:
		return 1
	default:
		return 0
	}
}

// probeTimeout es corto a propósito: la desambiguación ocurre dentro del
// presupuesto de arranque, y un puerto que no contesta HTTP no debe comerlo.
const probeTimeout = 1500 * time.Millisecond

func probeStatus(port int, healthPath string) int {
	if healthPath == "" {
		healthPath = "/"
	}
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath))
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	return resp.StatusCode
}

// lineageListenersAt devuelve los puertos TCP en LISTEN cuyos sockets
// pertenecen a procesos del linaje de rootPid.
//
// Se cruzan /proc/net/tcp{,6} (inodo del socket) con /proc/<pid>/fd del
// linaje. Es el camino preciso: preguntarle a la red "quién escucha" y
// luego "de quién es" evita atribuir a un servicio el listener de su twin
// en la otra familia de direcciones.
func lineageListenersAt(root string, rootPid int) []int {
	snap, err := procSnapshotAt(root)
	if err != nil {
		return nil
	}
	lineage := descendantsFrom(snap, rootPid)
	pids := append([]int{rootPid}, lineage...)

	// inodo → pid, sólo para los pids del linaje.
	owner := map[string]int{}
	for _, pid := range pids {
		fdDir := filepath.Join(root, strconv.Itoa(pid), "fd")
		entries, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
			if err != nil {
				continue
			}
			if ino, ok := socketInode(target); ok {
				owner[ino] = pid
			}
		}
	}

	seen := map[int]bool{}
	var ports []int
	for _, name := range []string{"net/tcp", "net/tcp6"} {
		for _, l := range listenSocketsAt(filepath.Join(root, name)) {
			if _, mine := owner[l.inode]; !mine || seen[l.port] {
				continue
			}
			seen[l.port] = true
			ports = append(ports, l.port)
		}
	}
	sort.Ints(ports)
	return ports
}

// listenSocket es una fila LISTEN de /proc/net/tcp{,6}.
type listenSocket struct {
	addr  string // bind address en hex, p.ej. "0100007F" = 127.0.0.1
	port  int
	inode string
}

// listenSocketsAt lee las filas TCP en LISTEN de un fichero de /proc/net.
// El estado 0A es TCP_LISTEN; el puerto viene en hexadecimal.
func listenSocketsAt(path string) []listenSocket {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var out []listenSocket
	sc := bufio.NewScanner(f)
	sc.Scan() // cabecera
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		addr, portStr, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseInt(portStr, 16, 32)
		if err != nil {
			continue
		}
		out = append(out, listenSocket{addr: addr, port: int(port), inode: fields[9]})
	}
	return out
}

// socketInode extrae el inodo de un enlace /proc/<pid>/fd/N -> socket:[N].
func socketInode(target string) (string, bool) {
	if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]"), true
}
