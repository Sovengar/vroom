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
	return vivoCon(func(pid int) (int64, error) {
		p, err := gopsprocess.NewProcess(int32(pid))
		if err != nil {
			return 0, err
		}
		return p.CreateTime()
	}, pid, creationTimeMs)
}

// vivoCon es `Alive` con la consulta al proceso inyectada.
//
// La razón es concreta: las dos ramas de error de `Alive` son de carrera. El proceso
// tiene que desaparecer ENTRE que `NewProcess` lo acepta y que se leen sus campos,
// y eso no se puede provocar desde un test sin un `kill -9` en el bucle correcto.
// Con la consulta inyectada, el contrato entero queda comprobable: existe pero no
// coincide, no existe, y no se puede preguntar.
//
// Y la tercera es la que de verdad importa: no se puede preguntar y el proceso sí
// estaba. Devolver true ahí sería afirmar que un servicio está vivo sin haberlo
// visto, que es el peor error posible en esta función.
func vivoCon(creationTime func(int) (int64, error), pid int, creationTimeMs int64) bool {
	ct, err := creationTime(pid)
	if err != nil {
		return false // PID libre, inexistente, o se fue mientras se miraba
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
	return dueñosCon(func() ([]gopsnet.ConnectionStat, error) {
		return gopsnet.ConnectionsPid("tcp", 0)
	}, port)
}

// dueñosCon es `PortOwnerPIDs` con la lectura de `/proc/net` inyectada.
//
// La lectura va inyectada porque su error es una RARA de verdad —`/proc/net` que no
// se puede leer, un contenedor con el proc restricted— y porque el valor que sale de
// aquí decide si vroom mata un proceso: "no hay dueño" y "no pude preguntar" tienen
// que ser lo mismo, y eso sólo se comprueba viendo los dos.
//
// MEDIDO: con la lista inyectada, un error devuelve `nil` igual que una lista vacía,
// y ambos casos hacen que `killPortHolderWith` se niegue a matar. Es el fallo cerrado
// que el contrato de propiedad exige.
func dueñosCon(leer func() ([]gopsnet.ConnectionStat, error), port int) []int32 {
	conns, err := leer()
	if err != nil {
		return nil // no se pudo preguntar: mismo veredicto que "no hay dueño"
	}
	var out []int32
	for _, c := range conns {
		if c.Status == "LISTEN" && c.Laddr.Port == uint32(port) && c.Pid > 0 {
			out = append(out, c.Pid)
		}
	}
	return out
}
