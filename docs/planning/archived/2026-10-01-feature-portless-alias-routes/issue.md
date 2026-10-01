# Issue — vroom registra rutas en portless (slice 4, proxy puro)

- Estado: borrador para revisión
- Fecha: 2026-09-30
- Slice: **4 de 4** de `docs/proposal-dynamic-ports.md` §6.3
- Base: `main` @ `e9df629` (slices 1–3 mergeados; esta issue **no los toca**)
- ADR padre: `docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md`, limitación 6

---

## 1. Qué falta

Con los slices 1–3 vroom ya es dueño del puerto: reserva uno libre en 4000–4999,
lo inyecta como `PORT`, descubre y **verifica** el puerto real contra un listener
de su propio linaje, y lo persiste. Ese descubrimiento ya existe y ya funciona.

Lo que **no** existe: una dirección estable. El puerto sigue siendo efímero, así
que cualquier referencia externa al servicio — una URL de callback OAuth, una
configuración CORS, un README, un bookmark, la config del proxy de otra app —
sigue atada a un número que cambia en cada arranque. El slice 4 cierra eso: cuando
vroom sabe el puerto real de un servicio, **registra una ruta con nombre en
portless** apuntando a ese puerto, y la **retira al parar**.

Eso es todo. vroom no cambia quién es dueño del puerto, ni del proceso, ni del
ciclo de vida. Sólo publica una dirección.

## 2. La decisión que ya está tomada y no se relitiga

**vroom sólo registra alias. Nunca arranca, gestiona, supervisa ni muestra el
proxy.** Si no hay un proxy de portless alcanzable, vroom **avisa una vez y
continúa**: el servicio sigue funcionando en su propio puerto, sólo que sin URL
estable.

Esto es deliberado y no es un detalle de implementación:

- Es la **limitación 6 de ADR-0012**: «`portless` ausente o incompatible es
  condición normal y no fatal».
- Es la **misma postura de fallo cerrado** que gobierna todo el cambio de puertos
  dinámicos: cuando vroom no puede probar algo, no lo afirma.
- El usuario **rechazó explícitamente** las dos alternativas:
  (a) que vroom arranque el proxy aunque no sea root, y (b) que vroom gestione el
  proxy como un servicio visible en la TUI — que además reintroduce la recursión
  de Stop-por-linaje (el `setsid` del proxy es exactamente lo que el slice 1
  arregló).

**La regla que gobierna todo el slice:** *la salud de un servicio nunca depende
de que exista su ruta*. Una ruta es una dirección, no una dependencia.

## 3. Evidencia empírica (medida, no asumida)

Todo lo de abajo se ejecutó contra portless **0.15.6** / Node **24.15.0** en esta
máquina, con el proxy del usuario ya corriendo en `http://127.0.0.1:1355`
(modo HTTP, `.localhost`, TLS desactivado, PID 1067267, **sin sudo**). La única
mutación autorizada fue un alias desechable `zz-vroom-probe`, **eliminado al
final**; el estado final está probado como idéntico al inicial
(`routes.json` = `[]`, `doctor` con 0 fallos y 0 avisos).

### 3.1 Los puntos que había que resolver

| # | Pregunta | Respuesta medida |
|---|---|---|
| 1 | ¿Un nombre de alias acepta punto? | **Sí, y se conserva literal.** `alias zz-vroom-probe.fix-ui 39999` → hostname `zz-vroom-probe.fix-ui.localhost`. **El esquema `<worktree>.<proyecto>` de la propuesta funciona tal cual.** |
| 2 | ¿`prune` destruye rutas de alias? (el riesgo más alto) | **No.** Con una ruta de alias registrada (`pid: 0`), `portless prune` → `No orphaned routes found.`, la ruta sobrevivió en `routes.json` y siguió sirviendo `200`. `doctor` la cuenta como `ok Routes: 1 active route`. |
| 3 | ¿El puerto destino debe estar escuchando? | **No.** Registrar contra un puerto cerrado sale con `exit 0`; el proxy responde `502`. Cuando aparece un listener, la **misma** ruta sirve `200` sin volver a registrar. |
| 4 | ¿Cómo se descubre el puerto del proxy? | **No hay API HTTP de administración** (`/`, `/health`, `/api/routes`, `/routes`, `/status` → `404`). El camino soportado es el fichero de estado **`~/.portless/proxy.port`**, 4 bytes con el número pelado, sin salto de línea. |
| 5 | Contrato de salida de `alias` / `--remove` / `list` | Ver §3.3. |

### 3.2 El resto de hallazgos que cambian el diseño

- **El registro es un UPSERT INCONDICIONAL.** Re-registrar el mismo nombre con
  otro puerto sale con `exit 0` y **sobrescribe en silencio**. `--force` no hace
  falta y no es observable. **No hay detección de conflictos** ⇒ vroom no puede
  confiar en que un nombre libre sea suyo, y por eso la lectura de vuelta importa.
- **`portless get` es INUTILIZABLE para verificar una ruta**: prefija la rama git
  actual. Desde este worktree, `get zz-vroom-probe.fix-ui` devolvió
  `portless-alias-routes.zz-vroom-probe.fix-ui.localhost`. Para verificar: `list`
  y `routes.json`.
- **El sufijo `.localhost` se normaliza, no se añade a ciegas.**
  `alias zz-vroom-probe.localhost` produjo hostname `zz-vroom-probe.localhost`,
  sin duplicar. `alias` y `--remove` normalizan igual, así que vroom puede pasar
  cualquiera de las dos formas.
- **portless deriva su propio prefijo de worktree de la RAMA git**, no del
  directorio: rama `feat/portless-alias-routes` → prefijo `portless-alias-routes`;
  desde `main` o desde un directorio sin repo no hay prefijo. Es decir, la
  convención nativa de portless es **inestable ante un rename de rama**.
- **Fragilidad de PATH confirmada**: `env -i PATH=/usr/bin:/bin` **no** resuelve
  `portless`. Sólo resuelve vía los shims de mise.
- `alias` acepta un nombre reservado: `alias run 39999` registra `run.localhost`.

### 3.3 Contrato exacto de salida (medido)

| Comando | stdout / stderr | exit |
|---|---|---|
| `alias <name> <port>` (nuevo) | `Alias registered: <name>.localhost -> 127.0.0.1:<port>` | **0** |
| `alias <name> <otro-puerto>` (ya existe) | idéntico, **sobrescribe** | **0** |
| `alias <name> <port> --force` | idéntico; `--force` no cambia nada observable | 0 |
| `alias` (sin args) | `Error: Missing arguments.` + usage | 1 |
| `alias <name> notaport` | `Error: Invalid port "notaport". Must be 1-65535.` | 1 |
| `alias --remove <existente>` | `Removed alias: <name>.localhost` | 0 |
| `alias --remove <inexistente>` | `Error: No alias found for "<name>.localhost".` | **1** |
| `list` (con o sin rutas) | tabla, o `No active routes.` | **0** |
| subcomando desconocido | — | 1 |

**Consecuencia para vroom:** `--remove` de algo que no existe es `exit 1` y es un
caso **benigno** (una parada repetida, o un stop de un servicio que ya no tenía
ruta, no es un error que deba subir). El upsert silencioso significa que un
`exit 0` **no prueba** que la ruta sea la que vroom cree: hay que leer de vuelta.

### 3.4 Efecto colateral sobre estado compartido (honestidad)

El primer `alias` **reescribió `routes.json` y se llevó por delante la ruta
stale** que el usuario tenía (`api-d.localhost`, PID 1066968 ya muerto, que
`doctor` reportaba como `warn Stale route`). Registrar una ruta recolecta basura
de forma automática.

Fue inofensivo — el PID estaba muerto, y antes y después hay **0 rutas activas** —
pero es un efecto real sobre estado compartido del usuario y **vroom debe saberlo**
en vez de suponer que `alias` es inocuo. No es motivo para no hacerlo: es el
comportamiento de la herramienta y la basura que recolecta es basura.

## 4. Qué se decide aquí

### 4.1 La perilla del manifiesto — aditiva pura

Precedente que hay que seguir: `port_mode` es una **adición**, y un manifiesto
que no lo declara se comporta exactamente como antes. Lo mismo aquí:

```toml
route_mode = "off"     # off (default) | auto | named
route_name = "api"     # sólo se lee con route_mode = "named"
```

Ausente = `off` = **cero cambios de comportamiento**, y en ese estado vroom ni
siquiera busca el binario de portless. `auto` deriva `<rama-o-worktree>.<proyecto>`;
`named` usa un nombre elegido por el usuario.

**Por qué dos campos y no uno.** El nombre es **configurable y derivable**, porque
sirven a dos propósitos distintos que no se pueden fundir:

- `auto` cubre el caso mayoritario: que el worktree tenga su propia URL sin
  escribir nada.
- `named` cubre la motivación real de la propuesta §2.2: una URL de callback
  OAuth o una regla CORS necesitan un nombre **estable**, y la convención nativa
  de portless —la rama— **no lo es**: cambia con un `git branch -m`.

Fusionarlos en un `route = "" | "auto" | "mi-nombre"` sería un enum de string
ambiguo que documenta su propio tipo en el schema. Y `route_mode` +
`route_name` es el mismo shape de dos campos que `port_mode` + `port`, con el
mismo trato: `port` conserva un único significado (`route_name` es el nombre de
la ruta y nada más).

Regla cross-field: `route_mode != "off"` exige puerto en algún modo. Es la
**segunda** regla cross-field del validador, siguiendo la de `health_path`.

### 4.2 El contrato JSON — honesto sobre lo que no se registró

Lección directa del slice 3: `port_verified: false` sólo se pudo emitir después de
quitar `omitempty` de un `bool`, y un `fixed` sano emite `verified: false` porque
vroom nunca lo confirmó. **No se emite una ruta que no se registró como si lo
estuviera.**

Dos superficies, dos papeles:

- `route_mode` — el eco de la **intención** del manifiesto, como hace `port_mode`.
- `route` (`*RouteInfo`, tri-estado, ausente = no hay contrato de ruta) con:
  - `name` — el hostname **pretendido**. Presente siempre que hay contrato,
    incluso degradado, porque el usuario necesita saber a qué URL *quería* llegar.
  - `status` — `registered` | `degraded`.
  - `url` — **sólo si `status == "registered"`**. Una URL que nadie ha registrado
    no se publica.
  - `reason` — sólo en `degraded`, enum legible por máquina: `portless_missing`,
    `proxy_unreachable`, `port_unresolved`, `no_port`, `register_failed`,
    `conflict`.

`url` ausente + `reason` presente es el estado honesto de "quise publicar una
dirección y no pude".

### 4.3 Verificar, no suponer

**`exit 0` no prueba que la URL resuelva.** Medido: `alias` es una escritura pura
del fichero de estado y **nunca contacta con el proxy**. Con el proxy parado sale
`exit 0` y escribe la ruta igual. Por tanto hay **dos** comprobaciones, y las dos
son obligatorias:

1. **Lectura de vuelta**: comparar el puerto publicado con el nuestro. Sin esto,
   dos vrooms con el mismo nombre se pisan en silencio y ambos reportan éxito.
2. **Verificación contra el proxy vivo**: un request al puerto del proxy, con el
   `Host` (y SNI TLS) al hostname de la ruta, sobre loopback. **Cualquier**
   respuesta HTTP prueba que el proxy enruta esa ruta, **incluido `502`** — un
   `502` dice "el proxy enruta y el servicio de detrás no responde", que es
   información distinta de "el proxy no sirve la ruta". Sólo un fallo de conexión o
   un timeout significa que no hay proxy sirviendo.

El **esquema se prueba, no se supone**: se intenta `https` y luego `http`, y se
publica el que respondió. Así el caso TLS/puerto 443 **no es un riesgo**: el diseño
no tiene un supuesto que el TLS pueda refutar, y no hizo falta medirlo.

### 4.4 El ciclo de vida

- **Registrar**: dentro de `startsvc`, **después** de que el discovery confirme un
  puerto real y **antes** de persistir el `Meta` final. Decidido por el
  hallazgo 3: como registrar no exige que el puerto escuche, registrar antes sólo
  compra una ventana de `502` visible, y registrar tarde además garantiza que
  nunca se publica una ruta para un servicio `port_unresolved` o `no_port`.
- **Retirar**: en el stop, junto a `ReleasePort`, en los **tres** sitios donde ya
  vive esa liberación (TUI, CLI, motor de stacks).
- **Cambio de puerto**: `alias` es un upsert, así que un re-registro en el
  arranque mueve la ruta al puerto nuevo sin tocar nada más.
- **Reconciliación en cada arranque, obligatoria.** `prune` **no** toca las rutas
  de alias (`pid: 0`, contadas como activas), luego **vroom es lo único que puede
  limpiarlas**. En cada arranque vroom toma las rutas que él mismo persistió y las
  lee contra el proxy vivo: retira la que ya no responde (incluida la que dejó un
  vroom que murió sin parar) y la que corresponde a un nombre ya derivado distinto.
  **Una ruta que responde y no es nuestra no se retira: se avisa** — el fallo
  cerrado aplica también a la limpieza.

Esto cierra por construcción lo que el primer borrador dejaba como limitación: un
`git branch -m` deja de dejar basura porque la reconciliación la retira, y las rutas
de un vroom muerto dejan de ser permanentes porque el siguiente arranque las
encuentra y las limpia.

### 4.4 Degradación — la regla, y la enumeración

**Regla:** la salud de un servicio nunca depende de que exista su ruta. Cada
fallo es un aviso, y el aviso sale por el canal que **ya existe**
(`Result.Warnings` → `m.notify` en la TUI, `stderr.log` del servicio en CLI).

| Fallo | Aviso | Dónde |
|---|---|---|
| `portless` no está en el PATH | sí, **una vez por servicio** | TUI + log |
| shim presente pero no ejecutable / Node < 24 | sí | TUI + log |
| `proxy.port` ausente o proxy no acepta conexión | sí | TUI + log |
| puerto `unresolved` o `no_port` | sí (ya existe hoy) | TUI + log |
| `alias` sale con != 0 | sí | TUI + log |
| nombre tomado por otro puerto | sí, `reason: conflict` | TUI + log + JSON |
| ruta escrita pero el proxy no la sirve | sí, `reason: proxy_unreachable` | TUI + log + JSON |
| `proxy.port` no existe (proxy parado) | sí, `reason: proxy_not_running` | TUI + log |
| `prune` se llevó la ruta | **imposible por medición** (pid: 0) | — |

Ningún caso de esta tabla aborta un arranque, falla un stop, rompe el JSON ni
cambia el estado de un servicio.

## 5. Fuera de alcance

- Que vroom arranque, supervise, muestre o detenga el proxy (rechazado).
- Un `vroom route prune` como comando aparte: las rutas huérfanas las limpia la
  **reconciliación del arranque**, que es obligatoria. No hace falta un comando.
- Escribir `~/.portless/routes.json` a mano, o hablar con el proxy por HTTP: no hay
  API, y escribir el fichero del proxy es exactamente la clase de acoplamiento
  que los slices 1–3 acaban de eliminar al hacer que vroom sea dueño del puerto.
- Tocar los slices 1–3.

## 6. Criterios de aceptación

- [ ] Un manifiesto sin `route_mode` se comporta **exactamente** como hoy, y vroom
      ni siquiera busca el binario de portless.
- [ ] Un servicio en `dynamic` con ruta queda accesible por su URL, y esa URL
      apunta al **puerto real**, no al reservado.
- [ ] El servicio es `running` y sano **con y sin** ruta; la ausencia de portless
      no cambia su estado ni su salud.
- [ ] Parar el servicio retira su ruta y deja intactas las de sus hermanos.
- [ ] `make check` en verde; binario desplegado a `~/.local/bin/vroom`.
- [ ] Las rutas que vroom crea sobreviven a `portless prune` (prueba de regresión
      sobre el hallazgo 2, que es la regresión más fácil de reintroducir).
- [ ] Una ruta escrita con el proxy parado **no** se reporta como disponible.
- [ ] Una ruta huérfana de un vroom muerto se retira en el siguiente arranque.
- [ ] El JSON nunca emite `url` para una ruta no verificada contra el proxy vivo.

## 7. La pieza de documentación

**ADR propio: `adr-0013-vroom-registers-portless-routes.md`.** No una enmienda a
ADR-0012. Motivos: 0012 es el contrato de **propiedad del puerto**, y esto no
toca la propiedad — la respeta. 0012 cerró explícitamente este punto como
«slice aparte (S4)», así que el registro de la ruta tiene su propio espacio
normativo que cerrar. Y hay decisiones con alternativas descartadas reales:
conocer la ruta por CLI frente a escribir `routes.json`; nombre derivado de la
rama (inestable) frente a nombre explícito; descubrimiento del puerto del proxy
por fichero de estado frente a raspar `doctor`.