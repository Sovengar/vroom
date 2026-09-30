# Plan — vroom registra rutas en portless (slice 4)

`adr_required: true` — este slice fija un contrato con una herramienta externa
(portless) que los slices 1–3 nunca tuvieron, y fija la obligación de **verificar
contra el proxy vivo** y de **reconciliar en cada arranque**. Hay alternativas
descartadas reales. ADR: **`adr-0013-vroom-registers-portless-routes.md`**,
formato de `adr-0011`.

**Este plan no acepta ningún riesgo.** Cada riesgo está **medido** (con la
evidencia) o **eliminado por diseño** (con el mecanismo). No queda ninguna fila
marcada como inferida ni como limitación asumida.

> **Corrección que este plan arrastra a propósito.** El plan archivado dice que
> vroom pasa `PORTLESS_APP_PORT`. **Es el mecanismo equivocado:** esa variable la
> consume `portless run <cmd>`, donde portless arranca el hijo y es dueño del
> proceso — el modelo que el usuario rechazó. El mecanismo correcto, medido, es
> `portless alias <name> <port>`. Un executor que siga el plan archivado al pie de
> la letra escribe código que no funciona.

---

## Resultado pretendido

Un servicio con ruta declarada es alcanzable por un nombre estable que apunta al
puerto **real** ya verificado, y ese nombre está **verificado contra el proxy vivo**
antes de publicarse. Al parar, desaparece. Y si portless no está, no funciona,
cuelga, no tiene Node 24, o no hay proxy corriendo, **el servicio arranca igual,
queda sano, y el usuario recibe un aviso**.

## Decisión del usuario — intacta

**vroom sólo registra alias. Nunca arranca, gestiona, supervisa ni muestra el
proxy.** Sin proxy alcanzable: un aviso, y el servicio sigue vivo en su puerto.
**La salud de un servicio nunca depende de que exista su ruta.**

## Hechos medidos que sostienen el diseño

Medidos con portless 0.15.6 / Node 24.15.0. Los cinco primeros en un proxy
**aislado** (`PORTLESS_STATE_DIR` en `/tmp`, `PORTLESS_PORT=1399`, `PORTLESS_HTTPS=0`,
`PORTLESS_SYNC_HOSTS=0`), sin tocar el proxy real del usuario ni `~/.portless`.

| # | Hecho medido | Consecuencia de diseño |
|---|---|---|
| M1 | `alias` es una **escritura pura del fichero de estado: nunca contacta con el proxy.** Con el proxy caído sale `exit 0` y escribe la ruta igual | **`exit 0` no prueba que la URL resuelva.** La verificación va contra el proxy vivo |
| M2 | Leer `routes.json` de vuelta sólo prueba que escribimos | La lectura de vuelta **no** es suficiente por sí sola; hace falta además la verificación en vivo |
| M3 | La ruta **sobrevive a un reinicio del proxy**: `kill -TERM` → `curl 000` → `proxy start` → `routes.json` sin cambios, `doctor`: `ok Routes: 1 active route` | Se puede registrar aunque el proxy se pare: es persistente |
| M4 | Escribir un alias **no daña las rutas vivas** de `portless run` (ruta con `pid` propio intacta, proceso vivo, sigue sirviendo) | vroom puede compartir el proxy con portless sin expulsar nada ajeno |
| M5 | `prune` **no toca** rutas de alias (`pid: 0`, contadas como `active`) | **vroom es lo único que puede limpiarlas** → la reconciliación es obligatoria |
| M6 | `proxy.port` **sólo existe mientras el proxy corre**; al pararlo desaparece | Su ausencia **es** la señal de que no hay proxy. Nunca se asume `1355` |
| M7 | Un nombre con punto se acepta y se conserva literal | El esquema `<worktree>.<proyecto>` funciona tal cual |
| M8 | El registro es un **upsert incondicional**: mismo nombre + otro puerto → `exit 0`, sobrescribe en silencio, sin detección de conflictos | La lectura de vuelta es obligatoria |
| M9 | El puerto destino **no** tiene que estar escuchando (el proxy responde `502`) | Registrar **después** de descubrir no cuesta nada y evita la ventana de `502` |
| M10 | `--remove` de un nombre inexistente → `exit 1` | **Benigno**: un stop repetido no es un error |
| M11 | Una app que **falla** bajo `portless run` imprime la URL pero **no registra ruta** | El espejo del comportamiento correcto: URL impresa sin ruta verificada no se publica |

## Enfoque

### 1. Perilla del manifiesto — aditiva pura

`route_mode = "off" | "auto" | "named"` (default `off`) más `route_name` (sólo con
`named`). Misma forma y mismo trato que `port_mode` + `port`: un manifiesto que no
declara nada se comporta exactamente como hoy — con `off`, vroom ni siquiera busca
el binario.

Dos campos porque el nombre sirve para dos cosas distintas: `auto` da URL propia a
cada worktree sin escribir nada; `named` da la URL **estable** que exigen un
callback OAuth o una regla CORS. La convención nativa de portless deriva de la
**rama**, que cambia con un `git branch -m` — y la reconciliación (§5) es
precisamente lo que hace que ese cambio no deje basura.

Reglas cross-field: `route_mode != "off"` exige puerto en algún modo;
`route_name` sin `named` se rechaza.

### 2. Seam inyectado, y **toda** ruta derivada

CI no tiene portless, ni Node 24, ni proxy. La integración vive detrás de un seam
inyectado, con la misma forma que el `procRoot` que ya usan las lecturas de
`/proc`: las pruebas usan fixtures sintéticos y la suite es hermética por defecto.

Y dos rutas que **no se pueden hardcodear**, porque vroom corre bajo un gestor de
servicios cuyo entorno no es el shell de login del usuario:

- **State dir**: `$PORTLESS_STATE_DIR` → `$XDG_STATE_HOME/portless` →
  `$HOME/.portless`. Nunca `/home/buble/.portless`.
  > **CORRECCIÓN MEDIDA (M16):** este plan decía `$PORTLESS_HOME` como primer
  > paso. Es incorrecto —el CLI honra `$PORTLESS_STATE_DIR` e ignora
  > `$PORTLESS_HOME`— e implementarlo así hacía las rutas imposibles de quitar.
  > Ver `context.md` y `portless.ResolveStateDir()`.
- **Binario**: `$PORTLESS_BIN` → `exec.LookPath("portless")` → directorios de shim
  de mise conocidos. Medido: `env -i PATH=/usr/bin:/bin` **no** lo resuelve.

Toda llamada a portless va **acotada por timeout**. Un `exec` sin cota contra un
binario colgado cuelga el arranque, y eso sí sería una pérdida de disponibilidad.

### 3. Registrar DESPUÉS de descubrir

Por **M9**, registrar contra un puerto cerrado sale con `exit 0` y da `502`.
Registrar antes sólo compra una ventana de error visible; registrar tarde
garantiza además que **nunca se publica una ruta para un servicio
`port_unresolved` o `no_port`**. Punto único de enganche: `internal/startsvc`, por
el que ya pasan TUI, CLI y stacks.

### 4. Verificar contra el proxy vivo, no contra el fichero

Este es el bloque que **M1 y M2** obligan a escribir, y el que el slice entero no
tenía:

1. **Descubrir el puerto del proxy.** Leer `proxy.port` del state dir resuelto. Por
   **M6**, si el fichero **no existe**, eso **es** el aviso de que no hay proxy:
   se degrada con motivo `proxy_not_running`. **Nunca** se cae a un `1355` supuesto.
2. **Registrar** con `portless alias <name> <puerto real>`.
3. **Leer de vuelta** (`portless list` / `routes.json`) y comparar el puerto
   publicado con el nuestro. Por **M8** esto es obligatorio: sin esta comparación,
   dos vrooms con el mismo nombre se pisan y ambos reportan éxito.
4. **Verificar contra el proxy vivo.** Un único request al puerto del proxy, con
   el `Host` (y el SNI TLS) puesto al hostname de la ruta, sobre la dirección de
   loopback — sin depender de la resolución de nombres. **Cualquier** respuesta
   HTTP prueba que el proxy enruta esa ruta, **incluido `502`**: un `502` dice
   "el proxy enruta y el servicio de detrás no responde", que es información
   distinta de "el proxy no sirve la ruta". Sólo un fallo de conexión o un timeout
   significa que no hay proxy sirviendo.

**El esquema de la URL se determina probando**, no suponiendo: se intenta `https`
y, si no responde, `http`; se publica el que respondió. Así el caso TLS/puerto no
443 **no es un riesgo**: no hay nada que suponer, se comprueba. El coste es una
sonda.

Si nada responde, el estado es `degraded` y **no se publica `url`**. Por **M11**,
esto es el mismo criterio que aplica portless cuando una app falla: URL impresa
sin ruta verificada no se publica.

Por **M3**, registrar con el proxy caído es **persistente y correcto**, no un
problema: la ruta se sirve cuando el proxy vuelva, sin registrarla otra vez. Por
eso la verificación decide qué se **publica**, no si se **registra**.

### 5. Reconciliación en cada arranque — obligatoria, no opcional

Por **M5**, `prune` no toca rutas de alias. Por tanto **vroom es lo único que
puede limpiarlas**, y una reconciliación es lo que hace que eso sea verdad.

En cada arranque, vroom toma las rutas que **él mismo** persistió y, para cada
una, la lee contra el proxy vivo:

- **El nombre persistido ≠ el nombre derivado ahora** (rama renombrada, `auto`):
  si la ruta antigua ya no responde, se retira y se registra la nueva.
- **La ruta persistida ya no responde** (la dejó un vroom que murió sin parar):
  se retira y se registra la correcta para el puerto actual.
- **La ruta persistida responde pero con otro puerto**: se detecta conflicto → se
  avisa y **no se registra nada encima**.
- **La ruta persistida responde y es la nuestra**: no se toca. La reconciliación es
  idempotente.

Y una línea que no es negociable: **una ruta que responde y no es nuestra no se
retira.** Se avisa. El fallo cerrado que gobierna todo el cambio de puertos aplica
también a la limpieza: borrar algo ajeno es peor que dejar una ruta de más.

Esto cierra, por construcción y no por documentación, los dos riesgos que la
primera versión de este plan dejó abiertos.

### 6. Superficie JSON — tres estados, y el ausente también es uno

- `route_mode` — la **intención**, como `port_mode`.
- `route` (`*RouteInfo`, tri-estado) — el **resultado**: `name` (el hostname
  *pretendido*, haya éxito o no), `status` (`registered` | `degraded`), `url`
  (**sólo si `registered`**, y sólo el esquema que se comprobó que responde) y
  `reason` (sólo si `degraded`).

La lección de `port_verified`, aplicada entera: **una URL que nadie verificó no se
publica.**

## Disposición de los riesgos — sin filas aceptadas

Cada fila está **medida** o **eliminada**. No hay filas "aceptadas" ni
"inferidas".

| # | Riesgo | Estado | Evidencia o mecanismo |
|---|---|---|---|
| R1 | `exit 0` no prueba propiedad: dos vrooms se pisan en silencio | **medido** + **eliminado** | M8: upsert incondicional, sin detección de conflictos. Eliminado por la lectura de vuelta obligatoria (comparar puerto) más la verificación contra el proxy vivo. Sin la comparación, `conflict` es inalcanzable y vroom publicaría una url ajena |
| R2 | Un vroom que muere deja rutas apuntando a puertos muertos | **eliminado** | M5: `prune` no las toca, luego **vroom es el único que puede**. La reconciliación de cada arranque (§5) las encuentra en el estado persistido, comprueba que no responden y las retira. Ya no es una limitación: es un mecanismo |
| R3 | Ruta registrada contra puerto cerrado → `502` | **eliminado** | M9 + orden: se registra sólo después de que el discovery confirme el puerto. La ventana es despreciable y la verificación en vivo distingue "el proxy enruta" de "el servicio responde" |
| R4 | Ruta apunta al **reservado** y no al **real** | **eliminado** | La fuente es `d.Port` del discovery, nunca `reserved` ni `meta.ReservedPort`. Escenario propio, porque es el fallo silencioso plausible de este slice |
| R5 | Binario colgado cuelga el arranque | **eliminado** | Timeout en toda llamada, incluido el propio arranque |
| R6 | Path hardcodeado de estado o `PATH` desnudo | **eliminado** | State dir por `$PORTLESS_STATE_DIR` → `$XDG_STATE_HOME/portless` → `$HOME/.portless` (orden corregido por M16); binario por `$PORTLESS_BIN` → `LookPath` → shims. Nada de `/home/buble/...` |
| R7 | URL con esquema TLS o puerto distinto de 1355 | **eliminado** | **No se supone nada: se prueba.** Se intenta `https` y luego `http`, y se publica el que responde. El caso TLS no está medido y **no hace falta medirlo**, porque el diseño no tiene un supuesto que el TLS pueda refutar |
| R8 | La ruta está escrita pero el proxy no la sirve, y se reporta como disponible | **medido** + **eliminado** | M1: `alias` no contacta con el proxy, así que `exit 0` con el proxy caído escribe la ruta y miente por omisión. Eliminado por la verificación obligatoria contra el proxy vivo, y por M11 como espejo del comportamiento correcto |
| R9 | `proxy.port` no existe porque el proxy está parado | **medido** + **eliminado** | M6: el fichero sólo existe mientras corre. Su ausencia **es** el mecanismo de detección: degrada con `proxy_not_running`. Nunca se cae a un puerto supuesto |
| R10 | Escribir la ruta de vroom daña rutas vivas de `portless run` | **medido** | M4: la ruta con `pid` propio sobrevive intacta, el proceso sigue vivo y `doctor` cuenta las rutas. vroom puede compartir proxy con portless |
| R11 | `git branch -m` en `auto` deja la ruta vieja apuntando a un puerto muerto | **eliminado** | La reconciliación (§5) detecta que el nombre persistido ≠ el derivado y retira el antiguo. Ya no hay rama que "deje" nada |
| R12 | `prune` se lleva la ruta de vroom | **medido** | M5: `prune` no las toca (`pid: 0`, `active`). Test de regresión obligatorio, porque es el hallazgo más fácil de reintroducir |
| R13 | La ruta muere con el proxy | **medido** | M3: sobrevive al reinicio; `routes.json` se recarga |
| R14 | CI sin portless ⇒ suite verde sin probar nada | **eliminado** | Seam inyectado + fixtures, suite hermética por defecto, y **como máximo un** test de integración marcado, skipped sin portless real |
| R15 | `get` como verificación | **medido** | Descartado: prefija la rama actual. Se usa `list` + la verificación en vivo |
| R16 | `--remove` de algo inexistente tumba el stop | **medido** | M10: `exit 1` es **benigno**; un stop repetido no es un error |

**Ninguna fila queda abierta.** Si algo no se puede resolver, es un `needs_input`
contra el usuario, no una limitación silenciosa.

## Orden de ejecución

1. **ADR-0013**, que fija el contrato antes de escribir código.
2. Seam de portless: resolución de state dir (con guarda de fichero ausente) y de
   binario, todas las llamadas acotadas.
3. Perilla de manifiesto y validación cross-field.
4. Registro + **lectura de vuelta + verificación contra el proxy vivo** en
   `startsvc`.
5. **Reconciliación** en cada arranque.
6. Retirada en los tres caminos de stop.
7. Superficie JSON y TUI.
8. ADR-0012 limitación 6 → cerrada; README con la reconciliación y el hecho de que
   vroom no gestiona el proxy.

## Puertas

- **`make check` en verde** (build + `golangci-lint v2.13.2` +
  `go test -race -count=1 -cover ./...`). Sin excepciones.
- **Suite hermética**: ninguna prueba llama a un `portless` real por defecto.
- **Guard estructural**: `TestDiscoveryIsNotInTheTUIRefreshPath`
  (`tui/app_test.go:2481`) prohíbe `DiscoverPort`/`ReservePort` en `app.go`. **El
  seam de portless tampoco puede entrar en el tick de la TUI**: si lo hace, el
  refresco de 2 s pasa a shelling out. Añadir el seam a esa lista de prohibidos.
- **Regresión obligatoria**: `prune` no destruye la ruta (sostiene R12).
- **Terminar con `make install`** → `~/.local/bin/vroom`, o la TUI que el usuario
  prueba sigue siendo la versión vieja.