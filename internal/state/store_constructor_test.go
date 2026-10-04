package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// El store estaba al 68.9% y el hueco tenía una forma concreta: TODOS los
// tests usaban NewStoreAt, que no crea nada, así que NewStore y DefaultBaseDir
// (la resolución real del directorio) no se ejercitaban, y con ellas las
// mensagens de error que son las que de verdad importan.
//
// Un store que no puede decir "no puedo crear el directorio de estado" deja el
// programa escribiendo el estado en un sitio que el usuario no controla, o
// fallando sin decir por qué.
//
// Estos tests no usan NewStoreAt: construyen el store por la vía real, con
// XDG_STATE_HOME y HOME redirigidos a temporales, y provocan los fallos reales
// de filesystem (chmod, padre-fichero, destino-directorio) en vez de stubear
// os.
// ---------------------------------------------------------------------------

// isolateHome redirige XDG_STATE_HOME y HOME a un temporal, para que ningun test
// toque el estado real del desarrollador. Se usa t.Setenv, que restaura solo.
//
// Las dos variables, no solo la primera: DefaultBaseDir consulta XDG_STATE_HOME
// y, si no está, cae en el home. Poner solo XDG_STATE_HOME deja el fallback
// apuntando al home real en cualquier test que la vacíe.
func isolateHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)
	t.Setenv("HOME", tmp)
	return tmp
}

// TestMetaWireFormatEstable: meta.json es un contrato entre versiones de vroom —
// un meta escrito por una versión anterior tiene que seguir leyéndose. Este test
// fija el JSON exacto, no solo el round-trip que ya hacía TestMetaRoundtrip,
// porque hay dos cosas que el round-trip NO detecta:
//
//   - el omitempty de los campos Route*. Si alguien lo quita, un Meta vacío
//     pasa a escribir "route_name": "" en disco: el round-trip sigue dando igual
//     y nadie se entera, pero cambia el formato y un meta viejo se distingue de
//     uno nuevo por el ruido.
//   - las etiquetas json. Un rename de Name a name sigue compilando y el
//     round-trip pasa; solo el JSON delata que el fichero persisted cambia de
//     forma.
//
// Las dos rutas se comprueban contra disco, no contra la struct.
func TestMetaWireFormatEstable(t *testing.T) {
	t.Run("meta vacio solo escribe los campos sin omitempty", func(t *testing.T) {
		s := NewStoreAt(t.TempDir())
		const path = "/home/user/dev/formato"
		if err := s.SaveMeta(path, Meta{Name: "n", ProjectPath: path}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(s.ServiceDir(path), "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		got := string(raw)
		for _, unwanted := range []string{"reserved_port", "route_name", "route_port", "route_status", "route_owned", "route_reason", "route_url"} {
			if strings.Contains(got, unwanted) {
				t.Errorf("un campo omitempty aparece en el meta vacio (%s):\n%s", unwanted, got)
			}
		}
		for _, want := range []string{`"name": "n"`, `"project_path"`, `"port": 0`, `"process_pattern": ""`, `"pid": 0`, `"state": ""`, `"port_verified": false`} {
			if !strings.Contains(got, want) {
				t.Errorf("falta %s en el meta persistido:\n%s", want, got)
			}
		}
	})

	t.Run("los campos de ruta aparecen cuando se rellenan", func(t *testing.T) {
		s := NewStoreAt(t.TempDir())
		const path = "/home/user/dev/con-ruta"
		m := Meta{
			Name: "n", ProjectPath: path,
			ReservedPort: 1, RouteName: "tienda", RoutePort: 2,
			RouteStatus: "registered", RouteOwned: true,
			RouteReason: "degradado", RouteURL: "https://tienda.local",
		}
		if err := s.SaveMeta(path, m); err != nil {
			t.Fatal(err)
		}
		got, err := s.LoadMeta(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != m {
			t.Errorf("los campos de ruta no sobreviven al round-trip:\n got %+v\nwant %+v", got, m)
		}
	})

	t.Run("cualquier valor de Meta es serializable", func(t *testing.T) {
		// Justifica por que las dos ramas de error de json.MarshalIndent de
		// SaveMeta y SaveCollapsed NO se pueden cubrir con los tipos actuales:
		// Meta solo tiene string, int, int64 y bool, y ninguno de ellos puede
		// fallar al serializar (probado tambien con un Meta con los 18 campos
		// puestos). El unico valor que json rechaza es un float NaN o Inf, y no
		// hay float en Meta.
		//
		// Las ramas se conservan a proposito: son la red que atraparia un NaN si
		// alguien añadiera un campo float64 a Meta. No son codigo muerto como el
		// `len(result) == 0` de bordered, que protegia una condicion de runtime
		// imposible; estas protegen una propiedad del TIPO, que es algo que un
		// cambio futuro puede romper.
		s := NewStoreAt(t.TempDir())
		full := Meta{
			Name: "n", ProjectPath: "/p", Port: 1, ReservedPort: 2,
			ProcessPattern: "pp", Command: "c", Pid: 3, Pgid: 4,
			CreationTimeMs: 5, StartedAt: "s", State: StateRunning,
			PortVerified: true, RouteName: "r", RoutePort: 6,
			RouteStatus: "registered", RouteOwned: true,
			RouteReason: "why", RouteURL: "http://x",
		}
		if err := s.SaveMeta(full.ProjectPath, full); err != nil {
			t.Fatalf("un Meta con todos los campos puestos no deberia fallar al guardar: %v", err)
		}
		// Y collapsed: map[string]bool es siempre serializable, incluidos nil
		// y el mapa vacio, que es lo que se guarda antes de que el usuario
		// colapse nada.
		for _, groups := range []map[string]bool{nil, {}, {"a": true}, {"a": true, "b/c": false}} {
			if err := s.SaveCollapsed(groups); err != nil {
				t.Errorf("SaveCollapsed(%v) fallo: %v", groups, err)
			}
		}
	})
}

// TestNewStoreCreaElLayoutReal: NewStore es lo que llama main() antes de
// escanear. Tiene que crear services/ con 0755 y devolver un store cuyo Base
// apunte a la ruta resuelta, no a otra.
func TestNewStoreCreaElLayoutReal(t *testing.T) {
	tmp := isolateHome(t)

	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	want := filepath.Join(tmp, "vroom")
	if s.Base() != want {
		t.Errorf("Base() = %q, want %q", s.Base(), want)
	}
	info, err := os.Stat(filepath.Join(want, "services"))
	if err != nil {
		t.Fatalf("services/ no creado: %v", err)
	}
	if !info.IsDir() {
		t.Error("services/ no es un directorio")
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("permisos de services/ = %v, want 0755", perm)
	}
}

// TestNewStoreEsIdempotente: llamarlo dos veces no debe fallar. main() lo llama
// en cada arranque, y un store ya existente es el caso normal, no el exceptional.
func TestNewStoreEsIdempotente(t *testing.T) {
	isolateHome(t)

	first, err := NewStore()
	if err != nil {
		t.Fatal(err)
	}
	// Un servicio real escrito por el store anterior debe sobrevivir.
	const path = "/home/user/dev/persistente"
	if err := first.SaveMeta(path, Meta{Name: "p", State: StateRunning}); err != nil {
		t.Fatal(err)
	}

	second, err := NewStore()
	if err != nil {
		t.Fatalf("el segundo NewStore fallo: %v", err)
	}
	got, err := second.LoadMeta(path)
	if err != nil {
		t.Fatalf("el meta escrito por el store anterior se perdio: %v", err)
	}
	if got.Name != "p" {
		t.Errorf("Name = %q, want p", got.Name)
	}
}

// TestNewStoreFallaConBaseDirNoEscribible: el error que el usuario ve cuando su
// HOME está en un sitio que no puede escribir. Tiene que ser explícito sobre el
// directorio, no un error de permisos pelado del que no se sabe qué hacer.
func TestNewStoreFallaConBaseDirNoEscribible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora los permisos del directorio: el test no puede provocar el fallo")
	}
	tmp := isolateHome(t)

	// Un fichero donde debería ir vroom/: MkdirAll de services/ debajo de un
	// fichero regular falla con ENOTDIR.
	if err := os.WriteFile(filepath.Join(tmp, "vroom"), []byte("bloqueado"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := NewStore()
	if err == nil {
		t.Fatal("NewStore deberia fallar con vroom/ ocupado por un fichero")
	}
	msg := err.Error()
	if !strings.Contains(msg, tmp) {
		t.Errorf("el error no nombra el directorio (%q): el usuario no puede saber que revisar", msg)
	}
	if !strings.Contains(msg, "permissions") {
		t.Errorf("el error %q no sugiere permisos", msg)
	}
}

// TestDefaultBaseDirCaeAlHomeSinXDG: sin XDG_STATE_HOME el store va a
// ~/.local/state/vroom. Es el camino por defecto en la mayoría de máquinas, y el
// test existente solo cubría la rama de XDG.
func TestDefaultBaseDirCaeAlHomeSinXDG(t *testing.T) {
	tmp := isolateHome(t)
	t.Setenv("XDG_STATE_HOME", "")

	dir, err := DefaultBaseDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, ".local", "state", "vroom")
	if dir != want {
		t.Errorf("DefaultBaseDir = %q, want %q", dir, want)
	}
}

// TestDefaultBaseDirSinHomeNiXDG: sin ninguna de las dos, no hay dónde persistir
// y hay que decirlo. Es el único camino que produce el error de resolución, y
// sin él el programa persistiría en un sitio arbitrario.
func TestDefaultBaseDirSinHomeNiXDG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("la resolucion del home en windows no depende de HOME")
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")

	if _, err := DefaultBaseDir(); err == nil {
		t.Error("DefaultBaseDir deberia fallar sin HOME ni XDG_STATE_HOME")
	}
}

// TestBaseDevuelveLaRaizDelStore: el getter más tonto del paquete y estaba al
// 0%. Lo que importa es que devuelva la raíz y no el directorio de un servicio:
// un Base() equivocado hace que todos los servicios compartan una carpeta.
func TestBaseDevuelveLaRaizDelStore(t *testing.T) {
	base := t.TempDir()
	if got := NewStoreAt(base).Base(); got != base {
		t.Errorf("Base() = %q, want %q", got, base)
	}
}

// TestNewStorePropagaElErrorDeResolucion: NewStore tiene que devolver el error de
// DefaultBaseDir tal cual, sin envolver ni sustituir. Si lo sustituyera por un
// "could not create state directory", el mensaje culparía a los permisos cuando
// el problema real es que no hay home: dos causas y dos acciones distintas.
func TestNewStorePropagaElErrorDeResolucion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("la resolucion del home en windows no depende de HOME")
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")

	s, err := NewStore()
	if err == nil {
		t.Fatal("NewStore deberia fallar sin HOME ni XDG_STATE_HOME")
	}
	if s != nil {
		t.Errorf("NewStore devolvio un store no nil junto al error: %+v", s)
	}
	if !strings.Contains(err.Error(), "home") {
		t.Errorf("el error %q no menciona la resolucion del home, y esa es la causa", err)
	}
}

// TestEnsureServiceDirFallaConPadreQueNoEsDirectorio: el error de creación del
// directorio del servicio, que ocurre cuando services/ fue sustituido por un
// fichero. Sin este test la rama de error está sin ejercitar y un mensaje mal
// escrito pasaría inadvertido.
func TestEnsureServiceDirFallaConPadreQueNoEsDirectorio(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora los permisos: el test no puede provocar el fallo")
	}
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "services"), []byte("soy un fichero"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStoreAt(base)

	dir, err := s.EnsureServiceDir("/home/user/dev/x")
	if err == nil {
		t.Fatalf("EnsureServiceDir deberia fallar, devolvio %q", dir)
	}
	if dir != "" {
		t.Errorf("EnsureServiceDir devolvio %q junto al error, deberia devolver cadena vacia", dir)
	}
	if !strings.Contains(err.Error(), "check permissions") {
		t.Errorf("el error %q no sugiere permisos", err)
	}
}

// TestSaveMetaFallaEnDirectorioNoEscribible: la escritura de meta.json es la
// operacion mas importante del store — es de donde sale el estado que la TUI
// reengancha al arrancar — y su fallo nunca se ha probado. Sin esto, un disco
// lleno o un HOME de solo-lectura produciría un arranque que cree que no hay
// ningún servicio, en vez de un error.
func TestSaveMetaFallaEnDirectorioNoEscribible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora los permisos: el test no puede provocar el fallo")
	}
	s := NewStoreAt(t.TempDir())
	const path = "/home/user/dev/solo-lectura"
	dir, err := s.EnsureServiceDir(path)
	if err != nil {
		t.Fatal(err)
	}
	// r-x: se puede listar y entrar, no crear ficheros.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := s.SaveMeta(path, Meta{Name: "n", State: StateRunning}); err == nil {
		t.Fatal("SaveMeta deberia fallar en un directorio de solo lectura")
	}
	if _, err := s.LoadMeta(path); err == nil {
		t.Error("SaveMeta fallo pero dejo un meta.json utilizable: el estado queda a medias")
	}
}

// TestSaveMetaNoDejaTmpResidualCuandoFallaElRename: la escritura es atómica
// (tmp + rename), así que un rename fallido tiene que dejar el tmp detrás. No es
// un bug —el próximo SaveMeta lo sobrescribe— pero conviene que sea visible y no
// un misterio cuando alguien lista el directorio del servicio.
func TestSaveMetaFallaEnRenameYDejaTmp(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	const path = "/home/user/dev/rename-fallido"
	dir, err := s.EnsureServiceDir(path)
	if err != nil {
		t.Fatal(err)
	}
	// meta.json como DIRECTORIO: rename(tmp, dir) falla con EISDIR.
	if err := os.MkdirAll(filepath.Join(dir, "meta.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := s.SaveMeta(path, Meta{Name: "n"}); err == nil {
		t.Fatal("SaveMeta deberia fallar al renombrar sobre un directorio")
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json.tmp")); err != nil {
		t.Errorf("el tmp no quedo tras el fallo del rename: %v", err)
	}
}

// TestRegisterPidFallaEnDirectorioNoEscribible: registrar el PID es lo que
// permite parar un servicio tras reiniciar la TUI. Si el write falla en silencio,
// el servicio queda sin dueño: nadie lo puede detener y ocupa el puerto para
// siempre.
func TestRegisterPidFallaEnDirectorioNoEscribible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora los permisos: el test no puede provocar el fallo")
	}
	s := NewStoreAt(t.TempDir())
	const path = "/home/user/dev/pid-ro"
	dir, err := s.EnsureServiceDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := s.RegisterPid(path, 4242, 4242); err == nil {
		t.Fatal("RegisterPid deberia fallar en un directorio de solo lectura")
	}
	// El pid tiene que haber fallado ANTES de escribir pgid: un pid a medias
	// sería peor que ninguno, porque el stop leería un pgid de otro servicio.
	if _, err := os.Stat(s.PgidFile(path)); err == nil {
		t.Error("se escribio pgid aunque pid fallo: el servicio queda con un grupo de proceso de otro")
	}
}

// TestClearPidFallaConPidQueNoEsBorrable: ClearPid se llama al parar, y su
// rama de error decide si el stop reporta fallo. Si el pid no se puede borrar y
// el error se traga, el siguiente arranque cree que el servicio sigue vivo.
func TestClearPidFallaConPidQueNoEsBorrable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora los permisos: el test no puede provocar el fallo")
	}
	s := NewStoreAt(t.TempDir())
	const path = "/home/user/dev/clear-ro"
	dir, err := s.EnsureServiceDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterPid(path, 4242, 4242); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := s.ClearPid(path); err == nil {
		t.Fatal("ClearPid deberia fallar sin poder borrar el pid")
	}
}

// TestSaveCollapsedFallaEnRename: el estado de plegado es UI, no del servicio,
// así que su fallo no es crítico — pero no debe ser silencioso, porque el
// síntoma (grupos que no se recuerdan) parece un fallo de la TUI y acabaría
// persiguiendo al código equivocado.
func TestSaveCollapsedFallaEnRename(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	if err := os.MkdirAll(s.CollapsedFile(), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := s.SaveCollapsed(map[string]bool{"a": true}); err == nil {
		t.Fatal("SaveCollapsed deberia fallar al renombrar sobre un directorio")
	}
}

// TestSaveCollapsedFallaEnDirectorioNoEscribible: el otro fallo del estado de UI.
func TestSaveCollapsedFallaEnDirectorioNoEscribible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora los permisos: el test no puede provocar el fallo")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o755) })

	if err := NewStoreAt(base).SaveCollapsed(map[string]bool{"a": true}); err == nil {
		t.Fatal("SaveCollapsed deberia fallar en un directorio de solo lectura")
	}
}

// TestRegisterPidFallaAntesDeEscribir: si el directorio del servicio no se puede
// crear, RegisterPid tiene que fallar antes de tocar nada. Es un orden de
// operaciones, no un valor de retorno: importa que el error de mkdir se propague
// en vez de que un write posterior lo enmascare.
func TestRegisterPidFallaAntesDeEscribir(t *testing.T) {
	base := t.TempDir()
	// services/ es un fichero: EnsureServiceDir no puede crear nada debajo.
	if err := os.WriteFile(filepath.Join(base, "services"), []byte("bloqueado"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStoreAt(base)
	const path = "/home/user/dev/sin-dir"

	if err := s.RegisterPid(path, 4242, 4242); err == nil {
		t.Fatal("RegisterPid deberia fallar si el directorio del servicio no existe")
	}
	if _, err := os.Stat(s.PidFile(path)); err == nil {
		t.Error("se escribio el pid pese a no poder crear su directorio")
	}
}

// TestRegisterPidDejaPidSiPgidFalla: los dos ficheros se escriben en orden y sin
// atomicidad entre ellos, así que un fallo del segundo deja el primero escrito.
// Se fija el comportamiento en vez de documentarlo: es lo que hay, y quien lea el pid
// tiene que saber que puede existir sin pgid.
//
// Importa porque los dos llamadores de RegisterPid (startsvc, dos sitios) hacen
// `_ =` con el error, y ClearPid borra los dos ficheros: un pgid ausente no
// rompe el stop, que ya tiene el pid, pero un pgid de otro servicio sí lo haría
// — por eso el test anterior asegura que un fallo del mkdir no escribe ninguno.
func TestRegisterPidDejaPidSiPgidFalla(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	const path = "/home/user/dev/pgid-falla"
	dir, err := s.EnsureServiceDir(path)
	if err != nil {
		t.Fatal(err)
	}
	// pgid como DIRECTORIO: WriteFile falla con EISDIR, pero pid ya se escribio.
	if err := os.MkdirAll(filepath.Join(dir, "pgid"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := s.RegisterPid(path, 111, 222); err == nil {
		t.Fatal("RegisterPid deberia fallar al escribir pgid")
	}
	data, rerr := os.ReadFile(s.PidFile(path))
	if rerr != nil {
		t.Fatalf("el pid no quedo escrito pese al fallo de pgid: %v", rerr)
	}
	if strings.TrimSpace(string(data)) != "111" {
		t.Errorf("pid = %q, want 111", data)
	}
}

// TestSaveMetaFallaAntesDeEscribirCuandoNoHayDirectorio: el mismo orden en
// SaveMeta. El mkdir va primero, y su error tiene que salir como error de mkdir
// y no como un fallo de escritura posterior, que mentiria sobre la causa.
func TestSaveMetaFallaAntesDeEscribirCuandoNoHayDirectorio(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "services"), []byte("bloqueado"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStoreAt(base)
	const path = "/home/user/dev/sin-dir"

	err := s.SaveMeta(path, Meta{Name: "n"})
	if err == nil {
		t.Fatal("SaveMeta deberia fallar si el directorio del servicio no existe")
	}
	if !strings.Contains(err.Error(), "could not create service directory") {
		t.Errorf("el error reporta la causa equivocada: %q", err)
	}
}

// TestRutasDeServicioCoherentesConElHash: las cinco rutas derivadas (log,
// pid, pgid, service dir) tienen que caer SIEMPRE dentro del directorio del
// servicio. Es la propiedad que hace que dos proyectos homónimos no se pisen, y
// dependía solo de que PathKey no colisionara, lo que ya se prueba en otro sitio.
func TestRutasDeServicioCoherentesConElHash(t *testing.T) {
	base := t.TempDir()
	s := NewStoreAt(base)
	const path = "/home/user/dev/coherente"

	dir := s.ServiceDir(path)
	for name, got := range map[string]string{
		"stdout": s.StdoutLog(path),
		"stderr": s.StderrLog(path),
		"pid":    s.PidFile(path),
		"pgid":   s.PgidFile(path),
	} {
		if filepath.Dir(got) != dir {
			t.Errorf("%s: dirname = %q, want %q", name, filepath.Dir(got), dir)
		}
	}
	if !strings.HasPrefix(dir, filepath.Join(base, "services")) {
		t.Errorf("ServiceDir fuera de services/: %q", dir)
	}
}
