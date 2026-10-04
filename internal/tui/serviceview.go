package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"vroom/internal/orchestrate"
	"vroom/internal/portless"
	"vroom/internal/scanner"
)

// commandColGap separa la columna meta de la columna de comandos.
const commandColGap = 2

// detailsLines renderiza el panel de detalles del servicio seleccionado
// Cabecera con nombre+estado y, debajo, dos columnas —
// meta a la izquierda (path, rama, puerto...) y los comandos del
// manifiesto (start/stop/install/build) a la derecha. Recorta a
// detailsHeight para uso en tests y otros contexts.
func (m Model) detailsLines(w int) []string {
	return clipLines(m.allDetailsLines(w), detailsHeight, w)
}

// allDetailsLines devuelve todas las líneas de detalles SIN recortar.
// Usado por rightColumnLines que aplica scroll offset.
func (m Model) allDetailsLines(w int) []string {
	p := m.selected()
	if p == nil {
		if s := m.selectedStack(); s != nil {
			return m.stackDetailsLines(s, w)
		}
		if pr, sec := m.selectedNode(); pr != "" { // Resumen del nodo
			return m.groupDetailsLines(pr, sec, w)
		}
		return []string{styleDim.Render("No project selected")}
	}
	sv := m.services[p.Path]
	header := trunc(p.Name, w) + "  " + statusBadge(*p, sv, m.spinner.View(), m.startSpinner.View())

	if !p.Configured {
		lines := []string{truncANSI(header, w)}
		lines = append(lines, styleWarn.Render("No manifest — create a .vroom.toml"))
		lines = append(lines, styleDim.Render(trunc(exampleManifest(p.Name), w)))
		return lines
	}

	// Dos columnas: meta (55%) | comandos. En panel estrecho cae a una.
	metaW := w * 55 / 100
	cmdW := w - metaW - commandColGap
	if cmdW < 16 {
		metaW = w
		cmdW = 0
	}
	meta := m.metaColumn(*p, sv, metaW)
	var rows []string
	if cmdW > 0 {
		rows = zipColumns(meta, m.commandsColumn(*p, cmdW), metaW, cmdW)
	} else {
		rows = meta
	}
	return append([]string{truncANSI(header, w)}, rows...)
}

// metaColumn compone la columna izquierda del panel de detalles: los
// campos del servicio sin los comandos (van en su propia columna).
func (m Model) metaColumn(p scanner.Project, sv *ServiceState, w int) []string {
	var lines []string
	valueW := max(8, w-11)
	row := func(label, value string) {
		lines = append(lines, styleLabel.Render(pad(label, 10))+truncTail(value, valueW))
	}
	row("path:", p.Path)
	if b := m.branches[p.Path]; b != "" { // Rama git
		row("branch:", b)
	}
	if p.Manifest != nil && p.Manifest.PrimaryGroup != "" {
		// El compuesto primario/secundario cuando exista
		// secundario (solo con primario).
		g := p.Manifest.PrimaryGroup
		if p.Manifest.SecondaryGroup != "" {
			g += "/" + p.Manifest.SecondaryGroup
		}
		row("group:", g)
	}
	if n := displayPort(p, sv); n > 0 {
		row("port:", fmt.Sprintf("%d", n))
	}
	// La URL va junto al puerto, y sólo si se ha VERIFICADO: una url sin
	// comprobar es una dirección que puede no llevar a nada, y el usuario
	// copiándola acabaría en un error sin explicación.
	if u := displayRouteURL(p, sv); u != "" {
		row("url:", u)
	}
	if p.Manifest != nil && p.Manifest.ProcessPattern != "" {
		row("pattern:", p.Manifest.ProcessPattern)
	}
	if sv.Meta.Pid > 0 {
		row("pid/pgid:", fmt.Sprintf("%d / %d", sv.Meta.Pid, sv.Meta.Pgid))
	}
	if sv.Meta.StartedAt != "" {
		row("started:", sv.Meta.StartedAt)
	}
	row("logs:", m.store.ServiceDir(p.Path))
	return lines
}

// displayRouteURL es la URL de la ruta, o "" si no hay ninguna verificada.
//
// Se lee del Meta persistido y NO se comprueba en vivo: la TUI refresca cada
// 2 s, y sondear el proxy en ese camino convertiría el refresco en un spawn
// por servicio. Lo que se muestra es el último estado conocido, y por eso no
// se muestra una URL degradada: no hay ninguna.
func displayRouteURL(p scanner.Project, sv *ServiceState) string {
	if sv == nil || sv.Meta.RouteStatus != portless.StatusRegistered {
		return ""
	}
	return sv.Meta.RouteURL
}

// commandsColumn compone la columna derecha del panel de detalles: los
// 4 comandos del manifiesto; los no configurados se muestran con "—".
func (m Model) commandsColumn(p scanner.Project, w int) []string {
	valueW := max(4, w-9)
	row := func(label, value string) string {
		if value == "" {
			return styleLabel.Render(pad(label, 9)) + styleDim.Render("—")
		}
		return styleLabel.Render(pad(label, 9)) + truncTail(value, valueW)
	}
	return []string{
		row("start:", p.Manifest.Command),
		row("stop:", p.Manifest.Stop),
		row("install:", p.Manifest.Install),
		row("build:", p.Manifest.Build),
	}
}

// zipColumns compone dos columnas lado a lado: la izquierda rellena a
// su ancho (ANSI-aware) y la derecha se trunca al suyo.
func zipColumns(a, b []string, aw, bw int) []string {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	gap := strings.Repeat(" ", commandColGap)
	out := make([]string, n)
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(a) {
			l = a[i]
		}
		if i < len(b) {
			r = b[i]
		}
		out[i] = padW(truncANSI(l, aw), aw) + gap + truncANSI(r, bw)
	}
	return out
}

// clipLines recorta lines a h filas, cada una a w columnas visibles.
func clipLines(lines []string, h, w int) []string {
	if len(lines) > h {
		lines = lines[:h]
	}
	for i := range lines {
		lines[i] = truncANSI(lines[i], w)
	}
	return lines
}

// groupDetailsLines muestra el resumen del nodo seleccionado:
// conteo running/total y los miembros con su punto de estado.
// El título usa el nombre del secundario si es un nodo secundario.
func (m Model) groupDetailsLines(primary, secondary string, w int) []string {
	label := primary
	if secondary != "" {
		label = secondary
	}
	r, n := m.nodeStats(primary, secondary)
	lines := []string{trunc(fmt.Sprintf("%s (%d/%d)", label, r, n), w)}
	lines = append(lines,
		styleLabel.Render(pad("services:", 10))+fmt.Sprintf("%d", n),
		styleLabel.Render(pad("running:", 10))+fmt.Sprintf("%d", r),
	)
	for _, p := range m.nodeMembers(primary, secondary) {
		label := trunc(p.Name, w-3)
		if n := displayPort(p, m.services[p.Path]); n > 0 {
			label += styleDim.Render(fmt.Sprintf(":%d", n))
		}
		lines = append(lines, treeDot(p, m.services[p.Path], m.spinner.View(), m.startSpinner.View())+" "+label)
	}
	return lines
}

// pad rellena s con espacios hasta n runes (para etiquetas sin ANSI).
//
// MEDIDO: rellenaba a n BYTES, que es lo que decía media doc. Con las etiquetas
// actuales —todas literales ASCII: path, branch, group, port, url, pattern,
// pid/pgid, started, logs, type, stages, services, running— bytes y runes dan el
// mismo número, así que hoy no se ve nada. Pero el día que una etiqueta lleve un
// acento, `ñ` ocupa dos bytes y un byte de relleno: la columna queda un carácter
// más estrecha a partir de ahí y todo lo de debajo se desplaza.
//
// Se cuenta por runes porque es lo que mide el terminal: un rune = una celda en
// el área BMP. (Un emoji = dos, y eso es otro problema que no tiene arreglo aquí.)
func pad(s string, n int) string {
	if r := utf8.RuneCountInString(s); r >= n {
		return s
	} else {
		return s + strings.Repeat(" ", n-r)
	}
}

// stackDetailsLines muestra el panel de detalles de un stack seleccionado
// Nombre + [stack] + estado, tipo, etapas, servicios con
// su estado y puerto (igual que groupDetailsLines).
func (m Model) stackDetailsLines(s *orchestrate.Stack, w int) []string {
	r, n, conflict := m.stackStats(s)
	running := ""
	if r > 0 {
		running = fmt.Sprintf("  %s", styleRunning.Render("● running"))
	} else {
		running = fmt.Sprintf("  %s", styleStopped.Render("· stopped"))
	}
	header := trunc(fmt.Sprintf("🎵 %s [stack]%s", s.Name, running), w)
	lines := []string{truncANSI(header, w)}
	row := func(label, value string) {
		lines = append(lines, styleLabel.Render(pad(label, 10))+truncTail(value, max(8, w-11)))
	}
	row("type:", "orchestration stack")
	row("stages:", fmt.Sprintf("%d", len(s.Stages)))
	row("services:", fmt.Sprintf("%d (%d running)", n, r))
	row("group:", s.PrimaryGroup)
	if conflict != nil {
		lines = append(lines, styleWarn.Render(trunc("⚠ "+conflict.Error(), w)))
	}
	lines = append(lines, "")
	for _, stage := range s.Stages {
		lines = append(lines, styleDim.Render(trunc(fmt.Sprintf("  %s:", stage.Name), w-4)))
		for _, name := range stage.Services {
			label := trunc(name, w-5)
			// Resolución con el criterio compartido: ante un
			// nombre ambiguo no se elige arbitrariamente el primero.
			p, err := orchestrate.LookupService(name, m.projects)
			if err != nil {
				lines = append(lines, "    "+styleWarn.Render("⚠")+" "+label)
				continue
			}
			if n := displayPort(p, m.services[p.Path]); n > 0 {
				label += styleDim.Render(fmt.Sprintf(":%d", n))
			}
			dot := treeDot(p, m.services[p.Path], m.spinner.View(), m.startSpinner.View())
			lines = append(lines, "    "+dot+" "+label)
		}
	}
	return lines
}
