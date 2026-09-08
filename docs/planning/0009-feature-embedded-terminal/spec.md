# Spec: Terminal embebida con "!" (PTY + emulador VT, sesión persistente)

Cambio: `0009-feature-embedded-terminal`
Base: 0008 (activa la reserva de `!` R47; el modal entra primero en la cadena
de captura R50); numeración continúa desde 0008 (última R51)

## Requirements

### R52: Tecla "!" abre el modal de terminal embebida

Al pulsar `!` (tecla universal, NO remapeable — reservada por 0008 R47) la TUI
SHALL abrir un modal overlay centrado (mismo patrón que askBox) que contiene
una terminal interactiva: el shell del usuario corriendo en un PTY, renderizado
por un emulador VT dentro del box. El modal SHALL tener título `terminal —
<basename del cwd de la sesión>`, el grid del emulador y el hint `ctrl+q hide ·
session keeps running`.

- El grid SHALL tener el ancho interior tipo ask (`askInnerW`, mínimo 20) y
  alto `m.height - 8` (mínimo 6, máximo 40).
- Con el modal abierto, la TUI SHALL NO suspenderse: el polling, los jobs y la
  TUI siguen vivos debajo del overlay.

#### S52.1: abrir con "!"

- GIVEN la TUI en el dashboard
- WHEN se pulsa `!`
- THEN `termOpen=true`, existe `term` (sesión nueva con `$SHELL`), el loop de
  lectura del PTY queda armado y el box renderiza título + grid + hint

#### S52.2: cwd de la sesión

- GIVEN el cursor sobre un proyecto con manifiesto
- WHEN se pulsa `!`
- THEN la sesión arranca con `cmd.Dir` = ruta del proyecto
- GIVEN el cursor sobre un header de grupo (o sin selección)
- WHEN se pulsa `!`
- THEN la sesión arranca con `cmd.Dir` = root del workspace

#### S52.3: sesión previa se reutiliza

- GIVEN el modal oculto con sesión viva
- WHEN se pulsa `!`
- THEN se re-muestra el MISMO `*termSession` (sin crear otra) y sin escribir
  al PTY

#### S52.4: help

- GIVEN defaults
- WHEN se renderiza la help full
- THEN contiene el segmento fijo `! shell` (entre `a ask` y `C clear`)

### R53: Shell y entorno de la sesión

La sesión SHALL lanzar `$SHELL` sin argumentos (interactivo: lee su rc con TTY
de fondo); `$SHELL` vacía SHALL degradar a `sh`. El entorno SHALL heredarse y
SHALL garantizarse `TERM` (fallback `xterm-256color`). En Unix el shell SHALL
arrancar con `Setsid`+`Setctty` (job control completo dentro de la terminal y
pgid propio = pid).

#### S53.1: resolución de shell

- GIVEN `SHELL=/bin/zsh`
- WHEN se crea la sesión
- THEN `argv[0] = "/bin/zsh"`
- GIVEN `SHELL` vacía
- WHEN se crea la sesión
- THEN `argv[0] = "sh"`

### R54: Forwarding de teclas al PTY

Con el modal abierto (`termOpen`), `handleKey` SHALL capturar las teclas ANTES
que cualquier otro modal y forwarderlas al PTY vía el keymap del emulador
(`vt.SendKey`: ctrl, alt, flechas, F-keys, keypad, shift+tab). Las únicas
excepciones SHALL ser `ctrl+q` (R56). En particular `q`, `!`, `esc` y `ctrl+c`
SHALL llegar al shell (SIGINT incluido) y SHALL NO disparar acciones de vroom.

#### S54.1: las teclas van al shell

- GIVEN el modal abierto
- WHEN se pulsan `q`, `!`, `esc` o `ctrl+c`
- THEN vroom NO sale, el modal NO se cierra y el PTY recibe la secuencia ANSI
  correspondiente (`q`, `!`, `\x1b`, `\x03`)

#### S54.2: encoding de especiales

- GIVEN el modal abierto
- WHEN se pulsan `enter`, `up`, `ctrl+a`, `alt+a`, `pgup`, `backspace`
- THEN el PTY recibe `\r`, `\x1b[A`, `\x01`, `\x1ba`, `\x1b[5~`, `\x7f`

#### S54.3: output del shell alimenta el emulador

- GIVEN el modal abierto
- WHEN el PTY produce bytes
- THEN `ptyDataMsg` los vuelca en el emulador y re-arma el loop de lectura

### R55: Render y resize

El box SHALL renderizar el grid del emulador (`Render()` del buffer, ANSI
intacto) con el cursor dibujado como bloque invertido en `CursorPosition()`
(el Render del buffer no lo incluye). Al cambiar el tamaño de la ventana con
el modal abierto, el emulador y el PTY SHALL redimensionarse (SIGWINCH al
shell) si las dims objetivo cambiaron.

#### S55.1: cursor visible

- GIVEN el shell con el cursor en (x, y)
- WHEN se renderiza el box
- THEN la línea y contiene un bloque invertido en la columna x

#### S55.2: resize

- GIVEN el modal abierto (o la sesión viva oculta)
- WHEN llega `WindowSizeMsg` con dims nuevas
- THEN el emulador y el PTY se redimensionan a las dims objetivo

### R56: Ciclo de vida de la sesión

La sesión SHALL ser persistente: `ctrl+q` oculta el modal y el shell SIGUE
vivo (el read loop sigue consumiendo su output en el emulador). La salida del
shell SHALL detectarse con el reaper (`xpty.WaitProcess`, armado al abrir la
sesión — el master del PTY no emite EOF al morir el shell mientras el pty
sostiene el slave) y SHALL: cerrar el modal, apagar la sesión (close PTY +
emu + SIGKILL al pgid, idempotente) y notificar. Al salir de vroom (`q`/`esc`)
con sesión viva, ésta SHALL apagarse antes del quit.

#### S56.1: ocultar no mata

- GIVEN el modal abierto con sesión viva
- WHEN se pulsa `ctrl+q`
- THEN `termOpen=false`, la sesión sigue viva y el PTY no recibe nada
- WHEN se pulsa `!` de nuevo
- THEN el modal re-aparece con el MISMO estado de emulador (misma sesión)

#### S56.2: exit del shell

- GIVEN el shell corriendo
- WHEN el usuario teclea `exit` + enter (o el shell muere)
- THEN el reaper entrega `ptyExitMsg`: modal cerrado, sesión apagada y
  mensaje `terminal closed` en la barra de estado

#### S56.3: exit code != 0

- GIVEN el shell que termina con código N != 0
- WHEN llega `ptyExitMsg`
- THEN el mensaje es `terminal exited (N)`

#### S56.4: cleanup al salir de vroom

- GIVEN sesión viva (modal abierto u oculto)
- WHEN se pulsa `q` o `esc`
- THEN la sesión se apaga (PTY cerrado, pgid SIGKILL) antes de `tea.Quit`
- GIVEN sin sesión
- WHEN se pulsa `q` o `esc`
- THEN es `tea.Quit` directo (comportamiento actual)

#### S56.5: modales y prioridad

- GIVEN el modal de terminal abierto
- WHEN llega cualquier tecla
- THEN va al PTY sin pasar por ask/picker/filtro (el terminal modal es
  exclusivo: no puede convivir con los otros modales abiertos)

## Out of Scope (recordatorio)

- Múltiples sesiones, forwarding de mouse, `[terminal]` en config, cambiar el
  cwd de la sesión tras crearla (ver proposal).
