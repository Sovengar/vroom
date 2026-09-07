# Spec: Build, Install y Tasks de mise

Cambio: `0003-feature-build-install-tasks`
Base: `docs/planning/0001-feature-vroom-tui/spec.md` + `0002-feature-services-dashboard/spec.md`

## Requirements

### R26: Comandos one-shot configurables

El manifiesto `.vroom.toml` SHALL aceptar dos campos opcionales de tipo string:
`install` (ejecutado con la tecla `i`) y `build` (ejecutado con la tecla `b`).
Ambos son comandos libres ejecutados con `sh -c` en el directorio del proyecto
(pueden invocar mise, pnpm, make, etc. — la elección es del consumidor). Si el
campo falta o está vacío, la tecla correspondiente SHALL notificarlo sin lanzar
nada. Estas acciones SHALL requerir un proyecto configurado (con `.vroom.toml`).

#### S26.1: build configurado

- GIVEN un proyecto con `build = "pnpm build"` en su manifiesto
- WHEN se pulsa `b`
- THEN se ejecuta `sh -c "pnpm build"` con workdir el proyecto y la salida
  se integra en la consola del proyecto

#### S26.2: build sin configurar

- GIVEN un proyecto configurado sin campo `build`
- WHEN se pulsa `b`
- THEN no se lanza nada y se notifica `no build command — set build = "..." in .vroom.toml`

#### S26.3: install es simétrico

- GIVEN un proyecto con/sin campo `install`
- WHEN se pulsa `i`
- THEN el comportamiento es idéntico a S26.1/S26.2 con kind `install`

#### S26.4: guards

- GIVEN el cursor sobre un grupo o un proyecto sin manifiesto
- WHEN se pulsa `b` o `i`
- THEN no se lanza nada y se notifica (`select a service to run build` /
  `No manifest — create a .vroom.toml to enable`)

### R27: Runner de jobs con salida en la consola

El sistema SHALL ejecutar los comandos one-shot escribiendo un banner de
inicio (`── vroom ▶ build: <comando> ──`) y su salida en **append** a los
ficheros de log del servicio, de modo que aparezcan en la pestaña Console
vía el tail existente (400ms). Al terminar SHALL escribir un banner de
veredicto (`── vroom ✓ build ok (3.2s) ──` o `── vroom ✗ build failed
(exit 1) ──`) y notificar lo mismo en la barra de mensajes. Mientras un job
esté en curso sobre un proyecto, otra acción one-shot sobre el MISMO
proyecto SHALL bloquearse con notificación; proyectos distintos SHALL poder
ejecutar jobs en paralelo.

#### S27.1: salida visible en la consola

- GIVEN un job de build en curso
- WHEN el tick de consola procesa los bytes nuevos
- THEN el banner, la salida del comando y el veredicto se ven en la
  pestaña Console con follow/ANSI/cap existentes

#### S27.2: veredicto con exit code

- GIVEN un comando que sale con código distinto de 0
- WHEN el job termina
- THEN la notificación indica `failed (exit N, duración)` y el banner
  `✗` queda en el log

#### S27.3: bloqueo concurrente por proyecto

- GIVEN un build en curso sobre un proyecto
- WHEN se pulsa `i` (u otra acción one-shot) sobre el mismo proyecto
- THEN se notifica `build already running in <proyecto>` y no se lanza nada
- WHEN se pulsa `b` sobre OTRO proyecto
- THEN el job corre en paralelo sin interferencias

#### S27.4: job huérfano

- GIVEN un job en curso
- WHEN la TUI se cierra
- THEN el proceso del job continúa (misma semántica de supervivencia que
  los servicios); documentado como limitación aceptada

### R28: Picker de tasks de mise

La tecla `t` SHALL abrir un modal centrado con los tasks de la sección
`[tasks.*]` del `mise.toml` del proyecto seleccionado (orden alfabético,
sin los marcados `hide = true`), mostrando nombre y descripción. El modal
SHALL capturar las teclas (modal): `j/k` navegan, `enter` cierra y ejecuta
`mise run <task>` como job (kind `task`), `esc` cierra sin ejecutar. Si el
proyecto no tiene `mise.toml` o no define tasks, SHALL notificarlo sin
abrir el modal. Listar tasks SHALL parsear el fichero (sin depender del
binario de mise); ejecutar un task requiere mise instalado.

#### S28.1: apertura y render

- GIVEN un proyecto configurado con tasks en mise.toml
- WHEN se pulsa `t`
- THEN se abre un modal con título `tasks — <proyecto>`, los tasks y el
  hint `j/k select · enter run · esc close`; el dashboard sigue visible
  alrededor

#### S28.2: ejecución desde el picker

- GIVEN el modal abierto con el cursor sobre un task
- WHEN se pulsa `enter`
- THEN el modal se cierra y se lanza `mise run <task>` como job kind
  `task` (R27 aplica: banner, salida, bloqueo)

#### S28.3: esc primero

- GIVEN el modal abierto
- WHEN se pulsa `esc`
- THEN el modal se cierra y la TUI sigue abierta (aunque details esté
  abierto)

#### S28.4: guards

- GIVEN un proyecto sin `mise.toml` (o con `[tasks]` vacío)
- WHEN se pulsa `t`
- THEN no se abre el modal y se notifica (`no mise.toml in <proyecto>` /
  `no tasks defined in <proyecto>/mise.toml`)

### R29: Remapeo de teclas

El dashboard SHALL remapear: `d` alterna el panel de detalles (antes `i`),
`i` lanza install (R26), `b` lanza build (R26), `t` abre el picker (R28),
`c` cicla el modo de stream de la consola (antes `t`), y `esc` cierra
picker → details → sale. La ayuda SHALL reflejar el nuevo mapa.

#### S29.1: d reemplaza a i para detalles

- GIVEN el dashboard visible
- WHEN se pulsa `d`
- THEN el panel de detalles alterna visibilidad; `i` ya no lo afecta

#### S29.2: c reemplaza a t para el stream

- GIVEN la pestaña Console activa
- WHEN se pulsa `c`
- THEN el modo cicla merged → stdout → stderr → merged

#### S29.3: orden de esc

- GIVEN picker abierto
- WHEN se pulsa `esc`
- THEN se cierra el picker primero (la TUI no sale)

#### S29.4: ayuda actualizada

- WHEN se renderiza la ayuda
- THEN menciona `d info`, `b build`, `i install`, `t tasks`, `c stream`
  y `l logfile`
