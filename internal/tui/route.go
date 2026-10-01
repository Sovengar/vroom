package tui

import (
	"os"
	"strings"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/state"
)

// portlessClient y releaseRoute son las ÚNICAS dos doorway desde app.go hacia
// el seam de portless, y existen sólo para que app.go no mencione "portless.".
//
// No es una cuestión de estilo: lo prohíbe el guard estructural
// TestDiscoveryIsNotInTheTUIRefreshPath. El seam hace exec al binario, y si se
// colara en app.go acabaría shelling out desde el tick de refresco de 2 s, o
// sea un spawn por servicio y por refresh. Estas dos funciones se llaman
// únicamente desde el arranque y desde el stop, que no son el tick.
//
// La lógica vive en internal/portless (ClientFor y Release) y no aquí, porque es
// la misma en la CLI y en el motor de stacks: tres copias de la comprobación
// "off no busca el binario" son tres sitios donde un día se abre la puerta de
// compatibilidad hacia atrás sin que nada falle.

// routeStubInstalled y tuiReleaseStub son el punto de inyección de la RETIRADA.
//
// Están en producción (no en un _test.go) porque releaseRoute tiene que poder
// leerlos, y existen porque sin ellos la retirada de la TUI no la observaba
// nadie: el reviewer comprobó que borrando los tres call sites de Release la
// suite seguía en verde, así que la decisión 13 del ADR —"se retira en los tres
// caminos"— no la verificaba nada. Un seam que nadie puede inyectar no es un
// seam, y un test que no puede fallar es peor que no tener test.
//
// En producción ambos están a cero y sale el camino real, que es el que se
// ejercita en el test de integración aislado.
var (
	routeStubInstalled bool
	tuiReleaseStub     portless.ReleaserFunc
)

// portlessClient devuelve el seam ya resuelto, o nil si no hay contrato de ruta.
func portlessClient(m *manifest.Manifest) *portless.Client {
	return portless.ClientFor(m)
}

// releaseRoute retira la ruta de un servicio parado y REVOCA la propiedad si la
// retirada surtió efecto. Muta el Meta; quien lo persiste es el SaveMeta del
// stop.
//
// La comprobación del stub mira las DOS cosas (flag Y puntero), igual que la
// CLI y el engine. Con sólo el flag, un test que lo(active) sin instalar doble
// dejaría un Releaser no nulo envuelto en una func nil, y el primer Remove
// reventaría con SIGSEGV. Es el mismo error en los tres sitios, y por eso se
// escriben igual.
func releaseRoute(meta *state.Meta) {
	if !portless.Release(tuiRouteReleaser(), meta.RouteName) {
		return // la retirada no surtió efecto: no se revoca nada
	}
	meta.RouteOwned = false
}

// tuiRouteReleaser devuelve el seam de retirada, o nil para el cliente real.
func tuiRouteReleaser() portless.Releaser {
	if routeStubInstalled && tuiReleaseStub != nil {
		return tuiReleaseStub
	}
	if strings.HasSuffix(os.Args[0], ".test") {
		// LOW-3: sin stub, en un binario de test, esto construiría el cliente real
		// y tocaría el state dir del desarrollador. Aquí no se toca nada.
		return portless.ReleaserFunc(func(string) error { return nil })
	}
	return nil
}
