package portless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
)

// ---------------------------------------------------------------------------
// Warn estaba al 18.2%: un solo caso probado (el de Node viejo). Es la función
// que traduce una degradación al texto que el usuario lee, y cada motivo existe
// porque SIGNIFICA algo distinto para quien depura:
//
//   - el binario no está      -> el entorno, no vroom
//   - el binario tardó        -> portless colgado
//   - el binario falló        -> Node viejo, el caso medido
//   - no hay proxy           -> portless no está en marcha
//   - el puerto no acepta    -> el proxy declara un puerto muerto
//   - el nombre está ocupado -> hay otro dueño, y la ruta no se tocó
//   - el puerto no está       -> el bind no se resolvió todavía
//   - no se sirve la ruta     -> la ruta está escrita pero nadie la enruta
//
// Con un solo caso probado, un texto equivocado en los otros siete pasaría: el
// aviso es lo único que dice qué pasó, y un aviso que no lo dice es ruido.
//
// Se comprueba lo que el aviso PROMETE, no que exista: cada motivo tiene que
// decir qué pasó Y que el servicio sigue en su puerto, porque la ausencia de
// ruta no es una caída y el usuario tiene que poder distinguirlo de un vistazo.
// ---------------------------------------------------------------------------

// TestWarnNombraCadaMotivoYPrometeElPuerto: tabla sobre TODOS los motivos
// declarados, incluidos los dos que ninguna ruta de código produce hoy
// (ReasonInvalidName y un motivo desconocido), porque un `default` mal escrito
// es justo lo que se cuela cuando solo se prueba lo que ya se sabe que pasa.
func TestWarnNombraCadaMotivoYPrometeElPuerto(t *testing.T) {
	tests := []struct {
		reason string
		host   string
		// mustContain son fragmentos que el aviso TIENE que llevar para
		// identificar el fallo concreto.
		mustContain []string
		// wantPortPromise si el aviso tiene que decir que el servicio sigue
		// vivo en su puerto. False para los motivos donde la ruta ni se
		// intentó (puerto sin resolver), porque ahí el servicio aún no ha
		// confirmado su puerto y prometerlo sería mentir.
		wantPortPromise bool
	}{
		{
			reason:          ReasonPortlessMissing,
			mustContain:     []string{"binary", "not available"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonPortlessTimeout,
			mustContain:     []string{"did not respond in time"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonPortlessFailed,
			mustContain:     []string{"failed", "Node"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonProxyNotRunning,
			mustContain:     []string{"no proxy running"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonProxyUnreachable,
			mustContain:     []string{"proxy port", "does not accept"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonRouteConflict,
			host:            "tienda.localhost",
			mustContain:     []string{"tienda.localhost", "already taken"},
			wantPortPromise: true,
		},
		{
			reason:          ReasonPortUnresolved,
			mustContain:     []string{"not resolved"},
			wantPortPromise: false,
		},
		{
			reason:          ReasonRouteNotServed,
			mustContain:     []string{"does not serve this route"},
			wantPortPromise: true,
		},
		{
			// Motivo desconocido: el default tiene que seguir siendo util.
			// Si no, un motivo nuevo que se añada a Result y se olvide en Warn
			// produciría un aviso vacio o mudo.
			reason:          "algo_raro_nuevo",
			mustContain:     []string{"algo_raro_nuevo"},
			wantPortPromise: true,
		},
		{
			// ReasonInvalidName existe como constante y ningun camino lo
			// publica todavia. El default tiene que cubrirlo igual, porque
			// puede aparecer hoy.
			reason:          ReasonInvalidName,
			mustContain:     []string{ReasonInvalidName},
			wantPortPromise: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			got := Warn(Result{Name: "svc", Host: tt.host, Reason: tt.reason})

			if got == "" {
				t.Fatalf("Warn(%q) devolvio aviso vacio", tt.reason)
			}
			for _, want := range tt.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("el aviso no nombra el fallo: falta %q\naviso: %q", want, got)
				}
			}
			if tt.wantPortPromise && !strings.Contains(got, "running on its own port") {
				t.Errorf("el aviso no dice que el servicio sigue en su puerto: %q", got)
			}
		})
	}
}

// TestWarnSilenciosoSinMotivo: sin motivo no hay nada que avisar. Es el caso
// normal de un servicio|Published con éxito, y un aviso ahí sería ruido que
// empuja al usuario a buscar un problema que no existe.
func TestWarnSilenciosoSinMotivo(t *testing.T) {
	for _, r := range []Result{
		{Name: "svc", Status: StatusRegistered, Registered: true},
		{Name: "svc", Status: StatusRegistered, Url: "https://svc.localhost"},
		{Name: "svc"},
	} {
		if got := Warn(r); got != "" {
			t.Errorf("Warn(%+v) = %q, want cadena vacia", r, got)
		}
	}
}

// TestWarnNombraElHostEnConflicto: el conflicto es el ÚNICO motivo cuyo aviso
// lleva el nombre de la ruta, y es el que más necesita llevarlo: es el aviso
// que dice "este nombre lo tiene otro en OTRO puerto", y sin el nombre el
// usuario no puede ir a mirar nada.
func TestWarnNombraElHostEnConflicto(t *testing.T) {
	got := Warn(Result{Name: "svc", Host: "tienda.localhost", Reason: ReasonRouteConflict})

	if !strings.Contains(got, "tienda.localhost") {
		t.Errorf("el aviso de conflicto no nombra la ruta: %q", got)
	}
	// Y tiene que decir que la ruta se dejó intacta: es lo que evita que el
	// usuario la borre a mano y rompa al dueño real.
	if !strings.Contains(got, "left untouched") {
		t.Errorf("el aviso de conflicto no dice que la ruta quedó intacta: %q", got)
	}
}

// TestClassifyTraduceCadaError: classify decide qué motivo se publica, y es lo
// que luego Warn traduce a texto. Un classify equivocado hace que el usuario
// lea "el puerto no está resuelto" cuando lo que pasó es que el binario tardó.
//
// Se cubren los errores por los que se llega aqui: los del binario (via exec)
// y los del cliente (timeout del contexto, proxy parado).
func TestClassifyTraduceCadaError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"timeout del contexto", context.DeadlineExceeded, ReasonPortlessTimeout},
		{"proxy parado", ErrProxyNotRunning, ReasonProxyNotRunning},
		{"binario ausente por nombre", execErrNotFound(), ReasonPortlessMissing},
		{"binario ausente por ruta", errors.New("fork/exec /opt/portless: no such file or directory"), ReasonPortlessMissing},
		{"fallo generico", errors.New("requires Node >= 24"), ReasonPortlessFailed},
		{"json corrupto", errors.New("SyntaxError: Unexpected token }"), ReasonPortlessFailed},
		{"envolvido de otro modo", wrappedProxyNotRunning(), ReasonProxyNotRunning},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.err); got != tt.want {
				t.Errorf("classify(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestIsMissingBinaryDistingueAusenteDeRoto: es la razon por la que existe esa
// funcion, segun su propio comentario: con una RUTA que no existe,
// exec.Command NO devuelve ErrNotFound sino el error de fork/exec. Confundir
// los dos hace que el usuario depure un portless roto que no existe, o un Node
// viejo que no es la causa.
func TestIsMissingBinaryDistingueAusenteDeRoto(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"ErrNotFound", execErrNotFound(), true},
		{"fork/exec sin fichero", errors.New(`fork/exec /opt/x: no such file or directory`), true},
		{"envuelto", wrappedNotFound(), true},
		{"node viejo", errors.New("requires Node >= 24"), false},
		{"permisos", errors.New("EACCES: permission denied"), false},
		{"json corrupto", errors.New("SyntaxError: Unexpected token }"), false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMissingBinary(tt.err); got != tt.want {
				t.Errorf("isMissingBinary(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestApplySinBinarioNoResuelvePuertoNiBinario: sin binario, Apply degrada
// inmediatamente. Lo que importa es que NO toca nada: ni busca el binario, ni
// escribe, ni sondea. Es la degradación que un runner de CI sin portless ve en
// cada arranque, y si intentara algo dejaría rastro en el entorno.
func TestApplySinBinarioNoResuelvePuertoNiBinario(t *testing.T) {
	c := New(WithBinary(""))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want %q", r.Status, StatusDegraded)
	}
	if r.Reason != ReasonPortlessMissing {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonPortlessMissing)
	}
	if r.Registered {
		t.Error("Registered=true sin binario: se afirmaria una escritura que no ocurrio")
	}
	if r.Url != "" {
		t.Errorf("Url = %q sin binario", r.Url)
	}
	// El nombre es el PRETENDIDO: siempre se rellena, haya éxito o no.
	if r.Name != "svc" {
		t.Errorf("Name = %q, want svc incluso degradado", r.Name)
	}
}

// TestApplyConPuertoNoResueltoNoInventaDireccion: port <= 0 no es un fallo del
// binario, es que el bind no se resolvió. Publicar una URL aquí sería afirmar una
// dirección que nadie confirmó.
func TestApplyConPuertoNoResueltoNoInventaDireccion(t *testing.T) {
	f := newFake()
	c := f.client(t)

	for _, port := range []int{0, -1} {
		r := c.Apply("svc", port, Ownership{})

		if r.Reason != ReasonPortUnresolved {
			t.Errorf("port=%d: Reason = %q, want %q", port, r.Reason, ReasonPortUnresolved)
		}
		if r.Registered {
			t.Errorf("port=%d: Registered=true sin puerto confirmado", port)
		}
		if len(f.calls) != 0 {
			t.Errorf("port=%d: se invoco el binario sin puerto: %v", port, f.calls)
		}
	}
}

// TestApplyConservaElHechoRegistradoCuandoFallaLaLecturaDeVuelta: la lectura
// de vuelta falla DESPUÉS de que el alta ocurriera. Degradar el estado no puede
// deshacer el hecho: la ruta está escrita y es nuestra, y perder ese dato haría
// que la reconciliación no supiera qué limpiar.
func TestApplyConservaElHechoRegistradoCuandoFallaLaLecturaDeVuelta(t *testing.T) {
	f := newFake()
	c := f.client(t)
	// list falla a partir de la segunda llamada: la consulta previa pasa, la
	// lectura de vuelta no.
	calls := 0
	realExec := f.exec
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			calls++
			if calls > 1 {
				return "", 1, errors.New("portless list: routes.json unreadable")
			}
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if !r.Registered {
		t.Error("Registered se perdio tras un fallo de lectura: la ruta se escribio y quedaria huerfana")
	}
	if r.Url != "" {
		t.Errorf("Url = %q sin haber podido verificar", r.Url)
	}
	// Y el motivo tiene que ser el del fallo real, no un generico.
	if r.Reason != ReasonPortlessFailed {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonPortlessFailed)
	}
}

// TestApplySinProxyDegradaPeroConservaElAlta: el caso M6 medido —el binario
// acepta exit 0 sin proxy en marcha porque no lo contacta—. La ruta se escribe y
// es nuestra; lo que no se puede es publicar una URL.
func TestApplySinProxyDegradaPeroConservaElAlta(t *testing.T) {
	f := newFake()
	dir := t.TempDir() // sin proxy.port: no hay proxy
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(2*time.Second),
	)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if r.Reason != ReasonProxyNotRunning {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonProxyNotRunning)
	}
	if !r.Registered {
		t.Error("Registered se perdio con el proxy parado: el alta ocurrio (M1, el binario no lo contacta)")
	}
	if r.Url != "" {
		t.Errorf("Url = %q sin proxy", r.Url)
	}
	// Y la ruta tiene que estar de verdad en el estado del fake.
	if _, ok := f.routes[Hostname("svc")]; !ok {
		t.Error("la ruta no se escribio en el binario, pero el Result dice Registered")
	}
}

// TestApplyPublicaCuandoElProxyEnruta: el camino de éxito completo, para que los
// degradados de arriba sean comparables con algo que sí funciona.
func TestApplyPublicaCuandoElProxyEnruta(t *testing.T) {
	f := newFake()
	c := f.client(t)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusRegistered {
		t.Fatalf("Status = %q (motivo %q), want registrado: %v", r.Status, r.Reason, f.calls)
	}
	if !strings.HasPrefix(r.Url, "http") {
		t.Errorf("Url = %q, quiero un esquema http/https", r.Url)
	}
	if !strings.HasSuffix(r.Url, Hostname("svc")) {
		t.Errorf("Url = %q no termina en el hostname de la ruta", r.Url)
	}
	if r.Reason != "" {
		t.Errorf("Reason = %q en un alta correcta", r.Reason)
	}
	if !r.Registered {
		t.Error("Registered=false en un alta correcta")
	}
}

// TestWithReasonConservaElHechoYQuitaLaUrl: withReason es lo que mantiene
// Registered mientras degrada. Si se equivocara al respectar el hecho, toda la
// garantía de reconciliación se pierde en silencio.
func TestWithReasonConservaElHechoYQuitaLaUrl(t *testing.T) {
	base := Result{
		Name: "svc", Host: "svc.localhost", Port: 8080,
		Status: StatusRegistered, Url: "https://svc.localhost", Registered: true,
	}

	got := base.withReason(ReasonProxyNotRunning)

	if got.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", got.Status)
	}
	if got.Reason != ReasonProxyNotRunning {
		t.Errorf("Reason = %q", got.Reason)
	}
	if got.Url != "" {
		t.Errorf("Url = %q: degradar tiene que quitar la URL no verificada", got.Url)
	}
	if !got.Registered {
		t.Error("Registered se perdio al degradar: la escritura ocurrio")
	}
	if got.Name != base.Name || got.Host != base.Host || got.Port != base.Port {
		t.Errorf("withReason perdio identidad: %+v", got)
	}
	// Y no puede mutar la original: es un receptor por valor y los callers la
	// reutilizan (`registered`) para los dos caminos.
	if base.Status != StatusRegistered || base.Url == "" {
		t.Errorf("withReason muto el receptor original: %+v", base)
	}
}

// TestReleaseConNombreVacioRevocaSinTocarNada: no había nada nuestro que
// retirar, luego sí se revoca. Es lo que evita que un stop de un servicio que
// nunca tuvo ruta deje la propiedad puesta.
func TestReleaseConNombreVacioRevocaSinTocarNada(t *testing.T) {
	called := false
	r := ReleaserFunc(func(string) error { called = true; return nil })

	if !Release(r, "") {
		t.Error("sin nombre no hay nada que retirar: debe revocar")
	}
	if called {
		t.Error("se llamo al releaser con nombre vacio")
	}
}

// TestReleaseRevocaConRutaAusente: ErrRouteAbsent SÍ revoca, porque la ruta no
// está que es justo lo que se quería. Un fallo real NO revoca, o la ruta
// quedaría huérfana sin nadie que la limpie.
func TestReleaseRevocaConRutaAusente(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"exito", nil, true},
		{"benigno M10: no estaba", ErrRouteAbsent, true},
		{"envuelto, sigue siendo el mismo", wrappedRouteAbsent(), true},
		{"fallo real", errors.New("requires Node >= 24"), false},
		{"permisos", errors.New("EACCES"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Release(failingReleaserWith{err: tt.err}, "mi-ruta"); got != tt.want {
				t.Errorf("Release con %v = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// failingReleaserWith devuelve siempre el mismo error.
type failingReleaserWith struct{ err error }

func (f failingReleaserWith) RemoveAbsent(string) error { return f.err }

// TestReleaserFuncAdaptaUnaFuncion: el adaptador existe para que un test pueda
// pasar una closure. Sin ejercitarlo, el seam de retirada sería solo teórico: la
// interfaz existe pero nadie la implementa fuera de aquí.
func TestReleaserFuncAdaptaUnaFuncion(t *testing.T) {
	var got string
	var r Releaser = ReleaserFunc(func(name string) error { got = name; return ErrRouteAbsent })

	if err := r.RemoveAbsent("x"); !errors.Is(err, ErrRouteAbsent) {
		t.Errorf("el adaptador no devuelve el error de la funcion: %v", err)
	}
	if got != "x" {
		t.Errorf("la funcion recibio %q, want x", got)
	}
}

// TestInertReleaserNoTocaNadaYEsEstatico: es el releaser por defecto de un binario
// de test. Lo que protege es que un test que se olvide de instalar el seam NO
// pueda mutar el routes.json del desarrollador, así que lo que hay que fijar es
// que devuelve nil SIEMPRE y que es la misma instancia (inmutable por
// construccion).
func TestInertReleaserNoTocaNadaYEsEstatico(t *testing.T) {
	r := InertReleaser()
	if err := r.RemoveAbsent("cualquier-cosa"); err != nil {
		t.Errorf("el releaser inerte devolvio %v, debe ser nil siempre", err)
	}
	// No se compara la identidad: ReleaserFunc es un tipo func, no comparable
	// con ==, y eso romperia el binario de test. Lo que importa es que TODAS
	// las llamadas son inertes, no que sea la misma.
	for _, name := range []string{"", "a", "ruta.larga.localhost"} {
		if err := InertReleaser().RemoveAbsent(name); err != nil {
			t.Errorf("InertReleaser().RemoveAbsent(%q) = %v, debe ser nil siempre", name, err)
		}
	}
	// Y que cumple el contrato de Releaser, que es lo que verifica el compilador
	// en los tres call sites de produccion.
	assertImplementsReleaser(t, InertReleaser())
}

// TestIsTestBinaryDetectaElBinarioDeTest: la guarda que hace que InertReleaser
// sea el releaser por defecto. Si dejara de detectar el .test, un test sin seam
// escribiría en el portless real del desarrollador.
func TestIsTestBinaryDetectaElBinarioDeTest(t *testing.T) {
	if !IsTestBinary() {
		t.Error("IsTestBinary() es false corriendo bajo `go test`: la guarda de InertReleaser no protege nada")
	}
	// Y depende del nombre del binario, no de una variable: se comprueba el
	// criterio contra las dos formas que se rechazan.
	orig := os.Args[0]
	t.Cleanup(func() { os.Args[0] = orig })

	os.Args[0] = "/usr/local/bin/vroom"
	if IsTestBinary() {
		t.Error("el binario de producción se identifica como test")
	}
	os.Args[0] = "/tmp/vroom.test"
	if !IsTestBinary() {
		t.Error("un binario .test deberia identificarse como test")
	}
}

// TestClientForDevuelveNilSinContratoDeRuta: la puerta de compatibilidad hacia
// atrás. Con route_mode = off hay que devolver nil ANTES de resolver nada, y
// ese "antes" es lo que impide que vroom busque portless en un proyecto que no
// lo pidió. Si alguien mueve la comprobación, la puerta se abre sin que nada
// falle.
func TestClientForDevuelveNilSinContratoDeRuta(t *testing.T) {
	// PORTLESS_BIN y PORTLESS_STATE_DIR a rutas que NO existen: si ClientFor
	// llegara a resolver, construiría un cliente con binario (o al menos
	// intentaría leer el entorno) y el test lo detectaría.
	t.Setenv("PORTLESS_BIN", filepath.Join(t.TempDir(), "no-existe"))
	t.Setenv("PORTLESS_STATE_DIR", t.TempDir())

	tests := []struct {
		name string
		m    *manifest.Manifest
		want bool // ¿debe devolver cliente?
	}{
		{"nil", nil, false},
		{"sin route_mode (default off)", &manifest.Manifest{Name: "svc"}, false},
		{"route_mode off", &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeOff}, false},
		{"route_mode invalido", &manifest.Manifest{Name: "svc", RouteMode: "inventado"}, false},
		{"route_mode named", &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeNamed, RouteName: "svc"}, true},
		{"route_mode auto", &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeAuto}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClientFor(tt.m)
			if tt.want && got == nil {
				t.Error("ClientFor devolvio nil con contrato de ruta activo")
			}
			if !tt.want && got != nil {
				t.Error("ClientFor devolvio un cliente sin contrato de ruta: se buscaria portless en un proyecto que no lo pidio")
			}
		})
	}
}

// TestResolveStateDirSigueElOrdenMedido: el orden es $PORTLESS_STATE_DIR →
// $XDG_STATE_HOME/portless → $HOME/.portless, y MEDIDO contra portless 0.15.6:
// $PORTLESS_HOME NO se honra. Implementar el orden del plan producia dos vistas
// del mismo estado y el síntoma era una ruta que se registraba y luego no se
// podía quitar.
func TestResolveStateDirSigueElOrdenMedido(t *testing.T) {
	t.Run("PORTLESS_STATE_DIR manda", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "/opt/pl-state")
		t.Setenv("XDG_STATE_HOME", "/xdg")
		t.Setenv("HOME", "/home/u")
		if got := ResolveStateDir(); got != "/opt/pl-state" {
			t.Errorf("ResolveStateDir = %q, want /opt/pl-state", got)
		}
	})

	t.Run("XDG_STATE_HOME cuando no hay PORTLESS_STATE_DIR", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "/xdg")
		t.Setenv("HOME", "/home/u")
		if got := ResolveStateDir(); got != filepath.Join("/xdg", "portless") {
			t.Errorf("ResolveStateDir = %q, want /xdg/portless", got)
		}
	})

	t.Run("HOME como ultimo recurso", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "/home/u")
		if got := ResolveStateDir(); got != "/home/u/.portless" {
			t.Errorf("ResolveStateDir = %q, want /home/u/.portless", got)
		}
	})

	t.Run("sin nada devuelve vacio y no un path inventado", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "")
		if got := ResolveStateDir(); got != "" {
			t.Errorf("ResolveStateDir = %q: un path de usuario concreto seria un modo de fallo silencioso", got)
		}
	})
}

// TestResolveBinarySigueElOrdenMedido: $PORTLESS_BIN → LookPath → shims de
// mise. El último paso no es decorativo: medido, `env -i PATH=/usr/bin:/bin` NO
// resuelve portless porque vive tras los shims (M14).
func TestResolveBinarySigueElOrdenMedido(t *testing.T) {
	t.Run("PORTLESS_BIN manda", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "/opt/bin/portless")
		if got := ResolveBinary(); got != "/opt/bin/portless" {
			t.Errorf("ResolveBinary = %q, want /opt/bin/portless", got)
		}
	})

	t.Run("shim de mise cuando no esta en el PATH", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "")
		// PATH vacio: LookPath no puede encontrar nada, y el unico camino que
		// queda son los shims.
		t.Setenv("PATH", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		shimDir := filepath.Join(home, ".local", "share", "mise", "shims")
		if err := os.MkdirAll(shimDir, 0o755); err != nil {
			t.Fatal(err)
		}
		shim := filepath.Join(shimDir, "portless")
		if err := os.WriteFile(shim, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		if got := ResolveBinary(); got != shim {
			t.Errorf("ResolveBinary = %q, want el shim %q: con PATH vacio este es el unico camino real", got, shim)
		}
	})

	t.Run("un shim que es directorio no cuenta", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "")
		t.Setenv("PATH", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		// Un DIRECTORIO llamado portless: resolverlo como binario daría un
		// cliente que no puede ejecutar nada.
		shimDir := filepath.Join(home, ".local", "bin")
		if err := os.MkdirAll(filepath.Join(shimDir, "portless"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := ResolveBinary(); got != "" {
			t.Errorf("ResolveBinary = %q, un directorio no es un binario", got)
		}
	})

	t.Run("nada resoluble devuelve vacio", func(t *testing.T) {
		t.Setenv("PORTLESS_BIN", "")
		t.Setenv("PATH", "")
		t.Setenv("HOME", t.TempDir())
		if got := ResolveBinary(); got != "" {
			t.Errorf("ResolveBinary = %q, want vacio (degradacion, no error)", got)
		}
	})
}

// TestDefaultConstruyeClienteConLoResuelto: Default es lo que usan los caminos de
// producción (Release con r nil y ClientFor). Sin binario devuelve un cliente SIN
// binario en vez de error: es una degradación que Apply traduce en aviso.
func TestDefaultConstruyeClienteConLoResuelto(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "/opt/bin/portless")
	t.Setenv("PORTLESS_STATE_DIR", "/opt/pl-state")

	c := Default()

	if !c.HasBinary() {
		t.Fatal("HasBinary() = false con PORTLESS_BIN puesto")
	}
	if c.Binary() != "/opt/bin/portless" {
		t.Errorf("Binary() = %q", c.Binary())
	}
	if c.StateDir() != "/opt/pl-state" {
		t.Errorf("StateDir() = %q", c.StateDir())
	}
}

func TestDefaultSinBinarioDevuelveClienteDegradado(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PORTLESS_STATE_DIR", t.TempDir())

	c := Default()

	if c == nil {
		t.Fatal("Default devolvio nil: sin binario es una degradacion, no un error")
	}
	if c.HasBinary() {
		t.Error("HasBinary() = true sin binario resoluble")
	}
	if c.Binary() != "" {
		t.Errorf("Binary() = %q, want vacio", c.Binary())
	}
}

func TestDefaultRespetaLasOpcionesSobreElEntorno(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "/opt/bin/portless")
	t.Setenv("PORTLESS_STATE_DIR", "/opt/pl-state")

	c := Default(WithBinary("/forzado/portless"), WithStateDir("/forzado/state"))

	if c.Binary() != "/forzado/portless" {
		t.Errorf("Binary() = %q, la opcion explicita deberia ganar al entorno", c.Binary())
	}
	if c.StateDir() != "/forzado/state" {
		t.Errorf("StateDir() = %q", c.StateDir())
	}
}

// execErrNotFound es el error que exec.Command devuelve cuando no encuentra el
// binario por el PATH. Se construye de verdad, no con una cadena: classify e
// isMissingBinary comparan con errors.Is, y un string hecho a mano no lo
// satisfaria y el test probaria el mensaje, no el comportamiento.
func execErrNotFound() error { return exec.ErrNotFound }

func wrappedNotFound() error {
	return fmt.Errorf("portless alias x: %w", exec.ErrNotFound)
}

func wrappedProxyNotRunning() error {
	return fmt.Errorf("verificando la ruta: %w", ErrProxyNotRunning)
}

func wrappedRouteAbsent() error {
	return fmt.Errorf("retirando: %w", ErrRouteAbsent)
}

// assertImplementsReleaser comprueba en ejecucion que r cumple Releaser. La
// comprobacion de compilacion la haria una sentencia `var _ Releaser = r`, pero
// el linter (staticcheck QF1011) pide omitir el tipo en una asignacion asi, y un
// test no es sitio para pelearse con el linter: el contrato se verifica aqui,
// con el mismo efecto.
func assertImplementsReleaser(t *testing.T, r Releaser) {
	t.Helper()
	if r == nil {
		t.Fatal("InertReleaser devolvio nil")
	}
}
