package process

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	gopsnet "github.com/shirou/gopsutil/v3/net"
)

// ---------------------------------------------------------------------------
// Las cuatro piezas del paquete que hablan con el exterior.
//
// Cada una consulta algo fuera del proceso —el árbol de `/proc`, la tabla de
// conexiones del kernel, la salida de `pgrep`, la apertura de un puerto— y cada una
// tenía una rama de error que no se podía provocar: son fallos de carrera o de una
// máquina en un estado que no se puede montar desde un test.
//
// La respuesta ha sido inyectar la consulta, no relajar la comprobación. Cada función
// keeps su veredicto y lo que cambia es de dónde sale el dato; así el contrato entero
// se puede comprobar, incluidas las ramas que antes eran código muerto.
// ---------------------------------------------------------------------------

// TestGrupoExisteTraduceCadaErrno: la tabla de `kill(-pgid, 0)`.
//
// La asimetría entre EPERM y ESRCH es lo único que hay que acertar aquí, y no es
// evidentemente cierta a simple vista: EPERM significa que el grupo EXISTE pero que
// el kernel no va a decirnos de quién es, y ESRCH que no hay nadie.
//
// Los dos casos que no se pueden provocar sin otro usuario en la máquina, el mapa
// entero es una tabla, y una tabla merece un test que la fije.
func TestGrupoExisteTraduceCadaErrno(t *testing.T) {
	casos := []struct {
		err  error
		want bool
		por  string
	}{
		{nil, true, "kill con éxito: el grupo existe y es consultable"},
		{syscall.ESRCH, false, "ESRCH: no hay ningún proceso en ese grupo"},
		{syscall.EPERM, true, "EPERM: el grupo existe, pero es de otro usuario. Decir false " +
			"haría que Stop creyera que ya no queda nadie por lo que hacer"},
		{syscall.EINVAL, false, "un errno que no es un veredicto sobre el grupo no se traduce a " +
			"'existe': la consulta falló, y no se afirma nada"},
		{errors.New("otro"), false, "un error que no es un errno tampoco"},
	}
	for _, c := range casos {
		if got := grupoExiste(c.err); got != c.want {
			t.Errorf("grupoExiste(%v) = %v, want %v: %s", c.err, got, c.want, c.por)
		}
	}

	// Y con un pgid de 0 no se pregunta nada: `kill(0, 0)` señalaría al propio grupo
	// de vroom, que siempre está vivo, así que la respuesta sería "sí" para siempre.
	if pgidAlive(0) {
		t.Error("pgidAlive(0) = true: preguntaría por el grupo de vroom, que siempre existe")
	}
	if pgidAlive(-1) {
		t.Error("pgidAlive(-1) = true: un pgid negativo no es un grupo")
	}
}

// TestVivoConDistingueLosTresVeredictos: existe, no existe, y no se pudo preguntar.
//
// La tercera es la que no se puede provocar contra el proceso real —haría falta que
// el `/proc/<pid>` desapareciera ENTRE que gopsutil lo acepta y que lee sus campos—
// y es la que más importa: si se respondiera "vivo" sin haber mirado, vroom declararía
// vivo un servicio del que nadie sabe nada.
func TestVivoConDistingueLosTresVeredictos(t *testing.T) {
	const guardado = 1_700_000_000_000

	if !vivoCon(func(int) (int64, error) { return guardado, nil }, 42, guardado) {
		t.Error("vivoCon = false con el creation_time intacto: un servicio en marcha se declararía parado")
	}
	if vivoCon(func(int) (int64, error) { return guardado + 1, nil }, 42, guardado) {
		t.Error("vivoCon = true con un creation_time distinto: el PID fue reciclado y no es " +
			"nuestro servicio")
	}
	if vivoCon(func(int) (int64, error) { return 0, syscall.ESRCH }, 42, guardado) {
		t.Error("vivoCon = true para un PID que no existe")
	}
	if vivoCon(func(int) (int64, error) { return 0, errors.New("se fue mientras lo miraba") }, 42, guardado) {
		t.Error("vivoCon = true sin poder preguntar: afirmar que algo vive sin haberlo visto es el " +
			"peor error posible aquí")
	}
}

// TestDueñosConTrataIgualNoVerYNoSaber: la lectura de `/proc/net` que falla.
//
// `killPortHolderWith` mata un proceso basándose en lo que salga de aquí, así que
// "no hay dueño" y "no pude preguntar" tienen que ser lo mismo: los dos niegan la
// prueba de propiedad, y los dos dejan el puerto como estaba.
//
// Que se devuelva `nil` y no una lista vacía es lo que lo hace explícito: `nil` es
// "no lo sé", `[]` es "lo sé y no hay nadie".
func TestDueñosConTrataIgualNoVerYNoSaber(t *testing.T) {
	const puerto = 4321

	escuchan := func(status string, pid int32, p uint32) gopsnet.ConnectionStat {
		return gopsnet.ConnectionStat{Status: status, Pid: pid, Laddr: gopsnet.Addr{Port: p}}
	}
	conns := []gopsnet.ConnectionStat{
		escuchan("LISTEN", 100, puerto),
		escuchan("LISTEN", 200, puerto+1), // otro puerto
		escuchan("ESTABLISHED", 300, puerto),
		escuchan("LISTEN", 0, puerto), // sin dueño: no cuenta
	}

	got := dueñosCon(func() ([]gopsnet.ConnectionStat, error) { return conns, nil }, puerto)
	if len(got) != 1 || got[0] != 100 {
		t.Errorf("dueñosCon = %v, want [100]: sólo LISTEN, sólo ese puerto y sólo con pid", got)
	}

	if g := dueñosCon(func() ([]gopsnet.ConnectionStat, error) { return nil, errors.New("proc restricted") },
		puerto); g != nil {
		t.Errorf("dueñosCon con error = %v, want nil: sin prueba de propiedad no se mata nada", g)
	}
	if g := dueñosCon(func() ([]gopsnet.ConnectionStat, error) { return nil, nil }, puerto); g != nil {
		t.Errorf("dueñosCon sin conexiones = %v, want nil", g)
	}
}

// TestDescendantsFromTerminaConUnCiclo: el guard de `seen`.
//
// Este guard NO protege contra un nodo repetido, que en un `/proc` real es imposible
// —cada proceso tiene un único `ppid`—, sino contra un ciclo: A es hijo de B y B es
// hijo de A. En un sistema real no pasa, pero `descendantsFrom` recibe un mapa y no
// puede descartarlo por su cuenta.
//
// El coste de que el guard no estuviera es un `Stop` que no termina nunca, en el
// momento en que el usuario está intentando parar su servicio. Eso se comprueba con un
// mapa cíclico y un reloj: si el guard falla, el test no vuelve y lo mata el timeout de
// la suite.
func TestDescendantsFromTerminaConUnCiclo(t *testing.T) {
	snap := map[int]procInfo{
		1: {ppid: 2},
		2: {ppid: 1},
		3: {ppid: 1},
	}

	got := descendantsFrom(snap, 1)
	// Desde 1: sus hijos son 2 y 3. 2 tiene como hijo 1, que ya se vio, así que se
	// corta ahí. El resultado ordenado es [2, 3].
	if len(got) != 2 {
		t.Fatalf("descendantsFrom = %v, want [2 3]: el ciclo tiene que cortar en el nodo ya visto", got)
	}
	for _, pid := range got {
		if pid != 2 && pid != 3 {
			t.Errorf("descendantsFrom = %v, want sólo los descendientes reales de 1", got)
		}
	}
}

// TestCaptureLineDegradaSinProcs: el `(0, nil)` de los tres fallos.
//
// Sin `/proc` legible, sin el PID, o sin el grupo, `captureLineage` devuelve `(0, nil)`
// y `Stop` degrada a señalar sólo el grupo. La degradación importa porque la rama
// "no hay grupo" de `Stop` no hace nada, y la rama "hay grupo" sí: confundir una con
// otra sería dejar procesos vivos.
//
// El caso del snapshot ilegible se provoca con un directorio vacío como raíz de
// `/proc`, que es exactamente lo que ve un proceso en un contenedor sin el proc
// montado.
func TestCaptureLineDegradaSinProcs(t *testing.T) {
	vacio := t.TempDir() // existe y está vacío: ReadDir funciona, no hay procesos

	casos := []struct {
		nombre string
		spec   StopSpec
	}{
		{"sin pid ni pgid", StopSpec{}},
		{"sin snapshot", StopSpec{Pid: 12345, Pgid: 12345}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			root, lineage := captureLineageWith(c.spec, vacio)
			if root != 0 || lineage != nil {
				t.Errorf("captureLineageWith = (%d, %v), want (0, nil): sin prueba de linaje no se "+
					"señala a nadie", root, lineage)
			}
		})
	}

	// Y con un root que NO es un directorio: `ReadDir` falla con ENOTDIR, que es el
	// otro camino del mismo error.
	fichero := filepath.Join(t.TempDir(), "no-soy-proc")
	if err := os.WriteFile(fichero, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if root, lineage := captureLineageWith(StopSpec{Pid: 1, Pgid: 1}, fichero); root != 0 || lineage != nil {
		t.Errorf("captureLineageWith con un root ilegible = (%d, %v), want (0, nil)", root, lineage)
	}
}

// TestReserveWithNoMarcaUnPuertoQueNoSePudoDevolver: el `Close` que falla.
//
// Abrir un puerto y cerrarlo es la forma de comprobar que está libre. Si el cierre
// falla, el puerto puede seguir ocupado por el propio listener, así que marcarlo como
// reservado sería mentir: el siguiente `ReservePort` lo saltaría y el hueco se
// acumularía sin que nadie supiera por qué.
func TestReserveWithNoMarcaUnPuertoQueNoSePudoDevolver(t *testing.T) {
	// Un listener que ya está cerrado: `Close` sobre él falla con `use of closed`.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cerrado := ln
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	// Se fotografía el set antes: el puerto concreto que se intenta es el primero
	// libre del rango, y con la suite completa puede que ya estén reservados unos
	// cuantos. Lo que importa es que el set NO CREZCA.
	antes := puertosReservados()

	_, err = reserveWith(func(string, string) (net.Listener, error) { return cerrado, nil })
	if err == nil {
		t.Fatal("reserveWith = nil con un listener que no se puede cerrar: el puerto puede seguir " +
			"ocupado por él")
	}
	if !strings.Contains(err.Error(), "could not release the reserved port") {
		t.Errorf("err = %q, want que diga que no se pudo devolver el puerto", err)
	}
	despues := puertosReservados()
	for p := range despues {
		if !antes[p] {
			t.Errorf("el puerto %d quedó marcado como reservado tras un cierre fallido: el rango "+
				"se encogería un hueco por cada fallo, sin explicación", p)
		}
	}
}

// TestPatternMatchToleraUnaSalidaDePgrepQueNoEsSoloPids: el parseo de la salida.
//
// `pgrep` imprime un pid por línea, y el recorrido es deliberadamente tolerante: una
// línea vacía se salta, y una línea que no es un número se salta en vez de abortar
// todo. La razón es que `pgrep` es una herramienta externa cuyo formato no depende de
// vroom, y un fallo de parseo devolvería "no está corriendo" para un servicio que sí
// lo está.
//
// MEDIDO: se pone un `pgrep` falso en el PATH. Lo que se controla es la FORMA de la
// salida, que es lo que esta función promise entender; el resultado —"hay otro
// proceso"— se produce con un pid real.
func TestPatternMatchToleraUnaSalidaDePgrepQueNoEsSoloPids(t *testing.T) {
	otro := procesosDePrueba(t, 1) // un `sleep` real con un nombre reconocible
	pid := strconv.Itoa(otro[0])

	casos := []struct {
		nombre string
		salida string
		want   bool
	}{
		{"una línea en blanco y un pid", "\n" + pid + "\n\n", true},
		{"una línea que no es un número", "pgrep: algo raro\n" + pid + "\n", true},
		{"sólo basura", "esto no es un pid\n", false},
		{"nada, pero con salida 0", "", false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			conPgrepFalso(t, c.salida)
			if got := PatternMatch(pruebaPatron); got != c.want {
				t.Errorf("PatternMatch con la salida %q = %v, want %v", c.salida, got, c.want)
			}
		})
	}
}

// TestKillPortHolderAvisaCuandoElPuertoNoSeLibera: el último recurso que no funciona.
//
// Cuando el puerto sigue ocupado tras el kill, vroom tiene que decirlo. El aviso es
// lo único que le queda al usuario para entender por qué su puerto sigue en uso por
// algo que no es suyo.
//
// MEDIDO: el killer de último recurso es `fuser -k`, así que se pone un `fuser` en el
// PATH que no mata nada. Es el caso real de un contenedor sin `fuser`, y produce el
// mismo desenlace que un proceso que se resiste: el puerto sigue ahí después de la
// espera, y hay que avisar.
func TestKillPortHolderAvisaCuandoElPuertoNoSeLibera(t *testing.T) {
	// Un puerto abierto y con dueño conocido, que es lo que hace falta para que
	// `killPortHolderWith` llegue al último recurso en vez de negarse de entrada.
	ln := escucharEn(t)
	puerto := puertoDe(t, ln)
	pid := os.Getpid()

	var avisos []string
	conFuserFalso(t) // no mata nada
	killPortHolderWith(puerto, pid, []int{pid}, func(int) []int32 { return []int32{int32(pid)} },
		func(f string, a ...any) { avisos = append(avisos, fmt.Sprintf(f, a...)) })

	if len(avisos) != 1 {
		t.Fatalf("avisos = %v, want exactamente uno: sin dueño atribuible hay dos "+
			"renuncias, y con dueño propio pero puerto ocupado hay una", avisos)
	}
	if !strings.Contains(avisos[0], "sigue ocupado") {
		t.Errorf("aviso = %q, want que diga que el puerto sigue ocupado tras el kill", avisos[0])
	}
}

// ---------------------------------------------------------------------------
// Utilidades
// ---------------------------------------------------------------------------

// pruebaPatron es el patrón que `pgrep` recibe en los tests de la salida falsa. No
// importa que no case con nada real: lo que se controla es lo que el falso imprime.
const pruebaPatron = "vroom-test-patterno-que-no-existe"

// conPgrepFalso pone un `pgrep` en el PATH que imprime la salida dada y sale con 0.
//
// Sale con 0 a propósito aunque no haya coincidencias, que es justo el caso raro que
// hace que la salida vacía llegue al parseo.
func conPgrepFalso(t *testing.T, salida string) {
	t.Helper()
	dir := t.TempDir()
	// La salida va en un fichero y el falso lo copia. Escribirla en el script con un
	// heredoc parecía más bonito y no funcionaba: `cat` lee de su stdin, y con
	// `exec.Command` el stdin del hijo es /dev/null, no el texto del heredoc.
	if err := os.WriteFile(filepath.Join(dir, "salida.txt"), []byte(salida), 0o644); err != nil {
		t.Fatal(err)
	}
	// La ruta del fichero va escrita en el script, no por $2: `pgrep -f patron`
	// deja el patrón ahí, y `cat` de un patrón no es `cat` de una respuesta.
	//
	// Y el PATH se ANADE en vez de sustituirse: con un PATH de un solo directorio,
	// el propio script no encuentra `cat` y sale con un error por stderr, que es
	// justo la forma en que un `pgrep` real se comporta cuando algo va mal.
	if err := os.WriteFile(filepath.Join(dir, "pgrep"),
		[]byte("#!/bin/sh\ncat "+filepath.Join(dir, "salida.txt")+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// conFuserFalso pone un `fuser` que no hace nada.
func conFuserFalso(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fuser"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// `pgrep` también tiene que existir para que el resto del paquete no se rompa.
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// procesosDePrueba lanza n procesos `sleep` y devuelve sus pids.
func procesosDePrueba(t *testing.T, n int) []int {
	t.Helper()
	var pids []int
	for range n {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, cmd.Process.Pid)
		// Sin `Wait`: se recogen al final del test. Un `sleep` de 5s con el proceso
		// de test vivo no se solapa con nada.
		t.Cleanup(func() { _ = cmd.Process.Kill() })
	}
	return pids
}

// puertosReservados copia el set de puertos reservados del proceso.
func puertosReservados() map[int]bool {
	reserveMu.Lock()
	defer reserveMu.Unlock()
	out := make(map[int]bool, len(reservedPorts))
	for p, v := range reservedPorts {
		out[p] = v
	}
	return out
}

// escucharEn abre un listener real en un puerto efímero y lo devuelve abierto hasta
// que termina el test.
func escucharEn(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// puertoDe devuelve el puerto de un listener.
func puertoDe(t *testing.T, ln net.Listener) int {
	t.Helper()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestElAvisoDeEscalonDependeDeQueHuboRootYDeQueNoMurio: la decisión de avisar.
//
// La escalera de parada es SIGTERM, luego SIGKILL, y si después de eso sigue habiendo
// alguien `Stop` avisa. Ese aviso sólo se puede provocar con un proceso que no muere
// ante SIGKILL, y eso exige un proceso de otro usuario: MEDIDO, en esta máquina
// `setpriv --reuid` falla con EPERM y `unshare -U` deja los hijos con el mismo uid, así
// que no hay forma de construirlo desde una cuenta normal.
//
// Lo que sí es comprobable, y es la parte que decide, es CUÁNDO se avisa. Son dos
// condiciones y las dos importan: sin proceso raíz no había nada que parar, así que
// `terminate` ni se llama; y un `Stop` que funcionó no debe dejar ruido en el log de
// un servicio que se paró bien.
func TestElAvisoDeEscalonDependeDeQueHuboRootYDeQueNoMurio(t *testing.T) {
	casos := []struct {
		nombre    string
		root      int
		noMurio   bool
		wantAviso bool
		por       string
	}{
		{"hubo root y sobrevivió alguien", 100, true, true, "se agotó la escalera: hay que decirlo"},
		{"hubo root y todo murió", 100, false, false, "el servicio se paró bien: un aviso aquí " +
			"sería ruido"},
		{"no hubo root", 0, true, false, "sin root no había nada que parar y `terminate` ni se " +
			"llama; avisar de un linaje vacío sería mentir"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			var avisos []string
			StopSpec{Warn: func(f string, a ...any) {
				avisos = append(avisos, fmt.Sprintf(f, a...))
			}}.avisaSiSeAgotó(c.root, []int{c.root, 200}, c.noMurio)

			if c.wantAviso {
				if len(avisos) != 1 {
					t.Fatalf("avisos = %v, want exactamente uno", avisos)
				}
				if !strings.Contains(avisos[0], "SIGKILL") {
					t.Errorf("aviso = %q, want que nombre el SIGKILL: si se agotó la escalera, "+
						"el usuario tiene que saber que no fue culpa de la cortesía", avisos[0])
				}
				if !strings.Contains(avisos[0], strconv.Itoa(c.root)) {
					t.Errorf("aviso = %q, want el pid del root: sin él el usuario no puede buscar "+
						"el proceso que se resiste", avisos[0])
				}
				return
			}
			if len(avisos) != 0 {
				t.Errorf("avisos = %v, want ninguno: %s", avisos, c.por)
			}
		})
	}
}

// TestTerminateDevuelveFalsoConUnProcesoQueNoSePuedeMatar: la escalera agotada.
//
// `terminate` responde false cuando, tras SIGTERM y SIGKILL, `waitLineageGone` agota
// el plazo con alguien todavía en pie. Para verlo basta con un linaje que contenga un
// proceso al que la escalera no puede matar.
//
// MEDIDO: un hilo del kernel —el pid 2 de esta máquina— encaja exactamente.
// `/proc` lo reporta en estado R, así que `lineageRunning` lo cuenta como vivo, y un
// usuario normal recibe EPERM al mandarle SIGKILL. Es el mismo motivo por el que el
// aviso no se puede provocar desde `Stop`: la escalera sí falla, pero contra alguien
// que no es nuestro.
func TestTerminateDevuelveFalsoConUnProcesoQueNoSePuedeMatar(t *testing.T) {
	kthread := hiloDelKernel()
	if kthread == 0 {
		t.Skip("esta máquina no tiene un hilo del kernel legible en /proc")
	}
	// Si algún día se pudiera señalar, este test dejaría de estar probando lo que dice
	// y `terminate` volvería a devolver true.
	if err := syscall.Kill(kthread, syscall.SIGKILL); err == nil {
		t.Skip("este usuario puede matar hilos del kernel: el escenario de prueba ya no aplica")
	}

	root := lanzar(t, "sleep", "30")

	if terminate(root, 0, []int{root, kthread}, 200*time.Millisecond) {
		t.Error("terminate = true con un proceso imparable en el linaje: la escalera se agotó y " +
			"`Stop` no avisaría de nada")
	}
}

// hiloDelKernel devuelve un pid de hilo del kernel (estado vivo, ppid 0 o 2) o 0.
func hiloDelKernel() int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid < 2 {
			continue
		}
		info, err := procStatAt(filepath.Join(procRoot, e.Name()))
		if err != nil || info.ppid > 2 || !info.running() {
			continue
		}
		return pid
	}
	return 0
}

// lanzar arranca un comando y devuelve su pid, sin esperar a que termine.
func lanzar(t *testing.T, name string, args ...string) int {
	t.Helper()
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}
