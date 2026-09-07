# Spec: Wrap de consola, agente jcode y prefill del prompt ask

Cambio: `0005-feature-console-wrap-ask-prefill`
Base: `0002-feature-services-dashboard`, `0004-feature-ask-ai-clear-console`

## Requirements

### R33: Soft wrap de la consola

Las líneas de log del viewport de consola SHALL envolverse (soft wrap) al
ancho del panel derecho en vez de truncarse al borde. El contenido completo
SHALL ser visible recorriendo las líneas de continuación. El wrap SHALL
preservar las secuencias ANSI: una línea coloreada SHALL continuar con su
color tras el corte. El comportamiento de scroll/follow (S19.4) SHALL
mantenerse (el total de líneas computa las líneas envueltas).

#### S33.1: línea larga envuelta

- GIVEN una línea de log con más celdas que `rightW`
- WHEN se renderiza la consola
- THEN ninguna línea renderizada excede `rightW` y el final del contenido
  es visible en una línea de continuación

#### S33.2: estilo preservado

- GIVEN una línea coloreada (p.ej. ERROR por highlightConsole) más larga
  que `rightW`
- WHEN se envuelve
- THEN la línea de continuación conserva la secuencia ANSI del estilo

#### S33.3: resize

- GIVEN la consola con contenido envuelto
- WHEN la ventana cambia de tamaño
- THEN el wrap se recalcula al nuevo ancho sin contenido residual

### R34: Agente built-in jcode

La lista de agentes built-in de ask AI SHALL incluir `jcode` con la
invocación `jcode run {prompt}`: la TUI interactiva de jcode no acepta
prompt inicial, por lo que el built-in usa `run` (one-shot: responde y
termina). El mensaje de "sin agentes" SHALL listar los built-in vigentes:
`no AI agent found in PATH (opencode, pi, hermes, jcode)`.

#### S34.1: built-in presente

- GIVEN sin sección `[ask.agents]` en el config
- WHEN se resuelve la lista de agentes
- THEN la lista incluye 4 built-in: opencode, pi, hermes, jcode

#### S34.2: expansión del prompt

- GIVEN el agente jcode y un prompt no vacío
- WHEN se despacha
- THEN el argv es `["jcode", "run", "<prompt>"]` con el prompt como un
  único argumento

#### S34.3: sin agentes

- GIVEN ningún binario de agente en PATH
- WHEN se pulsa `a`
- THEN se notifica `no AI agent found in PATH (opencode, pi, hermes, jcode)`

### R35: Prefill del prompt ask

Al abrir el modal de prompt de ask AI, el input SHALL prellenarse con el
template `[ask] prompt` del config global (default builtin en inglés:
`Given the app {name} with logs in {logs}, `). Los placeholders SHALL
expandirse: `{name}` → nombre del proyecto, `{dir}` → ruta del proyecto,
`{logs}` → directorio del servicio con `stdout.log`/`stderr.log`. El cursor
SHALL quedar al final del prefill. Un `prompt = ""` explícito en el config
SHALL desactivar el prefill (input vacío). Un config malformado SHALL
devolver el template default.

#### S35.1: prefill default

- GIVEN sin clave `prompt` en el config
- WHEN se abre el modal de prompt sobre un proyecto
- THEN el input contiene el template builtin expandido con el nombre del
  proyecto y el directorio de logs, y el cursor está al final

#### S35.2: template custom

- GIVEN `prompt = "About {name} in {dir}, logs at {logs}: "` en el config
- WHEN se abre el modal
- THEN el input es el template con los tres placeholders expandidos

#### S35.3: sin prefill

- GIVEN `prompt = ""` explícito en el config
- WHEN se abre el modal
- THEN el input queda vacío (comportamiento previo a R35)

#### S35.4: config malformado

- GIVEN un config.toml malformado
- WHEN vroom arranca y se abre el modal de prompt
- THEN el prefill usa el template default

#### S35.5: placeholder desconocido

- GIVEN un template con un placeholder no reconocido (p.ej. `{foo}`)
- WHEN se expande
- THEN `{foo}` queda literal en el input

#### S35.6: ancho del modal

- GIVEN una pantalla suficientemente ancha
- WHEN se renderiza el modal de prompt
- THEN el ancho interior crece con la pantalla hasta un cap (88) para que
  el prefill sea legible; en pantallas estrechas se ajusta sin desbordar
