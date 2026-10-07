package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeJSONAtomic panics on a type json cannot serialize, and the message must name both the type and the json error because it is read mid-boot.
func TestWriteJSONAtomicRevientaConUnTipoNoSerializable(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("writeJSONAtomic with a float64 NaN did not panic: a type that json cannot " +
				"serialize is a programming error, and it must be seen as such")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("the panic is of type %T, want string with the reason", r)
		}
		if !strings.Contains(msg, "float64") {
			t.Errorf("the panic says %q, want it to name the type that failed: without the type, whoever "+
				"reads it does not know which field to add or remove", msg)
		}
		if !strings.Contains(msg, "NaN") {
			t.Errorf("the panic says %q, want it to include the json error: \"type\" without \"why\" "+
				"leaves the job half done", msg)
		}
	}()

	// MEASURED: NaN is the only value encoding/json rejects here, and the one a stray numeric field would smuggle in.
	_ = writeJSONAtomic(filepath.Join(t.TempDir(), "never.json"), map[string]float64{"x": nan()})
}

func nan() float64 {
	var zero float64
	return zero / zero
}

// tmp + rename leaves the destination untouched until the rename, so an interrupted write keeps the previous meta instead of truncated JSON.
func TestSaveMetaYSaveCollapsedCompartenElEscribidoAtomico(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	const proyecto = "/srv/api"

	if err := s.SaveMeta(proyecto, Meta{Name: "api", Pid: 42, State: StateRunning}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCollapsed(map[string]bool{"shop": true}); err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{
		filepath.Join(s.ServiceDir(proyecto), "meta.json"),
		s.CollapsedFile(),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s does not exist: %v", f, err)
		}
		tmp := f + ".tmp"
		if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Errorf("the temp file %s was left behind (err=%v): rename does not clean it up", tmp, err)
		}
	}

	meta, err := s.LoadMeta(proyecto)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 42 || meta.Name != "api" {
		t.Errorf("meta = %+v, want Name=api Pid=42", meta)
	}
	if !s.LoadCollapsed()["shop"] {
		t.Error("collapsed.json was not saved: the collapsed group expands on the next boot")
	}
}

// A full disk or an unwritable dir is an environment condition someone can retry, so it must return an error rather than panic.
func TestSaveMetaPropagaElFalloDeEscribirElTemporal(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	const proyecto = "/srv/api"

	dir, err := s.EnsureServiceDir(proyecto)
	if err != nil {
		t.Fatal(err)
	}
	// A non-empty meta.json directory lets the tmp write succeed and fails the rename with EISDIR.
	if err := os.MkdirAll(filepath.Join(dir, "meta.json", "lock"), 0o755); err != nil {
		t.Fatal(err)
	}

	err = s.SaveMeta(proyecto, Meta{Name: "api"})
	if err == nil {
		t.Fatal("SaveMeta with a destination that is not a file must fail")
	}
	if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want it to name the file: the message is what tells the user "+
			"that their state directory is in a bad state", err)
	}
}
