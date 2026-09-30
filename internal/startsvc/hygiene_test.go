package startsvc

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
// Un helper que sobrevive a la suite retiene un puerto del rango de reserva
// para siempre: cada fuga quita una ranura y no se recupera sin reiniciar la
// TUI. Pasó una vez con un TestHelperService de 4 h 08 m.
//
// El emparejamiento es por EXECUTABLE, no por línea de comandos: cualquier
// proceso cuyo /proc/<pid>/exe resuelva a este mismo binario y siga vivo al
// terminar la suite es una fuga, con certeza. Un `pgrep -f` sería global de
// máquina y podría culpar o matar a la corrida de otro proceso.
//
// Y hay una trampa que este guard ya se comió una vez: los helpers de este
// paquete SON este mismo binario. Su TestMain correría el guard, encontraría
// al proceso que los lanzó y lo mataría. Por eso un helper se declara con
// su variable de entorno, y por eso el guard nunca toca a sus antepasados.

// helperEnvVar marca los procesos que son un helper lanzado por la suite.
const helperEnvVar = "VROOM_START_HELPER"

// leakedTestBinaries devuelve los pids que ejecutan ESTE binario y siguen vivos.
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
		if isTestBinary("/proc", self, pid) {
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
			"Algo que este paquete arranca no se está parando.\n",
		len(leaked), leaked)
	os.Exit(1)
}

// isTestBinary dice si el proceso pid ejecuta el binario `self`. Es la única
// pieza de lógica del guard, y está aparte para poder probarla contra un
// proceso que el test posee y controla, sin spawn ni shell de por medio.
func isTestBinary(root, self string, pid int) bool {
	exe, err := os.Readlink(filepath.Join(root, strconv.Itoa(pid), "exe"))
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return false
	}
	return resolved == self
}

// testBinaryPath resuelve el ejecutable de este binario. Falla en lugar de
// devolver cadena vacía: si no se puede resolver, un guard que no encuentra
// NADA parece un guard que pasa, y eso es peor que no tenerlo.
func testBinaryPath(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", self, err)
	}
	return resolved
}

// El guard tiene que poder fallar. Se comprueba contra el propio proceso del
// test, que poseemos y controlamos: determinista, sin spawn, sin shell y sin
// depender de qué /bin/sh sea en la máquina.
//
// El control negativo (pid 1) es lo que impide que un isTestBinary que
// devolviera true siempre pasara el aserto positivo.
func TestHygieneGuardMatchesOwnBinary(t *testing.T) {
	self := testBinaryPath(t)

	if !isTestBinary("/proc", self, os.Getpid()) {
		t.Errorf("el guard tiene que reconocer su propio binario (pid %d)", os.Getpid())
	}
	if isTestBinary("/proc", self, 1) {
		t.Error("pid 1 no ejecuta este binario: el emparejamiento no discrimina")
	}
	if isTestBinary("/proc", self, 999999999) {
		t.Error("un pid inexistente no puede ser este binario")
	}
	if containsInt(leakedTestBinaries(), os.Getpid()) {
		t.Error("el guard no puede listarse a sí mismo: se mataría al terminar")
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
