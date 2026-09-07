# Spec: Ask AI con dispatch configurable, limpiar consola y detalles fijos

Cambio: `0004-feature-ask-ai-clear-console`
Base: `0001-feature-vroom-tui`, `0002-feature-services-dashboard`, `0003-feature-build-install-tasks`

## Requirements

### R30: Panel de detalles fijo

El panel de detalles SHALL mostrarse siempre que haya espacio
(`rightW >= 40` y alto suficiente) y SHALL carecer de toggle. La tecla
`d` SHALL eliminarse del mapa de teclas. `esc` SHALL salir de la TUI
cuando no haya ningún modal abierto (prompt ask o picker).

#### S30.1: sin toggle

- GIVEN el dashboard visible con espacio suficiente
- WHEN se renderiza
- THEN el panel de detalles está visible; `d` no hace nada y `esc` sale

#### S30.2: auto-hide por espacio

- GIVEN un ancho donde `rightW < 40`
- WHEN se renderiza
- THEN el panel de detalles no se muestra (comportamiento S18.3 intacto)

### R31: Limpiar consola en memoria

La tecla `C` sobre un servicio configurado SHALL vaciar los buffers en
memoria de su consola (stdout/stderr/merged) y colocar los offsets de
tail en el EOF actual de ambos ficheros. Los ficheros de log SHALL NO
modificarse. La vista SHALL quedar en el placeholder "No logs available"
y el siguiente tick SHALL NOT reintroducir el contenido borrado.

#### S31.1: limpieza efectiva

- GIVEN una consola con contenido y logs en disco
- WHEN se pulsa `C`
- THEN la vista queda limpia, los offsets apuntan al EOF, el siguiente
  delta es vacío y el notify dice `console cleared`

#### S31.2: histórico intacto

- GIVEN un `C` ejecutado
- WHEN se inspeccionan los ficheros de log
- THEN su contenido previo sigue presente (reaparece al reabrir la TUI)

#### S31.3: guards

- GIVEN un grupo seleccionado o un proyecto sin manifiesto
- WHEN se pulsa `C`
- THEN solo se notifica (`select a service to clear its console` /
  `No manifest — create a .vroom.toml to enable`)

### R32: Ask AI con dispatch configurable

La tecla `a` sobre una fila de proyecto SHALL iniciar el flujo de ask
AI: selección de agente y captura de un prompt, seguidas del despacho
del agente con el prompt como chat nuevo, según la estrategia definida
en el config global `~/.config/vroom/config.toml` (override
`$VROOM_CONFIG`; `$XDG_CONFIG_HOME` respetado). El despacho SHALL ser
externo y configurable sin recompilar vroom.

Agentes built-in (invocaciones con `{prompt}` como argumento argv
completo): `opencode --prompt {prompt}`, `pi {prompt}`,
`hermes chat -q {prompt}`. La sección `[ask.agents.*]` del config, si
existe, SHALL reemplazarlos. Solo se ofrecen los agentes cuyo binario
esté en PATH.

Estrategias: `herdr` (pane/tab nuevo del multiplexer con el agente
dentro), `inline` (suspende vroom y corre el agente en primer plano) y
`custom` (plantilla shell `launcher_cmd` con `{dir}`/`{agent}`/`{cmd}`
quoteados). El valor `auto` (default) SHALL usar herdr si `HERDR_ENV=1`
y el binario está en PATH; si no, inline. La estrategia `herdr`
explícita sin sesión herdr SHALL caer a inline notificándolo.

#### S32.1: flujo con varios agentes

- GIVEN agentes ≥ 2 instalados y una fila de proyecto seleccionada
- WHEN se pulsa `a`
- THEN se abre un picker con los agentes instalados; `enter` abre el
  modal de prompt `ask <agente> — <proyecto>`

#### S32.2: flujo con un agente

- GIVEN exactamente 1 agente instalado
- WHEN se pulsa `a`
- THEN se abre directamente el modal de prompt

#### S32.3: despacho herdr

- GIVEN `launcher` resuelto a herdr y un prompt no vacío
- WHEN se confirma con `enter`
- THEN vroom ejecuta `herdr pane split --current --direction <dir>
  --cwd <proyecto> [--no-focus]` (o `tab create` si `target = "tab"`),
  extrae el `pane_id` del JSON y lanza
  `herdr pane run <id> "<comando del agente quoteado>"`; vroom sigue
  vivo y notifica `<agente> → herdr pane <id>`

#### S32.4: despacho inline

- GIVEN estrategia inline
- WHEN se confirma el prompt
- THEN vroom se suspende (tea.ExecProcess), el agente corre en primer
  plano con cwd del proyecto y al salir la TUI se restaura con el
  notify `<agente> closed`

#### S32.5: quoting seguro

- GIVEN un prompt con espacios, comillas o metacaracteres de shell
- WHEN se compone el comando para herdr/custom
- THEN el prompt viaja como un único argumento sin interpretación de
  shell (quoting POSIX)

#### S32.6: guards del prompt

- GIVEN el modal de prompt abierto
- WHEN se confirma con `enter` y el prompt está vacío
- THEN no se lanza nada y se notifica `empty prompt — type a request or
  esc to cancel`; `esc` cierra el modal sin salir de la TUI

#### S32.7: sin agentes

- GIVEN ningún binario de agente en PATH
- WHEN se pulsa `a`
- THEN se notifica `no AI agent found in PATH (opencode, pi, hermes)`
  sin abrir nada

#### S32.8: config malformada

- GIVEN un config.toml inválido (enums, campos requeridos)
- WHEN vroom arranca
- THEN aplica defaults y notifica el error de parseo

#### S32.9: prioridad de esc

- GIVEN el modal de prompt o el picker abiertos
- WHEN se pulsa `esc`
- THEN se cierra el modal y la TUI continúa (en ese orden si ambos
  estuvieran abiertos)
