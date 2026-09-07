# Proposal: Build, Install y Tasks de mise

## Intent

Añadir acciones **one-shot** por proyecto al dashboard: `b` (build), `i` (install) y
`t` (picker de tasks de mise). La salida de estas acciones se integra en la consola
del proyecto (misma mecánica que los logs del servicio), con banner de inicio/fin,
notificación del resultado y bloqueo de ejecuciones concurrentes sobre el mismo
proyecto. El acoplamiento con mise es **opcional y explícito**: `build`/`install`
son comandos libres en `.vroom.toml` (el consumidor decide si usa `mise run build`,
`pnpm build` o lo que sea); el picker solo aparece si el proyecto tiene `mise.toml`.

## Scope

### In Scope
- Manifiesto: campos opcionales `install` y `build` (comandos one-shot, igual de
  libres que `command`).
- Runner de jobs one-shot: banner + salida en append a los logs del servicio
  (visibles en la pestaña Console vía el tail existente), notificación
  `build ok (3.2s)` / `build failed (exit 1)`, bloqueo de jobs concurrentes por
  proyecto.
- Picker de tasks: parseo de `[tasks.*]` del `mise.toml` del proyecto (sin
  depender del binario para listar), modal centrado con j/k/enter/esc; enter
  ejecuta `mise run <task>` como job.
- Keybindings: `i` info → `d` details; `i` ahora install; `t` stream → `c`
  console; `t` ahora picker; `esc` cierra picker → details → sale.

### Out of Scope
- Cancelar un job en marcha (quedan documentados como huérfanos si la TUI se
  cierra; misma semántica que los servicios).
- Tareas file-based (`mise-tasks/`, `.mise/tasks/`): el picker solo lista tasks
  TOML de `mise.toml`.
- Vista separada de "Jobs" con historial.
- Filtro de escritura en el picker.
- Jobs sobre proyectos sin `.vroom.toml` (requieren manifiesto como el resto).

## Capabilities

### New Capabilities
- `one-shot-jobs`: ejecución de comandos no-daemon (build/install/task) con
  salida integrada en la consola del proyecto y bloqueo por proyecto.
- `mise-tasks`: descubrimiento de tasks desde `mise.toml` y ejecución vía
  `mise run` (opcional: solo si el proyecto usa mise).

### Modified Capabilities
- `manifest`: dos campos opcionales nuevos (`install`, `build`).
- `tui-core`: remapeo de teclas (`i`/`d`/`t`/`c`) y modal picker con override
  del flujo de teclas mientras está abierto.

## Approach

### Cambios de módulos

```
internal/
├── mise/                  # NUEVO: parser de [tasks.*] de mise.toml
│   ├── mise.go
│   └── mise_test.go
├── manifest/manifest.go   # MODIFICADO: campos install/build opcionales
└── tui/
    ├── app.go             # MODIFICADO: jobs map, jobCmd, keybindings, picker
    ├── dashboard.go       # MODIFICADO: render del modal + overlay centrado
    └── styles.go          # MODIFICADO: estilo del cursor del picker
```

### Decisiones

- **Salida a los logs del servicio, no a un canal propio**: los jobs escriben en
  append a `stdout.log`/`stderr.log` del proyecto; el tick de consola de 400ms ya
  tail-ea esos ficheros, así que la integración es gratis (follow, ANSI, cap).
  Los banners `── vroom ▶ build: ... ──` hacen la salida auto-explicativa.
- **`sh -c` con exit code real**: igual que el Start de servicios; `cmd.Wait()`
  da el exit code para el veredicto sin parsear output.
- **Listar tasks parsea TOML, no spawn**: `mise tasks --json` sería más robusto
  (cubre file-tasks) pero hace que listar dependa del binario; vroom mantiene el
  principio de que mise es opcional. Solo *ejecutar* un task requiere mise.
- **Bloqueo por proyecto** (`m.jobs map[string]string`): evita doble build por
  despiste; proyectos distintos pueden correr jobs en paralelo.
