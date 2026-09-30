//go:build unix

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// HYGIENE GUARD.
//
// Un helper que sobrevive a la suite no es cosmético: si quedó escuchando,
// retiene un puerto del rango de reserva y cada fuga quita una ranura
// permanente. Pasó una vez: un TestHelperService vivió 4 h 08 m con el 41999
// cogido.
//
// La detección empareja por EXECUTABLE, no por línea de comandos: cualquier
// proceso cuyo /proc/<pid>/exe resuelva a este mismo binario de test y que siga
// vivo cuando la suite termina es una fuga, con certeza. Un `pgrep -f` sería
// global de máquina y podría matar o culpar a la corrida de otro proceso, que
// es exactamente el falso positivo que no se quiere en un guard.

// leakedTestBinaries devuelve los pids que ejecutan ESTE binario de test y que
// siguen corriendo.
func leakedTestBinaries() []int {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return nil
	}

	// El propio binario de test cumple el criterio por definición: hay que
	// excluirnos, o el guard se mata a sí mismo al terminar.
	me := os.Getpid()
	myGroup, _ := syscall.Getpgid(0)

	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var leaked []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() || pid == me {
			continue
		}
		// Tampoco el resto de nuestro grupo de procesos: son el runner y
		// cualquier hijo de test, no una fuga.
		if grp, err := syscall.Getpgid(pid); err == nil && grp == myGroup {
			continue
		}
		// Ni nuestros antepasados. Un helper es este mismo ejecutable, así
		// que un guard sin esta guarda puede acabar matando a quien lo
		// lanzó — que es justo lo que pasó la primera vez.
		if isAncestor(pid, me) {
			continue
		}
		exe, err := os.Readlink(filepath.Join(procRoot, e.Name(), "exe"))
		if err != nil {
			continue // ya no existe, o no es nuestro
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved == self {
			leaked = append(leaked, pid)
		}
	}
	return leaked
}

// helperEnvVar marca los procesos que son un helper lanzado por la propia
// suite. Hace falta porque un helper ES este mismo binario: sin esta guarda,
// el TestMain del hijo buscaría fugas, encontraría al padre (mismo ejecutable)
// y lo mataría.
const helperEnvVar = "VROOM_TEST_HELPER"

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

	// Se limpian igual: un guard que además deja la fuga puesta es peor que
	// no tener guard.
	for _, pid := range leaked {
		_ = syscallKill(pid)
	}
	fmt.Fprintf(os.Stderr,
		"\nHYGIENE: %d proceso(s) de este binario de test sobrevivieron a la suite: %v\n"+
			"Alguien no está parando lo que arranca, y esos procesos retienen puertos del rango.\n",
		len(leaked), leaked)
	os.Exit(1)
}

// El guard tiene que poder FALLAR. Si el emparejamiento por ejecutable no
// detectara nada, este test no valdría nada; así que se comprueba sobre un
// proceso real del propio binario.
func TestHygieneGuardDetectsItsOwnBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}

	before := len(leakedTestBinaries())

	// Esto lanza un helper, así que hay que declararlo: si no, el TestMain
	// del hijo correría el guard y mataría a este proceso.
	t.Setenv(helperEnvVar, "listener")

	res := startSleep(t, newTestManager(t), StartSpec{
		Command:    testBinary(t) + " -test.run=^TestHelperListener$",
		WorkDir:    t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "out.log"),
		StderrPath: filepath.Join(t.TempDir(), "err.log"),
	})
	t.Cleanup(func() {
		_ = syscallKillGroup(res.Pgid)
		_ = syscallKill(res.Pid)
	})
	waitFor(t, 5*time.Second, "helper vivo", func() bool {
		return containsInt(leakedTestBinaries(), res.Pid)
	})

	if got := len(leakedTestBinaries()); got != before+1 {
		t.Errorf("el guard vio %d fugas, esperaba %d: el emparejamiento no funciona", got, before+1)
	}
	if !strings.Contains(testBinary(t), "test") {
		t.Skip("ruta de test inesperada")
	}
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
