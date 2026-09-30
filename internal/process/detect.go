package process

import (
	"fmt"
	"net"
	"time"

	gopsnet "github.com/shirou/gopsutil/v3/net"
	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

// Alive verifica liveness del PID con protección anti-reuse:
// el proceso debe existir Y su creation_time coincidir con el registrado.
//
// gopsutil es pure Go (sin cgo) y portable Linux/Windows, por eso se usa
// en lugar de os.FindProcess (que en Linux siempre tiene éxito aunque el
// proceso no exista) o de leer /proc directamente.
func Alive(pid int, creationTimeMs int64) bool {
	p, err := gopsprocess.NewProcess(int32(pid))
	if err != nil {
		return false // PID libre o inexistente
	}
	ct, err := p.CreateTime()
	if err != nil {
		return false
	}
	return ct == creationTimeMs // mismatch → PID reciclado
}

// PortOpen verifica que el puerto responde con net.DialTimeout.
func PortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf(":%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// PortOwnerPID devuelve el PID del proceso que escucha en el puerto dado.
// Retorna 0 si no se puede determinar (puerto libre, permisos, o varios
// dueños ⇒ ambiguo). Ambiguo y desconocido son la misma cosa a propósito:
// quien pregunta necesita una prueba, no un candidato.
func PortOwnerPID(port int) int32 {
	owners := PortOwnerPIDs(port)
	if len(owners) == 1 {
		return owners[0]
	}
	return 0
}

// PortOwnerPIDs devuelve todos los PIDs que escuchan en el puerto dado, sin
// deduplicar el orden. Más de uno = el puerto está compartido (mismo número
// en IPv4 e IPv6, o dos procesos), y por tanto la propiedad NO está probada.
func PortOwnerPIDs(port int) []int32 {
	conns, err := gopsnet.ConnectionsPid("tcp", 0)
	if err != nil {
		return nil
	}
	var out []int32
	for _, c := range conns {
		if c.Status == "LISTEN" && c.Laddr.Port == uint32(port) && c.Pid > 0 {
			out = append(out, c.Pid)
		}
	}
	return out
}
