package portless

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
)

// ---------------------------------------------------------------------------
// Los ultimos bordes: ramas de un statement que seemed decorativas hasta que se
// miro por que existian. Todas comparten una forma: una condicion que el codigo
// de produccion hace imposible o casi imposible, y que sin embargo tiene un
// comportamiento que DEPENDE de ella.
//
// No se eliminan como el `len(result)==0` de bordered (guarda de runtime
// imposible): estas son validas y un cambio futuro las puede activar. Se
// cubren para que su comportamiento sea conocido en vez de supuesto.
// ---------------------------------------------------------------------------

// TestHostnameNoDuplicaElSufijoNiLoPierde: la normalizacion de Hostname. Un
// nombre que ya acaba en .localhost no debe duplicarlo (alias y --remove
// normalizan igual, asi que vroom puede pasar cualquiera de las dos formas), y
// uno que solo tiene el sufijo no debe producir nada.
//
// El segundo caso del test es el que no es obvio: "..localhost" colapsa los
// puntos DESPUES de quitar el sufijo, asi que puede RECONSTRUIR uno. Por eso el
// HasSuffix de Hostname existe y no es decorativo: sin el, la entrada
// "a..localhost" —que sanitize reduce a "a"— no fallaria, pero una entrada donde
// el colapso reconstruya el sufijo devolveria "x.localhost.localhost".
func TestHostnameNoDuplicaElSufijoNiLoPierde(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"sin sufijo", "svc", "svc.localhost"},
		{"con sufijo", "svc.localhost", "svc.localhost"},
		{"sufijo en mayusculas", "svc.LOCALHOST", "svc.localhost"},
		{"sufijo repetido", "svc.localhost.localhost", "svc.localhost"},
		{"solo el sufijo: nada utilizable", ".localhost", ""},
		{"vacio", "", ""},
		{"solo caracteres no utilizables", "!!!", ""},
		{"con espacios alrededor", "  svc  ", "svc.localhost"},
		{"subdominio", "a.b", "a.b.localhost"},
		{
			// El caso que hace que el HasSuffix de Hostname exista: quitar el
			// sufijo ocurre ANTES de colapsar los puntos, asi que una entrada
			// con ".localhost" separado puede RECONSTRUIR el sufijo al colapsar.
			// Sin el HasSuffix, esta devolveria "x.localhost.localhost".
			"sufijo reconstruido por el colapso de puntos",
			"localhost..LocalHostLocalHost", "localhost.localhost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Hostname(tt.in); got != tt.want {
				t.Errorf("Hostname(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestHostnameNuncaEmiteElSufijoDosVeces: invariante sobre un barrido. El
// sufijo ".localhost" tiene que aparecer UNA vez como mucho en la salida, y solo
// si queda algo antes. Se recorre un conjunto de entradas adversariales —con
// puntos, guiones, mayusculas y sufijo repetido— porque la combinacion de "quitar
// el sufijo" y "colapsar puntos" es donde un nombre malformado se escapa.
func TestHostnameNuncaEmiteElSufijoDosVeces(t *testing.T) {
	atoms := []string{"", ".", "..", "a", "svc", "-", "_", "localhost", ".localhost", "LocalHost", "9"}
	checked := 0

	for _, a := range atoms {
		for _, b := range atoms {
			for _, c := range atoms {
				in := a + b + c
				got := Hostname(in)
				checked++

				if got == "" {
					continue
				}
				if !strings.HasSuffix(got, HostSuffix) {
					t.Errorf("Hostname(%q) = %q no termina en el sufijo", in, got)
				}
				if strings.Count(got, HostSuffix) > 1 {
					t.Errorf("Hostname(%q) = %q repite el sufijo", in, got)
				}
				if strings.HasPrefix(got, ".") || strings.HasSuffix(got, ".") {
					t.Errorf("Hostname(%q) = %q tiene un punto colgante", in, got)
				}
			}
		}
	}
	t.Logf("barridas %d combinaciones", checked)
}

// TestDeriveNameAutoSinRamaUsaElProyecto: en auto, sin rama utilizable, el
// nombre cae al proyecto. Es la degradacion honesta: se sigue Derrickndo una
// direccion en vez de fallar, y el conflicto por rama repetida lo reporta Apply.
func TestDeriveNameAutoSinRamaUsaElProyecto(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		want   string
	}{
		{"rama vacia", "", "tienda-api"},
		{"rama no utilizable", "!!!", "tienda-api"},
		{"rama con espacios", "   ", "tienda-api"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeriveName(manifest.RouteModeAuto, "", tt.branch, "tienda-api")
			if err != nil {
				t.Fatalf("DeriveName fallo: %v", err)
			}
			if got != tt.want {
				t.Errorf("DeriveName = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDeriveNameAutoSinRamaNiProyectoUtilizableFalla: sin rama Y sin proyecto
// utilizable no hay nada que derivar. Falla en vez de inventar un nombre: un
// nombre inventado seria una ruta que colisionaria con la de otro sin que nada
// lo dijera.
func TestDeriveNameAutoSinRamaNiProyectoUtilizableFalla(t *testing.T) {
	got, err := DeriveName(manifest.RouteModeAuto, "", "", "!!!")

	if err == nil {
		t.Fatalf("DeriveName devolvio %q, want error: no hay nada de quoi derivar un nombre", got)
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("el error %q no explica que el problema es el nombre", err)
	}
}

// TestDeriveNameAutoPrefiereLaRama: el orden de la derivacion. La rama manda
// porque auto es SCOPE DE RAMA, no de worktree: dos ramas distintas del mismo
// repo no pueden compartir direccion.
func TestDeriveNameAutoPrefiereLaRama(t *testing.T) {
	got, err := DeriveName(manifest.RouteModeAuto, "", "feature/login", "tienda-api")
	if err != nil {
		t.Fatal(err)
	}
	if got != "feature-login.tienda-api" {
		t.Errorf("DeriveName = %q, want feature-login.tienda-api", got)
	}
}

// TestResolveBinaryEncuentraElDelPATH: el segundo paso del orden. Con
// PORTLESS_BIN vacio y un portless ejecutable en el PATH, tiene que resolverlo
// sin llegar a los shims.
func TestResolveBinaryEncuentraElDelPATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "portless")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())

	got := ResolveBinary()

	if got != bin {
		t.Errorf("ResolveBinary = %q, want %q (el del PATH)", got, bin)
	}
}

// TestResolveBinaryIgnoraUnFicheroNoEjecutableDelPATH: LookPath exige permiso de
// ejecucion. Resolver un fichero sin el bit seria devolver un binario que no se
// puede ejecutar, y el error seria un "permission denied" en vez del aviso claro
// de "no hay portless".
func TestResolveBinaryIgnoraUnFicheroNoEjecutableDelPATH(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede ejecutar un fichero sin permiso de ejecucion: la rama no se puede provocar")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "portless")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", dir)
	// Un HOME sin shim: si el PATH no resuelve, no debe aparecer nada.
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := ResolveBinary(); got != "" {
		t.Errorf("ResolveBinary = %q: un fichero sin permiso de ejecucion no es un binario", got)
	}
}

// TestHTTPProbeConElNombreDeUnProxyMuerto: httpProbe devuelve un error (no un
// status) cuando no hubo respuesta. verify depende de esa distincion: un error
// significa "ningun esquema respondio" y le deja seguir al siguiente, mientras
// que un 404 significa "el proxy no conoce el host" y corta.
//
// Sin este test, un httpProbe que devolviera 0 con error en vez de propagar el
// error haria que verify tratara un fallo de conexion como un 404.
func TestHTTPProbeConElNombreDeUnProxyMuerto(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	status, err := httpProbe(ctx, "http", "svc.localhost", deadPort, "/")

	if err == nil {
		t.Fatalf("httpProbe a un puerto muerto devolvio status %d sin error: verify lo confundiria con un 404", status)
	}
	if status != 0 {
		t.Errorf("status = %d con error de transporte, want 0", status)
	}
}

// TestHTTPProbePropagaElStatusDeVerdad: y el camino feliz, con el Host en la
// peticion. El Host es lo que el proxy usa para elegir la ruta, asi que una
// sonda que no lo enviara consultaria siempre la misma ruta por defecto.
func TestHTTPProbePropagaElStatusDeVerdad(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	status, err := httpProbe(ctx, "http", "svc.localhost", srvPort(t, srv), "/")
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", status)
	}
	if gotHost != "svc.localhost" {
		t.Errorf("la peticion llevo Host = %q, want svc.localhost: es lo que elige la ruta en el proxy", gotHost)
	}
}

// TestAcceptsConnectionsConUnPuertoRealYCerrado: la distincion que verify usa
// para separar "el puerto declarado esta muerto" de "el proxy responde y no
// conoce la ruta". Un net.Dial de verdad, sin stub: lo que se comprueba es que
// dialea o no.
func TestAcceptsConnectionsConUnPuertoRealYCerrado(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	livePort := l.Addr().(*net.TCPAddr).Port

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()

	c := New(WithBinary("/fake/portless"), WithTimeout(2*time.Second))

	if !c.acceptsConnections(livePort) {
		t.Error("acceptsConnections = false con un listener real")
	}
	if c.acceptsConnections(deadPort) {
		t.Error("acceptsConnections = true con el puerto cerrado")
	}
	_ = l.Close()
}

// TestDeriveNamedRechazaUnNombreNoUtilizable: en named, un route_name que no es
// un hostname utilizable es un ERROR, no una degradacion: aqui el nombre es un
// contrato (OAuth, CORS) y una direccion inventada no serviria de nada.
func TestDeriveNamedRechazaUnNombreNoUtilizable(t *testing.T) {
	for _, name := range []string{"", "   ", "!!!", ".localhost", "-"} {
		got, err := DeriveName(manifest.RouteModeNamed, name, "", "proyecto")
		if err == nil {
			t.Errorf("DeriveName(named, %q) = %q, want error", name, got)
		}
	}
}

// TestDeriveNamedIgnoraLaRama: named es estable por construccion, y es lo que
// OAuth y CORS necesitan. Si dependiera de la rama, un `git branch -m` cambiaria
// la URL de un callback ya configurado.
func TestDeriveNamedIgnoraLaRama(t *testing.T) {
	got, err := DeriveName(manifest.RouteModeNamed, "tienda", "cualquier-rama", "proyecto")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tienda" {
		t.Errorf("DeriveName(named) = %q, want tienda: no puede depender de la rama", got)
	}
}

// TestDeriveNameRechazaUnModoDesconocido: un route_mode que EffectiveRouteMode
// ya habria convertido en off. DeriveName es la segunda linea y tambien tiene que
// rechazarlo, porque es publico y un llamador puede pasar la cadena cruda.
func TestDeriveNameRechazaUnModoDesconocido(t *testing.T) {
	for _, mode := range []string{"", "OFF", "auto ", "inventado"} {
		got, err := DeriveName(mode, "tienda", "rama", "proyecto")
		if err == nil {
			t.Errorf("DeriveName(%q) = %q, want error", mode, got)
		}
	}
}

// TestRouteModeEnabledPuertaDeCompatibilidad: la condicion que gobierna
// ClientFor. "" (manifiesto sin route_mode) tiene que estar DESHABILITADO, que es
// la puerta hacia atras: un manifiesto que no declara nada se comporta como antes
// de que existiera esta funcion.
func TestRouteModeEnabledPuertaDeCompatibilidad(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{"", false}, // ausente: off, la puerta de compatibilidad
		{manifest.RouteModeOff, false},
		{manifest.RouteModeAuto, true},
		{manifest.RouteModeNamed, true},
	}

	for _, tt := range tests {
		if got := RouteModeEnabled(tt.mode); got != tt.want {
			t.Errorf("RouteModeEnabled(%q) = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

// TestEffectiveRouteModeCaeAOff: un route_mode invalido tiene que acabar en off,
// no en un valor que se propague. Es lo que hace que ClientFor devuelva nil ante un
// manifiesto corrupto en ese campo.
func TestEffectiveRouteModeCaeAOff(t *testing.T) {
	for _, mode := range []string{"", "OFF", "Auto", "inventado", "auto "} {
		m := &manifest.Manifest{Name: "svc", RouteMode: mode}
		if got := m.EffectiveRouteMode(); got != manifest.RouteModeOff {
			t.Errorf("EffectiveRouteMode(%q) = %q, want %q", mode, got, manifest.RouteModeOff)
		}
	}
	for _, mode := range []string{manifest.RouteModeAuto, manifest.RouteModeNamed, manifest.RouteModeOff} {
		m := &manifest.Manifest{Name: "svc", RouteMode: mode}
		if got := m.EffectiveRouteMode(); got != mode {
			t.Errorf("EffectiveRouteMode(%q) = %q, want %q (debe preservarse)", mode, got, mode)
		}
	}
}

// TestOwnershipAuthorisesSoloSuPropioPuerto: la regla que decide si se puede
// reescribir una ruta o hay que declararla ajena. Con Owned=false no autoriza NADA
// (aun con el puerto correcto), porque no hay prueba de que siga siendo nuestra.
func TestOwnershipAuthorisesSoloSuPropioPuerto(t *testing.T) {
	tests := []struct {
		name string
		held Ownership
		port int
		want bool
	}{
		{"sin propiedad, puerto correcto", Ownership{}, 8080, false},
		{"con propiedad, su puerto", Ownership{Owned: true, Port: 8080}, 8080, true},
		{"con propiedad, otro puerto", Ownership{Owned: true, Port: 8080}, 9999, false},
		{"con propiedad, puerto 0", Ownership{Owned: true, Port: 8080}, 0, false},
		{"propiedad sin puerto", Ownership{Owned: true}, 8080, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.held.Authorises(tt.port); got != tt.want {
				t.Errorf("Authorises(%d) = %v, want %v", tt.port, got, tt.want)
			}
		})
	}
}

// TestExecCommandSinSubcomandoNoRevienta: el error se nombra con args[0], y
// antes de arreglarlo indexaba la lista a pelo. Con la lista vacia reventaba con
// index out of range EN MEDIO DEL ARRANQUE, que es lo peor que puede hacer este
// seam: su razon de existir es no colgar ni romper un arranque.
func TestExecCommandSinSubcomandoNoRevienta(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "falla")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 4\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, code, err := execCommand(context.Background(), script) // sin args

	if err == nil {
		t.Fatal("un exit 4 deberia ser error aunque no haya subcomando")
	}
	if code != 4 {
		t.Errorf("code = %d, want 4", code)
	}
	// El error tiene que nombrar ALGO: sin subcomando nombra el binario.
	if !strings.Contains(err.Error(), "falla") {
		t.Errorf("el error no nombra ni el subcomando ni el binario: %q", err)
	}
}

// TestVerifyDegradaCuandoElPuertoAceptaPeroNoHablaHTTP: el ultimo final de
// verify, y el que mas se confunde con los demas.
//
// Un listener que ACEPTA la conexion pero no responde deja probe con error
// (ningun esquema respondio) y acceptsConnections en true (el puerto vive).
// Los otros dos finales degradados se separan por el status, asi que este solo
// se alcanza aqui. La consecuencia es que se degrada con ReasonRouteNotServed:
// hay alguien en el puerto, pero no es un proxy que sirva la ruta.
//
// Y Registered=true: el alta ocurrio antes de sondear y eso no lo deshace un
// fallo de sonda.
func TestVerifyDegradaCuandoElPuertoAceptaPeroNoHablaHTTP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			// Cierra sin responder: el dial funciona, el HTTP no.
			_ = conn.Close()
		}
	}()

	f := newFake()
	f.noProxy = true // ninguna sonda recibe respuesta
	c := newClientWithProxy(t, l.Addr().(*net.TCPAddr).Port, f.exec)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q: el puerto vive pero no sirve la ruta", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q: nadie respondio a la sonda", r.Url)
	}
	if !r.Registered {
		t.Error("Registered se perdio: el alta ocurrio antes de sondear")
	}
}

// TestReconcileRetiraLaRutaEnrutaYMuertaCuandoEsNuestra: la limpieza que hace
// falta Reconcile. La ruta esta ENRUTADA (el proxy la sirve con 502) y no hay
// nadie detras, que es la huella que deja un vroom que murio sin parar su
// servicio. Y es la unica que `portless prune` no limpia (M5, medido).
//
// La condicion de propiedad es la que se prueba aqui: con held.Authorises(published)
// en true, la retirada proceeds. El caso contrario —la misma situacion sin
// propiedad— NO retira y avisa, y esta cubierto aparte.
func TestReconcileRetiraLaRutaEnrutaYMuertaCuandoEsNuestra(t *testing.T) {
	f := newFake()
	c := f.client(t)
	host := Hostname("ruta-vieja")
	f.routes[host] = 8080
	f.probeStatus = 502 // enruta, y el backend no responde

	held := Ownership{Owned: true, Port: 8080}
	warnings := c.Reconcile("ruta-vieja", held, "ruta-nueva")

	if len(warnings) != 0 {
		t.Errorf("una ruta nuestra enruta y muerta deberia retirarse sin avisar, avise: %v", warnings)
	}
	if _, still := f.routes[host]; still {
		t.Errorf("la ruta %q sigue en el estado tras la reconciliacion: es la huella de un vroom muerto", host)
	}
	if len(f.removedNames) == 0 {
		t.Error("no se registro ninguna retirada: la limpieza no ocurrio")
	}
}

// TestReconcileNoRetiraUnaRutaEnrutaYMuertaSinPropiedad: MISMO estado que el
// anterior —proxy enruta, 502, nadie detrás, y el puerto persistido COINCIDE con
// el que sirve el proxy— pero con la propiedad revocada. No se puede probar que
// sea nuestra, y borrar lo ajeno es el daño que todo esto evita.
//
// El estado de entrada es real y es el que dejó MEDIUM-C: un Meta con
// route_port = 8080 y route_owned = false, que es lo que hay cuando alguien ya
// retiró la ruta o cuando el Meta es de antes de que route_owned existiera. Que
// el puerto coincida NO la salva, y esa es la parte importante: el puerto es un
// número que no caduca, luego la autoridad es la propiedad.
//
// Si alguien cambiara la guarda para retirar en cuanto el puerto coincida, este
// test falla y el anterior pasa: son la pareja mínima que hace verificable la
// decisión.
func TestReconcileNoRetiraUnaRutaEnrutaYMuertaSinPropiedad(t *testing.T) {
	f := newFake()
	c := f.client(t)
	host := Hostname("ruta-ajena")
	f.routes[host] = 8080
	f.probeStatus = 502

	// El puerto coincide con el que sirve el proxy, pero la propiedad está
	// revocada: el numero solo no autoriza nada.
	held := Ownership{Owned: false, Port: 8080}
	warnings := c.Reconcile("ruta-ajena", held, "ruta-nueva")

	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want 1 aviso: la ruta no es nuestra", warnings)
	}
	if !strings.Contains(warnings[0], "left untouched") {
		t.Errorf("el aviso no dice que la ruta quedo intacta: %q", warnings[0])
	}
	if _, still := f.routes[host]; !still {
		t.Error("se borro una ruta sin prueba de que fuera nuestra")
	}
	if len(f.removedNames) != 0 {
		t.Errorf("se Registro una retirada: %v", f.removedNames)
	}
}

// TestHTTPProbeConUnHostnameNoParseable: la rama de error de http.NewRequest, que
// ocurre antes de tocar la red. Se cubre con un hostname invalido, y lo que
// importa es que devuelva ERROR y no un status: verify trata las dos cosas de
// forma distinta, y un 0 disfrazado de status seria un 404.
func TestHTTPProbeConUnHostnameNoParseable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for _, hostname := range []string{"host con espacio", "host	con	tab", "%%"} {
		status, err := httpProbe(ctx, "http", hostname, 1, "/")
		if err == nil {
			t.Errorf("hostname %q: sin error y status %d, se confundiria con un 404", hostname, status)
		}
		if status != 0 {
			t.Errorf("hostname %q: status = %d con error de parseo, want 0", hostname, status)
		}
	}
}

// TestLiveRouteSaltaALLookupCuandoLaTablaNoLaTiene: el proxy ENRUTA la ruta
// (responde 200, no 404) pero la tabla de `portless list` no la menciona.
//
// Sin ese salto, published seria 0 y la reconciliacion compararia 0 contra el
// puerto persistido, y concluiria que "no es nuestra" cuando lo que pasa es que
// la lectura de la tabla y la sonda discrepan. Es la ventana entre M8 y la sonda.
func TestLiveRouteSaltaALLookupCuandoLaTablaNoLaTiene(t *testing.T) {
	f := newFake()
	c := f.client(t)
	host := Hostname("ruta-fantasma")
	// El proxy la sirve (no 404)…
	f.routes[host] = 8080
	f.serve404[host] = false
	f.probeStatus = 200

	// …pero list no la devuelve.
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			return "\nActive routes:\n\n", 0, nil
		}
		return f.exec(ctx, bin, args...)
	}

	published, st := c.liveRoute("ruta-fantasma")

	if st != routeUnknown {
		t.Errorf("state = %v, want %v: sin lectura de la tabla no se puede afirmar nada", st, routeUnknown)
	}
	if published != 0 {
		t.Errorf("published = %d en un routeUnknown: ese camino devuelve 0 SIEMPRE", published)
	}
}

// TestLiveRouteSaltaALLookupCuandoLaLecturaFalla: lo mismo, pero la lectura de
// la tabla falla en vez de no encontrar la ruta. Distinguirlo de "no esta" es lo
// que evita tratar un estado de portless ilegible como evidencia de nada.
func TestLiveRouteSaltaALLookupCuandoLaLecturaFalla(t *testing.T) {
	f := newFake()
	c := f.client(t)
	host := Hostname("ruta-ilegible")
	f.routes[host] = 8080
	f.probeStatus = 200

	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			return "", 0, errors.New("portless list: routes.json ilegible")
		}
		return f.exec(ctx, bin, args...)
	}

	published, st := c.liveRoute("ruta-ilegible")

	if st != routeUnknown {
		t.Errorf("state = %v, want %v: una lectura fallida no es evidencia", st, routeUnknown)
	}
	if published != 0 {
		t.Errorf("published = %d con la lectura fallida, want 0", published)
	}
}
