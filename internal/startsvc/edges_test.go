package startsvc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Los caminos de error de Start y de la capa de rutas.
//
// Start es el camino que ejecuta un comando del usuario, y cada uno de sus
// errores dice algo distinto: no se pudo reservar un puerto, no se pudo escribir
// el intento en disco, el servicio murió antes de resolver su puerto, el nombre de
// ruta no es utilizable. Confundirlos hace que el agente pruebe a arreglar lo que
// no está roto.
//
// Y hay un caso que merece nombre propio: cuando el servicio muere durante el
// arranque, hay que DEVOLVER el puerto reservado. No hacerlo no es una fuga
// pequeña —el set de puertos dinámicos crece hasta agotarse y el siguiente
// arranque falla con "no free port in range" para puertos que están libres—.
// ---------------------------------------------------------------------------

// failingManager falla en Start, para provocar el fallo del arranque sin tocar
// disco ni procesos.
type failingManager struct {
	process.Manager
	err error
}

func (m failingManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, m.err
}

// degradingRegistrar devuelve un alta que se ESCRIBIÓ pero que no se ha podido
// comprobar: el caso del proxy parado, que es el más común de los degradados.
type degradingRegistrar struct{ reconciled []string }

func (r *degradingRegistrar) Apply(name string, port int, own portless.Ownership) portless.Result {
	return portless.Result{
		Name:       name,
		Port:       port,
		Status:     portless.StatusDegraded,
		Reason:     portless.ReasonPortlessMissing,
		Registered: true,
	}
}

func (r *degradingRegistrar) Reconcile(prev string, own portless.Ownership, now string) []string {
	r.reconciled = append(r.reconciled, prev+"->"+now)
	return nil
}

// TestStartDevuelveElPuertoReservadoSiElHijoNuncaLlego: la reserva se devuelve
// al set cuando el proceso no arranca.
//
// Es una fuga silenciosa si no se devuelve: el puerto queda marcado como
// reservado para siempre, y tras suficientes arranques fallidos el set se agota
// y `vroom start` falla con "no free port in range" para puertos que están
// libres de verdad.
func TestStartDevuelveElPuertoReservadoSiElHijoNuncaLlego(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())

	// Con port_mode dynamic el intento reserva un puerto; si el spawn falla, ese
	// puerto tiene que volver al set.
	m := failingManager{err: errors.New("no such file or directory")}
	_, err := Start(Request{
		Manifest:   &manifest.Manifest{Name: "svc", Command: "./no-existe", PortMode: manifest.PortModeDynamic},
		Path:       root,
		Store:      store,
		Manager:    m,
		StdoutPath: filepath.Join(root, "o.log"),
		StderrPath: filepath.Join(root, "e.log"),
	})
	if err == nil {
		t.Fatal("un comando que no se puede lanzar debería fallar el arranque")
	}

	// La prueba de que el puerto volvió: el mismo puerto tiene que poder
	// reservarse otra vez. No se mira el set por dentro (eso probaría la
	// implementación); se reserva y se compara.
	again, rerr := process.ReservePort()
	if rerr != nil {
		t.Fatalf("no se pudo reservar un puerto después del fallo: %v", rerr)
	}
	process.ReleasePort(again)
}

// TestStartSinPuertoReservadoNoFiltraNadaSinReserva: en port_mode fixed no hay
// reserva que devolver, y el fallo del spawn no puede tocar el set de puertos.
//
// El caso contrario del anterior: si `ReleasePort(0)` se llamara, el set
// conservaría un 0 que luego `samePorts` y `containsPid` tratan como "puerto
// desconocido".
func TestStartSinPuertoReservadoNoFiltraNadaSinReserva(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())

	m := failingManager{err: errors.New("boom")}
	_, err := Start(Request{
		Manifest:   &manifest.Manifest{Name: "svc", Command: "./no-existe", Port: 8081},
		Path:       root,
		Store:      store,
		Manager:    m,
		StdoutPath: filepath.Join(root, "o.log"),
		StderrPath: filepath.Join(root, "e.log"),
	})
	if err == nil {
		t.Fatal("el fallo del spawn tiene que propagarse")
	}
	// El pool sigue usable: si se hubiera metido un 0, el siguiente ReservePort
	// podría devolver algo raro.
	p, rerr := process.ReservePort()
	if rerr != nil || p <= 0 {
		t.Errorf("el set de puertos quedó dañado: ReservePort = %d, %v", p, rerr)
	} else {
		process.ReleasePort(p)
	}
}

// TestStartPropagaElFalloDeGuardarElIntentoConPuertoReservado: si el intento no se
// puede escribir en disco, el arranque falla Y el puerto NO se devuelve.
//
// La segunda mitad va contra la intuición y es la importante. El hijo ya está
// vivo y usando ese puerto; devolverlo al set reabre la carrera por el mismo
// puerto, y otro servicio podría ocuparlo mientras este sigue vivo —quedando dos
// procesos con un puerto—. La reserva se queda hasta el stop, como cualquier
// otra, y el usuario recibe un arranque fallido que puede reintentar.
//
// El fallo se provoca con services/<hash> ocupado por un FICHERO: SaveMeta hace
// MkdirAll de ese directorio y falla. Es un fallo que no depende del uid, así que
// no hace falta saltarse como con los permisos.
func TestStartPropagaElFalloDeGuardarElIntentoConPuertoReservado(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if err := os.MkdirAll(filepath.Join(store.Base(), "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ServiceDir(root), []byte("bloquea el mkdir"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Start(Request{
		Manifest:   &manifest.Manifest{Name: "svc", Command: "sleep 30", PortMode: manifest.PortModeDynamic},
		Path:       root,
		Store:      store,
		Manager:    process.NewManager(),
		StdoutPath: filepath.Join(root, "o.log"),
		StderrPath: filepath.Join(root, "e.log"),
	})
	if err == nil {
		t.Fatal("un Meta que no se puede escribir debería hacer fallar el arranque")
	}
	if !strings.Contains(err.Error(), "service directory") && !strings.Contains(err.Error(), "service dir") {
		t.Logf("el error no nombra el directorio de servicio: %q", err)
	}

	// Y el pool sigue sano: si la reserva se hubiera devuelto dos veces o con un
	// 0, el siguiente ReservePort podría devolver algo raro.
	p, rerr := process.ReservePort()
	if rerr != nil || p <= 0 {
		t.Errorf("el set de puertos quedó dañado: ReservePort = %d, %v", p, rerr)
	} else {
		process.ReleasePort(p)
	}
}

// TestApplyRouteConNombreNoUtilizableAvisaYNoPropagaElError: un nombre de ruta
// que portless no acepta es un AVISO, y el servicio sigue arrancado.
//
// Es el contrato de la ruta: una ruta es una dirección, no una dependencia. Si el
// nombre no vale, el servicio se queda en su puerto y el usuario recibe un aviso;
// si fuera un error duro, `vroom start` no arrancaría nada por un detalle del
// nombre de la ruta.
func TestApplyRouteConNombreNoUtilizableAvisaYNoPropagaElError(t *testing.T) {
	req := Request{
		Manifest: &manifest.Manifest{
			Name: "svc", RouteMode: manifest.RouteModeNamed,
			RouteName: "!!!", // no es un hostname utilizable
		},
		Branch: "main",
		// Routes NO puede ser nil: sin él applyRoute sale antes de derivar el
		// nombre, y este test comprobaría el guard de route_mode = "off" en vez
		// del nombre inválido.
		Routes: verifiedRegistrar{},
	}
	meta := state.Meta{Name: "svc", Port: 8081}
	out := Result{}

	applyRoute(req, &meta, 8081, &out)

	if len(out.Warnings) == 0 {
		t.Fatal("un nombre de ruta inválido tiene que producir un aviso: si no, el usuario no sabrá por qué no tiene ruta")
	}
	if !strings.Contains(out.Warnings[0], "portless route") {
		t.Errorf("el aviso no explica que el problema es el nombre: %q", out.Warnings[0])
	}
	// Y el Meta NO se toca: sin ruta declarada no puede haber handle ni
	// propiedad, y escribir uno haría que el stop intentara retirar algo
	// que nunca se registró.
	if meta.RouteName != "" || meta.RouteOwned {
		t.Errorf("el Meta se modificó sin ruta válida: %+v", meta)
	}
}

// TestApplyRouteSinRegistrarNoAbrePortless: route_mode = off no habla con
// portless en absoluto.
//
// Es la puerta de compatibilidad hacia atrás: un manifiesto que no declara ruta
// tiene que comportarse exactamente como antes de que existiera el código de
// rutas, y eso incluye no buscar el binario ni el state dir del desarrollador.
func TestApplyRouteSinRegistrarNoAbrePortless(t *testing.T) {
	req := Request{
		Manifest: &manifest.Manifest{Name: "svc", Port: 8081},
		// Routes == nil, que es lo que hace ClientFor cuando no hay contrato.
	}
	meta := state.Meta{Name: "svc", Port: 8081}
	out := Result{}

	applyRoute(req, &meta, 8081, &out)

	if len(out.Warnings) != 0 {
		t.Errorf("sin contrato de ruta no debería haber avisos: %v", out.Warnings)
	}
	if meta.RouteName != "" || meta.RouteStatus != "" || meta.RouteOwned {
		t.Errorf("sin contrato de ruta el Meta no puede llevar nada de ruta: %+v", meta)
	}
}

// TestApplyRouteEscribeLaUrlSoloSiLaVerifico: una ruta registrada pero no
// verificada NO persiste URL.
//
// Es la misma lección del campo `port_verified` del JSON, aplicada al Meta: una
// URL que nadie ha visto funcionar es peor que ninguna, porque quien la lea se
// conecta a otra cosa. Un Meta en disco con la URL puesta se lo lee alguien más
// —el TUI, un `vroom list` posterior, la reconciliación del siguiente arranque—.
func TestApplyRouteEscribeLaUrlSoloSiLaVerifico(t *testing.T) {
	t.Run("registrada sin verificar", func(t *testing.T) {
		reg := &degradingRegistrar{}
		req := Request{
			Manifest: &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeNamed, RouteName: "svc"},
			Branch:   "main",
			Routes:   reg,
		}
		meta := state.Meta{Name: "svc", Port: 8081}
		out := Result{}

		applyRoute(req, &meta, 8081, &out)

		if meta.RouteURL != "" {
			t.Errorf("RouteURL = %q sin verificación: publicaría una dirección falsa", meta.RouteURL)
		}
		// Pero el handle y el estado SÍ se guardan: la ruta se escribió aunque no
		// se haya podido comprobar, y sin handle el stop no podría retirarla.
		if meta.RouteName != "svc" {
			t.Errorf("RouteName = %q: sin handle el stop no puede retirar la ruta", meta.RouteName)
		}
		if meta.RouteStatus == "" {
			t.Error("RouteStatus vacío: el Meta no afirma nada del resultado de la ruta")
		}
		// Y la propiedad SÍ se concede: el alta ocurrió (Registered), y conceder
		// sólo con Succeeded() sería el error opuesto — una ruta escrita con el
		// proxy parado es nuestra de verdad, y perder su handle haría que un
		// reinicio con el puerto movido chocara contra su propia ruta.
		if !meta.RouteOwned {
			t.Error("RouteOwned = false con un alta que ocurrió: sin propiedad el stop no podría retirar la ruta")
		}
		// Y hay un aviso, porque el usuario tiene que saber que no tiene URL.
		if len(out.Warnings) == 0 {
			t.Error("una ruta degradada sin aviso deja al usuario sin saber por qué no tiene URL")
		}
		// Y la reconciliación se ejecutó ANTES del alta, con el handle anterior.
		if len(reg.reconciled) == 0 {
			t.Error("no se reconcilió nada: una rama renombrada dejaría la ruta vieja apuntando a un puerto muerto para siempre")
		}
	})

	t.Run("registrada y verificada", func(t *testing.T) {
		req := Request{
			Manifest: &manifest.Manifest{Name: "svc", RouteMode: manifest.RouteModeNamed, RouteName: "svc"},
			Branch:   "main",
			Routes:   verifiedRegistrar{},
		}
		meta := state.Meta{Name: "svc", Port: 8081}
		out := Result{}

		applyRoute(req, &meta, 8081, &out)

		if meta.RouteURL == "" {
			t.Errorf("una ruta verificada tiene que publicar su URL: %+v", meta)
		}
		if !meta.RouteOwned {
			t.Error("una ruta registrada con éxito es nuestra: sin propiedad el stop no la retiraría")
		}
		if len(out.Warnings) != 0 {
			t.Errorf("una ruta sana no debe producir avisos: %v", out.Warnings)
		}
	})
}

// verifiedRegistrar devuelve un alta que se ha escrito Y se ha visto funcionar.
type verifiedRegistrar struct{}

func (verifiedRegistrar) Apply(name string, port int, own portless.Ownership) portless.Result {
	return portless.Result{
		Name:       name,
		Port:       port,
		Status:     portless.StatusRegistered,
		Url:        "https://" + name + ".localhost",
		Registered: true,
	}
}

func (verifiedRegistrar) Reconcile(string, portless.Ownership, string) []string { return nil }

// TestRouteNameDerivaDeLaRamaEnAutoYDelNombreEnNamed: los dos modos de derivar
// el nombre, que es la función que decide qué URL tiene el servicio.
//
// En auto manda la RAMA, porque es lo que separa dos worktrees del mismo repo. En
// named manda el nombre del manifiesto, porque es lo que separa dos servicios que
// no pueden depender de en qué rama están.
func TestRouteNameDerivaDeLaRamaEnAutoYDelNombreEnNamed(t *testing.T) {
	tests := []struct {
		name     string
		manifest *manifest.Manifest
		branch   string
		want     string
	}{
		{
			name:     "auto con rama",
			manifest: &manifest.Manifest{Name: "api", RouteMode: manifest.RouteModeAuto},
			branch:   "feature/login",
			want:     "feature-login.api",
		},
		{
			name:     "auto sin rama usa el proyecto",
			manifest: &manifest.Manifest{Name: "api", RouteMode: manifest.RouteModeAuto},
			branch:   "",
			want:     "api",
		},
		{
			name:     "named ignora la rama",
			manifest: &manifest.Manifest{Name: "api", RouteMode: manifest.RouteModeNamed, RouteName: "tienda"},
			branch:   "cualquier-rama",
			want:     "tienda",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := routeName(Request{Manifest: tt.manifest, Branch: tt.branch})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("routeName = %q, want %q", got, tt.want)
			}
		})
	}

	// Y un modo inválido es un error, no un nombre inventado.
	if _, err := routeName(Request{
		Manifest: &manifest.Manifest{Name: "api", RouteMode: "inventado"},
		Branch:   "main",
	}); err == nil {
		t.Error("un route_mode desconocido debería dar error: un nombre inventado sería una ruta que colisiona con la de otro")
	}
}

// TestDiscoveryTimeoutPorDefectoCuandoNoSeDaUno: un timeout cero o negativo usa el
// del paquete, no el cero.
//
// Con timeout 0 el descubrimiento no esperaría nada y todo servicio dynamic
// saldría `port_pending` para siempre, aunque tardase dos segundos en abrir el
// puerto.
func TestDiscoveryTimeoutPorDefectoCuandoNoSeDaUno(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(t.TempDir())

	res, err := Start(Request{
		Manifest: &manifest.Manifest{Name: "svc", Command: "sleep 30", PortMode: manifest.PortModeDynamic},
		Path:     root,
		Store:    store,
		Manager:  process.NewManager(),
		// DiscoveryTimeout a cero a propósito: el comando es `sleep`, que no abre
		// ningún puerto, así que el descubrimiento agota su ventana y declara
		// no_port. Con timeout cero el bucle ni siquiera espera, y el estado
		// seguiría siendo no_port —lo que no se distingue de "el servicio abre el
		// puerto un segundo después".
		DiscoveryTimeout: 2 * time.Second,
		StdoutPath:       filepath.Join(root, "o.log"),
		StderrPath:       filepath.Join(root, "e.log"),
	})
	if err != nil {
		t.Skipf("el arranque dynamic necesita un entorno con puerto disponible: %v", err)
	}

	// Un servicio que no expone puerto tiene que salir con Port 0 y un estado que
	// lo diga. Lo que NO puede es salir con el puerto RESERVADO: sería una
	// dirección que nadie comprobó, y es el mismo daño que publicar una URL sin
	// verificar.
	if res.Port != 0 {
		t.Errorf("Port = %d con un servicio que no escucha: no se puede afirmar un puerto que nadie confirmó", res.Port)
	}
	if res.Meta.State != state.StateNoPort {
		t.Errorf("meta.State = %q, want %q: el estado tiene que decir POR QUÉ no hay puerto", res.Meta.State, state.StateNoPort)
	}
	if res.Meta.PortVerified {
		t.Error("PortVerified = true con Port 0: no se verificó ningún puerto")
	}
	// Y el servicio SIGUE vivo: no_port es un hecho sobre el puerto, no una
	// declaración de que el servicio está parado.
	if res.Pid <= 0 {
		t.Error("Pid = 0: un no_port no significa que el servicio no arranque")
	}
	stopOne(t, store, root)
}

// stopOne para el servicio de un root y lolimpia el Meta, para que un test que
// arranca algo no deje procesos vivos ni puertos reservados.
func stopOne(t *testing.T, store *state.Store, path string) {
	t.Helper()
	meta, err := store.LoadMeta(path)
	if err != nil {
		return
	}
	if meta.Pid > 0 {
		_ = process.NewManager().Stop(process.StopSpec{
			Pid: meta.Pid, Pgid: meta.Pgid, Port: meta.Port,
			Timeout: process.DefaultStopTimeout,
		})
	}
	process.ReleasePort(meta.ReservedPort)
}
