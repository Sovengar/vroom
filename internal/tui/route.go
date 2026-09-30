package tui

import (
	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/state"
)

// routeClient devuelve el seam de portless para un manifiesto, o nil si no hay
// contrato de ruta.
//
// Vive en su propio fichero, y no en app.go, por el guard estructural
// TestDiscoveryIsNotInTheTUIRefreshPath: la TUI no puede shelling out a
// portless desde su camino de refresco, o un tick de 2 s se convertiría en un
// spawn por servicio y por refresh. La función es la misma en la CLI y en el
// motor de stacks; lo que cambia es quién la llama, y sólo desde el arranque y
// el stop, que no son el tick.
//
// Con route_mode = "off" devuelve nil ANTES de resolver nada: vroom ni siquiera
// busca el binario.
func routeClient(m *manifest.Manifest) *portless.Client {
	if m == nil || !portless.RouteModeEnabled(m.EffectiveRouteMode()) {
		return nil
	}
	return portless.Default()
}

// releaseRoute retira la ruta de un servicio parado. El fallo es BENIGNO por
// diseño: quitar una ruta inexistente sale con 1 (medido) y un stop repetido no
// es un error. Una ruta viva que no es nuestra tampoco se toca: el fallo
// cerrado que gobierna la limpieza es el mismo que gobierna el alta.
//
// No hace falta el manifiesto: RouteName sólo se persiste cuando hubo un
// contrato de ruta, así que su presencia YA es la prueba de que hay algo que
// retirar. Así el stop no depende de releer el manifiesto, que podría haber
// cambiado desde el arranque.
func releaseRoute(meta state.Meta) {
	if meta.RouteName == "" {
		return
	}
	portless.Default().Remove(meta.RouteName)
}
