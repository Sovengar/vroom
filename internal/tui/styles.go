package tui

import "charm.land/lipgloss/v2"

// Estilos Lipgloss (spec R11): running=verde, stopped=gris,
// unknown=amarillo, sin configurar=gris atenuado.
var (
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))

	styleGroupHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))

	styleRunning      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleStopped      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleUnknown      = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleStarting     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	styleStopping     = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	styleUnconfigured = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)

	styleSelected = lipgloss.NewStyle().Bold(true)
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
	styleHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
	styleMsg      = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleLabel    = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))

	// Pestañas del dashboard (spec 0002 S18.4): la activa se pinta con
	// fondo invertido para distinguirse de las inactivas a simple vista.
	styleTabActive   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12"))
	styleTabInactive = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleSep         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// Modal del picker de tasks (0003 R28): fila del cursor con el
	// mismo estilo invertido que la pestaña activa.
	stylePickerCursor = styleTabActive
)
