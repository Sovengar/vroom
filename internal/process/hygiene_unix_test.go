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

// testBinaryPath resuelve el ejecutable de este binario de test. Falla en
// lugar de devolver cadena vacía: si esto no se puede resolver, un guard que
// no encuentra NADA parece un guard que pasa, y eso es peor que no tenerlo.
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

// isTestBinary dice si el proceso pid ejecuta el binario `self`. Es la única
// pieza de lógica del guard, y está aparte para poder probarla contra un
// proceso que el test posee y controla, sin spawn ni shell de por medio.
func isTestBinary(root, self string, pid int) bool {
	exe, err := os.Readlink(filepath.Join(root, strconv.Itoa(pid), "exe"))
	if err != nil {
		return false // ya no existe, o no es nuestro
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return false
	}
	return resolved == self
}

// leakedTestBinaries devuelve los pids que ejecutan ESTE binario de test y que
// siguen corriendo.
func leakedTestBinaries() []int {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
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
		if isTestBinary(procRoot, self, pid) {
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

// helperEnvVar marca los procesos que son un helper lanzado por la suite.
const helperEnvVar = "VROOM_TEST_HELPER"

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
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	fmt.Fprintf(os.Stderr,
		"\nHYGIENE: %d proceso(s) de este binario de test sobrevivieron a la suite: %v\n"+
			"Alguien no está parando lo que arranca, y esos procesos retienen puertos del rango.\n",
		len(leaked), leaked)
	os.Exit(1)
}

// ---- El guard tiene que poder fallar ----

// TestHygieneGuardMatchesOwnBinary comprueba la lógica de emparejamiento
// contra un proceso que este test POSEE Y CONTROLA — su propio pid — y sin
// spawn ni shell. Es determinista por construcción: no hay carrera, no hay
// ventana de tiempo y no depende de qué /bin/sh sea en la máquina.
//
// La versión anterior de este test comprobaba lo mismo racing a un helper
// lanzado por `sh -c` y usando el pid del WRAPPER. Eso sólo funciona si la
// shell hace exec, y exec no está garantizado: en CI /bin/sh es dash y ahí
// reventó con un timeout de 5 s y ningún otro rastro. Este no puede fallar
// por el entorno.
func TestHygieneGuardMatchesOwnBinary(t *testing.T) {
	self := testBinaryPath(t)

	if !isTestBinary(procRoot, self, os.Getpid()) {
		t.Errorf("el guard tiene que reconocer su propio binario (pid %d)", os.Getpid())
	}

	// Control negativo: pid 1 no es este binario. Sin esto, un isTestBinary
	// que devolviera true siempre pasaría el aserto anterior.
	if isTestBinary(procRoot, self, 1) {
		t.Error("pid 1 no ejecuta este binario: el emparejamiento no discrimina")
	}

	// Un pid que no existe no puede generar un falso positivo.
	if isTestBinary(procRoot, self, 999999999) {
		t.Error("un pid inexistente no puede ser este binario")
	}

	// Y el guard completo no se incluye a sí mismo ni a sus antepasados.
	if containsInt(leakedTestBinaries(), os.Getpid()) {
		t.Error("el guard no puede listarse a sí mismo: se mataría al terminar")
	}
}

// TestHygieneGuardFindsSpawnedHelper es la parte de descubrimiento: un helper
// real tiene que aparecer. Se lanza SIN shell (exec directo), así que el pid
// es inequívocamente nuestro y la aserción no depende de que `sh` haga exec.
func TestHygieneGuardFindsSpawnedHelper(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}

	before := len(leakedTestBinaries())

	pid := spawnTestHelperDirectly(t)
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})

	waitFor(t, 10*time.Second, "helper visible para el guard", func() bool {
		return containsInt(leakedTestBinaries(), pid)
	})

	if got := len(leakedTestBinaries()); got != before+1 {
		t.Errorf("el guard vio %d fugas, esperaba %d: el descubrimiento no funciona", got, before+1)
	}
	if !strings.Contains(os.Args[0], "test") {
		t.Errorf("ruta de test inesperada: %q", os.Args[0])
	}
}

// spawnTestHelperDirectly lanza este binario sin pasar por una shell, de modo
// que el pid devuelto es el del helper y no el de un wrapper.
func spawnTestHelperDirectly(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperListener$")
	cmd.Env = append(os.Environ(), helperEnvVar+"=listener", "VROOM_TEST_PORT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("helper directo: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
