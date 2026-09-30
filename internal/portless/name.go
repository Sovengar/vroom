package portless

import (
	"fmt"
	"regexp"
	"strings"
)

// HostSuffix es el TLD que portless sirve. El hostname completo de una ruta
// `<name>` es `<name>` + HostSuffix.
const HostSuffix = ".localhost"

// sanitiser normaliza un segmento a lo que portless acepta como hostname.
//
// MEDIDO, y esto no es un detalle cosmético: `portless alias` RECHAZA con exit
// 1 cualquier nombre con guion bajo, espacio, dos puntos o acentos
// ("must contain only lowercase letters, digits, hyphens, and dots"), y una
// rama de git está llena de guiones bajos y barras. Peor: un nombre con barra
// no falla, se TRUNCA en silencio — `Feat/My_Branch.proj` se registró como
// `feat.localhost`, que es el nombre de otro proyecto y una colisión silenciosa.
//
// Por eso vroom sanea en vez de pasar el nombre crudo: el nombre derivado es
// una dirección que el usuario acaba escribiendo en un navegador, y una
// colisión silenciosa con el worktree vecino es peor que un nombre feo.
var sanitiser = regexp.MustCompile(`[^a-z0-9.-]+`)

// consecutiveDots limpia los puntos repetidos, también rechazados por
// portless ("consecutive dots are not allowed").
var consecutiveDots = regexp.MustCompile(`\.{2,}`)

// Hostname devuelve el hostname completo de la ruta name, normalizado igual
// que lo hace portless: un nombre que ya acaba en .localhost no se duplica.
// alias y --remove normalizan igual, así que vroom puede pasar cualquiera de
// las dos formas.
func Hostname(name string) string {
	n := sanitizeName(name)
	if n == "" {
		return ""
	}
	if strings.HasSuffix(n, HostSuffix) {
		return n
	}
	return n + HostSuffix
}

// sanitizeName reduce name a un nombre de ruta que portless acepta, o "" si no
// queda nada utilizable.
func sanitizeName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.ReplaceAll(n, HostSuffix, "")
	n = sanitiser.ReplaceAllString(n, "-")
	n = consecutiveDots.ReplaceAllString(n, ".")
	n = strings.Trim(n, "-.")
	// Una etiqueta que empieza por dígito o acaba en guion es inválida como
	// nombre de host; se recorta por el lado que falle, en vez de inventar.
	n = strings.TrimRight(n, "-")
	if n == "" {
		return ""
	}
	return n
}

// DeriveName calcula el nombre de ruta de un servicio.
//
// Dos modos, porque el nombre sirve para dos cosas distintas:
//
//   - RouteModeAuto: nombre derivado de la RAMA, sin escribir nada en el
//     manifiesto.
//   - RouteModeNamed: la URL ESTABLE que exigen un callback OAuth o una regla
//     CORS, que no puede depender de una rama.
//
// ALCANCE DE `auto`: es scope de RAMA, no de worktree. La función recibe la
// rama y el nombre del proyecto, y NO la ruta del worktree, así que no puede —y
// no pretende— separar dos worktrees que están en la misma rama. Lo que evita es
// que dos RAMAS distintas del mismo repo compartan una dirección.
//
// La consequence honesta: dos clones en `main` derivan el mismo nombre, y el
// segundo NO recibe una segunda dirección sino un conflicto claro, con la ruta
// del primero intacta (ver Apply). Para el caso de la rama repetida está
// `named`, que es único por construcción y es justo lo que OAuth y CORS
// necesitan. La convención nativa de portless deriva de la rama también (M13),
// así que `auto` no introduce una convención nueva: sigue la de la herramienta.
//
// Y al derivarse de la rama, un `git branch -m` cambia el nombre: por eso la
// reconciliación del arranque (Reconcile) existe y es obligatoria.
func DeriveName(mode, routeName, branch, project string) (string, error) {
	switch mode {
	case RouteModeNamed:
		n := sanitizeName(routeName)
		if n == "" {
			return "", fmt.Errorf("route_name %q is not a usable hostname (portless accepts only lowercase letters, digits, hyphens and dots)", routeName)
		}
		return n, nil
	case RouteModeAuto:
		if b := sanitizeName(branch); b != "" {
			return b + "." + sanitizeName(project), nil
		}
		n := sanitizeName(project)
		if n == "" {
			return "", fmt.Errorf("project name %q is not a usable hostname", project)
		}
		return n, nil
	default:
		return "", fmt.Errorf("unknown route_mode %q", mode)
	}
}
