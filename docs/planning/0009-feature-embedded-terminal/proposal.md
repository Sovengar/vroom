# Proposal: Terminal embebida con "!" (PTY + emulador VT, sesión persistente)

## Intent

vroom hoy solo ejecuta comandos en background (jobs one-shot `b`/`i`/`t`) o
suspendiéndose (`l` logs, launcher inline del ask). No hay forma de correr un
comando interactivo sin salir de la TUI. Este cambio añade la tecla `!`
(reservada por 0008 R47 como "shell interactivo, feature futura"): un modal
overlay con una **terminal de verdad dentro de vroom** — el shell del usuario
en un PTY, renderizado por un emulador VT dentro del box. Sin suspender la
TUI: vim, htop o un pager funcionan dentro del modal mientras vroom sigue vivo
debajo.

La sesión es **persistente**: `ctrl+q` oculta el modal y el shell sigue
corriendo en background (como un pane oculto); `!` lo vuelve a mostrar con todo
el estado intacto (vim abierto, variables exportadas, directorio actual).

Cambio: `0009-feature-embedded-terminal`
Base: 0008 (usa la reserva de `!` en R47 y el orden de modales en R50);
numeración continúa desde 0008 (última R51).

## Scope

### In Scope
- `internal/tui/term.go` (NUEVO): `termSession` (PTY + emulador VT + pump),
  `resolveShell()` ($SHELL, fallback `sh`), loop de lectura (readPtyCmd),
  reaper (waitCmd), `termBox()` (modal), dims del grid.
- `internal/tui/term_unix.go` / `term_windows.go` (NUEVOS): `Setsid`+`Setctty`
  para job control completo y pgid propio; `killSessionGroup` (SIGKILL al
  grupo como backstop; no-op en Windows, ConPTY cierra solo).
- `internal/tui/app.go`: campos `termOpen`/`term`, tecla universal `!`,
  `termKey` (captura de teclas del modal), mensajes `ptyDataMsg`/`ptyEOFMsg`/
  `ptyExitMsg`, resize en WindowSizeMsg, overlay en `View()`, cleanup en quit
  (`quitCmd`), segmento `! shell` en la help full.
- Deps: `github.com/charmbracelet/x/xpty` (PTY Unix/ConPTY) +
  `github.com/charmbracelet/x/vt` (emulador VT100/ANSI con alt-screen,
  scrollback y keymap).
- Tests: `term_test.go` (encoding de teclas vía emulador, ciclo de vida con
  PTY stub, integración con sh real en PTY, quit/reaper, box).

### Out of Scope
- Múltiples sesiones (una por proyecto o pestañas de terminal): una sola
  sesión global por instancia de la TUI.
- Forwarding de mouse al PTY (la rueda dentro del modal no scrollea).
- Config `[terminal] shell = "..."`: v1 usa $SHELL/sh; remapeable a futuro.
- Cambiar el cwd de una sesión tras crearla (el `cd` es del shell).
- Scrollback navegable con teclas de vroom (el emulador mantiene scrollback
  interno; programas full-screen lo manejan solos).

## Capabilities

### New Capabilities
- `embedded-terminal`: modal `!` con shell interactivo en PTY renderizado por
  emulador VT; sesión persistente al ocultar; reaper y cleanup garantizados.

### Modified Capabilities
- `tui-core` (0008 R50): cadena de modales gana el terminal modal (prioridad
  sobre los demás); `q`/`esc` pasan por `quitCmd` que mata la sesión viva
  antes de salir.
- `keybindings` (0008 R47): la tecla reservada `!` deja de ser "feature
  futura" y se activa; sigue NO remapeable.

## Approach

### Decisiones

- **PTY + emulador VT, no tea.ExecProcess**: el usuario pidió explícitamente
  una terminal *dentro* de vroom. ExecProcess suspende la TUI (los logs y el
  polling se congelan); con PTY+VT vroom sigue vivo, el shell tiene TTY real
  (job control, vim/htop, colores) y la sesión sobrevive al ocultado.
- **`ctrl+q` para ocultar, no `!` ni `esc`**: dentro del shell ambas teclas
  son necesarias (`!` history expansion, `esc` para vim). `ctrl+q` no colisiona
  con nada y es el único keybind de vroom activo con el modal abierto; todo lo
  demás viaja al PTY (incluido `ctrl+c` → SIGINT al shell).
- **Reaper por wait, no por EOF del master**: el master del PTY no emite EOF
  al morir el shell mientras el propio pty sostiene el slave fd; la salida se
  detecta con `xpty.WaitProcess` (armado al abrir, junto al read loop), que
  además resuelve el caso Windows (ConPTY, donde `cmd.Wait` no funciona).
- **Mutex solo para lifecycle**: el emulador se muta exclusivamente en el
  hilo de Update (Write/SendKey/Resize/Render); la goroutine `pump` solo
  copia el input pipe del emulador al PTY. Retener el lock durante SendKey
  (escritura bloqueante al pipe) deadlockearía con el pump — aprendido en
  implementation.
- **`Setsid`+`Setctty` en el shell**: job control real dentro de la terminal
  (cada app su pgid, ctrl+c por app) y un pgid propio para rematar el árbol
  con SIGKILL al salir de vroom (patrón de stopCmd, S8.2).
- **Grid = modal interior**: dims tipo ask (ancho `askInnerW`, alto
  `m.height-8` con caps 20×6..40); resize en WindowSizeMsg redimensiona
  emulador + PTY (SIGWINCH al shell). El cursor se dibuja como bloque
  invertido (el Render del buffer no lo incluye).
- **cwd fijo al crear**: proyecto seleccionado, o root del workspace si el
  cursor está sobre un grupo; el título del modal muestra el basename.

## Impact

- Sin cambios de datos ni de schema (`meta.json`, manifiesto: nada).
- Deps nuevas de la familia charm (`x/xpty`, `x/vt`) — experimentales pero
  aisladas tras `termSession`; el resto de la TUI no las toca.
- La help full cambia (gana `! shell`): `TestHelpDefaultsDerived` se actualiza
  al nuevo texto canónico.
- Riesgo medio: primera integración con PTY/emulador en el proyecto; los
  modales y el keymap quedan intactos y los tests existentes pasan sin
  cambios (salvo la help).
