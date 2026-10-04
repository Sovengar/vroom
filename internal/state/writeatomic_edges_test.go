package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteJSONAtomicRevientaConUnTipoNoSerializable: la rama que era código muerto.
//
// `writeJSONAtomic` serializa lo que sea, y con los tipos que vroom guarda aquí —
// `Meta` y `map[string]bool`, todo string, int, int64 y bool— `encoding/json`
// siempre funciona. Antes esa comprobación era un `if err != nil { return }` que
// ningún test podía ejecutar: código muerto con forma de comprobación.
//
// Ahora es un panic, y este test es lo que le da sentido. El caso de referencia es
// un `float64` con NaN, que es el ejemplo canónico de "json falla" y también el
// primero que aparecería si alguien añadiera un campo numérico real a `Meta`.
//
// Lo que se comprueba no es que el panic exista por decoración, sino que el mensaje
// diga QUÉ tipo no se pudo serializar: un panic que sólo dice "json: unsupported
// value" deja a quien lo ve sin saber de dónde viene, y este panic se va a leer en
// mitad de un arranque.
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

	// MEDIDO: NaN es exactamente lo que `encoding/json` rechaza y nada más. Un canal
	// también sirve, pero el NaN es el caso que de verdad puede colarse en un struct
	// por accidente.
	_ = writeJSONAtomic(filepath.Join(t.TempDir(), "nunca.json"), map[string]float64{"x": nan()})
}

func nan() float64 {
	var zero float64
	return zero / zero
}

// TestSaveMetaYSaveCollapsedCompartenElEscribidoAtomico: el tmp + rename.
//
// Los dos caminos de guardado de estado usan el mismo helper, y lo que se comprueba
// es que el patrón se cumple: se escribe un `.tmp`, se renombra, y no queda nada
// detrás.
//
// Que el fichero destino no se toca hasta el rename es lo que hace que un corte a
// mitad de escritura deje el meta ANTERIOR intacto en vez de un json truncado. Sin
// eso, `LoadMeta` leería un `meta.json` a medias y el servicio aparecería con el
// estado de la ejecución anterior.
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

	// Y el contenido es el que se pidió, no un json vacío.
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

// TestSaveMetaPropagaElFalloDeEscribirElTemporal: el error que SÍ puede ocurrir.
//
// El error de `os.WriteFile` es de los de verdad: disco lleno, permisos, un
// directorio que se sustituyó por un fichero. Y tiene que salir como error, no como
// panic, porque es una condición del entorno y hay quien pueda reintentar.
//
// Se provoca con el `meta.json` convertido en un directorio no vacío: el temporal
// se escribe bien y el `rename` sobre un destino que es un directorio falla con
// EISDIR.
func TestSaveMetaPropagaElFalloDeEscribirElTemporal(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	const proyecto = "/srv/api"

	dir, err := s.EnsureServiceDir(proyecto)
	if err != nil {
		t.Fatal(err)
	}
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
