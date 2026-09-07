# Proposal: Wrap de consola, agente jcode y prefill del prompt ask

## Intent

Tres refinamientos del dashboard:

1. **Wrap de la consola**: las líneas de log más largas que el panel derecho se
   cortaban al alcanzar el borde derecho (perdiendo el final). Con soft wrap el
   contenido completo es visible: la línea continúa en la línea siguiente.
2. **`jcode` como agente built-in**: añadir jcode a la lista de agentes de ask
   AI. Su TUI interactiva no acepta prompt inicial (verificado en `jcode
   --help`: sin flag de prompt ni mensaje posicional), así que la invocación
   built-in es `jcode run {prompt}` (one-shot: responde y termina).
3. **Prefill del prompt ask**: al abrir el input de ask AI se asume que la
   petición va sobre la app seleccionada. El input se prellena con un template
   configurable en `[ask] prompt` (default builtin en inglés), con placeholders
   `{name}` (proyecto), `{dir}` (ruta del proyecto) y `{logs}` (directorio del
   servicio con `stdout.log`/`stderr.log`). `prompt = ""` desactiva el prefill.

## Scope

### In Scope
- `SoftWrap = true` en el viewport de la consola (bubbles v2: corte ANSI-aware
  con preservación de estilo en las líneas de continuación).
- Built-in `jcode run {prompt}` en `agents.Builtins()`.
- `AskConfig.Prompt` (clave `[ask] prompt`) con default builtin, soportando el
  escape hatch `prompt = ""` explícito (sin prefill).
- Expansión de placeholders en `startAskPrompt` (función pura, testeada).
- Modal askBox con ancho interior responsive (cap 88) para que el prefill sea
  legible.
- Actualización del mensaje "no AI agent found" con la lista vigente.

### Out of Scope
- Word-wrap inteligente (respetar límites de palabra): para logs el corte duro
  por celdas preserva la fidelidad del contenido.
- Templates de prompt por agente (el template es global de la sección `[ask]`).
- Prompt inicial en la TUI interactiva de jcode (no existe en jcode hoy).

## Capabilities

### New Capabilities
- `console-softwrap`: las líneas de log largas se envuelven al ancho del panel.
- `ask-prompt-template`: prefill del input de ask desde config con placeholders.

### Modified Capabilities
- `ask-ai`: agente built-in `jcode` (`run` one-shot) y mensaje de "sin agentes".
- `realtime-console`: render con wrap (sin truncado horizontal).

## Approach

### Decisiones

- **SoftWrap del viewport, no pre-wrap manual**: `ansi.Cut` (usado por el
  softWrap de bubbles v2) re-emite las secuencias de escape previas al corte
  (`truncateLeft` escribe los escapes de la zona ignorada), así que una línea
  ERROR roja sigue roja tras el corte. `ansi.Hardwrap` en cambio NO re-emite el
  estado SGR → descartado. Además el viewport re-calcula el wrap en resize sin
  código extra.
- **jcode one-shot**: la única vía verificada de pasarle el prompt. El
  despacho por herdr/inline sigue intacto; `run` responde y termina.
- **Template global con placeholders expandibles**: `{name}`/`{dir}`/`{logs}`;
  los placeholders desconocidos quedan tal cual (sin sorpresas).
- **Default builtin en inglés**: el prompt prefilled asume el contexto de la
  app ("Given the app … with logs in …, ") y el usuario escribe la petición al
  final (cursor al final tras el prefill).
