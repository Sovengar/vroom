# Context — `dynamic-ports` (ejecutor: este es tu único input de navegación)

- **Worktree:** `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports`
- **Rama:** `feat/dynamic-ports`
- **Planning dir:** `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/docs/planning/0001-feature-dynamic-ports`
- **Freshness del índice:** commit `d48da40686ae8a8b53bf6ee23a1dc6e5906925a2`
- **codegraph:** ready (`.codegraph/` inicializado en el worktree: 1389 nodos, 4653 aristas) — usalo read-only como fallback
- Índice persistente en Engram: `topic_key: codebase-index/vroom` (obs #2209, refrescado)

Artefactos de planificación (léelos en este orden):
`issue.md` → `behavior.feature` → `behavior-portless.feature` → `plan.md`.

**Las rutas de este documento son absolutas a propósito.** Son el registro durable del
run y deben seguir siendo legibles desde cualquier cwd.

---

## 1. Secuencia de slices (CONFIRMADA por el usuario)

| Slice | Contenido | Estado al terminar |
|---|---|---|
| **S1** | Stop por linaje + guard fail-closed de `killPortHolder` | verde, instalable solo |
| **S2** | Núcleos de puertos dinámicos (`port_mode`, reserva, inyección de `PORT` con merge de entorno, discovery, migración `Manifest.Port` → `meta.Port`, `WaitForPort` sobre el puerto real, guard spawn→`SaveMeta`) | verde, instalable sin S3/S4 |
| **S3** | Desambiguación multi-puerto R1/R2/R3 | verde, instalable sin S4 |
| **S4** | `portless` como proxy puro | **PR independiente.** Su ausencia NO bloquea a S1–S3 |

**Regla dura:** S1–S3 deben estar verdes y ser publicables **con `portless`
completamente ausente**. No construyas ninguna dependencia de S4 en S1–S3.

S1 va primero porque los puertos efímeros vuelven mucho más peligroso un `fuser`
equivocado. El usuario acepta que la capacidad titular (misma app en dos worktrees)
llegue en S2.

---

## 2. Disposición de los riesgos auditados 5–17

Nada se descarta en silencio. `G` = cerrado por guard. `D` = aceptación documentada.

| # | Riesgo | Disposición | Dónde vive el guard |
|---|---|---|---|
| **5** | `cmd.Env` no-nil **reemplaza** `os.Environ()`; el hijo se quedaría con UNA variable | **G** | `internal/process/daemon_unix.go:59-64` — merge explícito con `os.Environ()` antes de inyectar. Escenario en `behavior.feature`: "El entorno del hijo NO se trunca" |
| **6** | Ventana spawn→`SaveMeta` pasa de sub-ms a hasta ~3.5 s; el tick de 2 s lee el meta viejo (`CreationTimeMs` obsoleto) ⇒ proceso vivo reportado `stopped` | **G** | `internal/tui/app.go:527-548` (`startCmd` → `SaveMeta`) y `internal/tui/app.go:511-520` (`refreshCmd`). Registrar el intento **antes** del discovery; ese estado se renderiza "arrancando, puerto pendiente", nunca `stopped`; no evaluar contra `CreationTimeMs` de otra corrida |
| **7** | `Stop` de un servicio **ya parado** ejecuta `fuser -k`: guard `Pgid>0 \|\| Port>0`, y los callers ponen `Pid=0/Pgid=0` pero **preservan `Port`**. 4 call sites, uno es `abortAndCleanup` | **G (S1)** | `internal/process/daemon_unix.go:103-106` + `:218-221` (`killPortHolder`). Call sites: `internal/tui/app.go:575-586`, `internal/cli/cli.go:531-535`, `internal/orchestrate/engine.go:360-364` y `:376-380`. Exigir prueba de propiedad contra el lineage; sin prueba → no matar + aviso |
| **8** | `WaitForPort(0,…)` duerme 1 s y devuelve `nil` ⇒ el gate de salud pasa **sin verificar nada** | **G, con alcance acotado a `dynamic`** | `internal/orchestrate/health.go:12-26`. El modo nuevo solo altera `dynamic` con discovery en vuelo. `fixed` y `port = 0` legado conservan **literalmente** el comportamiento actual (sleep corto y avanza, sin estado nuevo ni aviso). `internal/orchestrate/engine.go:300-304` y `:339-342` son los call sites |
| **9** | `PortOwnerPID` devuelve 0 (permiso/error) y `daemon_unix.go:151-157` cae en `return StatusRunning` ⇒ propietario ambiguo resuelve al veredicto **optimista** | **G** | `internal/process/daemon_unix.go:145-157`. Propietario indeterminado deja de resolverse a `StatusRunning`; degrada a indeterminado y se refleja en el estado |
| **10** | `PortOpen` dializa `:port` con host vacío ⇒ resuelve a `127.0.0.1` **y** `::1`; `PortOwnerPID` matchea `Laddr.Port` ignorando la bind address y devuelve el **primer** hit ⇒ dueño equivocado en host dual-stack, que vira el servicio a `stopped` | **G** | `internal/process/detect.go:32` y `:49`. Discriminar dirección de bind y familia. Si sigue ambiguo, aplica el fail-closed del riesgo 7 en vez de decidir |
| **11** | `Meta.State` es **write-only**: se escribe en start/stop, nadie lo lee (todo consumidor llama `Evaluate`) ⇒ grabar "sin puerto" ahí es inerte | **G** | `internal/state/state.go:42`. Añadir el consumidor que lo lee; sin él, la información se pierde al reiniciar la TUI |
| **12** | `statusUnknown` se renderiza como spinner (`app.go:2152`) y cuenta como "stoppable" (`app.go:134`) ⇒ un puerto pendiente parece vivo para siempre | **G** | `internal/tui/app.go:2142-2158` (`statusBadge`) + `:134`. El puerto pendiente tiene representación propia, visible y distinguible de "sano"; deja de disfrazarse con el spinner genérico y sigue siendo detenible |
| **13** | `port == 0` es centinela load-bearing en **7** sitios | **G** | `internal/manifest/manifest.go:9` y `:90`, `internal/process/process.go:43` y `:51`, `internal/orchestrate/health.go:13-17`, `internal/tui/outputtabs.go:326` y `:373`. `port_mode` es el sustituto explícito; revisarlos uno a uno. `port = 0` se conserva como alias silencioso de `none` |
| **14** | `state.StateUnknown` (`state.go:28`) y `process.StatusUnknown` (`process.go:19`) son constantes string duplicadas, mismo valor, **sin vínculo de compilación** | **G** | Declarar el estado nuevo en los dos sitios a la vez; propagar a los switch de render: `internal/tui/app.go:2146-2158` y `internal/tui/dashboard.go` |
| **15** | `Validate()` **no** aplica la regla cross-field que su propio doc declara (`health_path` requiere `port > 0`) | **G** | `internal/manifest/manifest.go:83-97` (validación) vs. `:17-18` (doc). Sería la **primera** regla cross-field del validador: `health_path` exige puerto en algún modo; `health_path` + `none` se rechaza |
| **16** | `internal/tui/app_test.go:1026-1046` (`TestBadgeShowsPort`) fija `":8081"` desde un `Manifest` ⇒ **tripwire deliberado** | **G** | Editarlo en el **mismo** cambio que migra el origen del puerto del badge. Es la aserción que detecta una migración incompleta |
| **17** | `p.Manifest` es snapshot de scan y **nunca** se re-parsea por tick (`app.go:799` batcha solo `refreshCmd`+`tickCmd`); `meta.Port` se lee de disco cada tick (`app.go:506`) ⇒ el puerto puede cambiar bajo una sonda en vuelo | **D — ACEPTADO (decisión del usuario: NO añadir re-resolución por tick)** | El sub-guard NO negociable: una sonda y el estado deben referirse **al mismo proceso**. Nunca informar "vivo y sano" con un puerto de otra generación |

### Riesgos descartados por auditoría previa — no gastes presupuesto en ellos

- `internal/process/daemon_windows.go` es un **stub** cuyo `Start`/`Stop`/`Evaluate`
  devuelven error ⇒ **cambiar `StartResult` no rompe Windows.**
- `state.PathKey` hashea la ruta del proyecto
  (`/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/internal/state/hash.go:11-14`,
  sha256 truncado a 8 hex) ⇒ **dos worktrees ya reciben state dirs distintos.** La premisa
  completa del escenario se sostiene.

---

## 3. Contratos a respetar

- **`process.Manager`** (`internal/process/process.go:54+`) es la frontera cross-platform.
  `StartSpec` no tiene `Env` todavía; `StartResult{Pid, Pgid, CreationTimeMs}`.
  `StopSpec{Pgid, Port, Timeout}`, `EvalSpec{Pid, CreationTimeMs, Port, ProcessPattern}`.
- **`state.Meta`** (`internal/state/state.go:32-42`): `Name, ProjectPath, Port,
  ProcessPattern, Command, Pid, Pgid, CreationTimeMs, StartedAt, State`. `Port` deja de
  ser una copia literal del manifiesto y pasa a ser **el puerto real**.
- **`state.Store`**: `PathKey`, `ServiceDir`, `EnsureServiceDir`, `LoadMeta`, `SaveMeta`,
  `ClearPid`, `StdoutLog`, `StderrLog`. `SaveMeta` debe ocurrir **antes** de que el
  arranque devuelva el control (riesgo 6).
- **`manifest.Manifest`**: `Port int` (`toml:"port"`, `manifest.go:43`) gana `PortMode string`;
  `HealthPath` (`manifest.go:48`) es la entrada de R2 en S3. `Validate()` en `:83-97`.
- **`scanner.Project`** (`internal/scanner/scanner.go:20-30`): campos aditivos existentes
  `RepoRoot` e `IsWorktree`. Ya son todo lo que S4 necesita para el naming de rutas.

---

## 4. Patrón a seguir por cada concern

### 4.1 Lectura de `/proc` con raíz inyectada (YA EXISTE — no lo re-inventes)

Precedente en el repo, con fixtures sintéticos y skip guard:

- `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/internal/process/threads_unix.go:14`
  → `const procRoot = "/proc"`
- `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/internal/process/threads_unix.go:17-22`
  → `ListThreads(pid)` es un wrapper thin sobre `listThreadsAt(procRoot, pid)`
- `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/internal/process/metrics_unix.go:14-19`
  → mismo patrón con `readMetricsAt(root, pid)`
- `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/internal/process/threads_unix_test.go`
  → fixtures sintéticos de `/proc` en `t.TempDir()`
- `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/internal/process/threads_unix_test.go:94-98`
  → skip guard `if _, err := os.Stat("/proc/self/task"); err != nil { t.Skip(...) }`

**Aplica:** el discovery por linaje de S1 y la lectura `pid → ppid` de S2 deben usar
exactamente esta convención `xxxAt(root, pid)` con `procRoot`, para que sean testeables
con fixtures y no dependan de `/proc` real en CI.

**Justificación de rendimiento (medida en esta máquina):** snapshot directo de
`/proc` (pid→ppid) = **11 ms**; `/proc/net/tcp` (LISTEN) = **1.7 ms**; discovery completo
con `/proc` directo = **13 ms**. Con `gopsutil`: `Processes()` = 54–62 ms,
`Connections("tcp")` = 29 ms, discovery por pgid = 61 ms, por linaje = 129 ms.
⇒ **Usa `/proc` directo.** Con `gopsutil`, 20 proyectos a 1 Hz serían 2.5 s de CPU por
segundo.

### 4.2 `gopsutil v3.24.5` — lo que existe y lo que NO

**VERIFICADO con `go doc` en esta máquina:**

- Disponible: `(*Process).Ppid()`, `(*Process).Parent()`, `(*Process).Children()`,
  `(*Process).CreateTime()`, `(*Process).Name()` (todas con su variante `WithContext`).
- **AUSENTE: `Pgid()` no existe en ningún SO.** `go doc .../v3/process | grep -ic pgid`
  devuelve **0**. Si necesitas el PGID, lee `/proc/<pid>/stat` campo 5 o usa `ps`.
  **Confirma con `go doc` de nuevo si vas a depender de esto.**

### 4.3 Tests

- **`teatest` NO es dependencia.** No lo importes. Los tests de TUI llaman **directamente
  a los métodos de `Model`**: ej. `internal/tui/app_test.go:1033-1035` invoca
  `statusBadge(p, sv, "·", "·")` como función pura.
- Helpers de proceso:
  `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/internal/process/process_test.go:16-27`
  → `newTestManager(t)` y `startSleep(t, m, spec)`.
- Los tests de proceso son **integraciones reales que spawlean** y están gated detrás de
  `testing.Short()`.
- `t.TempDir()` en 21 ficheros. CI corre la suite **completa sin `-short`** y requiere
  `fd` instalado (el scanner prefiere `fd --hidden`).

---

## 5. Tests afectados que DEBES tocar

### 5.1 El tripwire de S2

`/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/internal/tui/app_test.go:1026-1046`
— `TestBadgeShowsPort` construye
`&manifest.Manifest{Name: "x", Command: "run", Port: 8081}` y exige que el badge contenga
`":8081"` para `running` y `unknown`, y **no** lo contenga para `stopped`. Al migrar el
badge a `meta.Port`, esta aserción debe cambiar al mismo tiempo. Está puesta ahí a
propósito: es la que delata una migración a medias.

### 5.2 Cobertura cero que hay que crear

- **`WaitForPort` no tiene ningún test.** No existe `internal/orchestrate/health_test.go`.
  Crear: puerto que abre a tiempo, puerto que nunca abre (timeout), `port = 0` legado
  (sigue deviate — sleep corto y `nil`), `none` (skip explícito), `dynamic` con puerto
  pendiente (bloquea dentro del presupuesto y reporta distinto).
- **El path `fuser` / `killPortHolder` nunca corre en CI.**
  `TestStopKillsProcessGroup` y `TestStopAlreadyDead` pasan `StopSpec` **sin `Port`**,
  así que `daemon_unix.go:104-106` no se ejecuta jamás. Crear tests con `Port > 0` que
  cubran: dueño en nuestro lineage → sí mata; dueño fuera → no mata + aviso; dueño
  desconocido → no mata + aviso (fail-closed); servicio ya parado con `Port` preservado
  → no toca al twin.
- **`internal/orchestrate/engine_test.go:14-40` usa un `mockManager`** cuyos defaults son
  `StartResult{Pid: 1000, Pgid: 1000, CreationTimeMs: 100}` (`:26`) y `Evaluate` →
  `StatusRunning` (`:38-40`). Como los tests pasan `Port: 0`, **`WaitForPort` se
  cortocircuita** y el gate de salud nunca se ejercita de verdad. Mientras siga así, la
  suite no puede observar el comportamiento de S2/S3 en el motor de stacks.
- **S1**: cubrir el descendiente re-`sid` (patrón real: `sh -c 'setsid sleep 300 &'`),
  el caso normal de un solo process group sin regresión, e idempotencia.
- **S2**: cubrir la inyección de env (PATH preservado), la ventana de arranque sin falso
  `stopped`, el caso "app ignora PORT" como **warning** no error, UDP-only sin hang, bind
  lento que conserva puerto, y la coherencia display/JSON/health del mismo número.
- **S3**: R1, R2, R3 y el marcado "puerto no verificado".

---

## 6. Ficheros a tocar (símbolo + por qué)

Rutas absolutas; el prefijo común es
`/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/`.

### S1 — Stop por linaje + guard fail-closed

| Ruta | Símbolo | Por qué |
|---|---|---|
| `internal/process/daemon_unix.go` | `Stop` (`:89-108`) | Señalizar grupo **y** descendientes, iterando hasta vaciar el linaje o vencer el timeout |
| `internal/process/daemon_unix.go` | `killPortHolder` (`:218-221`) | Exigir prueba de propiedad; fail-closed |
| `internal/process/daemon_unix.go` | nuevo lector de lineage `xxxAt(root, pid)` | Seguir la convención 4.1 |
| `internal/process/detect.go` | `PortOwnerPID` (`:43-52`) | Necesario para la prueba de propiedad |
| `internal/process/process.go` | `StopSpec` (`:41-45`) | Quizá un campo para distinguir "puerto esperado" de "puerto a liberar" |
| `internal/tui/app.go` | stop path (`:570-590`) | Caller que preserva `Port` con `Pgid=0` |
| `internal/cli/cli.go` | stop path (`:531-535`) | Idem |
| `internal/orchestrate/engine.go` | stop (`:360-364`), `abortAndCleanup` (`:372-380`) | Idem; el rollback es el caso más peligroso |

### S2 — Puertos dinámicos

| Ruta | Símbolo | Por qué |
|---|---|---|
| `internal/manifest/manifest.go` | `Port` (`:43`), `Validate()` (`:83-97`) | Añadir `PortMode`; primera regla cross-field (riesgo 15) |
| `internal/process/process.go` | `StartSpec` (`:26-31`) | Campo `Env`; revisar los 2 centinelas `Port` del riesgo 13 |
| `internal/process/daemon_unix.go` | `Start` (`:29-64`) | Merge con `os.Environ()` (riesgo 5); **nunca** asignar `cmd.Env` sin mergear |
| `internal/process/detect.go` | nuevos: reserva de puerto, listeners del lineage, descubrimiento | `/proc` directo, no `gopsutil` |
| `internal/state/state.go` | `Meta.Port` (`:35`), `Meta.State` (`:42`) | `Port` = puerto real; añadir lector de `State` (riesgo 11) |
| `internal/tui/app.go` | `startCmd` (`:527-548`), `refreshCmd` (`:495-524`), `statusBadge` (`:2142-2158`), `isRunning` (`:134`) | Orden spawn→`SaveMeta` (riesgo 6), estado pendiente (riesgo 12) |
| `internal/tui/serviceview.go` | `:84`, `:167`, `:218` | Migrar de `Manifest.Port` a `meta.Port` |
| `internal/tui/dashboard.go` | `:220` | Idem + switch de estados (riesgo 14) |
| `internal/tui/outputtabs.go` | `:326`, `:329`, `:373`, `:387` | Probe HTTP contra el puerto real |
| `internal/cli/cli.go` | `ProjectInfo.Port` (`:291`), `evaluateStatus` (`:307`), start (`:469`) | `:291` se fija **antes** de `:307` — reordenar |
| `internal/orchestrate/health.go` | `WaitForPort` (`:12-26`) | Riesgo 8, alcance acotado a `dynamic` |
| `internal/orchestrate/engine.go` | `:259`, `:297` (lectura), `:300-304`, `:339-342` (gate) | Usar el puerto real |

**Aviso de orden en `cli.go`:** `ProjectInfo.Port` se asigna en `:291` y
`evaluateStatus` se invoca en `:307`. Si la migración no reordena, el JSON seguirá
emitiendo el puerto del manifiesto.

### S3 — R1/R2/R3

| Ruta | Por qué |
|---|---|
| El discovery introducido en S2 | R1 (reservado entre listeners, determinista), R2 (`health_path`, mejor respuesta: 200 > 2xx/3xx > 5xx > 404), R3 (menor puerto + marcado "no verificado") |
| `internal/manifest/manifest.go` | `HealthPath` ya existe (`:48`, `HealthURLPath()` en `:55-61`) ⇒ **no añadir campos** |

**Regla de S3, verificada empíricamente en esta máquina:** la ambigüedad **solo** existe
cuando vroom adivina. Si la app honra `PORT`, R1 es determinista y no hay heurística.
La ambigüedad vive en la ruta de reserva, no en la principal.

**Limitación honesta (documentada, no garantía):** una app que **además de no-HTTP
ignora `PORT`** deja a vroom sin forma de saber cuál listener es el principal. R3 es
moneda al aire y se presenta como tal. La evidencia medida:

| Caso | Candidatos | Resultado |
|---|---|---|
| Honra `PORT`, metrics abre primero | `[41501, 42501]` | R1 → 41501 OK |
| Honra `PORT`, main abre primero | `[41502, 42502]` | R1 → 41502 OK |
| Ignora `PORT`, metrics 404 en `/health`, main 200 | `[41510, 42510]` | R2 → 41510 OK |
| Empate: ambos 200 en `/health` | `[41520, 42520]` | R3 → 41520 determinista OK |
| No-HTTP (gRPC-like, sockets crudos) | `[41530, 42530]` | R3 fallback — vroom **no puede** saber cuál es el principal |

**Bug ya encontrado al implementar el bucle (fácil de reintroducir):** la primera versión
cerraba con "sin puerto" en cuanto una muestra venía vacía, antes de que el proceso
tuviera tiempo de hacer bind. **"No hay puertos" solo es válido si (a) el linaje está
muerto, o (b) tras una ventana de observación suficiente sin cambios.**

**Detalle de pruebas:** `socketserver.TCPServer` **no** pone `SO_REUSEADDR` por defecto
(`allow_reuse_address=False`), así que un puerto reutilizado falla con `EADDRINUSE` por
`TIME_WAIT`. Usa puertos frescos por corrida o `allow_reuse_address=True`.

### S4 — `portless` (PR propio, al final)

- **Contexto de entorno MEDIDO:** `portless` **sí** está instalado en
  `/home/buble/.local/bin/../mise/installs/node/24.15.0/bin/portless`, **v0.13.0**. Pero
  **no resuelve por el shim** hoy: el default global de node de mise es **20.19.0**, así
  que falla con `No version is set for shim: portless`.
  `mise x node@24.15.0 -- portless --version` → `0.13.0`.
  El `node` en PATH es **v22.23.2** desde `~/.local/bin/node` — una tercera versión distinta.
- **RESTRICCIÓN:** vroom **NO** debe hardcodear `24.15.0`. Resuelve `portless` desde
  `PATH` y degrada con diagnóstico claro. **No** mutar la config de node del usuario
  (blast radius sobre todos sus proyectos, fuera de alcance). El usuario hará ese
  upgrade por su cuenta, más tarde.
- **Topología de worktree ya existe — reúsala, no la re-derives:**
  `internal/gitinfo`, `internal/worktree` (`List(dir)`, `IsBareRepo(dir)`) y los campos
  del scanner `RepoRoot` / `IsWorktree` (`internal/scanner/scanner.go:20-30`). El ADR
  `docs/adr/adr-0011-worktree-topology-discovery-boundary.md` explica el límite.
- Contrato verificado (`PORTLESS_APP_PORT`): `PORTLESS_APP_PORT=39677 portless api-d python3 server.py`
  → el backend escucha **exactamente** en 39677. **vroom es dueño del puerto; portless
  enruta.** `portless` auto-detecta el worktree git y prefija la rama como subdominio,
  y requiere **Node 24+**.
- "portless ausente o irresoluble" es condición **normal, no fatal** en start, stop,
  refresh, TUI y JSON de la CLI. El JSON **mantiene su forma**.
- El nombre de ruta se deriva de la topología existente. El lifecycle del proxy se
  decide **dentro de S4**, no antes: por defecto **no** aparece en la lista de servicios
  ni es detenible por la vía normal.

---

## 7. Convenciones del repo

- **Idioma:** código y comentarios en **inglés**. Reportes al usuario en español.
- **ADR** en `docs/adr/`, formato de
  `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-dynamic-ports/docs/adr/adr-0011-worktree-topology-discovery-boundary.md`
  → `# ADR-00NN — Título` + `Estado` / `Fecha` / `Feature`, luego `## Contexto`,
  `## Decisión`, `## Consecuencias` (positivas / negativas-tradeoffs / limitaciones
  documentadas), `## Alternativas consideradas`.
- **ADR a escribir:** `docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md`
  — semántica de `port_mode`, vroom como único dueño del puerto, y `Stop` con fallo
  cerrado ante propiedad no probada. Alternativas descartadas con tradeoffs reales:
  unión `int|string` en TOML vs. campo aditivo; `gopsutil` vs. `/proc` directo;
  matar-vs-preguntar en el guard de propiedad. Se escribe en S2, cuando el contrato
  queda congelado.
- Comentarios muy concisos; nada de comentarios obvios.

---

## 8. Puertas (gate)

- **`make check` DEBE quedar en verde en cada slice.** Es `build` + `lint` +
  `test` (`golangci-lint` **v2.13.2** pineado, `go test -race -count=1 -cover ./...`).
- **`make install` al terminar CADA slice** — obligatorio y repetido, no una vez al final.
  El usuario ejecuta `~/.local/bin/vroom`, **no** el binario del repo. Sin esto, la TUI
  que él prueba es la versión vieja y el trabajo correcto se lee como roto.
  (`make install` = `build` + `cp .local/bin/vroom $(HOME)/.local/bin/vroom`.)
- CI corre en **todo PR** y en **todo push a `main`**, con tres jobs: `Build`, `Lint`,
  `Test` (este último con `-race` y `fd` instalado). `main` está protegida: merge solo
  por PR con los tres checks verdes.

## 9. No-goals

- No reescribir el proxy: se mantiene `portless` "a tope". Un proxy nativo (estilo Caddy,
  ~400 líneas, mismo contrato `nombre → puerto`) queda anotado **solo** como salida
  futura; es reversible porque vroom siempre es dueño del puerto.
- No tocar el arranque fuera de vroom.
- No cambiar el default global de node del usuario.
- No añadir campos extra al manifiesto para la desambiguación mientras `health_path`
  cubra el caso HTTP y las apps que honran `PORT` queden en R1.
- No añadir re-resolución de puerto por tick (riesgo 17 aceptado).

## 10. Documentadas como NO-garantías (no como certezas)

1. App no-HTTP que además ignora `PORT` ⇒ vroom no sabe cuál listener es el principal.
   R3 determinista + marcado "puerto no verificado".
2. Servicio solo-UDP ⇒ sin puerto TCP descubrible: se marca "sin puerto", **nunca** cuelga.
3. **TOCTOU de la reserva:** `bind(127.0.0.1:0)` + `close` devuelve el puerto al pool
   antes de que arranque el hijo. En 4000–4999 la colisión es improbable, la ventana existe.
4. `portless` ausente o incompatible ⇒ degradación con diagnóstico.
5. Bind duro duplicado (la app ignora `PORT` y hace bind literal) ⇒ sigue siendo un fallo
   de arranque de la app. vroom lo reporta más rápido y mejor; no lo evita.
6. **Orphan superviviente:** con el guard fail-closed, un listener huérfano genuino puede
   sobrevivir a un stop. Aceptado: se muestra un aviso explícito.