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
			t.Fatal("writeJSONAtomic con un float64 NaN no saltó: un tipo que json no sabe " +
				"serializar es un error de programación, y tiene queVerse como tal")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("el panic es de tipo %T, want string con el motivo", r)
		}
		if !strings.Contains(msg, "float64") {
			t.Errorf("el panic dice %q, want que nombre el tipo que falló: sin el tipo, quien lo "+
				"lee no sabe qué campo añadir o quitar", msg)
		}
		if !strings.Contains(msg, "NaN") {
			t.Errorf("el panic dice %q, want que incluya el error de json: \"tipo\" sin \"por qué\" "+
				"deja el trabajo a medias", msg)
		}
	}()

	// MEDIDO: NaN is the only value encoding/json rejects here, and the one a stray numeric field would smuggle in.
	_ = writeJSONAtomic(filepath.Join(t.TempDir(), "nunca.json"), map[string]float64{"x": nan()})
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
	if err := s.SaveCollapsed(map[string]bool{"tienda": true}); err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{
		filepath.Join(s.ServiceDir(proyecto), "meta.json"),
		s.CollapsedFile(),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s no existe: %v", f, err)
		}
		tmp := f + ".tmp"
		if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Errorf("quedó el temporal %s detrás (err=%v): el rename no lo limpia", tmp, err)
		}
	}

	meta, err := s.LoadMeta(proyecto)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 42 || meta.Name != "api" {
		t.Errorf("meta = %+v, want Name=api Pid=42", meta)
	}
	if !s.LoadCollapsed()["tienda"] {
		t.Error("collapsed.json no se guardó: el grupo plegado se despliega en el siguiente arranque")
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
	if err := os.MkdirAll(filepath.Join(dir, "meta.json", "bloqueo"), 0o755); err != nil {
		t.Fatal(err)
	}

	err = s.SaveMeta(proyecto, Meta{Name: "api"})
	if err == nil {
		t.Fatal("SaveMeta con un destino que no es un fichero tiene que fallar")
	}
	if !strings.Contains(err.Error(), "meta.json") {
		t.Errorf("err = %q, want que nombre el fichero: el mensaje es lo que le dice al usuario "+
			"que su directorio de estado está en mal estado", err)
	}
}
