package portless

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// MEDIUM-C: RemoveAbsent revocaba la propiedad ante CUALQUIER exit 1 que no
// fuera el mensaje de "no existe".
//
// Y hay varios que son exit 1 y sí son fallo real:
//   - `requires Node >= 24`  (Node viejo)
//   - `EACCES: permission denied`
//   - routes.json corrupto
//
// El `default → nil` los revocaba todos. El efecto: una retirada que falló
// declaraba la ruta nuestra=false, cuando la ruta podía seguir ahí. Y una
// propiedad revocada ya no puede reclamar la ruta, así que se quedaba huérfana
// con nadie que la limpiera — el fallo abierto, etiquetado como cerrado.
//
// Sólo revocan el éxito (exit 0) y el benigno de M10.
// ---------------------------------------------------------------------------

// Estos casos NO pasan por Release: se exerten contra el RemoveAbsent REAL, con
// el exec inyectado, porque Release delega en un stub y un test que sólo mira el
// stub no probaría nada del código bajo prueba.
func TestRemoveAbsentOnlyRevokesOnSuccessOrAbsent(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		code    int
		wantErr bool // ¿debe propagar como error (y por tanto NO revocar)?
	}{
		{"éxito", "", 0, false},
		{"benigno M10: no existe", `Error: No alias found for "x.localhost".`, 1, false},
		{"Node viejo", "Error: requires Node >= 24", 1, true},
		{"permisos", "Error: EACCES: permission denied", 1, true},
		{"json corrupto", "SyntaxError: Unexpected token } in JSON", 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := clientWithExit(t, tc.out, tc.code)
			err := c.RemoveAbsent("x")

			// Y el efecto sobre la propiedad, que es lo que importa: revocar es
			// no recibir error.
			if revoked := err == nil || errors.Is(err, ErrRouteAbsent); revoked == tc.wantErr {
				t.Errorf("%s: revocado=%v, esperado revocado=%v (err=%v)",
					tc.name, revoked, !tc.wantErr, err)
			}
		})
	}
}

// Un binario ausente: RemoveAbsent sale bien porque no hay nada que retirar, y
// quien revoca es Release con el cliente real — que aquí no procede, porque sin
// portless no hay ruta nuestra que gestionar.
func TestReleaseWithFailingReleaserDoesNotRevoke(t *testing.T) {
	if Release(failingReleaser{}, "mi-ruta") {
		t.Error("sin binario no se puede afirmar nada sobre la ruta: no revocar")
	}
}

// clientWithExit construye un cliente cuyo binario sale con el código y el
// mensaje dados, para modelar los exit 1 reales de portless.
func clientWithExit(t *testing.T, out string, code int) *Client {
	t.Helper()
	return New(
		WithBinary("/fake/portless"),
		WithStateDir(t.TempDir()),
		WithExec(func(context.Context, string, ...string) (string, int, error) {
			if code == 0 {
				return out, 0, nil
			}
			return "", code, errors.New(out)
		}),
	)
}

// codeOneReleaser devuelve un exit concreto con un mensaje, para modelar los
// fallos reales de portless sin un binario.
// failingReleaser falla como lo haria un binario ausente (code 0 + err).
type failingReleaser struct{}

func (failingReleaser) RemoveAbsent(string) error { return errRemoveFailed }

// Y el caso nuevo: sí escrito con route_owned, concede lo que le corresponde.
func TestMetaWithRouteOwnedAuthorisesItsOwnPort(t *testing.T) {
	current := []byte(`{
  "name": "svc",
  "port": 4321,
  "route_name": "svc",
  "route_port": 4321,
  "route_owned": true,
  "route_status": "registered"
}`)

	var m state.Meta
	if err := json.Unmarshal(current, &m); err != nil {
		t.Fatal(err)
	}
	held := Ownership{Owned: m.RouteOwned, Port: m.RoutePort}
	if !held.Authorises(4321) {
		t.Error("route_owned=true con su propio puerto debe autorizar")
	}
	if held.Authorises(9999) {
		t.Error("pero nunca un puerto que no es el suyo")
	}
}
