package tui

import "charm.land/lipgloss/v2"

// Fixed hex palette, not ANSI 8/4: the terminal resolves those, and on dark themes 8+Faint made git commits and console noise unreadable.
var (
	mutedFg  = lipgloss.Color("#6c7086")
	accentFg = lipgloss.Color("#89b4fa")
)

var (
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))

	styleGroupHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))

	styleRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleStopped = lipgloss.NewStyle().Foreground(mutedFg)

	// Cyan 6, chosen to stay distinguishable from running green 10, stopping magenta 13 and the bold group header 14.
	styleWorktreeRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleUnknown         = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleStarting        = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	styleStopping        = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	styleUnconfigured    = lipgloss.NewStyle().Foreground(mutedFg).Faint(true)

	styleWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleDim   = lipgloss.NewStyle().Foreground(mutedFg)
	styleHelp  = lipgloss.NewStyle().Foreground(mutedFg).Faint(true)
	styleMsg   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleLabel = lipgloss.NewStyle().Foreground(accentFg)

	styleTabActive   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12"))
	styleTabInactive = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))

	borderFg = lipgloss.Color("8")

	stylePickerCursor = styleTabActive

	styleTermCursor = lipgloss.NewStyle().Reverse(true)

	styleLineError = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleLineWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))

	styleStack = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)
