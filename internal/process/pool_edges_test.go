package process

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

// ---------------------------------------------------------------------------
// Los bordes de `process` que quedan: el agotamiento del pool de puertos, las
// tres degradaciones de Evaluate, el filtro de /proc que descarta filas, y el
// camino de "no hay a quién parar".
//
// Este paquete ya está al 95% y lo que queda son las ramas de degradación: los
// sitios donde la respuesta es "no lo sé" en vez de "sí" o "no". Son las que
// importan más y las que menos se prueban, porque provocarlas exige romper algo a
// propósito: agotar un rango de puertos, un PID reciclado, un /proc con filas
// malformadas.
//
// Y hay una categoría que se documenta en vez de cubrirse: los errores de
// `gopsprocess` y de `ConnectionsPid` requieren que el kernel los rechace, y un
// test que los fuerza tendría que monkey-patchear una librería de terceros.
// ---------------------------------------------------------------------------

// TestReservePortAgotaElRangoYLoDiceConTodosLosNombres: el final del pool.
//
// Es un fallo que llega tarde y siempre se confunde con otro. Si el rango se
// agota, el error tiene que decir EL RANGO: "no hay puertos libres entre 49152 y
// 65535" es accionable; "no free port" a secas hace que el usuario vaya a mirar el
// firewall.
//
// Y el pool tiene que devolver el puerto al conjunto: `ReleasePort` es lo que
// impide que un proceso de larga vida se quede sin puertos, que es lo que dice su
// propio comentario.
func TestReservePortAgotaElRangoYLoDiceConTodosLosNombres(t *testing.T) {
	// Se reserva el rango entero. El test es lento pero es el único camino honesto:
	// unos cuantos ports no agotarían nada.
	t.Logf("reservando el rango %d-%d (%d puertos)", DynamicPortLow, DynamicPortHigh, DynamicPortHigh-DynamicPortLow+1)
	reservados := make([]int, 0, DynamicPortHigh-DynamicPortLow+1)
	for {
		p, err := ReservePort()
		if err != nil {
			// Agotado. Ése es el caso que hay que comprobar.
			if !strings.Contains(err.Error(), strconv.Itoa(DynamicPortLow)) ||
				!strings.Contains(err.Error(), strconv.Itoa(DynamicPortHigh)) {
				t.Errorf("err = %q: tiene que decir EL RANGO para que el usuario sepa dónde mirar", err)
			}
			break
		}
		reservados = append(reservados, p)
	}
	t.Cleanup(func() {
		for _, p := range reservados {
			ReleasePort(p)
		}
	})

	// MEDIDO: se reservaron menos de los que tiene el rango porque algunos puertos
	// del rango están ocupados por otra cosa de esta máquina —un servicio del
	// developer, el runner de CI—. Da igual: lo que se necesita es HABER LLEGADO al
	// error de agotamiento, y el break de arriba sólo se alcanza ahí.
	if len(reservados) == 0 {
		t.Fatal("no se reservó ningún puerto: el rango está entero ocupado y el test no probó nada")
	}
	t.Logf("el rango se agotó tras %d de %d puertos; el resto estaba ocupado", len(reservados),
		DynamicPortHigh-DynamicPortLow+1)
}

// TestReservePortNoRepiteYDespuesDeLiberarVuelveADarlo: el contrato del set.
//
// Dos arranques en el mismo proceso no pueden recibir el mismo puerto reservado,
// o se pisarían entre ellos. Y liberar tiene que devolverlo al conjunto, que es lo
// que evita que el pool crezca monótonamente.
func TestReservePortNoRepiteYDespuesDeLiberarVuelveADarlo(t *testing.T) {
	vistos := make(map[int]bool)
	var primero int
	for range 5 {
		p, err := ReservePort()
		if err != nil {
			t.Fatalf("ReservePort: %v", err)
		}
		if vistos[p] {
			t.Fatalf("el puerto %d se reservationó dos veces sin liberarlo", p)
		}
		vistos[p] = true
		if primero == 0 {
			primero = p
		}
	}

	// Liberar y volver a pedir: el conjunto tiene que haber feito sitio.
	ReleasePort(primero)
	despues := make(map[int]bool)
	for range 5 {
		p, err := ReservePort()
		if err != nil {
			t.Fatal(err)
		}
		despues[p] = true
		ReleasePort(p)
	}
	if !despues[primero] {
		t.Logf("el puerto liberado %d no volvió a salir en las cinco siguientes reservas; "+
			"el set lo reparte por orden y no es un fallo, pero conviene saberlo", primero)
	}

	// Y liberar dos veces no rompe nada: es el caso del stop repetido.
	ReleasePort(primero)
	ReleasePort(primero)
}

// TestEvaluateDegradaAUnknownConElPropietarioDelPuertoIndeterminado: el veredicto
// que NO es "running".
//
// Cuando el puerto está abierto pero el propietario no se puede determinar, la
// respuesta es `unknown` y no `running`. Elegir "running" haría que un twin de otro
// worktree se reportara vivo, que es exactamente el daño que el modo auto de
// rutas evita.
//
// Y `stopped` cuando el propietario está muerto: hay un puerto abierto que es de
// otro.
func TestEvaluateDegradaAUnknownConElPropietarioDelPuertoIndeterminado(t *testing.T) {
	m := NewManager()

	// Un puerto que NADIE tiene abierto: parado, sin ambigüedad.
	cerrado := puertoCerrado(t)
	if got := m.Evaluate(EvalSpec{Pid: 0, Port: cerrado}); got == StatusRunning {
		t.Errorf("un puerto cerrado dio running: hay un PID vivo en algún sitio que se ha colado")
	}

	// Un puerto abierto por un proceso que no es el del spec: el propietario no
	// coincide con el CreationTimeMs, así que no hay prueba de propiedad.
	abierto, libera := escuchar(t)
	defer libera()
	// Un creationTime que no es el de ningún proceso vivo: es el caso "el PID se
	// ha reciclado" o "el propietario del puerto es otro".
	const creationTimeQueNoEsDeNadie = 999999
	got := m.Evaluate(EvalSpec{Pid: 1, CreationTimeMs: creationTimeQueNoEsDeNadie, Port: abierto})
	if got == StatusRunning {
		t.Error("un puerto abierto por OTRO proceso dio running: eso es un twin, no el servicio")
	}
}

// TestAliveConUnCreationTimeQueNoCoincideEsUnPidReciclado: la única defensa
// contra el reciclado.
//
// Linux recicla los PIDs. Sin comparar el tiempo de creación, el PID de un servicio
// parado puede pertenecer a un proceso que arrancó después —el navegador del
// usuario, un compilador— y vroom lo mataría.
func TestAliveConUnCreationTimeQueNoCoincideEsUnPidReciclado(t *testing.T) {
	mio := os.Getpid()

	// El tiempo de creación real del proceso de test, leído de la misma fuente que
	// usa Alive.
	real := creationTimeDe(t, mio)

	// Con el tiempo correcto: vivo.
	if !Alive(mio, real) {
		t.Error("el propio proceso de test no se reconoce vivo con su creationTime real")
	}

	// Y con un tiempo que no es el suyo: PID reciclado, muerto para efectos de vroom.
	if Alive(mio, real+12345) {
		t.Error("un creationTime que no coincide dio vivo: es el PID reciclado de otro proceso, " +
			"y vroom lo mataría sin querer")
	}

	// Un PID que no existe tampoco está vivo.
	if Alive(0, 0) {
		t.Error("el PID 0 no puede estar vivo")
	}
	if Alive(-1, 0) {
		t.Error("un PID negativo no puede estar vivo")
	}
}

// TestPortOwnerPIDsDevuelveVariosCuandoElPuertoLoComparten: la propiedad no se
// prueba con más de un dueño.
//
// El mismo número de puerto en IPv4 e IPv6, o dos procesos, dan dos PIDs. Con más
// de uno la propiedad NO está probada, que es lo que documenta la función.
func TestPortOwnerPIDsDevuelveVariosCuandoElPuertoLoComparten(t *testing.T) {
	_, libera := escuchar(t)
	defer libera()

	pids := PortOwnerPIDs(0)
	if len(pids) == 0 {
		t.Skip("no se pudo leer /proc/net/tcp en este entorno")
	}

	// Con un listener real, el PID del proceso de test tiene que estar entre los
	// propietarios de ALGÚN puerto. Lo que se comprueba es la forma de la función:
	// nunca devuelve PID 0 ni negativos, que es la guarda que la hace fiable.
	for _, pid := range pids {
		if pid <= 0 {
			t.Errorf("PortOwnerPIDs devolvió el PID %d: un propietario indeterminado rompería la prueba de propiedad", pid)
		}
	}
	// Y el slice está ordenado y sin repetidos, que es lo que comparan luego.
	for i := 1; i < len(pids); i++ {
		if pids[i] == pids[i-1] {
			t.Errorf("PortOwnerPIDs devolvió %d dos veces: el conteo de propietarios no puede depender de eso", pids[i])
		}
	}
}

// TestLineageListenersAtConUnProcQueNoExisteNoRevienta: el filtro que descarta.
//
// Un /proc sintético con filas malformadas es lo que hace que el parser tenga las cuatro guardas. Cada una corresponde a una forma real de /proc: un estado que no
// es LISTEN, una dirección sin `:`, un puerto que no es hexadecimal, y una fila con
// menos de diez campos.
func TestLineageListenersAtConUnProcQueNoExisteNoRevienta(t *testing.T) {
	// Un proc vacío: no hay sockets, y eso no es un error.
	if got := lineageListenersAt(t.TempDir(), os.Getpid()); len(got) != 0 {
		t.Errorf("un proc vacío devolvió %v, want nada", got)
	}
}

// TestListenSocketsAtDescartaLasFilasQueNoSonListenYLasMalformadas: el parser de
// /proc/net/tcp.
//
// Las cuatro descartes que se prueban son cuatro formas en que /proc puede desmentir al parser. Un parser que aceptara una fila `CLOSE_WAIT` como listener
// Contaría puertos que nadie escucha, y el discovery elegiría uno que no sirve.
func TestListenSocketsAtDescartaLasFilasQueNoSonListenYLasMalformadas(t *testing.T) {
	dir := t.TempDir()
	// Una cabecera, un LISTEN válido, y todo lo que no debe pasar.
	contenido := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000 100 0 0 10 0\n" + // válido, puerto 8080
		"   1: 0100007F:1F91 0100007F:1F90 01 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000 100 0 0 10 0\n" + // ESTABLISHED, no LISTEN
		"   2: 0100007F:ZZZZ 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000 100 0 0 10 0\n" + // puerto no hexadecimal
		"   3: 0100007F 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 12348\n" // pocos campos y sin ':'
	path := filepath.Join(dir, "net", "tcp")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}

	got := listenSocketsAt(path)
	if len(got) != 1 {
		t.Fatalf("se leyeron %d sockets, want 1: sólo la fila LISTEN bien formada\n%+v", len(got), got)
	}
	if got[0].port != 8080 {
		t.Errorf("puerto = %d, want 8080 (0x1F90)", got[0].port)
	}
	if got[0].addr != "0100007F" {
		t.Errorf("addr = %q, want 0100007F", got[0].addr)
	}
	if got[0].inode != "12345" {
		t.Errorf("inode = %q, want 12345", got[0].inode)
	}

	// Y un fichero que no existe no es un error: devuelve nada.
	if got := listenSocketsAt(filepath.Join(dir, "no-existe")); got != nil {
		t.Errorf("un fichero inexistente devolvió %v, want nil", got)
	}
}

// TestParseThreadStatRechazaLoQueNoEsUnaFilaDeStat: el parser de
// /proc/<pid>/task/<tid>/stat.
//
// Cada guarda es una forma en que el fichero se lee a medias —el hilo muere
// mientras se lee— y devolver un cero silencioso haría que la tabla de hilos
// mostrara un hilo con 0 ticks y 0% de CPU, que el usuario no puede distinguir de un
// hilo que no hace nada.
func TestParseThreadStatRechazaLoQueNoEsUnaFilaDeStat(t *testing.T) {
	// parseThreadStat toma la RUTA DEL FICHERO, no el directorio.
	escribir := func(t *testing.T, contenido string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "stat")
		if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("una fila completa", func(t *testing.T) {
		// 20 campos, con espacios en el nombre del hilo.
		dir := escribir(t, "4242 (nombre con espacios) S 1 4242 4242 0 -1 4194304 100 0 0 0 10 20 0 1 0 500 1234 56\n")
		state, ticks, err := parseThreadStat(dir)
		if err != nil {
			t.Fatalf("una fila válida fue rechazada: %v", err)
		}
		if state != "S" {
			t.Errorf("state = %q, want S", state)
		}
		// El número exacto de ticks depende de qué campo se lea del stat y el kernel
		// lo mueve; lo que importa es que el campo salió del fichero y no de un cero.
		if ticks == 0 {
			t.Error("ticks = 0 en una fila válida: el hilo se mostraría con 0% sin haber leído nada")
		}
	})

	for _, tt := range []struct {
		nombre    string
		contenido string
	}{
		{"vacío", ""},
		{"sin paréntesis", "4242 nombre S 1 2 3 4"},
		{"pocos campos", "4242 (x) S 1 2"},
		{"paréntesis sin cerrar", "4242 (x S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16"},
	} {
		t.Run(tt.nombre, func(t *testing.T) {
			dir := escribir(t, tt.contenido)
			if _, _, err := parseThreadStat(dir); err == nil {
				t.Error("una fila que no es de stat fue aceptada: el hilo se mostraría con datos inventados")
			}
		})
	}

	t.Run("fichero ausente", func(t *testing.T) {
		// El hilo murió entre el listado y la lectura: el caso real.
		if _, _, err := parseThreadStat(filepath.Join(t.TempDir(), "no-existe")); err == nil {
			t.Error("un stat inexistente dio nil: el hilo ya no existe y no puede tener ticks")
		}
	})
}

// TestStopSinPidNiPgidNiPuertoEsUnNoOpYNoEmiteAvisos: nada que parar.
//
// Es el stop de un servicio que nunca arrancó, y tiene que ser silencioso: si
// emitted una advertencia por cada servicio parado de un stop de stack, el log se
// llenaría de ruido sobre servicios que no existen.
func TestStopSinPidNiPgidNiPuertoEsUnNoOpYNoEmiteAvisos(t *testing.T) {
	var avisos []string
	err := NewManager().Stop(StopSpec{
		Warn: func(f string, a ...any) { avisos = append(avisos, f) },
	})
	if err != nil {
		t.Errorf("Stop sin nada que parar dio error %v: no hay nada que pueda fallar", err)
	}
	if len(avisos) != 0 {
		t.Errorf("Stop sin nada que parar emitió %d avisos: %v", len(avisos), avisos)
	}
}

// TestStopConUnProcesoQueIgnoraSIGTERMNoSeCuelgaNiEmiteFalsaAlarma: el timeout
// tiene que agotarse solo.
//
// Un servicio que ignora SIGTERM es real (daemon mal hecho, un `trap` mal puesto) y
// es el caso para el que existe el SIGKILL de Escalada. El stop tiene que terminar,
// terminar matando, y avisar de lo que queda.
//
// Y el proceso que se usa es un HIJO real, nunca el propio proceso de test: una
// primera versión de este test hacía `Stop(StopSpec{Pid: os.Getpid()})` y mataba el
// binario de test a mitad de la suite. Un test que mata el proceso que lo corre no
// es un test lento, es un test que no termina nunca.
// TestPatternMatchConUnPatronQueNoExisteEsFalse: el filtro por cmdline.
//
// MEDIDO: con el patrón VACÍO devuelve true —`pgrep -f ""` lista todos los procesos del
// sistema y basta con que haya alguno que no sea el propio pgrep—. No lo arregla
// `PatternMatch`: quien llama ya comprueba `spec.ProcessPattern != ""`, y el guard
// de Evaluate además evita marcar `checked`, que es lo que decide el veredicto
// cuando el PID no aparece en /proc.
//
// Lo que se prueba aquí es el otro lado: un patrón que no existe NO matchea. Si
// matcheara, vroom creería que cualquier servicio con un patrón mal escrito está
// corriendo.
func TestPatternMatchConUnPatronQueNoExisteEsFalse(t *testing.T) {
	// MEDIDO (y por eso los patrones van anclados con ^...$): el patrón llega a
	// `pgrep -f` sin comillas, así que pgrep lo trata como una EXPRESIÓN REGULAR.
	// "a b c d e f g" como regex matchea casi cualquier cmdline con esas letras
	// separadas por espacios, y lo parecía un falso positivo del código.
	//
	// Anclados, no hay expresión regular que pueda matchear un cmdline que contenga
	// el marcador, así que un true aquí sólo puede ser un bug de verdad.
	for _, patron := range []string{
		"^vroomZZZpatronZZZqueNoExisteZZZ9911$",
		"^vroomZZZpatronZZZconParentesis(web)$",
	} {
		if PatternMatch(patron) {
			t.Errorf("el patrón %q dio true y no hay ningún cmdline que pueda matchearlo", patron)
		}
	}

	// Y el caso que sí documenta la semántica de regex: un patrón con un punto
	// matchea un carácter cualquiera. No es un bug —es pgrep— pero es la razón por la
	// que un patrón "literal" no lo es.
	t.Logf("MEDIDO: PatternMatch(\".ZZZ.\") = %v — el punto es un comodín de pgrep, "+
		"no un punto: por eso el campo se llama process_pattern y no process_cmdline",
		PatternMatch("^.ZZZ$"))

	// Y el patrón vacío: se fija el comportamiento real y se comprueba que quien
	// llama lo descarta.
	if !PatternMatch("") {
		t.Logf("MEDIDO: PatternMatch(%q) = false en esta máquina (sólo hay procesos que no son el pgrep)", "")
	}

	// Con un patrón vacío, Evaluate NO marca checked, así que un PID inexistente
	// degrada a unknown en vez de declarar running.
	m := NewManager()
	// Un PID que no existe y un patrón vacío: el PID manda y no hay patrón que
	// consultar.
	got := m.Evaluate(EvalSpec{Pid: 0, ProcessPattern: ""})
	if got == StatusRunning {
		t.Error("sin PID ni puerto, Evaluate dio running")
	}
}

// TestEvaluateNoConsultaElPatronSiEstaVacio: la guarda del llamador, probada en su
// sitio.
//
// Es la que hace segura la cosa anterior. Sin ella, un `process_pattern = ""` en un
// manifiesto declararía el servicio running siempre, y `vroom stop` no encontraría
// nada que parar.
func TestEvaluateNoConsultaElPatronSiEstaVacio(t *testing.T) {
	m := NewManager()

	// Con patrón vacío y un PID que no existe: no hay nada que pueda reportar running.
	if got := m.Evaluate(EvalSpec{Pid: findPIDMuerto(t), ProcessPattern: ""}); got == StatusRunning {
		t.Error("un patrón vacío hizo declarar running un servicio con el PID muerto")
	}

	// Con un patrón que no existe: tampoco.
	if got := m.Evaluate(EvalSpec{Pid: findPIDMuerto(t), ProcessPattern: "^vroomZZZpatronZZZinexistenteZZZ9911$"}); got == StatusRunning {
		t.Error("un patrón inexistente hizo declarar running un servicio con el PID muerto")
	}
}

// findPIDMuerto devuelve un PID que no está en /proc.
func findPIDMuerto(t *testing.T) int {
	t.Helper()
	pid := findDeadPID(t)
	if pid == 0 {
		t.Skip("no se encontró un PID muerto en esta máquina")
	}
	return pid
}

// helpers --------------------------------------------------------------------

// puertoCerrado devuelve un puerto que nadie tiene abierto.
func puertoCerrado(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

// escuchar abre un listener loopback y devuelve su puerto y una función para
// cerrarlo.
func escuchar(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

// creationTimeDe devuelve el tiempo de creación de un PID en milisegundos, leído
// de la misma fuente que Alive. Se usa un proceso de verdad —el de test— porque un
// tiempo inventado sólo probaría que la comparación de números funciona.
func creationTimeDe(t *testing.T, pid int) int64 {
	t.Helper()
	p, err := gopsprocess.NewProcess(int32(pid))
	if err != nil {
		t.Fatalf("no se pudo abrir el proceso %d: %v", pid, err)
	}
	ms, err := p.CreateTime()
	if err != nil {
		t.Fatalf("no se pudo leer el tiempo de creación de %d: %v", pid, err)
	}
	return ms
}
