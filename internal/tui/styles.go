package tui

import "charm.land/lipgloss/v2"

// Estilos Lipgloss (spec R11): running=verde, stopped=gris,
// unknown=amarillo, sin configurar=gris atenuado.
var (
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))

	styleGroupHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))

	styleRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleStopped = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// Badge +N de worktrees en ejecución bajo una fila de repo (0012):
	// cyan (6) para distinguirlo del verde del servicio propio (10), del
	// magenta de stopping (13) y del cyan brillante/negrita del header de
	// grupo (14), que además nunca comparte fila con el badge.
	styleWorktreeRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleUnknown         = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleStarting        = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	styleStopping        = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	styleUnconfigured    = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)

	styleSelected = lipgloss.NewStyle().Bold(true)
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
	styleHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086")).Faint(true)
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

	// Cursor de la terminal embebida (0009 R55): bloque invertido.
	styleTermCursor = lipgloss.NewStyle().Reverse(true)

	// Resaltado de consola estilo IntelliJ: línea con ERROR→rojo vivo,
	// WARN→amarillo; el ruido (debug/trace, stack traces, [INFO] Maven)
	// reutiliza styleDim.
	styleLineError = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleLineWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))

	// Estilo de stacks (0010): magenta para distinguir de apps.
	styleStack = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)
