package orchestrate

import (
	"vroom/internal/manifest"
	"vroom/internal/portless"
)

// routeClient devuelve el seam de portless para un manifiesto, o nil si no hay
// contrato de ruta. Los stacks pasan por el mismo startsvc que la TUI y la CLI,
// así que una ruta se registra igual por los tres caminos.
//
// Con route_mode = "off" devuelve nil ANTES de resolver nada: vroom ni siquiera
// busca el binario.
func routeClient(m *manifest.Manifest) *portless.Client {
	if m == nil || !portless.RouteModeEnabled(m.EffectiveRouteMode()) {
		return nil
	}
	return portless.Default()
}
