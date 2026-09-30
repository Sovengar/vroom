# ADR-0013 — vroom registra rutas en portless sin poseer el proxy

- Estado: aceptada
- Fecha: 2026-09-30
- Feature: `0001-feature-portless-alias-routes`
- Cierra: `adr-0012` limitación 6

## Contexto

Con los slices 1–3 (`adr-0012`), vroom es dueño del puerto: lo reserva, lo inyecta
como `PORT`, descubre y verifica el real, y lo persiste. El proceso de la app es
suyo. Eso está resuelto.

Lo que queda abierto es la **dirección**. El puerto sigue siendo efímero, así que
cualquier referencia externa al servicio —callback OAuth, regla CORS, README,
bookmark, la config del proxy de otra app— queda atada a un número que cambia en
cada arranque. `adr-0012` cerró esto como «slice aparte (S4)» y como *«`portless`
ausente o incompatible es condición normal y no fatal»*.

Ese slice llega con dos restricciones ya decididas por el usuario, que aquí no se
relitigan:

- **vroom sólo registra alias.** No arranca, no gestiona, no supervisa, no muestra
  el proxy. Se rechazaron explícitamente las dos alternativas: que vroom
  auto-arranque el proxy, y que lo gestione como servicio visible en la TUI — que
  además reintroduce la recursión de Stop-por-linaje, porque el `setsid` del proxy
  es justo lo que el slice 1 arregló.
- **La ausencia de portless degrada a aviso, nunca a error.** Es la misma postura
  de fallo cerrado que gobierna el cambio de puertos dinámicos: cuando vroom no
  puede probar algo, no lo afirma.

Lo que faltaba era evidencia. Ninguna línea de código en vroom mencionaba portless.
Todo lo de abajo está **medido** contra portless 0.15.6 / Node 24.15.0, en parte
en un proxy **aislado** (`PORTLESS_STATE_DIR` en `/tmp`, `PORTLESS_PORT=1399`,
`PORTLESS_HTTPS=0`, `PORTLESS_SYNC_HOSTS=0`) para no tocar el proxy real del
usuario. **Este ADR no acepta ningún riesgo**: cada hazard está medido o
eliminado por diseño.

## Hechos medidos

| # | Hecho | Por qué importa |
|---|---|---|
| M1 | **`alias` es una escritura pura del fichero de estado: nunca contacta con el proxy.** Con el proxy caído sale `exit 0` y escribe la ruta igual | `exit 0` **no** prueba que la URL resuelva. Cambia el diseño entero |
| M2 | Leer `routes.json` de vuelta sólo prueba que escribimos | La verificación tiene que ser **contra el proxy vivo** |
| M3 | La ruta **sobrevive a un reinicio del proxy**: `kill -TERM` → `curl 000` → `proxy start` → `routes.json` sin cambios, `doctor`: `ok Routes: 1 active route` | Registrar aunque el proxy esté parado es **persistente y correcto** |
| M4 | Escribir un alias **no daña las rutas vivas** de `portless run`: la ruta con `pid` propio sigue presente, su proceso sigue vivo, sigue sirviendo | vroom puede compartir proxy con portless |
| M5 | **`prune` no toca rutas de alias** (`pid: 0`, contadas como `active`) | **vroom es lo único que puede limpiarlas** → reconciliación obligatoria |
| M6 | **`proxy.port` sólo existe mientras el proxy corre**; al pararlo desaparece | Su ausencia **es** la señal de que no hay proxy. Nunca asumir `1355` |
| M7 | Un nombre con punto se acepta y se conserva literal | El esquema `<worktree>.<proyecto>` funciona tal cual |
| M8 | **Upsert incondicional**: mismo nombre + otro puerto → `exit 0`, sobrescribe en silencio. `--force` no cambia nada observable. Sin detección de conflictos | La lectura de vuelta es obligatoria, no pulido |
| M9 | El puerto destino **no** tiene que estar escuchando; el proxy responde `502` hasta que aparece | Registrar **después** de descubrir no cuesta nada |
| M10 | `--remove` de un nombre inexistente → `exit 1` | **Benigno**: un stop repetido no es un error |
| M11 | Una app que **falla** bajo `portless run` imprime la URL pero **no registra ruta** | El espejo exacto del comportamiento correcto |
| M12 | `portless get` **prefija la rama git actual** | Inservible para verificar |
| M13 | portless deriva su prefijo de worktree de la **rama**, no del directorio | La convención nativa es inestable ante `git branch -m` |
| M14 | `env -i PATH=/usr/bin:/bin` **no** resuelve `portless` (vía shims de mise) | Un `portless` desnudo no se puede asumir |
| M15 | No hay API HTTP de administración (`/`, `/health`, `/api/routes`, `/routes`, `/status` → `404`) | El puerto del proxy se lee de `proxy.port` |
| M16 | **`$PORTLESS_HOME` no existe**: el CLI honra `$PORTLESS_STATE_DIR` y no `$PORTLESS_HOME` | El seam debe resolver el mismo directorio que el binario, o vroom lee `proxy.port` de un sitio y el binario escribe `routes.json` en otro |
| M17 | Un hostname con guion bajo, espacio, dos puntos o acentos se **rechaza** (exit 1), y uno con barra se **trunca en silencio** (`Feat/My_Branch.proj` → `feat.localhost`) | vroom tiene que sanear el nombre derivado: una rama de git está llena de guiones bajos y barras |
| M18 | El proxy responde **404 a un host que no conoce** y **502 a uno que enruta con el backend caído** | 404 y 502 no son la misma categoría; 404 contradice el enrutado, 502 lo prueba |

## Decisión

1. **vroom registra; el proxy enruta.** El mecanismo es
   `portless alias <name> <puerto real>`. vroom **no** pasa `PORTLESS_APP_PORT`:
   esa variable la consume `portless run <cmd>`, donde portless arranca el hijo y
   es dueño del proceso — el modelo que el usuario rechazó, y que además rompe el
   Stop-por-linaje del slice 1.

2. **`route_mode = "off" | "auto" | "named"`, default `off`, más `route_name`.**
   Adición pura, con la misma forma y trato que `port_mode` + `port`: el primer
   campo tiene un único significado, el segundo conserva el suyo, y un manifiesto
   que no declara nada se comporta exactamente como hoy — con `off`, vroom ni
   siquiera busca el binario. `route_mode != "off"` exige puerto en algún modo;
   `route_name` sin `named` se rechaza.

3. **El nombre es derivable y configurable.** `auto` da URL propia a cada worktree
   sin escribir nada; `named` da la URL **estable** que exigen un callback OAuth o
   una regla CORS. Ambas hacen falta porque la convención nativa de portless (M13)
   deriva de la rama y cambia con un `git branch -m`.

4. **Se registra después de descubrir y antes de persistir el `Meta` final.** Por
   **M9**, registrar antes sólo compra una ventana de `502`; registrar tarde
   garantiza además que nunca se publica una ruta para un servicio
   `port_unresolved` o `no_port`. El punto único de enganche es
   `internal/startsvc`, por el que ya pasan TUI, CLI y stacks.

5. **La lectura de vuelta es obligatoria.** Tras registrar, vroom lee la ruta
   (`list`) y compara el puerto publicado con el suyo. Por **M8** esto no es
   opcional: sin la comparación, dos instancias registrando el mismo nombre se
   pisan y **ambas reportan éxito**.

6. **Y además, se verifica contra el proxy vivo.** Ésta es la decisión que M1 y
   M2 obligan a escribir y de la que este slice no podría prescindir:

   - El puerto del proxy se lee de `proxy.port`. Por **M6**, si el fichero **no
     existe**, eso **es** el aviso de que no hay proxy → se degrada. **Nunca** se
     cae a un puerto supuesto.
   - Se emite **un** request al puerto del proxy, con el `Host` (y el SNI TLS)
     puesto al hostname de la ruta, sobre la dirección de loopback — sin depender
     de la resolución de nombres. **Cualquier** respuesta HTTP prueba que el proxy
     enruta esa ruta, **incluido `502`**: un `502` dice "el proxy enruta y el
     servicio de detrás no responde", que es información distinta de "el proxy no
     sirve la ruta". Sólo un fallo de conexión o un timeout significa que no hay
     proxy sirviendo.
   - Si nada responde: `degraded`, y **no se publica `url`**. Por **M11** es el
     mismo criterio que aplica portless cuando una app falla.

   Por **M3**, registrar con el proxy caído es persistente y correcto: la ruta se
   sirve cuando el proxy vuelva. La verificación decide **qué se publica**, no si
   se **registra**.

7. **El esquema de la URL se determina probando, no suponiendo.** Se intenta
   `https` y, si no responde, `http`; se publica el que respondió. **Así el
   caso TLS / puerto 443 no es un riesgo**: el diseño no contiene un supuesto que
   el TLS pueda refutar, y no hizo falta medirlo. El coste es una sonda.

8. **La reconciliación en cada arranque es obligatoria.** Por **M5**, `prune` no
   toca las rutas de alias, luego **vroom es lo único que puede limpiarlas**. En
   cada arranque vroom toma las rutas que **él mismo** persistió y, para cada una,
   la lee contra el proxy vivo:

   - el nombre persistido ≠ el derivado ahora → si la antigua no responde, se
     retira y se registra la nueva (cubre `git branch -m`);
   - la ruta persistida ya no responde → se retira (cubre lo que dejó un vroom que
     murió sin parar el servicio);
   - responde con otro puerto → conflicto, se avisa, **no se registra nada encima**;
   - responde y es la nuestra → no se toca (idempotencia).

   **Una ruta que responde y no es nuestra no se retira: se avisa.** El fallo
   cerrado que gobierna todo el cambio de puertos aplica también a la limpieza:
   borrar algo ajeno es peor que dejar una ruta de más.

   Esto es lo que hace que «un vroom que muere deja rutas apuntando a puertos
   muertos» no sea una limitación sino un mecanismo con recuperación.

9. **Ni el state dir ni el `PATH` se hardcodean.** State dir: `$PORTLESS_STATE_DIR`
   → `$XDG_STATE_HOME/portless` → `$HOME/.portless`. Binario: `$PORTLESS_BIN` →
   `exec.LookPath` → directorios de shim conocidos. Por **M14** un `portless`
   desnudo no se puede asumir; y vroom corre bajo un gestor de servicios cuyo
   entorno no es el shell de login del usuario, de modo que un path que funciona
   en el shell del usuario y falla en el daemon es un bug, no una configuración.

   > **CORRECCIÓN MEDIDA (M16).** Este texto decía `$PORTLESS_HOME` como primer
   > paso. Es incorrecto: contra portless 0.15.6, el CLI **ignora**
   > `$PORTLESS_HOME` y honra `$PORTLESS_STATE_DIR`. Implementar el orden aquí
   > documentado produce dos vistas distintas del mismo estado — vroom lee
   > `proxy.port` de un directorio y el binario escribe `routes.json` en otro— y
   > el síntoma es una ruta que se registra y luego **no se puede quitar**. Un
   > seam que resuelve una ruta que la herramienta no resuelve no es una
   > ventaja: es un modo de fallo silencioso. El orden correcto es el de arriba.

10. **Toda llamada está acotada por timeout.** Un `exec` sin cota contra un binario
    colgado cuelga el arranque, y eso sí sería una pérdida de disponibilidad — la
    degradación nunca puede ser peor que no tener la feature.

11. **El seam es inyectado y la suite es hermética.** El runner de CI no tiene
    portless, ni Node 24, ni proxy. La integración vive detrás de un seam, con la
    misma forma que el `procRoot` que ya usan las lecturas de `/proc`. A lo sumo
    un test de integración, marcado y skipped sin portless real.

12. **El contrato JSON tiene tres estados, y el ausente también es uno.**
    `route_mode` es la **intención**. `route` (`*RouteInfo`) es el **resultado**:
    `name` (hostname *pretendido*, haya éxito o no), `status`
    (`registered` | `degraded`), `url` (**sólo si `registered`**, y sólo con el
    esquema comprobado) y `reason` (sólo si `degraded`). Lección de
    `port_verified`, aplicada entera: **una URL que nadie verificó no se publica.**

13. **Retirada en el stop, junto a `ReleasePort`, en los tres caminos** (TUI, CLI,
    motor de stacks). Por **M10**, `--remove` de un nombre inexistente es `exit 1`
    y es **benigno**: un stop repetido no es un error.

## Consecuencias

- Positivas: la salud de un servicio **nunca** depende de que exista su ruta; el
  slice es publicable sin portless instalado; la suite es hermética; el estado que
  leen los agentes distingue siempre intención de resultado, y **verificado** de
  **escrito**.
- Negativas / trade-offs: una dependencia externa más en el arranque, aunque
  **acotada, derivada y degradable**; y como `exit 0` dejó de ser una señal,
  **cada** arranque paga una lectura de vuelta **y** una verificación en vivo. Es
  el precio de no poder afirmar una dirección falsa.
- Por **M4**, vroom puede compartir el proxy del usuario con las sesiones de
  `portless run` sin expulsar nada ajeno: no se gestionan entre sí.
- **Ninguna limitación asumida.** Las dos que existían en el borrador de este
  ADR —"un vroom que muere deja rutas permanentes" y "el esquema TLS no está
  medido"— están **eliminadas por diseño**: la primera por la reconciliación
  obligatoria (§8), la segunda por determinar el esquema probando (§7).
- Coste residual, no riesgo de producto: registrar una ruta recolecta entradas
  stale del `routes.json` del usuario. Es inofensivo (son PIDs muertos) y es
  comportamiento de la herramienta.

## Alternativas consideradas

- **Pasar `PORTLESS_APP_PORT` al arrancar la app** (lo que dice el plan
  archivado). Rechazada **por medición, no por gusto**: esa variable la consume
  `portless run <cmd>`, donde **portless** arranca el hijo y es dueño del proceso.
  Es el modelo que el usuario rechazó, y rompe el Stop-por-linaje del slice 1. Un
  executor que siguiera el plan archivado al pie de la letra escribiría código que
  no funciona.
- **Escribir `~/.portless/routes.json` directamente** en vez de llamar a la CLI.
  Rechazada: es el fichero de estado interno del proxy, sin contrato versionado, y
  reintroduce el tipo de acoplamiento que los slices 1–3 eliminaron al hacer que
  vroom sea dueño del puerto y nada más.
- **Raspar `portless doctor` para el puerto del proxy.** Rechazada: la salida es
  para humanos y cambia entre versiones. `proxy.port` es un fichero de estado con
  el número pelado. No hay API HTTP que consultar (M15).
- **Asumir el puerto por defecto (`1355`, o `80`/`443`).** Rechazada: contradice
  **M6** y el caso TLS. Un puerto supuesto que además se puede mover es exactamente
  el tipo de constante que este slice no debe introducir. Nada se supone: se lee,
  y si no está, se degrada.
- **Verificar con `portless get`.** Rechazada por medición (M12): prefija la rama
  git actual, así que devuelve un nombre que no es el que se registró.
- **Leer `routes.json` de vuelta como verificación suficiente.** Rechazada por
  **M1** y **M2**: `alias` no contacta con el proxy, así que el fichero demuestra sólo que escribimos. Una ruta en el fichero con el proxy parado
  parecería disponible y no lo es. **La verificación es contra el proxy vivo.**
- **Confiar en `exit 0` de `alias` como prueba de propiedad.** Rechazada por
  medición (M8): upsert incondicional sin detección de conflictos. Es la
  alternativa que habría producido direcciones mentirosas en silencio.
- **Suponer el esquema de la URL a partir de la configuración.** Rechazada: no
  hace falta saberlo. Se prueban los esquemas y se publica el que responde; así
  el caso TLS no está medido **y no importa**, porque no hay supuesto que el TLS
  pueda refutar.
- **Nombre derivado de la rama únicamente** (la convención nativa). Rechazada
  porque no es estable ante `git branch -m` (M13) y una URL estable es justamente
  lo que necesitan OAuth y CORS. Se conserva como `auto`, no como única opción.
- **Un solo campo `route = "" | "auto" | "mi-nombre"`.** Rechazada: un enum de
  string ambiguo documenta su propio tipo en el schema y rompe la simetría con el
  precedente `port_mode` + `port`.
- **Dejar la limpieza de rutas huérfanas para después / para `portless prune`.**
  Rechazada por **M5**: `prune` no toca `pid: 0`. Esperar a un barrido externo es
  esperar algo que no va a llegar. De aquí sale que la reconciliación sea
  obligatoria y no una mejora futura.
- **Retirar una ruta que responde pero no es nuestra** (limpieza agresiva).
  Rechazada: contradice el fallo cerrado de `adr-0012`. Se avisa. Borrar algo ajeno
  es el daño que este conjunto de ADRs existe para evitar.
- **Arrancar o supervisar el proxy desde vroom.** Rechazada por el usuario, y por
  §3.4 de `adr-0012`: el `setsid` del proxy es el huérfano que el slice 1 arregló,
  y hacerlo servicio visible reintroduce la recursión.
- **Fallo abierto cuando portless no está** (error de arranque). Rechazada:
  contradice `adr-0012` limitación 6 y la postura de todo el cambio de puertos.