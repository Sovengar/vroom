# Especificación: Dashboard estilo IntelliJ Services

## Propósito

Dashboard único en la TUI: árbol de proyectos a la izquierda (ancho fijo, scroll
automático), panel de detalles a la derecha (toggle `enter`), panel inferior con
pestañas Console (logs en tiempo real) y Threads (nivel OS). La numeración
continúa la del spec 0001 (última: R17).

---

## Capability: services-dashboard

### R18: Layout del dashboard

El sistema SHALL renderizar un único dashboard con tres regiones: columna de
árbol a la izquierda con **ancho fijo** (30 columnas), panel de detalles a la
derecha y panel inferior con pestañas bajo el árbol. El resto de la altura va al
panel de pestañas.

#### S18.1: Estructura por defecto

- GIVEN la TUI iniciada en un terminal ≥ 100x30
- WHEN se renderiza
- THEN el árbol ocupa la columna izquierda completa, el panel de detalles la
  esquina superior derecha y el panel de pestañas el resto

#### S18.2: Toggle de detalles con i

- GIVEN el dashboard visible
- WHEN se pulsa `i`
- THEN el panel de detalles se oculta y el panel de pestañas crece; otra
  `i` lo vuelve a mostrar

#### S18.3: Auto-ocultado en anchos mínimos

- GIVEN un terminal con ancho < 60
- WHEN se renderiza con el panel de detalles "abierto"
- THEN el panel no se dibuja (queda marcado como oculto por espacio)

#### S18.4: Pestaña activa marcada

- GIVEN el panel de pestañas visible
- WHEN se renderiza
- THEN la pestaña activa (Console/Threads) se pinta con estilo destacado
  (color/inversión) distinto de las inactivas

#### S18.5: Scroll automático del árbol

- GIVEN más entradas que líneas visibles en el árbol
- WHEN el cursor navega fuera del rango visible
- THEN la ventana de líneas se desplaza para mantener el cursor visible y
  la navegación sigue siendo cíclica (S12.1)

### R22: Keybindings del dashboard

El sistema SHALL mapear las teclas: `1`/`2` seleccionan las pestañas
Console/Threads, `tab` cicla pestañas, `t` cicla el modo de stream de la
consola, `i` alterna el panel de info/detalles, `enter` colapsa/expande el
grupo seleccionado (R24), `l` y `o` abren los ficheros de log en el editor,
`r` refresca estados, `s`/`R` mantienen su función de 0001 (sobre un grupo,
`s` aplica el toggle a todos sus miembros), `q`/`ctrl+c` salen, `esc` cierra
el panel de detalles si está abierto (si no, sale).

#### S22.1: `l` abre el editor (logfile)

- GIVEN un servicio configurado seleccionado
- WHEN se pulsa `l`
- THEN se abre el editor con ambos logs (mismo comportamiento del antiguo `o`,
  R17 de 0001: nvim con `-O`, foco según stream activo)

#### S22.2: `r` mantiene el refresh

- WHEN se pulsa `r`
- THEN se re-ejecuta el refresh de estados (R13/S-T6) sin cambiar de vista

#### S22.3: esc cierra detalles primero

- GIVEN el panel de detalles abierto
- WHEN se pulsa `esc`
- THEN el panel se cierra y la TUI sigue abierta

### R24: Grupos seleccionables y colapsables

El árbol SHALL incluir los headers de grupo como filas navegables. `enter`
sobre un grupo seleccionado SHALL alternar colapsado/expandido; colapsado,
sus miembros se ocultan y el header muestra el conteo
`nombre (running/total)`, p.ej. `vsocial-backend (3/8)`. El panel de info
sobre un grupo SHALL mostrar el resumen (conteo y miembros) y la consola
un placeholder. `s` sobre un grupo SHALL aplicar el toggle contextual a
sus miembros (si hay parados, arranca los parados; si no, para los running).

#### S24.1: Selección de grupo

- GIVEN el árbol con grupos
- WHEN el cursor navega sobre un header de grupo
- THEN el grupo queda seleccionado (la consola muestra placeholder y el
  panel de info el resumen del grupo)

#### S24.2: Colapsar con enter

- GIVEN un grupo seleccionado con N miembros y R en ejecución
- WHEN se pulsa `enter`
- THEN los miembros se ocultan y el header muestra `nombre (R/N)`; otro
  `enter` los expande; el header conserva su posición de cursor

#### S24.3: Toggle de grupo con s

- GIVEN un grupo seleccionado con miembros stopped y running mezclados
- WHEN se pulsa `s`
- THEN se arrancan los stopped (los running quedan como están); si no hay
  stopped, se paran los running

### R25: Filas del árbol compactas

El árbol SHALL renderizar cada proyecto con solo el glifo de estado y el
nombre (sin el texto "running"/"stopped": el panel de info y la consola
transportan el detalle). Los proyectos sin manifiesto o con manifiesto
inválido SHALL mostrar un icono de "roto" (⚠) en lugar del punto.

#### S25.1: Fila compacta

- GIVEN servicios en distintos estados
- WHEN se renderiza el árbol
- THEN cada fila es glifo + nombre; el texto del estado no aparece

#### S25.2: Icono roto para unconfigured

- GIVEN un proyecto sin `.vroom.toml`
- WHEN se renderiza su fila
- THEN el glifo es ⚠ (ámbar), no el punto de stopped

---

## Capability: realtime-console

### R19: Consola en tiempo real

El sistema SHALL tailar los ficheros de log del servicio seleccionado de forma
**incremental** (offset por stream; solo bytes nuevos) en un tick propio de
400ms, y mostrarlos en la pestaña Console con auto-follow al fondo. El contenido
por servicio y stream SHALL estar limitado (buffer con cap de ~192KB).

#### S19.1: Append incremental

- GIVEN la consola del servicio A visible con offset O sobre stdout.log
- WHEN el fichero crece N bytes
- THEN solo los N bytes nuevos se añaden al buffer y al viewport, sin resetear
  el scroll previo

#### S19.2: Cambio de servicio conserva buffers

- GIVEN la consola del servicio A con contenido acumulado
- WHEN el cursor navega al servicio B y de vuelta a A
- THEN A conserva su buffer (no se re-lee el fichero completo) y el offset
  continúa desde el último punto

#### S19.3: Modos de stream

- GIVEN la consola visible
- WHEN se pulsa `t`
- THEN el modo cicla `merged → stdout → stderr → merged` y el indicador del
  header lo refleja; el modo por defecto es `merged`

#### S19.4: Follow con pausa

- GIVEN el follow activo (por defecto)
- WHEN llega contenido nuevo
- THEN el viewport salta al fondo; si el usuario hace scroll-up (pgup/g), el
  follow se pausa y el contenido nuevo NO mueve el scroll hasta reactivar
  follow con `G`

#### S19.5: ANSI removido

- GIVEN un log con secuencias ANSI (p.ej. `\x1b[32m`)
- WHEN el contenido se añade al buffer
- THEN las secuencias se eliminan y el viewport conserva el ancho

#### S19.6: Cap del buffer

- GIVEN un servicio que escribe más del cap
- WHEN se añade contenido
- THEN se recorta el inicio del buffer (líneas enteras) y el tamaño nunca
  supera el cap

#### S19.7: Servicio parado

- GIVEN el servicio seleccionado parado
- WHEN la consola se muestra
- THEN se ve el histórico persistente de logs (los ficheros sobreviven)

### R23: Consola mergeada aproximada

En modo `merged` el sistema SHALL intercalar los deltas de stdout y stderr
leídos en el mismo tick (stdout primero). El orden entre streams dentro del
mismo tick es aproximado y aceptado como limitación.

#### S23.1: Merged sin duplicados

- GIVEN ambos streams crecen en un tick
- WHEN se renderiza merged
- THEN cada byte aparece exactamente una vez y el contenido por-stream se
  mantiene disponible para los modos aislados

---

## Capability: threads-os

### R20: Pestaña Threads (nivel OS)

El sistema SHALL listar los hilos del proceso del servicio seleccionado vía
`/proc/<pid>/task/` con: nombre (`comm`), TID, estado (`R/S/D/Z/T`… del campo 3
de `stat`) y CPU% calculado como delta de `utime+stime` entre muestras
(ventana = tick de 2s, `USER_HZ=100`). La tabla SHALL ordenarse por CPU%
descendente. Solo aplica a servicios running; en otro caso muestra un
placeholder.

#### S20.1: Tabla completa

- GIVEN un servicio running con N hilos
- WHEN la pestaña Threads se renderiza tras un tick
- THEN la tabla lista los N hilos con nombre/TID/estado y CPU% (0% en la
  primera muestra)

#### S20.2: Delta de CPU

- GIVEN dos muestras separadas 2s
- WHEN un hilo consumió 50 ticks de usuario y 50 de sistema (0.5s + 0.5s)
- THEN su CPU% mostrado es 50

#### S20.3: Orden por CPU

- WHEN se renderiza la tabla
- THEN los hilos aparecen ordenados por CPU% descendente (desempate por TID)

#### S20.4: Servicio no running

- GIVEN el servicio parado o sin manifiesto
- WHEN se abre la pestaña Threads
- THEN se muestra placeholder sin error ("service not running" / hint de
  manifiesto)

#### S20.5: Proceso muerto entre ticks

- GIVEN el servicio muere tras un sample
- WHEN se consulta `/proc/<pid>/task` y no existe
- THEN la pestaña muestra el placeholder sin crashear

---

## Capability: git-info

### R21: Rama git en el panel de detalles

El sistema SHALL mostrar la rama git del proyecto en el panel de detalles,
leída de `<proyecto>/.git`: si es directorio, de `.git/HEAD`; si es fichero
(worktree), del `gitdir` referenciado. `ref: refs/heads/X` → rama `X`; HEAD
detached → sha corto (7). Sin repo → la fila se omite.

#### S21.1: Rama normal

- GIVEN un proyecto con `.git/HEAD` = `ref: refs/heads/main`
- WHEN se renderiza el detalle
- THEN muestra `branch: main`

#### S21.2: Detached HEAD

- GIVEN un proyecto con HEAD = `abc1234…` (40 hex)
- WHEN se renderiza el detalle
- THEN muestra `branch: abc1234 (detached)`

#### S21.3: Worktree

- GIVEN un proyecto con `.git` fichero `gitdir: /x/.git/worktrees/w`
- WHEN se renderiza el detalle
- THEN lee `/x/.git/worktrees/w/HEAD` y muestra su rama

#### S21.4: Sin repo

- GIVEN un proyecto sin `.git`
- WHEN se renderiza el detalle
- THEN no aparece la fila de branch y no hay error
