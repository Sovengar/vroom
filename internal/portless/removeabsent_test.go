package portless

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"vroom/internal/state"
)

// MEDIUM-C: RemoveAbsent revoked ownership on ANY exit 1 that was not the "no such alias" message, so a failed removal declared the route not ours while it could still be there, orphan with nobody to clean it.

// Exercised against the real RemoveAbsent with injected exec: Release delegates to a stub, so testing the stub proves nothing.
func TestRemoveAbsentOnlyRevokesOnSuccessOrAbsent(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		code    int
		wantErr bool // wantErr: must propagate as an error, and therefore must not revoke
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

			// What matters is the effect on ownership: revoking means receiving no error.
			if revoked := err == nil || errors.Is(err, ErrRouteAbsent); revoked == tc.wantErr {
				t.Errorf("%s: revocado=%v, esperado revocado=%v (err=%v)",
					tc.name, revoked, !tc.wantErr, err)
			}
		})
	}
}

func TestReleaseWithFailingReleaserDoesNotRevoke(t *testing.T) {
	if Release(failingReleaser{}, "mi-ruta") {
		t.Error("sin binario no se puede afirmar nada sobre la ruta: no revocar")
	}
}

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

type failingReleaser struct{}

func (failingReleaser) RemoveAbsent(string) error { return errRemoveFailed }

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
