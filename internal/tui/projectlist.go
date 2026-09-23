package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"vroom/internal/group"
	"vroom/internal/orchestrate"
	"vroom/internal/scanner"
)

type treeItemKind int

const (
	itemPrimary   treeItemKind = iota // header de primario
	itemSecondary                     // header de secundario (dentro de un primario)
	itemProject                       // proyecto
	itemStack                         // stack de orquestación (0010) — concepto propio
	itemRepo                          // fila contenedora de repo sin proyecto (bare / fuera de root)
)

// treeItem es una fila navegable del árbol: header primario, header
// secundario, proyecto o stack. primary/secondary viajan en todas las
// filas para conocer el contenedor de un proyecto al plegarlo (S38.4).
type treeItem struct {
	kind      treeItemKind
	primary   string             // primario del bloque ("" solo en proyecto inline)
	secondary string             // secundario del bloque ("" = directo bajo el primario)
	project   scanner.Project    // válido en itemProject/itemRepo
	stack     *orchestrate.Stack // válido en itemStack
	repoPath  string             // ruta del repo contenedor (filas de repo)
	indent    int                // indentación extra (worktrees anidados = 1)
	hasKids   bool               // la fila de repo tiene worktrees
}

// buildTree compone las filas del árbol (0006 R38 + 0010 + 0011): header
// primario antes de su bloque; dentro, header de secundario antes de
// sus miembros. Los worktrees (0011) se anidan bajo la fila de su repo
// (colapsada por defecto) y no se emiten como filas top-level. Los stacks
// (0010) se añaden al FINAL de cada bloque primario bajo un header
// "Composers".
func (m Model) buildTree() []treeItem {
	nStacks := 0
	if m.composeFile != nil {
		nStacks = len(m.composeFile.Stacks)
	}

	// Reparto repo→worktrees: cada worktree cuelga de la ruta de su main
	// checkout (RepoRoot). Un repo con fila propia es un proyecto normal
	// del slice; si su main checkout está fuera del scan root, no hay
	// fila y se sintetiza una contenedora.
	children := make(map[string][]scanner.Project)
	hasOwnRow := make(map[string]bool)
	for _, e := range m.entries {
		p := e.Project
		if p.IsWorktree && p.RepoRoot != "" {
			children[p.RepoRoot] = append(children[p.RepoRoot], p)
			continue
		}
		hasOwnRow[p.Path] = true
	}
	for k := range children {
		sort.Slice(children[k], func(i, j int) bool { return children[k][i].Path < children[k][j].Path })
	}

	// Los worktrees no participan de la agrupación top-level: su
	// primary_group es inerte para el posicionamiento (0011, option B).
	visible := make([]group.Entry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.Project.IsWorktree {
			continue
		}
		visible = append(visible, e)
	}

	items := make([]treeItem, 0, len(visible)+nStacks+1)
	skipPrimary := ""   // primario colapsado cuyos miembros y headers se omiten
	skipSecondary := "" // clave compuesta "primario/secundario" colapsada

	// Track stacks emitted per primary (to avoid duplicates)
	stacksEmitted := make(map[string]bool)

	for i, e := range visible {
		if group.IsPrimaryHeader(visible, i) {
			items = append(items, treeItem{kind: itemPrimary, primary: e.Primary})
			skipPrimary, skipSecondary = "", "" // nuevo bloque: reset
			if m.collapsed[e.Primary] {
				skipPrimary = e.Primary
				continue
			}
		}
		if skipPrimary != "" && e.Primary == skipPrimary {
			continue
		}
		key := m.secondaryKey(e.Primary, e.Secondary)
		if e.Secondary != "" && group.IsSecondaryHeader(visible, i) {
			items = append(items, treeItem{kind: itemSecondary, primary: e.Primary, secondary: e.Secondary})
			if m.collapsed[key] {
				skipSecondary = key
				continue
			}
			skipSecondary = ""
		}
		if skipSecondary != "" && key == skipSecondary {
			continue
		}
		items = append(items, m.repoBlock(e.Project, e.Primary, e.Secondary, children, false)...)
	}

	// Contenedores sintetizados para worktrees cuyo main checkout está
	// fuera del scan root (no tienen fila propia en el slice).
	synthetic := make([]string, 0, len(children))
	for repoPath := range children {
		if !hasOwnRow[repoPath] {
			synthetic = append(synthetic, repoPath)
		}
	}
	sort.Strings(synthetic)
	for _, repoPath := range synthetic {
		cp := scanner.Project{Path: repoPath, Name: filepath.Base(repoPath)}
		items = append(items, m.repoBlock(cp, "", "", children, true)...)
	}

	// Append stacks at the end of each primary group (0010).
	// Stacks are a SEPARATE concept — own code, own rendering,
	// visually grouped under "Composers" but not mixed into secondary_group.
	if m.composeFile != nil {
		// Collect primaries in order of appearance
		primaries := make([]string, 0)
		seenPrimaries := make(map[string]bool)
		for _, e := range visible {
			if e.Primary != "" && !seenPrimaries[e.Primary] {
				seenPrimaries[e.Primary] = true
				primaries = append(primaries, e.Primary)
			}
		}
		// Also add primaries that only have stacks (no projects)
		for _, s := range m.composeFile.Stacks {
			if !seenPrimaries[s.PrimaryGroup] {
				seenPrimaries[s.PrimaryGroup] = true
				primaries = append(primaries, s.PrimaryGroup)
			}
		}

		for _, prim := range primaries {
			if m.collapsed[prim] {
				continue // primary collapsed: skip its stacks too
			}
			stacks := m.stacksForPrimary(prim)
			if len(stacks) == 0 {
				continue
			}
			// Emit Composers header (only if not collapsed)
			composersKey := m.secondaryKey(prim, composersGroup)
			if !m.collapsed[composersKey] {
				items = append(items, treeItem{kind: itemSecondary, primary: prim, secondary: composersGroup})
			}
			if m.collapsed[composersKey] {
				continue
			}
			for i := range stacks {
				items = append(items, treeItem{kind: itemStack, primary: prim, secondary: composersGroup, stack: &stacks[i]})
			}
			_ = stacksEmitted // used for tracking
		}
	}

	return items
}

// repoBlock compone la fila de un proyecto y, si es una fila de repo con
// worktrees, sus hijos indentados cuando el repo está expandido. Las
// filas contenedoras (bare o sintetizadas) no son ejecutables (itemRepo).
func (m Model) repoBlock(p scanner.Project, primary, secondary string, children map[string][]scanner.Project, container bool) []treeItem {
	kids := children[p.Path]
	kind := itemProject
	if container || p.IsBareContainer {
		kind = itemRepo
	}
	out := []treeItem{{
		kind:      kind,
		primary:   primary,
		secondary: secondary,
		project:   p,
		repoPath:  p.Path,
		hasKids:   len(kids) > 0,
	}}
	if len(kids) > 0 && m.repoExpanded(p.Path) {
		for _, k := range kids {
			out = append(out, treeItem{
				kind:      itemProject,
				primary:   primary,
				secondary: secondary,
				project:   k,
				repoPath:  p.Path,
				indent:    1,
			})
		}
	}
	return out
}

// repoKey es la clave de plegado de una fila de repo (0011): namespace
// propio, disjunto de las claves de grupo (primary o primary/secondary),
// para que el estado persistido no colisione.
func repoKey(repoPath string) string { return "repo:" + repoPath }

// repoExpanded reporta si la fila de repo está expandida. Default:
// colapsada (inverso al default de los grupos, que empiezan expandidos).
func (m Model) repoExpanded(repoPath string) bool {
	return m.collapsed[repoKey(repoPath)]
}

// repoGlyph es el glifo de expansión de una fila de repo.
func (m Model) repoGlyph(repoPath string) string {
	if m.repoExpanded(repoPath) {
		return "▾"
	}
	return "▸"
}

// stacksForPrimary devuelve los stacks que pertenecen a un primary_group.
func (m Model) stacksForPrimary(primary string) []orchestrate.Stack {
	if m.composeFile == nil {
		return nil
	}
	var out []orchestrate.Stack
	for _, s := range m.composeFile.Stacks {
		if s.PrimaryGroup == primary {
			out = append(out, s)
		}
	}
	return out
}

// composersGroup es el secondary_group visual para stacks (0010).
// Es solo un label de renderizado, NO un secondary_group de projectos.
const composersGroup = "Composers"

// treeLines genera las líneas de la columna de árbol; la posición de
// línea del cursor es su propio índice (una fila por ítem). El header
// secundario se dibuja indentado 2 espacios extra (S38.1).
func (m Model) treeLines() ([]string, int) {
	lines := make([]string, 0, len(m.tree))
	for i, it := range m.tree {
		cursor := "  "
		if i == m.cursor {
			cursor = "▶ "
		}
		switch it.kind {
		case itemPrimary:
			lines = append(lines, cursor+m.primaryRow(it.primary))
		case itemSecondary:
			lines = append(lines, cursor+"  "+m.secondaryRow(it.primary, it.secondary))
		case itemStack:
			lines = append(lines, cursor+"  "+m.stackRow(it.stack))
		case itemRepo:
			lines = append(lines, cursor+m.repoGlyph(it.repoPath)+" "+m.containerRow(it))
		default:
			lines = append(lines, cursor+strings.Repeat("  ", it.indent)+m.projectRow(it))
		}
	}
	return lines, m.cursor
}

// projectRow dibuja una fila de proyecto; las filas de repo con
// worktrees anteponen el glifo de expansión y los worktrees anidados
// usan worktreeRow (rama incluida).
func (m Model) projectRow(it treeItem) string {
	row := m.treeRow(it.project)
	if it.indent > 0 {
		row = m.worktreeRow(it.project)
	}
	if it.hasKids {
		prefix := m.repoGlyph(it.repoPath) + " "
		if it.project.WorktreeErr != "" {
			prefix = styleWarn.Render("⚠") + prefix
		}
		row = prefix + row
	}
	return row
}

// containerRow dibuja una fila contenedora (bare repo o main checkout
// fuera del scan root): no ejecutable, sin estado de servicio.
func (m Model) containerRow(it treeItem) string {
	row := trunc(it.project.Name, treeWidth-6)
	if it.project.IsBareContainer {
		row += " " + styleDim.Render("(bare)")
	}
	return row
}

// worktreeRow dibuja una fila de worktree anidada: estado + nombre +
// rama git (incluye "<sha> (detached)" para HEAD detached).
func (m Model) worktreeRow(p scanner.Project) string {
	row := treeDot(p, m.services[p.Path], m.spinner.View(), m.startSpinner.View()) + " " + trunc(p.Name, treeWidth-8)
	if b := m.branches[p.Path]; b != "" {
		row += " " + styleDim.Render(b)
	}
	return row
}

// primaryRow dibuja el header del primario (columna 0): ▾ expandido,
// ▸ colapsado con conteo running/total (R24); el total incluye todos
// sus secundarios (S38.5).
func (m Model) primaryRow(primary string) string {
	r, n := m.nodeStats(primary, "")
	return m.groupHeaderRow(primary, primary, r, n)
}

// secondaryRow dibuja el header del secundario; su conteo es el de sus
// propios miembros (la indentación la aplica treeLines).
func (m Model) secondaryRow(primary, secondary string) string {
	r, n := m.nodeStats(primary, secondary)
	return m.groupHeaderRow(m.secondaryKey(primary, secondary), secondary, r, n)
}

// groupHeaderRow compone un header de grupo: ▾ nombre expandido, ▸
// nombre (r/t) colapsado.
func (m Model) groupHeaderRow(key, label string, running, total int) string {
	glyph := "▾"
	text := label
	if m.collapsed[key] {
		glyph = "▸"
		text = fmt.Sprintf("%s (%d/%d)", label, running, total)
	}
	return styleGroupHeader.Render(glyph + " " + trunc(text, treeWidth-4))
}

// treeRow dibuja la fila del proyecto: punto de estado + nombre. El
// texto del estado vive en el panel de detalles; en el árbol la bolita
// basta (y los sin manifiesto llevan icono de "roto").
func (m Model) treeRow(p scanner.Project) string {
	return treeDot(p, m.services[p.Path], m.spinner.View(), m.startSpinner.View()) + " " + trunc(p.Name, treeWidth-4)
}

// treeDot es el glifo de estado: ● running, spinner para starting,
// spinner animado para unknown y ⚠ para sin manifiesto / manifiesto inválido.
func treeDot(p scanner.Project, sv *ServiceState, spinnerView, startSpinnerView string) string {
	if !p.Configured || p.ManifestErr != "" {
		return styleWarn.Render("⚠")
	}
	if sv == nil {
		return styleStopped.Render("·")
	}
	switch sv.Status {
	case statusRunning:
		return styleRunning.Render("●")
	case statusStarting:
		return startSpinnerView
	case statusStopping:
		return styleStopping.Render("○")
	case statusUnknown:
		return spinnerView
	default:
		return styleStopped.Render("·")
	}
}

// filterMatch reporta si el proyecto matchea la query del filtro
// (0007 R41): substring case-insensitive contra el nombre y los nombres
// de grupo (primary/secondary); filtrar un grupo trae a todos sus
// miembros aunque el texto no aparezca en ningún nombre de proyecto.
func filterMatch(p scanner.Project, q string) bool {
	q = strings.ToLower(q)
	if strings.Contains(strings.ToLower(p.Name), q) {
		return true
	}
	if strings.Contains(strings.ToLower(group.PrimaryOf(p)), q) {
		return true
	}
	return strings.Contains(strings.ToLower(group.SecondaryOf(p)), q)
}

// stackMatch reporta si un stack matchea la query del filtro.
func stackMatch(s *orchestrate.Stack, q string) bool {
	q = strings.ToLower(q)
	return strings.Contains(strings.ToLower(s.Name), q) ||
		strings.Contains(strings.ToLower(s.PrimaryGroup), q)
}

// stackRow dibuja la fila del stack: 🎵 nombre (running/total). Si el
// stack tiene un nombre de servicio ambiguo, lo marca como conflicto.
func (m Model) stackRow(s *orchestrate.Stack) string {
	r, n, err := m.stackStats(s)
	label := fmt.Sprintf("🎵 %s (%d/%d)", s.Name, r, n)
	if err != nil {
		label = fmt.Sprintf("🎵 %s ⚠ conflict", s.Name)
	}
	return styleStack.Render(trunc(label, treeWidth-4))
}

// stackStats cuenta servicios running y total de un stack. Devuelve un
// error explícito si un nombre de servicio es ambiguo (varios proyectos lo
// declaran): mismo criterio que el engine/CLI (0011), sin elegir el
// primero arbitrariamente.
func (m Model) stackStats(s *orchestrate.Stack) (running, total int, err error) {
	seen := make(map[string]bool)
	for _, stage := range s.Stages {
		for _, name := range stage.Services {
			if seen[name] {
				continue
			}
			seen[name] = true
			total++
			p, lookupErr := orchestrate.LookupService(name, m.projects)
			if lookupErr != nil {
				return running, total, lookupErr
			}
			if sv := m.services[p.Path]; sv != nil && sv.Status == statusRunning {
				running++
			}
		}
	}
	return running, total, nil
}

// exampleManifest genera un manifiesto de ejemplo para proyectos sin
// configurar.
func exampleManifest(name string) string {
	return fmt.Sprintf(`name = %q
command_start = "go run main.go"
port = 0`, name)
}
