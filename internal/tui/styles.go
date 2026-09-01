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
)
