# Proposal: Dashboard estilo IntelliJ Services

## Intent

Reestructurar la TUI de 3 vistas apiladas a full-screen (lista → detalle → logs) en un
**dashboard único** similar al panel "Services" de IntelliJ: navegación de proyectos
siempre visible a la izquierda (ancho fijo, scroll automático), panel de detalles a la
derecha (conmutado con `enter` para pantallas pequeñas), y un panel inferior con
pestañas `Console` (stdout+stderr en tiempo real, mergeados) y `Threads` (a nivel OS,
sin debugger). El foco del proyecto sigue siendo ejecutar, parar y ver logs; el debug
serio se hace con attach en IDE (IntelliJ/nvim) y queda explícitamente fuera de alcance.

## Scope

### In Scope
- Layout dashboard: árbol de grupos/servicios a la izquierda con ancho fijo y
  scroll automático cíclico; panel de detalles a la derecha con toggle `enter`;
  panel inferior con pestañas Console/Threads.
- Pestaña activa marcada visualmente (color distinto).
- Consola en tiempo real: tail incremental (solo bytes nuevos), streams mergeados
  por defecto, toggle para aislar stdout/stderr, auto-follow con pausa al hacer
  scroll-up y reactivación con `G`, strip de ANSI, buffer con cap por servicio.
- Pestaña Threads a nivel OS vía `/proc/<pid>/task`: nombre, TID, estado y CPU%
  (delta entre ticks). Universal para cualquier lenguaje, sin debugger.
- Panel de detalles: campos actuales + rama de git del proyecto (leída de
  `.git/HEAD`, sin spawnar `git`).
- Keybindings: `l` abre los ficheros de log en el editor (antes `o`, que queda
  como alias); `r` sigue refrescando; `enter` alterna el panel de detalles.

### Out of Scope
- Debugger (DAP/delve/JDWP), variables, stack frames, breakpoints. Para debug
  serio: attach en IntelliJ o nvim.
- Colapsar/expandir grupos en el árbol.
- Rotación o compresión de logs.
- Threads en Windows (stub documentado, igual que el resto del paquete process).
- Merge cronológico exacto entre stdout y stderr (dentro del mismo tick el orden
  es aproximado; documentado como limitación aceptada).

## Capabilities

### New Capabilities
- `services-dashboard`: layout único con árbol fijo, panel de detalles conmutable
  y panel inferior con pestañas.
- `realtime-console`: tail incremental con offset por stream, buffers con cap,
  modos merged/stdout/stderr y follow-mode.
- `threads-os`: muestreo de hilos via `/proc` con cálculo de CPU% por delta.
- `git-info`: lectura de la rama actual desde `.git/HEAD` (incluye worktrees).

### Modified Capabilities
- `tui-core`: se elimina la navegación por vistas apiladas; el modelo pasa a un
  único dashboard con pestañas y panel de detalles.
- `log-viewing`: la vista de logs a pantalla completa desaparece; su
  funcionalidad se integra en la pestaña Console y en `l` (editor externo).

## Approach

### Cambios de módulos

```
internal/
├── tail/                 # NUEVO: lectura incremental de ficheros de log
│   ├── tail.go           # ReadNew(offset), StripANSI, cap de buffer
│   └── tail_test.go
├── process/
│   ├── threads_unix.go   # NUEVO (build tag unix): ListThreads via /proc
│   ├── threads_windows.go# NUEVO (stub documentado)
│   └── threads_unix_test.go
├── gitinfo/              # NUEVO: rama actual desde .git/HEAD
│   ├── gitinfo.go
│   └── gitinfo_test.go
└── tui/
    ├── app.go            # MODIFICADO: modelo dashboard (tabs, detalles, focus)
    ├── dashboard.go      # NUEVO: composición del layout
    ├── projectlist.go    # MODIFICADO: renderiza columna de árbol con scroll
    ├── console.go        # NUEVO: estado de consola por servicio + pestaña
    ├── threadsview.go    # NUEVO: pestaña de threads
    ├── serviceview.go    # MODIFICADO: panel de detalles (+ rama git)
    ├── logview.go        # ELIMINADO (integrado en Console)
    └── styles.go         # MODIFICADO: estilos de pestaña activa/inactiva
```

### Layout

```
┌──────────────┬──────────────────────────────────────────┐
│ árbol (fijo) │ detalles (toggle con enter)              │
├──────────────┼──────────────────────────────────────────┤
│ (scroll      │ [1 Console] [2 Threads]                  │
│  automático) │ contenido de la pestaña activa           │
├──────────────┴──────────────────────────────────────────┤
│ help / mensajes                                         │
└─────────────────────────────────────────────────────────┘
```

Con `enter` se oculta el panel de detalles y el panel de pestañas crece. En
anchos mínimos el detalle se auto-oculta aunque esté "abierto".

### Decisiones

- **Tail incremental con offset por stream**: cada servicio mantiene su estado
  de consola (`offset` + buffer por stream) en memoria mientras corre la TUI;
  navegar entre servicios no re-lee el fichero entero.
- **Tick de consola más rápido** (400ms) que el tick de estados (2s): la
  sensación de "tiempo real" en logs no debe depender del polling de liveness.
- **CPU% por delta de ticks**: `utime+stime` de `/proc/<pid>/task/<tid>/stat`
  entre dos muestras (ventana = tick de 2s), `USER_HZ=100` en Linux.
- **Strip de ANSI**: los logs de servicios (p.ej. Spring Boot) traen color; el
  viewport no interpreta ANSI y rompería el ancho. Se limpia al hacer append.
- **Rama git sin spawn**: leer `.git/HEAD` (directorio o fichero worktree) es
  instantáneo y testeable; sin dependencia de que `git` esté instalado.
