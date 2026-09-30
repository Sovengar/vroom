package orchestrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// HYGIENE GUARD.
//
// Un helper que sobrevive a la suite no es cosmético. Este paquete levanta
// servicios de verdad, y un helper vivo se queda con su proceso —y, si
// escuchara, con su puerto— durante horas. Pasó: dos helpers de este mismo
// paquete se quedaron vivos porque los cleanups llamaban a StopStack con un
// stack SIN ETAPAS, que no para nada.
//
// Empareja por EXECUTABLE, no por línea de comandos: cualquier proceso cuyo
// /proc/<pid>/exe resuelva a este binario y siga vivo al terminar la suite es
// una fuga. Un `pgrep -f` sería global de máquina y podría matar la corrida de
// otro proceso.
//
// Y la trampa que este guard se come si no se guarda: los helpers de este
// paquete SON este mismo binario, así que su TestMain correría el guard,
// encontraría al proceso que lo lanzó y lo mataría. De ahí la guarda por
// variable de entorno y la de no tocar antepasados.

const helperEnvVar = "VROOM_NOPORT_HELPER"

func leakedTestBinaries() []int {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return nil
	}

	me := os.Getpid()
	myGroup, _ := syscall.Getpgid(0)

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var leaked []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() || pid == me {
			continue
		}
		if grp, err := syscall.Getpgid(pid); err == nil && grp == myGroup {
			continue
		}
		if isAncestor(pid, me) {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved == self {
			leaked = append(leaked, pid)
		}
	}
	return leaked
}

// isAncestor dice si candidate está en la cadena de padres de pid.
func isAncestor(candidate, pid int) bool {
	for range 64 {
		ppid := syscall.Getppid()
		if ppid <= 1 {
			return false
		}
		if ppid == candidate {
			return true
		}
		if ppid == pid {
			return false
		}
	}
	return false
}

func TestMain(m *testing.M) {
	code := m.Run()

	if os.Getenv(helperEnvVar) != "" {
		os.Exit(code) // soy un helper: no soy el guard
	}

	leaked := leakedTestBinaries()
	if len(leaked) == 0 {
		os.Exit(code)
	}
	for _, pid := range leaked {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	fmt.Fprintf(os.Stderr,
		"\nHYGIENE: %d proceso(s) de este binario sobrevivieron a la suite: %v\n"+
			"Revisa los cleanups: StopStack con un stack sin etapas no para nada.\n",
		len(leaked), leaked)
	os.Exit(1)
}
