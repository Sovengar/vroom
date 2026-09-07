# Proposal: Ask AI con dispatch configurable, limpiar consola y detalles fijos

## Intent

Tres refinamientos del dashboard:

1. **Detalles siempre visibles**: el toggle con `d` no aporta — el panel se muestra
   siempre que hay espacio (auto-hide por ancho mínimo se conserva).
2. **`C` limpiar consola**: vacía la vista en memoria y salta los offsets al EOF;
   los ficheros de log conservan el histórico.
3. **`a` ask AI**: pregunta a un agente de IA (opencode, pi, hermes) con un prompt
   del usuario → **chat nuevo en el agente**, con el despacho **externo y
   configurable** vía `~/.config/vroom/config.toml` (hoy herdr con un pane/tab
   nuevo; mañana tmux o lo que sea, sin recompilar vroom).

El patrón de referencia es el de worktrunk (`wt switch -x <agent> -- <prompt>`):
ejecutar el binario del agente con el prompt como argumento en el directorio del
proyecto. vroom añade la capa de dispatch configurable sobre ese patrón.

## Scope

### In Scope
- Quitar el toggle de detalles (tecla `d`, campo `detailsOpen`); panel fijo con
  auto-hide por espacio.
- `C`: limpiar buffers de consola + offsets al EOF (los ficheros no se tocan).
- Config global `~/.config/vroom/config.toml` (`$XDG_CONFIG_HOME` respetado,
  `$VROOM_CONFIG` para override): sección `[ask]` con launcher
  (`auto|herdr|inline|custom`), dirección/target/focus de herdr, plantilla
  `launcher_cmd` para custom, y override opcional de agentes.
- Agentes built-in con invocaciones verificadas: `opencode --prompt {prompt}`,
  `pi {prompt}`, `hermes chat -q {prompt}` (en TTY, `-q` siembra el primer turno
  y la sesión queda interactiva).
- Launcher `herdr`: `pane split --current --cwd <proyecto> [--no-focus]` →
  `pane run <id> "<comando quoteado>"`; `target = "tab"` usa `tab create` +
  root pane. Requiere `HERDR_ENV=1` (vroom corre como pane de herdr).
- Launcher `inline`: suspende vroom con `tea.ExecProcess` y corre el agente en
  primer plano (patrón wt).
- Launcher `custom`: plantilla shell con `{dir}`/`{agent}`/`{cmd}` quoteados.
- `auto` (default): herdr si está disponible; si no, inline. `herdr` explícito
  sin sesión → fallback inline con notify.
- Flujo TUI: `a` → picker de agentes instalados (`exec.LookPath`; uno solo va
  directo) → input de prompt (`textinput`) → `enter` despacha, `esc` cancela.

### Out of Scope
- Cancelar/observar el agente una vez lanzado (el pane es del usuario).
- Streaming del prompt al agente por RPC (pi `--mode rpc`, opencode server).
- Truncar ficheros de log al limpiar (solo vista en memoria, por decisión).
- `d` como tecla (eliminada; sin toggle no hace falta).

## Capabilities

### New Capabilities
- `global-config`: fichero TOML global con defaults, validación de enums y
  notify en arranque si está malformado.
- `ask-ai`: flujo picker → prompt → dispatch con agentes configurables.
- `launcher-strategies`: herdr / inline / custom con resolución `auto` y
  shell-quoting seguro del comando compuesto.

### Modified Capabilities
- `tui-core`: detalles fijos (R30), teclas `C` y `a`, prioridad de `esc`
  (prompt → picker → salir).
- `realtime-console`: operación de limpiar la vista sin tocar los logs.

## Approach

### Cambios de módulos

```
internal/
├── config/                 # NUEVO: config.toml global (sección [ask])
├── agents/                 # NUEVO: built-ins + override + {prompt} argv
├── launcher/               # NUEVO: herdr / inline / custom / auto
└── tui/
    ├── app.go              # MODIFICADO: sin detailsOpen; C; flujo ask
    ├── dashboard.go        # MODIFICADO: picker genérico + askBox
    └── ...
```

### Decisiones

- **Dispatch externo por config, no por código**: la estrategia de apertura es
  una opción de config (herdr hoy, tmux vía `launcher_cmd` mañana). Cambiar de
  multiplexer no exige release.
- **herdr como pane hermano, no como exec suspendido**: vroom sigue vivo y la
  consola de otros servicios sigue tail-eando mientras el agente trabaja en su
  pane. `--no-focus` por defecto (configurable).
- **`{prompt}` ocupa un argumento argv completo**: split de campos primero,
  sustitución después; prompts con espacios/comillas viajan intactos. Para
  herdr/custom el argv se compone con quoting POSIX (`'...'` con `'\''`).
- **Prompt vacío no lanza nada**: predecible; `esc` cancela.
- **Limpiar es de vista, no de disco**: el histórico en ficheros es una
  feature (re-adjunta); `C` solo resetea la vista y los offsets.
