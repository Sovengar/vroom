# Plan — Puertos dinámicos y URLs estables

`adr_required: true` — razón: se fija el contrato de propiedad del puerto (`port_mode` como semántica de tres estados, vroom como único dueño del puerto, y `Stop` con fallo cerrado ante propiedad no probada). Hay alternativas descartadas con tradeoffs reales (unión `int|string` en TOML vs. campo aditivo; descubrimiento por `gopsutil` vs. `/proc` directo; matar-vs-preguntar en el guard de propiedad) y el cambio altera la semántica de un campo público del manifiesto. ADR propuesto: **`adr-0012-port-ownership-contract-and-dynamic-ports.md`**, en `docs/adr/`, siguiendo el formato de `adr-0011`.

---

## Resultado pretendido

El mismo `.vroom.toml` sirve para N worktrees a la vez. Cada servicio arranca en su
propio puerto real, la UI, el JSON y la sonda de salud muestran **ese mismo** número,
y el arranque manual fuera de vroom sigue usando el puerto por defecto. El servicio se
puede referenciar por nombre estable. Parar un worktree no toca al twin.

## Enfoque

Cuatro slices, cada uno verde por su cuenta y publicable por separado.

### Slice 1 — Stop por linaje + guard de propiedad (primero, liberable solo)

Es un bug **preexistente e independiente de los puertos**: hoy `Stop` señaliza el
process group, y todo hijo que haga `setsid` (`nohup`, `pm2`, `portless`,
`docker run -d`) sobrevive como huérfano y **sigue escuchando**. Se resuelve el
linaje real del PID registrado leyendo `/proc`, se señaliza grupo **y** descendientes,
iterando hasta vaciar el linaje o vencer el timeout. El caso de un solo process group
debe seguir funcionando igual.

Encima, `killPortHolder` deja de matar a ciegas. Hoy corre `fuser -k` sin comprobar
dueño, y como el guard es `Pgid > 0 || Port > 0`, **detener un servicio ya parado
también mata a quien tenga el puerto** — cuatro call sites alcanzables, uno de ellos el
rollback de un arranque multi-etapa fallido. Con la pieza 2 los puertos pasan a ser
efímeros y ese riesgo crece.

**Decisión (aprobada): fallo cerrado.** Si vroom no puede probar que el dueño del
puerto es su propio servicio, no mata nada y avisa. El coste es que un listener
huérfano genuino puede sobrevivir a un stop; se acepta porque el daño silencioso entre
worktrees es peor que un huérfano visible más un aviso explícito.

### Slice 2 — Puertos dinámicos (núcleo)

El manifiesto gana `port_mode = "fixed" | "dynamic" | "none"`, default `fixed`.
`port` conserva **un único significado**: el puerto por defecto de la app, el mismo
valor que aparece en `PORT=${PORT:-8080}`. Manifiesto y app quedan alineados por
construcción.

En `dynamic`, vroom es dueño del puerto: reserva uno libre en **4000–4999**
(rango fijo ahora, ampliable después), lo inyecta en el entorno del hijo junto con
`HOST=127.0.0.1`, **descubre y verifica** el puerto real, y lo persiste antes de
devolver el control. El discovery está acotado por tres cosas: deadline, **liveness
del linaje** (fallo rápido ~<1 s si el proceso muere) y una **ventana de
estabilización** antes de aceptar un puerto. Si no aparece puerto TCP, se registra
**"sin puerto"** explícitamente — nunca agotar el timeout en silencio.

El discovery lee `/proc` directamente (11 ms) en vez de `gopsutil.Processes()` (54 ms),
y **no vive en el tick de la TUI**: hoy `refreshCmd` ya corre `Evaluate` → `PortOpen`
cada 2 s, y sumarle discovery lo convertiría en trabajo por segundo con N proyectos.

Además, y esto es parte de la pieza, no un extra: **el puerto real pasa a ser la única
fuente de verdad.** Hoy 10+ sitios de display leen el puerto del manifiesto y solo 3
leen el del estado; con `dynamic` eso significa "la UI dice 8080 y la salud prueba
41501". Se migran display, dashboard, tabs, badge, JSON de la CLI y el gate de salud de
stages al puerto resuelto.

**Contrato con las apps:** `PORT=${PORT:-8080}`. Si la app honra `PORT`, vroom no adivina
— solo verifica, y la ambigüedad multi-puerto desaparece en la ruta principal.

### Slice 3 — Desambiguación multi-puerto (R1/R2/R3)

Solo hay ambigüedad cuando vroom **adivina**. **R1**: el puerto reservado está entre los
listeners → ese es, determinista y sin heurística. **R2**: hay varios y el reservado no
está → se sondea el `health_path` que ya existe en el manifiesto y gana la mejor
respuesta (200 > 2xx/3xx > 5xx > 404). **R3**: empate o protocolo no-HTTP → gana el de
menor número, determinista, y el servicio queda marcado **"puerto no verificado"**.

**Limitación documentada, no garantía:** una app que además de no-HTTP ignora `PORT`
deja a vroom sin forma de saber qué listener es el principal. R3 es una moneda al aire
y se presenta como tal.

### Slice 4 — `portless` como proxy puro (defendido, PR propio)

Se mantiene la decisión de "portless a tope": aporta URL estable, HTTPS con CA local y
nombres por subdominio, que es exactamente lo que rompe CORS/OAuth/HMR. vroom le pasa
el puerto que ya posee (`PORTLESS_APP_PORT`), así que `portless` enruta y no decide.
El ciclo de vida del proceso lo gestiona vroom, nunca `portless` — su `setsid` es
justo lo que el slice 1 arregla. El nombre de ruta se deriva de la topología de
worktree que **vroom ya tiene** (`internal/gitinfo`, `internal/worktree`, campos
`RepoRoot` / `IsWorktree` del scanner); no se recalcula nada.

**Degradación, en todas partes.** El usuario actualizará su node global a 24+ por su
cuenta, más tarde. Por tanto vroom **no fija ninguna versión de node**, no escribe nada
en su configuración, y resuelve el binario desde el `PATH`. "portless ausente o
irresoluble" es una condición **normal y no fatal** en start, stop, refresh, TUI y JSON
de la CLI: se emite un diagnóstico claro y todo lo demás se comporta exactamente como
hoy. Los slices 1–3 se entregan y quedan verdes **con portless completamente
ausente**; este slice es un PR aparte y su falta no bloquea a los anteriores.

## Disposición de los riesgos auditados (5–17)

Ninguno se descarta en silencio. `G` = cerrado por guard, `D` = aceptación documentada.

| # | Riesgo | Disposición |
|---|---|---|
| 5 | `cmd.Env` no-nil **reemplaza** `os.Environ()`; el hijo se quedaría con una variable | **G.** El entorno se fusiona explícitamente con el del padre antes de inyectar `PORT`/`HOST`. Verificado por escenario: el hijo conserva PATH/HOME y resuelve `sh`. |
| 6 | La ventana spawn→`SaveMeta` pasa de sub-ms a hasta ~3.5 s; el tick de 2 s leería el meta viejo y reportaría `stopped` con el proceso vivo | **G.** Se registra el intento de arranque **antes** de lanzar el discovery, y ese estado se renderiza como "arrancando, puerto pendiente" — nunca `stopped`. No se evalúa contra el `CreationTimeMs` de otra corrida. |
| 7 | `Stop` de un servicio ya parado ejecuta `fuser -k` (guard `Pgid>0 \|\| Port>0`, `Port` preservado); 4 call sites, uno es `abortAndCleanup` | **G.** El guard de puerto exige prueba de propiedad contra el lineage. Sin prueba: no se mata, se avisa. Slice 1, antes de que existan puertos efímeros. |
| 8 | `WaitForPort(0,…)` duerme 1 s y devuelve `nil` ⇒ el gate de salud pasa sin verificar | **G**, con alcance acotado. El modo nuevo solo cambia el caso `dynamic` con discovery en vuelo; `fixed` y `port = 0` legado conservan literalmente el comportamiento actual (sleep corto y avanza), y `none` lo declara explícito. Verificado por escenarios separados por modo. |
| 9 | `PortOwnerPID` = 0 cae en `return StatusRunning` (veredicto optimista) | **G.** Propietario indeterminado deja de resolverse a "vivo" en el camino que decide por puerto; se degrada a indeterminado y se refleja en el estado. |
| 10 | `PortOpen` dializa `:port` (v4 **y** v6) y `PortOwnerPID` devuelve el primer `Laddr.Port` sin mirar la bind address ⇒ dueño equivocado en host dual-stack, que vira el servicio a `stopped` | **G.** La resolución de dueño discrimina dirección de bind y familia. Si sigue siendo ambiguo, aplica el fallo cerrado del riesgo 7 en vez de decidir. |
| 11 | `Meta.State` es write-only; grabar "sin puerto" ahí es inerte | **G.** Se añade el consumidor que lo lee; de lo contrario el estado persistido no sirve de nada y la información se pierde al reiniciar la TUI. |
| 12 | `statusUnknown` se dibuja como spinner y cuenta como stoppable ⇒ un puerto pendiente parece vivo para siempre | **G.** El puerto pendiente tiene representación propia, visible y distinguible de "sano"; deja de disfrazarse con el spinner genérico y sigue siendo detenible. |
| 13 | `port == 0` es centinela load-bearing en 7 sitios | **G.** `port_mode` es el sustituto explícito y esos 7 puntos se revisan uno a uno. `port = 0` se conserva como alias silencioso de `none` para no romper manifiestos. |
| 14 | `state.StateUnknown` y `process.StatusUnknown` son constantes string duplicadas sin vínculo de compilación; añadir estado toca ambos y dos switch chains | **G.** El estado nuevo se declara en los dos sitios a la vez y se propaga a los switch de render y dashboard. |
| 15 | `Validate()` no aplica la regla cross-field que su propio doc declara | **G.** Se aplica de verdad, y es la **primera** regla cross-field del validador: `health_path` exige puerto en algún modo; `health_path` + `none` se rechaza. |
| 16 | `app_test.go` `TestBadgeShowsPort` fija `":8081"` desde el manifiesto: tripwire deliberado | **G.** Se actualiza en el **mismo** cambio que migra el origen del puerto del badge. Es la aserción que detecta una migración incompleta. |
| 17 | `Manifest` es snapshot de scan y no se re-parsea; `meta.Port` se lee de disco cada tick ⇒ el puerto puede cambiar bajo una sonda en vuelo | **D**, con mitigación parcial. Se acepta que el puerto pueda cambiar entre ticks — es intrínseco al modelo de "el estado en disco es la verdad". Lo que **no** se acepta es mezclar generaciones: una sonda y el estado deben referirse al mismo proceso, nunca "vivo y sano" con un puerto de otra corrida. |

**Diferidas por acuerdo previo:** la pieza 4 y el modelo de datos ampliado quedan fuera
de este plan; no se añaden campos extra al manifiesto para la desambiguación mientras
`health_path` cubra el caso HTTP y las apps que honran `PORT` queden en R1.

## Orden de ejecución

1. Slice 1 — isolated, con sus tests. Sin dependencias de las otras piezas.
2. Slice 2 — el grueso; incluye la migración de display/JSON y el tripwire de test.
3. Slice 3 — se apoya en el discovery del slice 2.
4. ADR, o en el punto donde el contrato queda congelado.
5. Slice 4 — PR independiente, después.

## Puertas

- **`make check` en verde** en cada slice (build + `golangci-lint v2.13.2` + `go test -race -count=1 -cover ./...`). Sin Exceptions.
- **El binario del usuario es el instalado, no el del repo.** Cada slice termina con
  `make install` → `~/.local/bin/vroom`. Sin ese paso, la TUI que el usuario prueba
  sigue siendo la versión vieja y el trabajo se lee como roto.
- Cobertura a cerrar, porque hoy es cero: `WaitForPort` no tiene ningún test, y el
  camino `fuser`/`killPortHolder` nunca se ejercita en CI (los tests actuales pasan
  `StopSpec` sin `Port`). El motor de stacks usa un Manager falso con `Port: 0`, que
  cortocircuita el gate — hay que dejar de cortocircuitarlo para que la pieza valga algo.
- Convenciones a reutilizar, no a reinventar: lectura de `/proc` con inyección de raíz
  ya existe (`xxxAt(root, pid)` con `procRoot = "/proc"`) y tiene fixtures sintéticos;
  `gopsutil` **no** expone `Pgid()` en ningún SO; `teatest` no es dependencia;
  los tests de proceso son integraciones reales detrás de `testing.Short()`.