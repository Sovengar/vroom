# ADR-0012 — Contrato de propiedad del puerto y puertos dinámicos por worktree

- Estado: aceptada
- Fecha: 2026-09-30
- Feature: `0011-feature-dynamic-ports`

## Contexto

El puerto de un servicio estaba atado al `.vroom.toml`, y el manifiesto es un
snapshot que se lee una vez en el scan. Eso funciona mientras cada proyecto
tenga su propio directorio. Con **N worktrees del mismo repo** todos declaran
el mismo `port`, sólo uno arranca y los demás chocan con `EADDRINUSE` o, peor,
`Evaluate` los da por vivos porque "el puerto está abierto" — el del twin.

A eso se suman dos problemas preexistentes que el caso de los puertos
efímeros vuelve mucho más peligroso un `fuser` equivocado:

1. `Stop` señalizaba `kill(-pgid)`, que **no alcanza a los descendientes que
   hicieron `setsid`** (`nohup`, `pm2`, `docker run -d`, `portless`): quedan en
   otro process group y siguen escuchando.
2. El último recurso de `Stop` era `fuser -k` sobre el puerto, sin comprobar
   dueño, y el guard era `Pgid > 0 || Port > 0` con los cuatro call sites
   preservando `Port` con `Pgid = 0`. Detener un servicio ya parado mataba a
   quien tuviera ese puerto — normalmente el twin.

## Decisión

1. **`port_mode` es un campo aditivo de tres estados**, con `fixed` por
   defecto. Ausente significa `fixed`, y `port = 0` sigue siendo un alias
   silencioso de `none` para no romper ningún manifiesto existente.

2. **`port` conserva un único significado**: el puerto por defecto de la app,
   el mismo valor de `PORT=${PORT:-N}`. Manifiesto y app quedan alineados por
   construcción.

3. **En `dynamic`, vroom es el único dueño del puerto.** Reserva uno libre en
   `4000–4999`, lo inyecta como `PORT` (junto a `HOST=127.0.0.1`) y descubre y
   verifica el puerto real antes de devolver el control. La reserva lleva un
   mutex y un set en memoria de puertos ya entregados: **dentro de un proceso
   vroom** dos reservas concurrentes nunca coinciden. Entre procesos vroom
   distintos la ventana `bind`+`close` sigue abierta (ver trade-offs).
   El descubrimiento
   está acotado por tres cosas: deadline, **liveness del linaje** (fallo rápido
   de orden de 1 s si el proceso muere) y **ventana de estabilización**: el
   conjunto de listeners debe llevar 500 ms sin cambiar antes de aceptarse. Una
   app que abre metrics en una goroutine y el principal en otra produce dos
   muestras distintas, y aceptar la primera elige el listener equivocado.

4. **El puerto real es la única verdad.** `meta.Port` pasa a ser el puerto
   efectivamente escuchado y todo el display — badge, vista de servicio,
   dashboard, tab Health, JSON de la CLI y el gate de salud de los stacks —
   pasa a leerlo. Sólo cuando no hay servicio en marcha se cae al declarado.

5. **El entorno del hijo se fusiona explícitamente** con `os.Environ()` antes
   de inyectar. En Go, `cmd.Env == nil` hereda y cualquier slice no-nil
   **reemplaza** el entorno entero.

6. **`Stop` señaliza el linaje, no sólo el grupo.** El linaje real se lee de
   `/proc` y se captura **antes** de señalizar: al morir la raíz sus hijos se
   reparentan a init y la relación se pierde.

7. **El guard de propiedad del puerto falla cerrado.** `fuser -k` sólo corre si
   hay **un único dueño conocido** y **pertenece al linaje** del servicio. Cero
   dueños (permisos, `/proc` ilegible), varios dueños (mismo número en IPv4 e
   IPv6) o un dueño ajeno: no se mata nada y se emite un aviso. El aviso viaja
   por `StopSpec.Warn` hasta el log del servicio y la TUI.

8. **El propietario indeterminado no resuelve a "vivo".** `PortOwnerPID`
   devuelve 0 ante ambigüedad y `Evaluate` degrada a indeterminado en vez de
   aplicar el veredicto optimista.

9. **El descubrimiento lee `/proc` directamente** (11 ms) y cruza
   `/proc/net/tcp{,6}` con `/proc/<pid>/fd` del linaje. `gopsutil.Processes()`
   cuesta 54–62 ms medidos en esta máquina, y el descubrimiento **no vive en
   el tick de la TUI**.

10. **"Puerto pendiente" y "sin puerto" son estados con nombre**
    (`port_pending`, `no_port`), declarados a la vez en `state` y en `process`.
    El pendiente no se disfraza de sano con el spinner genérico, y ambos siguen
    siendo detenibles.

## Consecuencias

### Positivas

- El mismo `.vroom.toml` sirve para N worktrees a la vez.
- Parar un worktree no toca al twin, y un `Stop` ya no deja listeners
  huérfanos de descendientes re-`sid`.
- El fallo es visible y local: el aviso de propiedad nombra el puerto y el
  proceso que no se pudo probar.
- El fallo de arranque es rápido y con causa distinta: un servicio que muere
  se reporta en ~1 s en vez de agotar el timeout del discovery.

### Negativas / trade-offs

- El arranque en `dynamic` se alarga lo que tarde el proceso en hacer bind
  (hasta ~3,5 s en el caso de los tests). La ventana spawn→`SaveMeta` deja de
  ser sub-milisegundo.
- **TOCTOU de la reserva:** `bind` + `close` devuelve el puerto al pool antes de
  que arranque el hijo. Esa ventana **no** es improbable en el caso dominante.
  Corregido: la colisión vroom-contra-vroom dentro de un mismo proceso no es un
  caso raro sino el normal, porque `toggleNode` devuelve `tea.Batch` (bubbletea
  corre los comandos en paralelo) y `Launch` arranca cada servicio de una etapa
  en su propia goroutine. Medido antes del arreglo: **99,5 %** de colisiones
  entre pares de reservas concurrentes, porque todas entraban por el primer
  hueco libre del rango. Ahora hay un mutex y un set en memoria de puertos
  entregados (`ReservePort` / `ReleasePort`), de modo que **dos reservas
  concurrentes en el mismo proceso vroom nunca devuelven el mismo puerto**, y un
  intento fallido devuelve el suyo.
  Lo que **no** queda protegido: dos procesos vroom **distintos**. Cada uno
  tiene su propio set y ambos hacen `bind`+`close` sobre el mismo pool del
  kernel. Esa es la ventana que queda abierta; mitigarla exigiría socket passing
  o un fichero de lock, y el hijo es `sh -c`, así que no hay a quién pasarle el
  descriptor. Un `EADDRINUSE` en ese caso lo reporta la app, no vroom.
- Se necesita una variable de entorno en la app (`PORT=${PORT:-8080}`). Una app
  que no la honra funciona, pero hay que avisar y descubrir su puerto real.
- El fallo cerrado puede dejar un listener huérfano genuino vivo tras un stop.
  Es un coste consciente: el daño silencioso entre worktrees es peor que un
  huérfano visible más un aviso.
- `/proc` es específico de Linux. La convención `xxxAt(root, pid)` deja el
  reader inyectable, pero una implementación Windows necesita otro reader.

### Limitaciones documentadas (no garantías)

1. **App no-HTTP que además ignora `PORT`:** vroom no tiene forma de saber
   cuál listener es el principal. Se elige el de menor número, de forma
   determinista, y el servicio se marca **"puerto no verificado"** con
   `port_verified: false` y un aviso explícito.
2. **Listeners que se abren más de 500 ms escalonados:** la ventana de
   estabilización cubre la apertura típica en dos goroutines. Una app que
   abre un listener principal segundos después de otro puede tener su puerto
   elegido como el listener secundario, y no hay forma de distinguirlo sin una
   señal adicional.
3. **Servicio sólo-UDP:** sin puerto TCP descubrible. Se registra "sin puerto"
   (`no_port`), nunca un cuelgue.
4. **Bind duro duplicado** (la app ignora `PORT` y hace bind literal): sigue
   siendo un fallo de arranque de la app. vroom lo reporta más rápido y mejor;
   no lo evita.
5. **El puerto puede cambiar entre ticks.** `p.Manifest` es un snapshot de scan
   y `meta.Port` se lee de disco en cada tick. Aceptado. El sub-guard sí es
   firme: una sonda y el estado se refieren al mismo proceso; nunca se informa
   "vivo y sano" con un puerto de otra generación.
6. **`portless` ausente o incompatible** es condición normal y no fatal. Es un
   slice aparte (S4) y su ausencia no bloquea nada de lo anterior.

## Alternativas consideradas

- **Unión `int | string` en `port`** (`port = 8080` vs `port = "dynamic"`).
  Rechazada: un campo con dos tipos rompe la compatibilidad de todo consumidor
  que ya lo lee como `int`, y hace que "sin puerto" deje de ser
  representable. El campo aditivo mantiene `port` con un solo significado y
  hace la retrocompatibilidad trivial por construcción.
- **`gopsutil` para el descubrimiento de linaje y listeners** (54–62 ms por
  snapshot frente a 11 ms de `/proc` directo, y `Pgid()` **no existe en
  gopsutil v3.24.5 en ningún SO**). Rechazada: el tick ya paga un `Evaluate`
  por proyecto cada 2 s; sumarle 5× el coste lo convertiría en trabajo por
  segundo con N proyectos. Se conserva la convención de raíz inyectada del repo
  (`xxxAt(root, pid)` con `procRoot`) para que los tests usen fixtures
  sintéticos en vez de `/proc` real.
- **Matar-vs-preguntar en el guard de propiedad.** La alternativa era seguir
  con `fuser -k` incondicional. Se eligió fallar cerrado: preguntar al usuario
  por cada stop con puerto ocupado rompe el flujo de la TUI, y matar a ciegas
  es exactamente el daño que este ADR elimina.
- **Re-resolver el puerto en cada tick** para seguir el cambio entre
  ejecuciones. Rechazada: el snapshot del manifiesto es barato y el cambio
  entre ticks es raro; medir de nuevo en el tick convertiría una operación de
  11 ms por servicio en trabajo constante. Se acepta la limitación 4.
