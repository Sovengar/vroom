# Propuesta — Puertos dinámicos y URLs estables para worktrees paralelos

- Estado: **propuesta** (para revisión; no implementada)
- Fecha: 2026-09-30
- Alcance: `vroom` (núcleo) + convención en los `.vroom.toml` de los proyectos del usuario

> Este documento es un **informe de problema / necesidad / propuesta** pensado para
> que otro agente lo revise de forma crítica y lo implemente si lo considera
> correcto. Todo dato numérico de la sección "Evidencia" fue **medido** en esta
> máquina, no estimado. Se marcan los puntos donde la propuesta es discutible.

---

## 1. Problema

El usuario gestiona sus proyectos con **git worktrees** y los worktrees son
**copias del mismo repo**, cada una con su propio `.vroom.toml`. Como las copias
son idénticas, **declaran los mismos puertos fijos**.

Consecuencia directa: **no es posible levantar la misma app en dos worktrees a
la vez.** Y el conflicto no es un fallo limpio: vroom tiene comportamientos que
convierten la colisión en datos incorrectos y en muerte de procesos ajenos
(detalle en §3.3).

## 2. Necesidad

Que vroom permita:

1. Levantar la misma aplicación en **N worktrees simultáneamente**, cada uno con
   su propio puerto real, sin editar `.vroom.toml` por worktree.
2. Poder **referenciar cada servicio por un nombre estable** y no por un número
   de puerto, porque el número cambia en cada arranque. Sin esto, cualquier
   configuración del proyecto (URLs de OAuth, CORS, redirect URIs, links del
   README, bookmarks) queda atada a un valor efímero.
3. Mantener intacto el arranque manual fuera de vroom (`npm run dev`,
   `go run .`, `mise run dev`): los puertos por defecto **no deben cambiar** para
   quien no usa vroom.

## 3. Estado actual (verificado en el código)

### 3.1 `StartSpec` no tiene campo `Port`

`internal/process/process.go:26-31`. El arranque es **a ciegas**: vroom no
valida disponibilidad de puerto, no reserva nada y no comprueba nada tras
arrancar. El puerto del manifiesto se **copia literalmente** a
`state.Meta.Port` (`internal/tui/app.go:543`, `internal/cli/cli.go:469`).

### 3.2 El puerto del manifiesto tiene exactamente 4 usos

| # | Uso | Ubicación | Naturaleza |
|---|-----|-----------|-----------|
| 1 | Señal de vida del estado | `internal/process/daemon_unix.go:122-126` | lectura |
| 2 | Liberar el puerto al parar (`fuser -k`) | `internal/process/daemon_unix.go:104-105` | **acción** |
| 3 | Gate de salud de stages (`WaitForPort`) | `internal/orchestrate/engine.go:302,340` | lectura |
| 4 | Probe HTTP de la tab Health + display | `internal/tui/outputtabs.go:329,387`; `serviceview.go`, `dashboard.go`, `app.go:2142` | lectura |

Detalle crítico de (1): si `port > 0` y el PID está vivo pero el puerto no
responde, el estado es `unknown`, **no** `running`. Y en el fallback de
"reiniciado externamente", `PortOpen` + `PortOwnerPID` con `creation_time`
distinta ⇒ `stopped`, precisamente para no reportar `running` por el proceso de
otro servicio.

### 3.3 Consecuencias medidas con dos worktrees

Dos servicios que declaran `port = 8080`:

- **Bind duro duplicado** (Go/Python con `:8080` literal): el segundo proceso
  **muere**. vroom lo detecta en ~300 ms.
- **Auto-increment** (Next.js/Vite, `EADDRINUSE` → 8081): el segundo servicio
  sube a 8081, pero vroom sigue creyendo 8080. Como 8080 responde (lo tiene el
  worktree A), `Evaluate` reporta el worktree B como **`running` y "sano"**
  cuando en realidad está escuchando en otro sitio. Falso positivo silencioso.
- **`Stop` mata al twin.** `killPortHolder` ejecuta `fuser -k` sobre
  `meta.Port` **sin comprobar propietario** (`daemon_unix.go:218-229`). Parar el
  worktree A mata el proceso del worktree B.

### 3.4 Bug de Stop preexistente, independiente de los puertos

`Stop` (`daemon_unix.go:89-108`) mata por **process group**. Cualquier
`command_start` que se auto-demonice o llame a `setsid` deja hijos **fuera del
grupo** que sobreviven al stop. Medido con portless:

```
portless  PID/PGID/SID = 1066968
backend   PID/PGID/SID = 1067797   ← otro grupo, sobrevive a kill(-1066968)
```

Tras el `kill -9` del grupo, el backend queda **huérfano, reparentado a init, y
sigue escuchando** (confirmado con `ss`). Hoy esto ya afecta a `nohup`, `setsid`,
`pm2`, `docker run -d`, etc. Es un bug de `Stop`, no de puertos, pero **es
prerrequisito** para cualquier pieza que envuelva al hijo (ver §6.1).

---

## 4. Evidencia (medida, no estimada)

### 4.1 Primitivas disponibles

- `gopsutil v3.24.5` ya está en `go.mod`; `PortOwnerPID` ya usa
  `gopsnet.ConnectionsPid("tcp", pid)` (`internal/process/detect.go:42-52`).
- **`gopsutil v3 NO expone `Pgid()` en ningún OS** (verificado en
  `process/process.go`): hay que leer `/proc/<pid>/stat` campo 5 o usar `ps`.

### 4.2 Comportamiento del discovery (A)

Pruebas con `sh -c` + `setsid`, sondeando cada 300 ms:

| Escenario | Resultado |
|---|---|
| Arranque con error, cierre inmediato | detectado a los **300 ms** |
| Bind y muerte a los 200 ms | detectado a los 300 ms, puerto ya muerto (verificado con dial) |
| Bind lento (JVM/Spring, 3.5 s) | detectado a los 3.5 s |
| Bind inmediato (nieto `sh` → servidor) | detectado a los 300 ms |
| **Solo UDP, nunca abre TCP** | **nunca se detecta** |
| Dos listeners (metrics y http) | ambos se detectan |

**El riesgo de "esperar eternamente a un puerto" no se materializa, pero sólo
porque el bucle comprueba la liveness del grupo en cada iteración.** Un bucle que
sólo espere "aparezca un puerto" se queda hasta agotar el timeout. Es un requisito
de diseño, no un detalle.

### 4.3 Ambigüedad multi-puerto

Si el servicio abre `metrics` antes que `http` (medido: 1.5 s de diferencia),
tomar el **primer puerto visto** devuelve el equivocado. Resuelto en §6.4.

### 4.4 Coste por llamada

| Primitiva | Coste |
|---|---|
| `gopsutil.Processes()` | 54–62 ms |
| `gopsutil.Connections("tcp")` | 29 ms |
| discovery por pgid (completo) | 61 ms |
| discovery por linaje de ppid (completo) | 129 ms |
| **snapshot `/proc` directo (pid→ppid)** | **11 ms** |
| **`/proc/net/tcp` (LISTEN)** | **1.7 ms** |
| **discovery completo con `/proc` directo** | **13 ms** |

⇒ **El discovery debe vivir en `start` / refresh explícito, NUNCA en el tick de
la TUI.** Con `gopsutil`, 20 proyectos a 1 Hz serían **2.5 s de CPU por segundo**.
Si se implementa, parsear `/proc` a mano en lugar de `gopsutil`.

### 4.5 portless con backend no-Node

`portless` **no es una herramienta de frontend**: es un proxy inverso genérico.
Verificado con un backend Python:

```
portless api-a python3 server.py
  → inyecta PORT=4042, HOST=127.0.0.1, PORTLESS_URL=http://api-a.localhost:1355
  → ruta: http://api-a.localhost:1355 -> localhost:4042
```

Además **auto-detecta el worktree git y prefija la rama como subdominio**
(`https://fix-ui.myapp.localhost`) sin configuración. Requiere **Node 24+**.

### 4.6 `PORTLESS_APP_PORT`: vroom puede ser dueño del puerto

```
PORTLESS_APP_PORT=39677 portless api-d python3 server.py
  → el backend escucha EXACTAMENTE en 39677
  → ruta: http://api-d.localhost:1355 -> localhost:39677
```

Es decir: **portless puede usarse como proxy puro**, sin decidir el puerto. Esto
elimina la superposición entre "vroom reserva el puerto" y "portless lo asigna".

**Pero la topología de procesos NO cambia**: el backend sigue yendo en su propio
process group. El fix de `Stop` por linaje sigue siendo obligatorio.

### 4.7 Race de la reserva

`bind(127.0.0.1:0)` + `close` deja el puerto libre otra vez antes de que arranque
el hijo: hay **ventana TOCTOU** (verificado). En el rango 4000–4999 la colisión es
improbable, pero debe documentarse y/o reintentarse.

---

## 5. Decisiones ya tomadas

1. **Se mantiene `portless`.** Su proxy resuelve HTTPS con CA local, nombres por
   subdominio, CORS/cookies entre subdominios y HMR (websockets) — piezas que
   habría que reescribir y que además chocarían con el modelo de `Stop` de vroom.
2. **vroom es dueño del puerto y del ciclo de vida; `portless` es el proxy
   delante.** Se le pasa `PORTLESS_APP_PORT` y `portless` no decide.
3. **El usuario modifica sus apps** para leer `PORT` con fallback al puerto por
   defecto. Patrón validado:
   ```bash
   PORT=${PORT:-8080}      # lee si vroom lo inyecta, si no el default
   ```
4. **Se instalará Node 24.15.0 global** (hoy el shim de `portless` no resuelve
   versión: el default de mise es 20.19.0).
5. **El schema del manifiesto es libre.** Se puede cambiar lo que haga falta. La
   propuesta concreta está en §6.2 y **no** es una decisión abierta.
6. **Se mantiene `portless` a tope.** Es la pieza que aporta URL estable, HTTPS con
   CA local y nombres por subdominio. No se contempla sustituirlo por un proxy
   nativo de vroom; queda anotado en §8.3 sólo como salida futura.

---

## 6. Propuesta

Tres piezas, en este orden. **La primera es prerrequisito de la tercera.**

### 6.1 Pieza 1 — `Stop` por linaje (prerrequisito, y bug fix independiente)

**Qué:** al parar, no basta con `kill(-pgid)`. Construir el conjunto de PIDs
descendientes del PID registrado (mapa `pid → ppid` desde `/proc`) y señalizar
al grupo **y** a los descendientes que se hayan re-sid. Iterar hasta que el
linaje esté vacío o venza el timeout.

**Por qué:** hoy `Stop` deja huérfanos a los hijos que hacen `setsid` (`nohup`,
`pm2`, `portless`, `docker run -d`). Es un bug que ya existe sin cambiar nada de
puertos.

**Efecto lateral:** hace posible el descubrimiento por linaje y elimina la
dependencia de asumir "el servidor está en mi pgid".

**Restricción:** debe seguir funcionando el caso actual (grupo normal, sin re-sid)
sin regresión.

### 6.2 Pieza 2 — Puertos dinámicos: reserva + inyección + descubrimiento

**Qué:**

- `StartSpec` gana un campo `Env map[string]string` (hoy no existe).
- Al arrancar en modo dinámico: reservar un puerto libre en el rango 4000–4999,
  inyectar `PORT` (y `HOST=127.0.0.1`) en el entorno del hijo, y poner
  `PORTLESS_APP_PORT` si el proyecto usa `portless`.
- **Descubrir y verificar** el puerto real tras el arranque, y persistirlo en
  `state.Meta.Port` (el campo que los 4 usos de §3.2 ya consumen).
- El bucle de descubrimiento **debe** estar acotado por: (a) deadline, (b)
  liveness del linaje (fallo rápido ~300 ms si el proceso muere con error), y
  (c) **ventana de estabilización** antes de aceptar un puerto (ver §6.4).
- Si tras el deadline no hay puerto TCP, registrar **"sin puerto"** de forma
  explícita, no agotar el timeout en silencio (cubre UDP-only, §4.2).
- Leer `/proc` directamente (11 ms) en lugar de `gopsutil.Processes()` (54 ms).
- Desambiguar el caso multi-puerto según §6.4.

**Por qué:** es la mitad que portless no puede hacer por nosotros (descubrir y
verificar el puerto real) y la que hace funcionar la reserva sin colisiones.

**Cuidado adicional:** `killPortHolder` (`daemon_unix.go:218`) debe **validar
propiedad** antes de matar: comprobar que el PID dueño del puerto (`PortOwnerPID`)
tiene `creation_time` coherente o pertenece a nuestro linaje. Con puertos
dinámicos el riesgo de matar un proceso ajeno reciclado aumenta.

#### 6.2.1 Schema del manifiesto (decidido)

Hoy `port` (`manifest.go:43`) está sobrecargado: significa a la vez *"el puerto que
usa la app"* y *"la señal que usa vroom para saber si el servicio vive"*, y `0`
significa "deshabilitado" (validación en `manifest.go:90`). Eso es lo que hace el
puerto *load-bearing*.

**Propuesta: separar las dos cosas, sin romper compatibilidad hacia atrás.**

```toml
port = 8080                 # puerto por defecto de la app (fallback). int, igual que hoy.
port_mode = "dynamic"       # "fixed" (default, actual) | "dynamic" | "none"
```

- `port` conserva **un único significado**: el puerto que la app usa cuando no la
  arranca vroom. Es exactamente el valor que la app usa en `PORT=${PORT:-8080}`,
  así que el patrón de §5.3 y el manifiesto quedan alineados.
- `port_mode` es una **adición pura**: sin `port_mode` el comportamiento es el de
  hoy, cero regresión. `fixed` = vroom no toca puertos. `dynamic` = reserva +
  inyecta + descubre. `none` = sustituye al `port = 0` actual (servicio sin
  puerto, p. ej. un worker de cola).
- No hace falta un tipo unión (`int` \| `string`) ni unmarshalling custom en TOML.

**Compatibilidad:** el `port = 0` actual equivale a `port_mode = "none"`. Se
puede conservar como alias silencioso o avisar por log; decisión de
implementación.

### 6.3 Pieza 3 — `portless` como proxy puro (decidido)

**Qué:** integrar `portless` como capa de nombres y TLS **delante**, sin dejar que
asigne puertos. vroom arranca el proxy una vez y, en cada `start`, registra la ruta
`<proyecto>.<worktree>.localhost → 127.0.0.1:<puerto que vroom ya sabe>` (vía
`PORTLESS_APP_PORT`).

**Por qué:** aporta URL estable, HTTPS con CA local y resolución de nombres por
subdominio — que es lo que rompe CORS/OAuth/HMR si el usuario accede por
`localhost:<puerto>`.

**Dependencias y riesgos:** Node 24+; el proxy quiere 80/443 (sudo) — se puede usar
un puerto custom tipo 1355 para evitar root; las rutas quedan *stale* si se mata
el proceso a la fuerza (hay reconciliación en `portless list`/prune).

**No-goal:** que `portless` gestione el ciclo de vida del proceso de la app. Su
`setsid` es justo lo que rompe `Stop` de vroom (§3.4). vroom lo lanza, vroom lo
mata; portless solo enruta.

### 6.4 Pieza 4 — Desambiguación multi-puerto (decidido, con evidencia)

**Por qué hay que resolverlo:** un servicio puede abrir varios listeners — Spring
con `management.server.port`, un exportador Prometheus, el puerto de debug de la
JVM, gRPC junto a HTTP, workers de multiproceso. Medido: si el servicio abre
`metrics` **antes** que el puerto principal, "primer puerto visto" elige el
equivocado (§4.3).

**Observación que simplifica el problema:** la ambigüedad sólo existe cuando vroom
*adivina*. Si la app honra `PORT` (compromiso de §5.3), vroom ya sabe de antemano
cuál es el puerto y sólo tiene que **verificarlo**. Por eso la ambigüedad se
resuelve en la ruta de reserva, no en la principal.

**Regla, en orden de preferencia:**

| Regla | Condición | Resultado |
|---|---|---|
| **R1** | el puerto reservado está entre los listeners | ese es el puerto. Determinista, sin heurística. |
| **R2** | varios listeners, el reservado no está | se sondea `health_path` (que **ya existe** en el manifiesto) en cada candidato; gana la mejor respuesta (200 > 2xx/3xx > 5xx > 404) |
| **R3** | varios, empate en R2 (incluye "no es HTTP") | gana el de **menor número de puerto**, de forma determinista. Marcar el servicio como *puerto no verificado*. |

**Evidencia medida:**

| Caso | Candidatos | Resultado |
|---|---|---|
| Honra `PORT`, metrics abre **primero** | `[41501, 42501]` | R1 → **41501** (reservado) OK |
| Honra `PORT`, main abre primero | `[41502, 42502]` | R1 → **41502** (reservado) OK |
| **Ignora `PORT`**, metrics 404 en `/health`, main 200 | `[41510, 42510]` | R2 → **41510** OK |
| Empate: ambos 200 en `/health` | `[41520, 42520]` | R3 → **41520**, determinista OK |
| **No es HTTP** (gRPC-like, sockets crudos) | `[41530, 42530]` | R3 → fallback. **vroom no puede saber cuál es el principal** |

**Limitación honesta:** para servicios que no son HTTP y además ignoran `PORT`,
vroom no tiene forma de saber cuál listener es el principal. La resolución por
heurística es una moneda al aire y **no debe presentarse como certeza**. Como
`health_path` ya cubre el caso HTTP (el mayoritario), y las apps que respetan el
compromiso de §5.3 quedan cubiertas por R1, **no se añade ningún campo de
manifiesto extra** hasta que aparezca un caso real que lo necesite.

**Bug encontrado al implementar el bucle** (relevante para quien lo escriba): la
primera versión de la sonda cerraba con "sin puerto" en cuanto una muestra venía
vacía, es decir **antes de que el proceso tuviera tiempo de hacer bind**. La
conclusión "no hay puertos" sólo es válida (a) si el linaje está muerto, o (b) tras
una ventana de observación suficiente sin cambios. Sin ese segundo guard, un
servicio lento se reporta sin puerto.

---

## 7. Puntos que siguen abiertos

1. **Qué hacer si el proyecto no acepta `PORT`.** El discovery sigue funcionando
   (§6.4), pero el puerto real puede diferir del reservado. Definir si eso es un
   error de arranque o sólo un aviso informativo. *Recomendación: aviso, no error*
   — el servicio funciona, simplemente no seized el puerto que se le ofreció.
2. **Alcance de la reserva.** Rango fijo 4000–4999 (propuesto) o cualquier puerto
   libre. El rango reduce la probabilidad de TOCTOU y facilita el debug manual.
   *No bloqueante: se puede empezar por el rango y ampliar después.*
3. **Ciclo de vida del proxy de `portless`.** Si lo arranca vroom, hay que
   decidir si se gestiona como un servicio más de vroom (aparece en la lista) o
   queda oculto como infraestructura. *No bloqueante.*

Nada de esto impide empezar por la Pieza 1, que es independiente de los puertos.

---

## 8. Contexto de la implementación

### 8.1 Restricciones del repositorio

- Gate local único: **`make check`** (build + lint + test). Debe quedar en verde.
- `go build ./... && go vet ./... && go test ./...` antes de dar nada por bueno.
- CI corre `Test` con `-race`; el scanner asume `fd` instalado.
- **El binario del usuario es `~/.local/bin/vroom`, no el del repo.** Tras
  cualquier cambio hay que desplegar: `go build -o ~/.local/bin/vroom ./cmd/vroom`
  (o `make install`). Sin esto, la TUI que prueba el usuario sigue siendo la
  versión vieja.
- Convención de ADRs en `docs/adr/` (ver `adr-0011` para el formato).

### 8.2 Zonas de código tocadas

- `internal/process/process.go` — `StartSpec.Env`, `StopSpec`
- `internal/process/daemon_unix.go` — `Stop` por linaje, `killPortHolder` con
  validación de propiedad
- `internal/process/detect.go` — nuevas primitivas de discovery
- `internal/manifest/manifest.go` — `port_mode`
- `internal/state/state.go` — `Meta.Port` pasa a ser el puerto real
- `internal/cli/cli.go`, `internal/tui/*` — mostrar puerto real + URL
- `internal/orchestrate/health.go` — `WaitForPort` con el puerto real

### 8.3 Salida de emergencia (no elegida)

Si en el futuro Node resultara problemático, un proxy nativo en vroom (estilo
Caddy) sería el sustituto: ~400 líneas, mismo contrato (`nombre → puerto`). El
resto del diseño —reserva, inyección, discovery, `Meta.Port`— no cambiaría, porque
vroom siempre es el dueño del puerto. Anotado para que la decisión sea reversible.

---

## 9. Criterios de aceptación

- [ ] Dos worktrees del mismo repo con el mismo `.vroom.toml` conviven, cada uno
      con un puerto distinto, ambos `running` y ambos con URL estable propia.
- [ ] Un servicio que muere con error al arrancar se reporta `stopped`/fallo en
      tiempo acotado (~<1 s), no tras agotar timeout.
- [ ] Un servicio que sólo abre UDP no cuelga el arranque (se marca "sin puerto").
- [ ] Un servicio lento (bind a los 3.5 s) se reporta con puerto, no como "sin
      puerto" (guard de estabilización, §6.4).
- [ ] Un servicio con dos listeners elige el principal de forma determinista
      (R1 si honra `PORT`; R2 por `health_path` si no).
- [ ] `Stop` no deja procesos huérfanos, ni siquiera cuando el hijo hace `setsid`
      o `portless` lo re-sid.
- [ ] Parar un worktree **no** mata el proceso de su twin.
- [ ] Arranque manual fuera de vroom sigue usando el puerto por defecto.
- [ ] Un manifiesto existente sin `port_mode` se comporta exactamente como hoy.
- [ ] `make check` en verde; binario desplegado a `~/.local/bin/vroom`.

---

*Informe generado tras investigación con validación empírica (sondas en
`/tmp/opencode/portprobe*` y `/tmp/opencode/mpprobe`). No se ha modificado código
del repositorio.*
