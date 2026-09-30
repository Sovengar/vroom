package portless_test

import (
	"errors"
	"testing"

	"vroom/internal/portless"
)

// ---------------------------------------------------------------------------
// La retirada, que antes no la observaba NADA.
//
// El reviewer comprobó que borrando los tres call sites de Release la suite
// seguía en verde: la decisión 13 del ADR ("se retira en los tres caminos") no
// la verificaba nada. Estos tests cierran ese hueco en el seam, y los de cada
// paquete en los tres caminos reales.
// ---------------------------------------------------------------------------

// un doble que registra lo que se le pide retirar.
type recordingReleaser struct {
	removed []string
	err     error
}

func (r *recordingReleaser) Remove(name string) error {
	r.removed = append(r.removed, name)
	return r.err
}

// errRemoveFailed es el error que devuelve una retirada que no pudo completarse.
var errRemoveFailed = errors.New("portless alias --remove exited 1")

// Release retira lo que se le dice, y sólo eso.
func TestReleaseRemovesThroughTheSeam(t *testing.T) {
	rec := &recordingReleaser{}
	portless.Release(rec, "mi-ruta")

	if len(rec.removed) != 1 || rec.removed[0] != "mi-ruta" {
		t.Errorf("Release debe retirar exactamente la ruta dada, got %v", rec.removed)
	}
}

// Un nombre vacío no invoca nada: es la señal de "este servicio no registró
// ruta", y una llamada con nombre vacío sería ruido en el binario.
func TestReleaseSkipsEmptyName(t *testing.T) {
	rec := &recordingReleaser{}
	portless.Release(rec, "")

	if len(rec.removed) != 0 {
		t.Errorf("sin nombre no hay nada que retirar, got %v", rec.removed)
	}
}

// El fallo de la retirada es BENIGNO y no se propaga: quitar una ruta
// inexistente sale con 1 (medido, M10) y un stop repetido no puede ser un error,
// porque el servicio ya está parado. Release no devuelve nada por eso.
func TestReleaseSwallowsErrors(t *testing.T) {
	rec := &recordingReleaser{err: errRemoveFailed}
	portless.Release(rec, "mi-ruta") // no debe panic ni propagar

	if len(rec.removed) != 1 {
		t.Errorf("se debe intentar la retirada aunque falle, got %v", rec.removed)
	}
}

// nil significa "construye el cliente real": es lo que usan los tres caminos de
// stop en produccion. Con un nombre vacio no debe llegar a construir el cliente
// ni a tocar nada, y eso es comprobable sin binario.
//
// El camino real de nil --una retirada de verdad contra el entorno-- no se
// prueba aqui a proposito: exigiria el portless del usuario. Vive en el test de
// integracion, que es aislado y opt-in.
func TestReleaseWithNilSeamAndEmptyNameIsInert(t *testing.T) {
	portless.Release(nil, "")
}

// La retirada real contra portless de verdad, aislada. Gated como el resto de la
// integración: sin la variable, esto no corre.
func TestReleaseIntegrationRemovesForReal(t *testing.T) {
	iso := integrationStateDir(t)

	c := portless.New(portless.WithBinary(integrationBin(t)), portless.WithStateDir(iso), portless.WithTimeout(10))
	if c.Apply("vroom.release", 4321, 0).Succeeded() {
		t.Log("hay un proxy en marcha: la ruta se registró y va a comprobarse")
	}
	if _, found, err := c.Lookup("vroom.release"); err != nil || !found {
		t.Skipf("portless no responde como se espera: %v", err)
	}

	// Retirar, y retirar otra vez: las dos tienen que salir bien.
	portless.Release(c, "vroom.release")
	if _, found, _ := c.Lookup("vroom.release"); found {
		t.Error("tras la retirada la ruta debe desaparecer")
	}
	portless.Release(c, "vroom.release") // repetida: benigna
}
