package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Both vars, not just XDG_STATE_HOME: DefaultBaseDir falls back to HOME, so a test that clears one can still touch the real home.
func isolateHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)
	t.Setenv("HOME", tmp)
	return tmp
}

// meta.json is a cross-version contract: a round-trip test cannot catch a dropped omitempty or a renamed json tag, only the bytes on disk can.
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
		// Meta has no float field, so json.Marshal cannot fail; the error branches stay as a net for a future float64.
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
		for _, groups := range []map[string]bool{nil, {}, {"a": true}, {"a": true, "b/c": false}} {
			if err := s.SaveCollapsed(groups); err != nil {
				t.Errorf("SaveCollapsed(%v) fallo: %v", groups, err)
			}
		}
	})
}

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

// main() calls this on every boot, so an already existing store is the normal case rather than the exceptional one.
func TestNewStoreEsIdempotente(t *testing.T) {
	isolateHome(t)

	first, err := NewStore()
	if err != nil {
		t.Fatal(err)
	}
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

// The error must name the directory and suggest permissions, otherwise the user has nothing to act on.
func TestNewStoreFallaConBaseDirNoEscribible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora los permisos del directorio: el test no puede provocar el fallo")
	}
	tmp := isolateHome(t)

	// A regular file where vroom/ belongs makes MkdirAll fail with ENOTDIR.
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

// Base() must return the root, not a service dir, or every service ends up sharing one folder.
func TestBaseDevuelveLaRaizDelStore(t *testing.T) {
	base := t.TempDir()
	if got := NewStoreAt(base).Base(); got != base {
		t.Errorf("Base() = %q, want %q", got, base)
	}
}

// Propagate DefaultBaseDir's error verbatim: a generic "could not create state directory" blames permissions when the real cause is a missing home.
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

// A silent SaveMeta failure makes the TUI boot believing no service exists, instead of surfacing the error.
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
	// 0o500: enterable but not writable, which is what a read-only HOME looks like.
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

// A failed rename leaves meta.json.tmp behind: the next SaveMeta overwrites it, so it is leftover litter rather than corruption.
func TestSaveMetaFallaEnRenameYDejaTmp(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	const path = "/home/user/dev/rename-fallido"
	dir, err := s.EnsureServiceDir(path)
	if err != nil {
		t.Fatal(err)
	}
	// meta.json as a directory makes rename(tmp, dir) fail with EISDIR.
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

// A silent RegisterPid failure leaves the service ownerless forever: stop cannot reach it and it holds the port for good.
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
	// The pid write must fail before pgid is touched, or stop reads another service's process group.
	if _, err := os.Stat(s.PgidFile(path)); err == nil {
		t.Error("se escribio pgid aunque pid fallo: el servicio queda con un grupo de proceso de otro")
	}
}

// Swallowing this error makes the next boot believe the service is still alive.
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

// Collapsed state is UI-only so the failure is not critical, but it must not be silent: forgotten groups look like a TUI bug.
func TestSaveCollapsedFallaEnRename(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	if err := os.MkdirAll(s.CollapsedFile(), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := s.SaveCollapsed(map[string]bool{"a": true}); err == nil {
		t.Fatal("SaveCollapsed deberia fallar al renombrar sobre un directorio")
	}
}

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

// The mkdir error must propagate instead of being masked by a later write failure.
func TestRegisterPidFallaAntesDeEscribir(t *testing.T) {
	base := t.TempDir()
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

// pid and pgid are written in order with no atomicity between them, so a pgid failure leaves pid written and the callers discard the error.
func TestRegisterPidDejaPidSiPgidFalla(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	const path = "/home/user/dev/pgid-falla"
	dir, err := s.EnsureServiceDir(path)
	if err != nil {
		t.Fatal(err)
	}
	// pgid as a directory makes WriteFile fail with EISDIR, after pid is already written.
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

// Every derived path must land inside the service dir, which is what keeps two same-named projects from colliding.
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
