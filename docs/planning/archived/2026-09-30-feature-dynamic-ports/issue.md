# Issue — Puertos dinámicos y URLs estables para worktrees paralelos

- **Slug:** `dynamic-ports`
- **Rama:** `feat/dynamic-ports`
- **Alcance:** núcleo de `vroom` + convención en los `.vroom.toml` de los proyectos del usuario
- **Investigación previa:** `docs/proposal-dynamic-ports.md` (medido en esta máquina, no estimado)
- **Estado:** aprobada en alcance — las 4 piezas entran

---

## 1. Problema

El usuario trabaja con **git worktrees**. Un worktree es una copia del mismo repo
con su propio `.vroom.toml`, y como las copias son idénticas, **declaran los mismos
puertos fijos**.

Consecuencia directa: **no se puede levantar la misma app en dos worktrees a la vez**.
Y el conflicto no falla limpio — vroom lo convierte en dos daños distintos:

- **Dato incorrecto.** Con apps que auto-incrementan (Next.js/Vite: `EADDRINUSE`
  → 8081), el segundo servicio sube a 8081 mientras vroom sigue creyendo 8080.
  Como 8080 responde (lo tiene el worktree A), `Evaluate` reporta el worktree B
  como `running` **y sano**, cuando en realidad está escuchando en otro sitio.
  Falso positivo silencioso.
- **Muerte de un proceso ajeno.** `Stop` ejecuta `fuser -k` sobre el puerto
  **sin comprobar propietario**: parar el worktree A mata el proceso del worktree B.

Con `port = 0` hay un tercer modo de fallo: un bind duro duplicado mata el segundo
proceso, y vroom lo reporta como fallo de arranque aunque la app esté perfectamente
configurada. El conflicto es indistinguible de un error de la app.

## 2. Objetivo

1. Levantar la misma aplicación en **N worktrees simultáneamente**, cada uno con su
   puerto real, **sin editar el `.vroom.toml` por worktree**.
2. Poder referenciar cada servicio por un **nombre estable**, no por un número de
   puerto que cambia en cada arranque. Sin esto, las URLs de OAuth, CORS, redirect
   URIs, README y bookmarks quedan atadas a un valor efímero.
3. **No tocar el arranque manual** fuera de vroom (`npm run dev`, `go run .`): el
   puerto por defecto no cambia para quien no usa vroom.

## 3. Contrato con las apps

El usuario modifica sus apps para leer el puerto del entorno con fallback al
puerto por defecto:

```bash
PORT=${PORT:-8080}      # lee si vroom lo inyecta; si no, el default
```

Ese default es **exactamente** el `port` del manifiesto. Manifiesto y app quedan
alineados por construcción, sin campo duplicado.

Consecuencia que simplifica todo el diseño: **la ambigüedad de "qué puerto es el
principal" sólo existe cuando vroom adivina**. Si la app honra `PORT`, vroom ya sabe
el puerto de antemano y sólo tiene que **verificarlo** que está escuchando.

## 4. Alcance aprobado — 4 piezas

### Pieza 1 — `Stop` por linaje (primero, y liberable solo)

**Qué:** al parar, `kill(-pgid)` no alcanza. Hay que resolver el conjunto de
descendientes del PID registrado (mapa `pid → ppid` leído de `/proc`) y señalizar
al grupo **y** a los descendientes que se hayan re-sid, iterando hasta que el linaje
quede vacío o venza el timeout.

**Por qué:** es un bug **preexistente e independiente de los puertos**. Cualquier
comando que se auto-demonice o llame a `setsid` (`nohup`, `pm2`, `portless`,
`docker run -d`) deja hijos fuera del grupo que **sobreviven al stop** y quedan
huérfanos, reparentados a init, **sigan escuchando**. Medido con `portless`:
el launcher queda en un process group y su backend en otro.

**Efecto lateral habilitante:** hace posible el descubrimiento de puertos por linaje
y elimina la dependencia de asumir "el servidor está en mi process group".

**Restricción dura:** el caso actual (grupo normal, sin re-sid) no puede regresar.

### Pieza 2 — Puertos dinámicos: reserva, inyección, descubrimiento

**Qué:**
- El manifiesto gana `port_mode = "fixed" | "dynamic" | "none"`. `fixed` es el
  default y es exactamente el comportamiento de hoy. `none` sustituye al `port = 0`
  actual (worker de cola, sin puerto). `dynamic` activa la pieza.
- En `dynamic`, vroom es **dueño del puerto**: reserva un puerto libre en el rango
  4000–4999, lo inyecta en el entorno del hijo, y **descubre y verifica** el puerto
  real tras el arranque antes de persistirlo.
- El bucle de descubrimiento está acotado por tres cosas: deadline, **liveness del
  linaje** (fallo rápido ~300 ms si el proceso muere con error), y una **ventana de
  estabilización** antes de aceptar un puerto.
- Si tras el deadline no hay puerto TCP, se registra **"sin puerto"** explícitamente
  — nunca agotar el timeout en silencio.
- Leer `/proc` directamente (11 ms) en lugar de `gopsutil.Processes()` (54 ms): el
  discovery **no puede vivir en el tick de la TUI**.
- El puerto real pasa a ser la **única** fuente de verdad de puerto en display,
  probes de salud y JSON de la CLI. Hoy el puerto se lee de dos sitios distintos
  (`Manifest.Port` en 10+ lugares de display, `Meta.Port` en 3) y esa divergencia
  es justamente lo que produciría "la UI muestra 8080 mientras la salud usa 41501".
  Migrar esa fuente es parte del trabajo, no un extra.
- `killPortHolder` debe **validar propiedad** antes de matar. Con puertos efímeros el
  riesgo de matar un proceso ajeno reciclado sube, y hoy el `Stop` de un servicio
  **ya parado** también ejecuta `fuser -k` (conserva el puerto, sólo limpia PID/PGID),
  lo que con puertos dinámicos mataría un proceso no relacionado.

### Pieza 3 — `portless` como proxy puro

**Qué:** `portless` se integra **delante**, como capa de nombres y TLS, sin decidir
puertos: vroom le pasa `PORTLESS_APP_PORT` con el puerto que ya posee. El proxy se
levanta una vez; en cada `start` se registra la ruta
`<proyecto>.<worktree>.localhost → 127.0.0.1:<puerto real>`.

**Por qué:** aporta URL estable, HTTPS con CA local y resolución por subdominio —
justo lo que rompe CORS / OAuth / HMR si el usuario entra por `localhost:<puerto>`.
Es la mitad que un "soporté el puerto fijo" no resuelve.

**No-goal:** `portless` no gestiona el ciclo de vida de la app. Su `setsid` es
precisamente lo que rompe el `Stop` de vroom. vroom lo lanza y vroom lo mata;
`portless` sólo enruta.

**Degradación:** `portless` requiere Node 24+. vroom **no** fija una versión de Node:
lo resuelve desde `PATH` y, si está ausente o falla, degrada con un diagnóstico
claro. "portless no disponible" es una condición **normal y no fatal**.

### Pieza 4 — Desambiguación multi-puerto (R1/R2/R3, ya validada empíricamente)

**Por qué:** un servicio puede abrir varios listeners — `management.server.port` de
Spring, un exportador Prometheus, el puerto de debug de la JVM, gRPC junto a HTTP.
Medido: si el servicio abre `metrics` **antes** que el principal (1.5 s de
diferencia), "primer puerto visto" elige el equivocado.

Regla, en orden de preferencia:

| Regla | Condición | Resultado |
|---|---|---|
| **R1** | el puerto reservado está entre los listeners | ese es el puerto. Determinista, sin heurística. |
| **R2** | varios listeners, el reservado no está | se sondea `health_path` (ya existe en el manifiesto) en cada candidato; gana la mejor respuesta (200 > 2xx/3xx > 5xx > 404) |
| **R3** | varios, empate en R2 (incluye "no es HTTP") | gana el de **menor número**, determinista. Se marca *puerto no verificado*. |

## 5. No-goals

- No se reescribe el proxy: `portless` se mantiene "a tope" (URL estable + TLS +
  subdominios). Un proxy nativo queda anotado **sólo** como salida futura
  (≈400 líneas, mismo contrato `nombre → puerto`, reversible porque vroom siempre
  es dueño del puerto).
- No se cambia el modelo de datos de `Meta` más allá del puerto y de un estado de
  puerto pendiente/no disponible.
- No se toca el arranque fuera de vroom.
- No se cambia el default global de Node del usuario: blast radius sobre todos sus
  proyectos, fuera de alcance.

## 6. Limitaciones documentadas (no garantías)

Estas se documentan como **no-garantías explícitas**, nunca como certeza:

- **No-HTTP que además ignora `PORT`**: vroom no tiene forma de saber qué listener es
  el principal. R3 elige de forma determinista y el servicio queda marcado
  *puerto no verificado*. Es una moneda al aire y así se presenta.
- **UDP-only**: no hay puerto TCP que descubrir. Se marca "sin puerto"; **nunca**
  cuelga el arranque.
- **TOCTOU de la reserva**: `bind(127.0.0.1:0)` + `close` devuelve el puerto al
  pool antes de que arranque el hijo. En 4000–4999 la colisión es improbable, pero
  la ventana existe.
- **`portless` ausente o incompatible**: se degrada con diagnóstico; el resto de las
  piezas sigue funcionando.
- **Bind duro duplicado** (la app ignora `PORT` y hace bind literal): sigue siendo un
  fallo de arranque de la app. vroom lo reporta más rápido y mejor, no lo evita.

## 7. Criterios de aceptación

- [ ] Dos worktrees del mismo repo, con el mismo `.vroom.toml`, conviven: cada uno
      con su puerto real, ambos `running`, ambos con su URL estable propia.
- [ ] **Display y JSON muestran el puerto real**, no el del manifiesto, y coinciden
      con el que usa la sonda de salud.
- [ ] Parar un worktree **no** mata el proceso de su twin.
- [ ] `Stop` no deja huérfanos, ni cuando el hijo hace `setsid` / `pm2` / `portless`.
- [ ] Un servicio que muere con error al arrancar se reporta en tiempo acotado
      (~<1 s), no tras agotar timeout.
- [ ] Un servicio sólo-UDP no cuelga el arranque: queda "sin puerto".
- [ ] Un servicio lento (bind a los 3.5 s) se reporta **con** puerto, no como
      "sin puerto".
- [ ] Un servicio con dos listeners elige el principal de forma determinista
      (R1 si honra `PORT`, R2 por `health_path` si no, R3 marcado como no verificado).
- [ ] Arranque manual fuera de vroom sigue usando el puerto por defecto.
- [ ] Un manifiesto existente **sin `port_mode`** se comporta exactamente como hoy.
- [ ] `portless` ausente ⇒ diagnóstico claro y el resto de vroom intacto.
- [ ] `make check` en verde y binario desplegado a `~/.local/bin/vroom`.