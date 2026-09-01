package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathKeyDeterministicAndDistinct(t *testing.T) {
	a := "/home/user/dev/work/api"
	b := "/home/user/dev/personal/api"

	ka1, ka2 := PathKey(a), PathKey(a)
	if ka1 != ka2 {
		t.Fatalf("hash no determinista: %q vs %q", ka1, ka2)
	}
	if ka1 == PathKey(b) {
		t.Fatal("paths distintos produjeron la misma clave (S4.1)")
	}
	if len(ka1) != 8 {
		t.Fatalf("clave debe tener 8 hex chars, got %d (%q)", len(ka1), ka1)
	}
}

func TestDefaultBaseDirRespectsXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	dir, err := DefaultBaseDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join("/tmp/xdg-state", "svc") {
		t.Errorf("dir = %q, want /tmp/xdg-state/svc", dir)
	}
}

func TestServiceDirLayout(t *testing.T) {
	base := t.TempDir()
	s := NewStoreAt(base)

	dir, err := s.EnsureServiceDir("/home/user/dev/vsocial")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(base, "services", PathKey("/home/user/dev/vsocial")) {
		t.Errorf("service dir inesperado: %s", dir)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("directorio no creado: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("permisos = %v, want 0755", info.Mode().Perm())
	}
}

func TestMetaRoundtrip(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	want := Meta{
		Name:           "vsocial-api",
		ProjectPath:    "/home/user/dev/vsocial",
		Group:          "vsocial",
		Port:           8080,
		ProcessPattern: "vsocial-api",
		Command:        "go run main.go",
		Pid:            1234,
		Pgid:           1234,
		CreationTimeMs: 1725200000000,
		StartedAt:      "2026-09-01T12:00:00Z",
		State:          StateRunning,
	}
	if err := s.SaveMeta(want.ProjectPath, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadMeta(want.ProjectPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("meta desigual:\n got %+v\nwant %+v", got, want)
	}
}

// S5.1
func TestLoadMetaCorrupt(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	path := "/home/user/dev/broken"
	if err := os.MkdirAll(s.ServiceDir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.ServiceDir(path), "meta.json"), []byte("{invalid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.LoadMeta(path)
	if err == nil {
		t.Fatal("esperaba error por meta.json corrupto, sin crashear")
	}
	if !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("error %q no indica corrupción", err.Error())
	}
}

func TestLoadMetaMissing(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	if _, err := s.LoadMeta("/home/user/dev/none"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("esperaba os.ErrNotExist, got %v", err)
	}
}

func TestRegisterAndClearPid(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	path := "/home/user/dev/svc-x"

	if err := s.RegisterPid(path, 4242, 4242); err != nil {
		t.Fatal(err)
	}
	pidData, err := os.ReadFile(s.PidFile(path))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(pidData)) != "4242" {
		t.Errorf("pid file = %q, want 4242", pidData)
	}
	pgidData, err := os.ReadFile(s.PgidFile(path))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(pgidData)) != "4242" {
		t.Errorf("pgid file = %q, want 4242", pgidData)
	}

	if err := s.ClearPid(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.PidFile(path)); !os.IsNotExist(err) {
		t.Errorf("pid file debería eliminarse, got %v", err)
	}
	// ClearPid idempotente
	if err := s.ClearPid(path); err != nil {
		t.Errorf("ClearPid no idempotente: %v", err)
	}
}

func TestLogPaths(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	path := "/home/user/dev/logs-proj"
	if s.StdoutLog(path) != filepath.Join(s.ServiceDir(path), "stdout.log") {
		t.Error("ruta stdout.log incorrecta")
	}
	if s.StderrLog(path) != filepath.Join(s.ServiceDir(path), "stderr.log") {
		t.Error("ruta stderr.log incorrecta")
	}
}
