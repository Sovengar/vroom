# Context — vroom registra rutas en portless (slice 4)

Guidado por `../issue.md`, `../behavior.feature`, `../plan.md` y
`../adr/adr-0013-vroom-registers-portless-routes.md`.

Este archivo es la **única** entrada de navegación del executor. Todas las rutas
son absolutas. No hace falta buscar nada en el repo.

---

## 0. Frescura

- Worktree: `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes`
- Rama: `feat/portless-alias-routes`
- Commit base: `502f5ee5f053bad5b6184830be6340a20abd1a96` (sobre `main` @ `e9df629`)
- `codegraph`: **ready** — re-inicializado en este worktree (96 ficheros, 1794
  nodos, 5885). Se puede consultar como respaldo.
- Índice persistente en Engram: `codebase-index/vroom` (obs #2209, refrescada).

## 1. Gate

```bash
make check        # build + golangci-lint v2.13.2 + go test -race -count=1 -cover ./...
make install      # OBLIGATORIO al final: el binario del usuario es ~/.local/bin/vroom
```

`fd` debe estar instalado (el scanner lo prefiere con `--hidden`).

## 2. Corrección que hay que llevar adelante

El plan archivado (`docs/planning/archived/2026-09-30-feature-dynamic-ports/plan.md`,
§Slice 4) dice que vroom pasa `PORTLESS_APP_PORT`. **Está mal.** Esa variable la
consume `portless run <cmd>`, donde portless arranca el hijo y es dueño del
proceso. El mecanismo correcto, medido, es:

```
portless alias <name> <port>
portless alias --remove <name>
portless list
```

No copies el mecanismo del plan archivado.

## 3. Ficheros a tocar

| Ruta absoluta | Qué | Por qué |
|---|---|---|
| `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/manifest/manifest.go` | campo `RouteMode`/`RouteName` en `Manifest` (líneas 41–53), constantes de modo junto a `PortMode*` (57–65), `EffectiveRouteMode()` junto a `EffectivePortMode()` (70–82), reglas en `Validate()` (126–152), y el doc-comment del schema (3–21) | La perilla. Misma forma que `port_mode` |
| `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/manifest/portmode_test.go` (86 líneas) | **patrón a copiar** | Cómo se testea un campo aditivo de tres estados |
| `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/state/state.go` | `RouteName`/`RouteStatus` en `Meta` (46–67); las constantes de estado van **junto** a las de puerto (22–43) | Persistir la ruta para poder retirarla y para que un reinicio de la TUI no la pierda |
| `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/startsvc/startsvc.go` | registrar tras `DiscoverPort`, antes del `SaveMeta` final de `resolveDynamicPort` (129–179). El doc-comment de orden del paquete (9–17) **debe actualizarse**: el orden es ahora 6 pasos | Punto único de enganche: TUI, CLI y stacks ya pasan por aquí |
| `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/cli/cli.go` | campos `route_mode` / `route` en `ProjectInfo` (46–86); `buildProjectInfo` los puebla (294–353); `cmdStop` retira la ruta (552–575) | Superficie JSON + camino de stop |
| `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/tui/app.go` | `stopCmd` retira la ruta (588–620); `startedMsg.warns` ya los publica (884–886); `displayPort` (2205–2218) **no** se toca | Avisos y stop |
| `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/tui/serviceview.go` | mostrar la URL junto al puerto (84, 167–168, 218–219) | El usuario tiene que ver a dónde ir |
| `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/orchestrate/engine.go` | `stopProcess` retira la ruta (478–489) — cubre `stopService` y `abortAndCleanup` | Camino de stop de stacks |
| **NUEVO** `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/portless/` | el seam: resolver state dir, resolver binario, `Register`/`Remove`/`Lookup`, todos acotados por timeout y con `paths inyectables` | Todo lo nuevo. Paquete propio, como `internal/worktree` en ADR-0011 |

Rutas de tests nuevos: `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/portless/*_test.go`,
`/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/startsvc/` (extender), `/home/buble/dev/projects/vroom/.worktrees/vroom.feat-portless-alias-routes/internal/manifest/` (extender).

## 4. Contratos a respetar (no romper)

- **`Meta.Port` es la única verdad del puerto** (ADR-0012 decisión 4). La ruta se
  construye desde `d.Port`, **nunca** desde `reserved` ni desde
  `meta.ReservedPort`. Este es el fallo silencioso plausible del slice.
- **`Result.Warnings` (`startsvc.Result`) es el canal de avisos que ya existe.**
  Flujo: `startsvc.Result.Warnings` → `startedMsg.warns` (`tui/app.go:459,575`)
  → `m.notify(w)` en `tui/app.go:884–886` (visible, no bloqueante). En CLI los
  avisos van a `stderr.log` del servicio. **No inventar un canal de avisos nuevo.**
- **`StopSpec.Warn`** (`process/process.go:64–74`) es el precedente de "aviso no
  fatal que viaja hasta el log y la TUI".
- **`ReleasePort` tiene tres call sites** y la ruta debe retirarse en los tres:
  `tui/app.go:606`, `cli/cli.go:562`, `orchestrate/engine.go:487`.
- **Guard estructural `TestDiscoveryIsNotInTheTUIRefreshPath`**
  (`tui/app_test.go:2481–2491`): `app.go` **no puede mencionar** `DiscoverPort`,
  `lineageListenersAt` ni `ReservePort`. **El seam de portless tampoco puede
  entrar en el tick de la TUI** — si lo hace, el refresco de 2 s pasa a shelling
  out. Si hay que añadir `portless.Register` a esa lista de prohibidos, se añade.
- **Convención de raíz inyectada**: `xxxAt(root, pid)` con `procRoot = "/proc"`
  (`process/dynamic_unix.go`, `process/lineage_unix.go`). Los tests usan fixtures
  sintéticos, nunca `/proc` real. **Copiar esta convención** para el state dir y
  para el binario.

## 5. Patrones a seguir, uno por preocupación

| Preocupación | Patrón a copiar | Dónde |
|---|---|---|
| Paquete nuevo con seam inyectado y degradación | `internal/worktree` | `worktree.go:61` `List`, `worktree.go:168` `IsBareRepo`, y el sentinel `ErrGitUnavailable` |
| Invocar un binario externo **acotado** | `internal/worktree` | `worktree.go` usa `exec.CommandContext` con timeout |
| Nombre derivado de la **rama** (no del worktree: `DeriveName` no recibe la ruta) | `gitinfo.Branch(path)` | `gitinfo/gitinfo.go:15` |
| Aviso no fatal que llega a la TUI | `Result.Warnings` → `m.notify` | `startsvc/startsvc.go:148` → `tui/app.go:884` |
| Tri-estado honesto en JSON | `PortVerified *bool` | `cli/cli.go:62–68` — el comentario explica por qué `*bool` y no `bool`+`omitempty` |
| Campo aditivo de tres estados | `PortMode` + `EffectivePortMode` + `port = 0` como alias | `manifest/manifest.go:57–82`, test en `manifest/portmode_test.go` |
| Regla cross-field en el validador | `health_path` exige puerto | `manifest/manifest.go:145–147` |
| Releases de puertos y estado Meta | `ReservedPort` (ciclo de vida propio, distinto del real) | `state/state.go:50–55` |
| Test de proceso real detrás de `testing.Short()` | `process/dynamic_unix_test.go` | integración real, no mock |

## 6. Resolución de rutas (decidido por el orquestador, no hardcodear)

**State dir**, en orden: `$PORTLESS_STATE_DIR` → `$XDG_STATE_HOME/portless` →
`$HOME/.portless`. Dentro, `proxy.port`.

> **CORRECCIÓN MEDIDA (M16) — este texto decía `$PORTLESS_HOME` como primer
> paso, y era incorrecto.** Contra portless 0.15.6 el CLI **ignora**
> `$PORTLESS_HOME` y honra `$PORTLESS_STATE_DIR`. Implementar el orden que decía
> aquí produce dos vistas distintas del mismo estado —vroom lee `proxy.port` de un
> directorio y el binario escribe `routes.json` en otro— y el síntoma es una ruta
> que se registra y luego **no se puede quitar**. La autoridad es el código:
> `portless.ResolveStateDir()`, y hay un test que falla si `PORTLESS_HOME` vuelve
> a decidir (`TestResolveStateDir/PORTLESS_HOME_NO_decide`).
**Binario**, en orden: `$PORTLESS_BIN` → `exec.LookPath("portless")` →
directorios de shim de mise conocidos.

Nunca `/home/buble/...`. vroom corre bajo un gestor de servicios cuyo entorno no
es el shell de login del usuario: un path que funciona en el shell y falla en el
daemon es un bug.

**Guard obligatorio:** `proxy.port` **sólo existe mientras el proxy corre** (§7.5,
M6). Si el fichero no está, eso **es** la señal de que no hay proxy → degrada con
`proxy_not_running`. **Nunca** se cae a un puerto supuesto como `1355`.

## 7. Contrato de portless — MEDIDO, no re-verificar

portless 0.15.6 / Node 24.15.0. M1–M6 se midieron en un proxy **aislado**
(`PORTLESS_STATE_DIR` en `/tmp`, `PORTLESS_PORT=1399`, `PORTLESS_HTTPS=0`,
`PORTLESS_SYNC_HOSTS=0`) para no tocar el proxy real del usuario. **No repitas
estas pruebas**: el proxy del usuario está en `~/.portless` y no es tuyo.

### 7.1 Comandos, salida y códigos de salida

| Comando | stdout / stderr | exit |
|---|---|---|
| `alias <name> <port>` | `Alias registered: <name>.localhost -> 127.0.0.1:<port>` | **0** |
| `alias <name> <otro-puerto>` (ya existe) | idéntico, **sobrescribe en silencio** | **0** |
| `alias <name> <port> --force` | idéntico; `--force` no cambia nada observable | 0 |
| `alias` (sin args) | `Error: Missing arguments.` + usage | 1 |
| `alias <name> notaport` | `Error: Invalid port "notaport". Must be 1-65535.` | 1 |
| `alias --remove <existente>` | `Removed alias: <name>.localhost` | 0 |
| `alias --remove <inexistente>` | `Error: No alias found for "<name>.localhost".` | **1 → BENIGNO** |
| `list` (con o sin rutas) | tabla, o `No active routes.` | **0** siempre |
| `prune` | `No orphaned routes found.` | 0 |
| subcomando desconocido | — | 1 |

### 7.2 Forma de `routes.json`

```json
[
  { "hostname": "zz-probe.fix-ui.localhost", "port": 39999, "pid": 0 }
]
```

- `hostname` = `<name>` + `.localhost`, **normalizado**: si el nombre ya acaba en
  `.localhost` no se duplica. `alias` y `--remove` normalizan igual, así que vroom
  puede pasar cualquiera de las dos formas.
- `pid: 0` ⇒ **ruta de alias, sin dueño**. Es lo que la hace a prueba de `prune`.

### 7.3 Los hechos que cambian el diseño

- **M1 — `alias` es una escritura pura del fichero de estado. NUNCA contacta con el
  proxy.** Con el proxy parado: `alias iso-probe-down 39996` → `Alias registered`,
  **exit 0**, y la ruta queda escrita igual.
  ⇒ **`exit 0` no prueba que la URL resuelva.** La verificación va contra el
  **proxy vivo**, nunca sólo contra el fichero (§7.4).
- **M2 — Leer `routes.json` de vuelta sólo prueba que escribimos.** Por eso la
  lectura de vuelta, aunque obligatoria, **no basta**: hace falta además §7.4.
- **M3 — La ruta sobrevive a un reinicio del proxy.** `kill -TERM <proxy.pid>` →
  `curl 000`; `proxy start` → `routes.json` **sin cambios**, `doctor`:
  `ok Routes: 1 active route`. Registrar con el proxy caído es **persistente y
  correcto**: se sirve cuando vuelva, sin registrarla otra vez. La verificación
  decide **qué se publica**, no si se **registra**.
- **M4 — Escribir un alias NO daña las rutas vivas de `portless run`.** Con
  `{"hostname":"miapp.localhost","port":4628,"pid":1335803}` sirviendo `200`, un
  `alias vroom-final 39992` deja esa entrada **intacta**, el proceso vivo, y
  `doctor` con `ok Routes: 6 active routes`. vroom y portless pueden compartir proxy.
- **M5 — `prune` NO toca las rutas de alias** (`pid: 0`, contadas como `active`).
  ⇒ **vroom es lo único que puede limpiarlas** ⇒ **la reconciliación en cada
  arranque es obligatoria**, no una mejora futura.
- **M6 — `proxy.port` sólo existe mientras el proxy corre** (ver el guard en §6).

Otros: nombre con punto aceptado y literal; puerto destino no necesita escuchar
(el proxy responde `502` hasta que hay listener); `portless get` **prefija la rama
git actual** (inservible para verificar); `portless run` acepta nombres
reservados; no hay API HTTP de administración (`/`, `/health`, `/api/routes`,
`/routes`, `/status` → `404`).

### 7.4 La verificación contra el proxy VIVO — cómo se hace exactamente

**Un** request al **puerto del proxy**, con el `Host` (y el SNI TLS) puesto al
hostname de la ruta, y dialing la **dirección de loopback** para no depender de la
resolución de nombres de `.localhost`:

- **Cualquier** respuesta HTTP prueba que el proxy enruta esa ruta — **incluido
  `502`**. Un `502` significa "el proxy enruta y el servicio de detrás no
  responde", que es información distinta de "el proxy no sirve la ruta". **No lo
  tractes como fallo de la ruta.**
- **Sólo** un fallo de conexión o un **timeout** significa que no hay proxy
  sirviendo → `degraded`, y **no se publica `url`**.
- **Esquema: se prueba, no se supone.** Intenta `https`; si no responde, `http`.
  Publica el que respondió. Esto **elimina** el caso TLS / puerto 443 sin
  necesitar medirlo, porque no hay supuesto que el TLS pueda refutar.
- Todo el request va **acotado por timeout**, como las llamadas al binario.

### 7.5 Lo que NO sirve — no lo intentes

- **`portless get` para verificar.** Prefija la rama git actual: desde un worktree,
  `get zz-probe.fix-ui` devuelve `portless-alias-routes.zz-probe.fix-ui.localhost`.
  Usa `list`.
- **No hay API HTTP.** Todo devuelve `404`.
- **No raspes `doctor`** para el puerto del proxy: salida para humanos que cambia
  entre versiones. `proxy.port` es un fichero con el número pelado.
- **No lances nunca `portless prune`.** No toca `pid: 0` (§7.3 M5) y el estado del
  proxy es del usuario, no tuyo.
- **`portless run <cmd>` no es el camino** (§2).
- **`env -i PATH=/usr/bin:/bin` no resuelve `portless`**: vive tras los shims de
  mise. Resolver según §6.

## 8. Pruebas

- **Herméticas por defecto.** Fixtures vía seam inyectado. Ninguna prueba llama a
  un `portless` real salvo el test de integración marcado.
- **Tests de integración marcados**, con skip explícito si no hay portless real
  (gated por `VROOM_PORTLESS_INTEGRATION=1`, estado aislado en un temporal).
  actual hay TRES: ciclo completo (registro → `list` → verificación → `remove` →
  desaparece), M3 (sobrevive a un reinicio real del proxy, parado por PID) y M4
  (escribir un alias no expulsa una app viva de `portless run`).
  *(Corrección posterior: este archivo decía "como máximo uno".)*
- **Regresión obligatoria: `prune` no destruye la ruta** (§7.3 M5). Es el hallazgo
  más fácil de reintroducir y el que justifica la reconciliación.
- **Regresión obligatoria: una ruta escrita con el proxy parado NO se reporta
  disponible** (§7.3 M1). Es el escenario que sólo pasa si existe la verificación
  contra el proxy vivo.
- **Regresión obligatoria: `proxy.port` ausente degrada y no supone `1355`**
  (§7.3 M6).
- **Regresión obligatoria: la reconciliación retira una ruta renombrada y una
  huérfana de un vroom muerto**, y **no** retira una ruta viva ajena.
- Runner: `go test -race -count=1 -cover ./...` vía `make test`. `fd` instalado.
- Hay un guard de higiene en `process/hygiene_unix_test.go` (`TestMain`) que falla
  si un helper sobrevive a la suite: no dejar helpers de test huérfanos.

## 9. No-objetivos

- Que vroom arranque, gestione, supervise o muestre el proxy.
- Que vroom escriba `~/.portless/routes.json` a mano (se usa la CLI).
- Un `vroom route prune` como comando aparte. **Las rutas huerfanas se limpian con
  la reconciliacion del arranque**, que es obligatoria (§7.3 M5: `prune` no las
  toca, luego vroom es el unico que puede). No hace falta un comando nuevo.
- Tocar los slices 1-3.
- Sugerir dependencias de Node: vroom no escribe ni modifica la config de node
  del usuario.

## 10. Documentacion al terminar

1. `docs/adr/adr-0012-*.md` - **limitacion 6** -> cerrarla, apuntando a ADR-0013.
2. `docs/proposal-dynamic-ports.md` §6.3 - corregir el mecanismo (`PORTLESS_APP_PORT`
   -> `portless alias`) y marcar la pieza 4 como entregada.
3. `docs/planning/archived/2026-09-30-feature-dynamic-ports/behavior-portless.feature`
   dice que portless 0.13.0 no resuelve por el shim: **desactualizado** (0.15.6,
   resuelve). **No modificar el archivo archivado** - esta bajo
   `docs/planning/archived/`, que es audit trail. La correccion vive en los
   documentos activos.
4. README/docs del usuario: decir que **vroom no gestiona el proxy** (solo registra
   rutas), y que **la reconciliacion del arranque es la que limpia las rutas
   huerfanas**, porque `prune` no las toca.

## 11. Riesgos - este slice no acepta ninguno

**No queda ningun riesgo abierto.** Los dieciseis hazards estan medidos o
eliminados por diseno; la tabla completa esta en `../plan.md` y los hechos
medidos en §7 de este archivo. Si al ejecutar aparece algo que no se puede
medir ni eliminar con el seam, **es un `needs_input` contra el usuario**, no una
limitacion que se escriba en un documento y se siga adelante.
