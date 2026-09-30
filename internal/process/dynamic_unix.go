//go:build unix

package process

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Rango de reserva del modo dynamic. Fijo por ahora, ampliable después:
// 4000-4999 es la banda que los backends de desarrollo ya usan, así que un
// listener legítimo ajeno que caiga dentro es improbable pero posible.
const (
	DynamicPortLow  = 4000
	DynamicPortHigh = 4999
)

// DefaultDynamicPortTimeout acota la ventana de arranque→bind. Un servicio
// lento (compilando, migrando) lo agota y se reporta "sin puerto", nunca un
// arranque colgado.
const DefaultDynamicPortTimeout = 8 * time.Second

// ReservePort pide un puerto libre del rango dynamic.
//
// TOCTOU documentado: bind(127.0.0.1:0) + close devuelve el puerto al pool
// antes de que el hijo llegue a hacer bind. La ventana existe; mitigarla
// exigiría socket passing, que no cabe en sh -c.
func ReservePort() (int, error) {
	for port := DynamicPortLow; port <= DynamicPortHigh; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue // ocupado o no disponible; el siguiente del rango
		}
		if err := ln.Close(); err != nil {
			return 0, fmt.Errorf("could not release the reserved port %d: %w", port, err)
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free port in range %d-%d", DynamicPortLow, DynamicPortHigh)
}

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
	// LineageDead significa que el linaje ya no corría: se abandona el
	// discovery en vez de agotar el timeout.
	LineageDead bool
}

// DiscoverPort resuelve el puerto real de un linaje acotando a los listeners
// que pertenecen a sus procesos.
//
// Reglas, en orden: R1 el puerto reservado está entre los listeners → ese
// es, determinista y sin heurística. Con un solo listener, ese es. Con varios
// y ninguno reservado, la elección la hace el llamador (R2/R3 en el slice 3).
//
// La respuesta vacía sólo es válida si el linaje está muerto: una app que
// aún no ha hecho bind y una app sin puerto TCP no se pueden distinguir por
// ausencia, y esperar al deadline cubre ambos casos sin inventarse ninguno.
func DiscoverPort(rootPid int, reserved int, timeout time.Duration) DiscoveryResult {
	deadline := time.Now().Add(timeout)
	for {
		if rootPid <= 0 || !lineageRunning([]int{rootPid}) {
			return DiscoveryResult{LineageDead: true}
		}

		listeners := lineageListenersAt(procRoot, rootPid)
		switch {
		case len(listeners) == 0:
			// Puede que aún no haya hecho bind. "No hay puertos" sólo es
			// una conclusión al agotar la ventana, no en la primera muestra.
		case reserved > 0 && containsPid(listeners, reserved):
			return DiscoveryResult{Port: reserved, All: listeners, HonoredReserved: true}
		case len(listeners) == 1:
			return DiscoveryResult{Port: listeners[0], All: listeners}
		default:
			// Varios sin el reservado: ambiguo hasta que el llamador
			// aplique R2/R3. Se entrega ya, no se espera al deadline.
			return DiscoveryResult{All: listeners}
		}

		if time.Now().After(deadline) {
			return DiscoveryResult{}
		}
		time.Sleep(100 * time.Millisecond)
	}
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
