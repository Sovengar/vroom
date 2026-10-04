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
	itemPrimary treeItemKind = iota
	itemSecondary
	itemProject
	itemStack
	itemRepo
)

// primary and secondary ride on every row because the tree is flattened: folding a group must find its members from the row itself.
type treeItem struct {
	kind      treeItemKind
	primary   string
	secondary string
	project   scanner.Project
	stack     *orchestrate.Stack
	repoPath  string
	indent    int
	hasKids   bool
}

func (m Model) buildTree() []treeItem {
	nStacks := 0
	if m.composeFile != nil {
		nStacks = len(m.composeFile.Stacks)
	}

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

	// Worktrees never group at top level: their primary_group is inert for positioning.
	visible := make([]group.Entry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.Project.IsWorktree {
			continue
		}
		visible = append(visible, e)
	}

	items := make([]treeItem, 0, len(visible)+nStacks+1)
	skipPrimary := ""
	skipSecondary := ""

	for i, e := range visible {
		if group.IsPrimaryHeader(visible, i) {
			items = append(items, treeItem{kind: itemPrimary, primary: e.Primary})
			skipPrimary, skipSecondary = "", ""
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

	// Stacks look like a secondary group but are a separate concept: own code, own rendering, grouped under "Composers" without ever touching secondary_group.
	if m.composeFile != nil {
		primaries := make([]string, 0)
		seenPrimaries := make(map[string]bool)
		for _, e := range visible {
			if e.Primary != "" && !seenPrimaries[e.Primary] {
				seenPrimaries[e.Primary] = true
				primaries = append(primaries, e.Primary)
			}
		}
		for _, s := range m.composeFile.Stacks {
			if !seenPrimaries[s.PrimaryGroup] {
				seenPrimaries[s.PrimaryGroup] = true
				primaries = append(primaries, s.PrimaryGroup)
			}
		}

		for _, prim := range primaries {
			if m.collapsed[prim] {
				continue
			}
			stacks := m.stacksForPrimary(prim)
			if len(stacks) == 0 {
				continue
			}
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
		}
	}

	return items
}

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

// The NUL byte can never come from a group name read out of a manifest, so repo-node fold keys cannot collide with group keys.
const repoKeyPrefix = "\x00repo:"

func repoKey(repoPath string) string { return repoKeyPrefix + repoPath }

// Repo rows default collapsed, the inverse of group nodes, which default expanded.
func (m Model) repoExpanded(repoPath string) bool {
	return m.collapsed[repoKey(repoPath)]
}

func (m Model) repoGlyph(repoPath string) string {
	if m.repoExpanded(repoPath) {
		return "▾"
	}
	return "▸"
}

func (m Model) repoRunningKids(repoPath string) int {
	n := 0
	for _, e := range m.entries {
		p := e.Project
		if !p.IsWorktree || p.RepoRoot != repoPath {
			continue
		}
		if sv := m.services[p.Path]; sv != nil && sv.Status == statusRunning {
			n++
		}
	}
	return n
}

func runningBadge(n int) string {
	if n <= 0 {
		return ""
	}
	return " " + styleWorktreeRunning.Render(fmt.Sprintf("+%d", n))
}

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

// A render-only label, not a real secondary_group: it must never collide with a manifest group of the same name.
const composersGroup = "Composers"

// Cursor position is the item index, so the tree must stay exactly one line per item.
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
			glyph := ""
			if it.hasKids {
				glyph = m.repoGlyph(it.repoPath) + " "
			}
			lines = append(lines, cursor+glyph+m.containerRow(it))
		default:
			lines = append(lines, cursor+strings.Repeat("  ", it.indent)+m.projectRow(it))
		}
	}
	return lines, m.cursor
}

// The warning shows even with no children, because a failed "git worktree list" discovers zero worktrees and hasKids would hide it.
func (m Model) projectRow(it treeItem) string {
	badge := ""
	if it.hasKids {
		badge = runningBadge(m.repoRunningKids(it.repoPath))
	}
	// padW pads but never truncates, so every prepended prefix must be subtracted here or the row overflows the fixed tree column.
	prefixW := 0
	if it.hasKids {
		prefixW += 2
	}
	if it.project.WorktreeErr != "" {
		prefixW += 2
	}
	row := m.treeRow(it.project, treeWidth-4-prefixW-lipglossWidth(badge))
	if it.indent > 0 {
		row = m.worktreeRow(it.project)
	}
	if it.hasKids {
		row = m.repoGlyph(it.repoPath) + " " + row
	}
	if it.project.WorktreeErr != "" {
		row = styleWarn.Render("⚠") + " " + row
	}
	return row + badge
}

func (m Model) containerRow(it treeItem) string {
	badge := runningBadge(m.repoRunningKids(it.repoPath))
	extraW := 0
	if it.hasKids {
		extraW += 2
	}
	if it.project.IsBareContainer {
		extraW += len(" (bare)")
	}
	warn := ""
	if it.project.WorktreeErr != "" {
		warn = styleWarn.Render("⚠") + " "
		extraW += 2
	}
	row := trunc(it.project.Name, treeWidth-2-extraW-lipglossWidth(badge))
	if it.project.IsBareContainer {
		row += " " + styleDim.Render("(bare)")
	}
	return warn + row + badge
}

func (m Model) worktreeRow(p scanner.Project) string {
	row := treeDot(p, m.services[p.Path], m.spinner.View(), m.startSpinner.View()) + " " + trunc(p.Name, treeWidth-8)
	if b := m.branches[p.Path]; b != "" {
		row += " " + styleDim.Render(b)
	}
	return row
}

func (m Model) primaryRow(primary string) string {
	r, n := m.nodeStats(primary, "")
	return m.groupHeaderRow(primary, primary, r, n)
}

func (m Model) secondaryRow(primary, secondary string) string {
	r, n := m.nodeStats(primary, secondary)
	return m.groupHeaderRow(m.secondaryKey(primary, secondary), secondary, r, n)
}

func (m Model) groupHeaderRow(key, label string, running, total int) string {
	glyph := "▾"
	text := label
	if m.collapsed[key] {
		glyph = "▸"
		text = fmt.Sprintf("%s (%d/%d)", label, running, total)
	}
	return styleGroupHeader.Render(glyph + " " + trunc(text, treeWidth-4))
}

func (m Model) treeRow(p scanner.Project, nameW int) string {
	return treeDot(p, m.services[p.Path], m.spinner.View(), m.startSpinner.View()) + " " + trunc(p.Name, nameW)
}

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
	// Measured bug: these three used to fall through to the stopped dot, showing a live service as stopped while the row badge and the s action said otherwise.
	case statusPortPending:
		return startSpinnerView
	case statusPortUnresolved, statusNoPort:
		return styleRunning.Render("●")
	default:
		return styleStopped.Render("·")
	}
}

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

func (m Model) stackRow(s *orchestrate.Stack) string {
	r, n, err := m.stackStats(s)
	label := fmt.Sprintf("🎵 %s (%d/%d)", s.Name, r, n)
	if err != nil {
		label = fmt.Sprintf("🎵 %s ⚠ conflict", s.Name)
	}
	return styleStack.Render(trunc(label, treeWidth-4))
}

// An ambiguous service name is an explicit error, never the first match: same rule as the engine and the CLI.
func (m Model) stackStats(s *orchestrate.Stack) (running, total int, err error) {
	_, running, total, err = m.resolveStack(s)
	return running, total, err
}

// Returns the resolved list too, because callers that validate a stack need both and resolving twice would open a window where projects could change.
func (m Model) resolveStack(s *orchestrate.Stack) (services []orchestrate.ResolvedService, running, total int, err error) {
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
				return nil, running, total, lookupErr
			}
			services = append(services, orchestrate.ResolvedService{Name: name, Project: p})
			if sv := m.services[p.Path]; sv != nil && sv.Status == statusRunning {
				running++
			}
		}
	}
	return services, running, total, nil
}

func exampleManifest(name string) string {
	return fmt.Sprintf(`name = %q
command_start = "go run main.go"
port = 0`, name)
}
