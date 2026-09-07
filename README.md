# vroom

TUI en Go + Bubbletea para gestionar servicios de múltiples proyectos desde un solo punto.
Escanea el directorio actual (2 niveles de recursividad), detecta proyectos por marcadores
de lenguaje (`pom.xml`, `go.mod`, `package.json`, `pyproject.toml`, `requirements.txt`,
`Cargo.toml`), y permite iniciar/detener servicios daemonizados que **sobreviven al cierre
de la terminal**, con consola en tiempo real y detección de estado (PID + puerto + patrón
de proceso).

## Dashboard

Un único dashboard estilo panel "Services" de IntelliJ:

- **Árbol de proyectos** (izquierda, ancho fijo, scroll automático):
  grupos seleccionables y colapsables con `enter` (colapsado muestra
  `grupo (running/total)`), filas compactas de glifo + nombre (⚠ = sin
  manifiesto); `s` sobre un grupo arranca/para todos sus miembros.
- **Panel de detalles** (derecha, fijo, dos columnas): a la izquierda
  ruta, lenguaje, rama git, grupo, puerto, patrón, PID y logs; a la
  derecha los comandos del manifiesto (`start`/`stop`/`install`/`build`,
  con `—` los no configurados).
- **Panel inferior con pestañas**:
  - `Console` — stdout + stderr mergeados en tiempo real (tail incremental
    cada 400ms, auto-follow, pausa al hacer scroll-up). `t` alterna merged/
    stdout/stderr.
  - `Threads` — hilos del proceso a nivel OS (`/proc/<pid>/task`): nombre,
    TID, estado y CPU% ordenados por consumo. Funciona para cualquier
    lenguaje sin debugger (Java expone nombres de hilo, Go goroutines).

## Instalación

```bash
go build -o ~/.local/bin/vroom ./cmd/vroom
```

Requisitos en runtime: Linux (v1), shell POSIX, `xdg-open` no requerido.

## Uso rápido — playground

El repo incluye `playground/` con 8 proyectos ficticios listos para probar todo el ciclo:

```bash
cd playground
vroom
```

| Proyecto | Lenguaje | Grupo | Puerto | Comando |
|---|---|---|---|---|
| products-api-java | Java | tienda | 8081 | `java src/main/java/com/example/Main.java` |
| orders-api-springboot | Java (Spring Boot) | tienda | 8084 | `mvn spring-boot:run` |
| billing-api-go | Go | — | 8082 | `go run main.go` |
| inventory-api-python | Python | — | 8083 | `python3 app.py` |
| web-frontend | JavaScript | tienda | 5173 | `node server.js` |
| search-api-python | Python | — | 8090 | `python3 -m http.server 8090` |
| auth-api-go | Go | — | 8091 | `go run main.go` |
| nginx-proxy | otro | — | 8080 | `docker run --rm -p 8080:80 nginx:alpine` |

Notas:
- `orders-api-springboot` requiere **JDK 17+ y Maven**; la primera ejecución descarga
  dependencias (verás todo el log de arranque de Spring en la vista de logs).
- `nginx-proxy` requiere Docker.
- `web-frontend` incluye `command_install`/`command_build` en su manifiesto y un `mise.toml` con
  tasks (y uno oculto) para probar `b`, `i` y el picker de `t` sin configurar nada.
- Cada proyecto define su servicio en un `.vroom.toml` — así se configura el tuyo:

```toml
name = "mi-servicio"
group = ""                       # vacío = sin agrupar
command_start = "go run main.go"
port = 8080                      # para detección (0 = deshabilitado)
process_pattern = ""             # patrón pgrep (opcional)
command_install = "npm install"  # one-shot con la tecla i (opcional)
command_build = "mise run build" # one-shot con la tecla b (opcional)
command_stop = "docker stop x"   # parada graciosa con la tecla s (opcional)
```

`command_stop` es para servicios donde matar el process group no basta (el
proceso hijo sobrevive al kill, ej. un contenedor Docker): al pulsar `s`, vroom
ejecuta ese comando primero (con banner, visible en la consola) y después aplica
el shutdown de limpieza habitual (SIGTERM → 5s → SIGKILL al PGID).

## Keybindings

| Tecla | Acción |
|---|---|
| `j`/`k` o flechas | Navegar el árbol (cíclico, scroll automático) |
| `enter` | Colapsar/expandir el grupo seleccionado |
| `s` | **Start/stop** (toggle contextual; sobre un grupo, a todos sus miembros) |
| `R` | Restart (stop → start con timeout) |
| `b` | **Build**: comando one-shot del manifiesto (`command_build = "..."`) |
| `i` | **Install**: comando one-shot del manifiesto (`command_install = "..."`) |
| `t` | **Tasks**: picker de tasks del `mise.toml` (ver [mise](#integración-con-mise-opcional)) |
| `a` | **Ask AI**: pregunta a un agente (opencode/pi/hermes/jcode) con tu prompt → chat nuevo; el input se prellena con contexto de la app ([config global](#configuración-global-ask-ai)); el despacho es configurable |
| `C` | **Clear**: limpia la consola en memoria (los ficheros conservan el histórico) |
| `1` / `2` | Pestaña Console / Threads (`tab` cicla) |
| `c` | Modo de consola: merged → stdout → stderr |
| `pgup`/`pgdn`, `g`/`G` | Scroll de consola con teclado (pausa el follow; `G` lo reactiva) |
| rueda del mouse | Scroll de consola (3 líneas por click; hasta el final reactiva el follow) |

Las líneas de log más largas que el panel se envuelven (soft wrap): el contenido
completo es visible y el color se conserva en las líneas de continuación.
| `l` | Abrir ambos logs en el editor (`$VISUAL`/`$EDITOR`, default nvim, split vertical) — `o` alias |
| `r` | Refresh forzado |
| `q`/`Esc` | Salir (`Esc` cierra primero el prompt/picker) |

El panel de detalles es fijo: se muestra siempre que hay espacio y no tiene toggle.

## Configuración global (ask AI)

`~/.config/vroom/config.toml` (respeta `$XDG_CONFIG_HOME`; override con
`$VROOM_CONFIG`). Sin fichero, todo funciona con defaults.

```toml
[ask]
launcher = "auto"     # auto | herdr | inline | custom
direction = "right"   # split de herdr: right | down
target = "pane"       # pane | tab
focus = false         # false = --no-focus (vroom conserva el foco)
# launcher_cmd = "tmux new-window -c {dir} -n vroom-{agent} -- {cmd}"  # solo custom
# prompt = "Given the app {name} with logs in {logs}, "  # prefill del input; "" desactiva

# Opcional: reemplaza los agentes built-in
[ask.agents.opencode]
cmd = "opencode --prompt {prompt}"
[ask.agents.pi]
cmd = "pi {prompt}"
[ask.agents.hermes]
cmd = "hermes chat -q {prompt}"
[ask.agents.jcode]
cmd = "jcode run {prompt}"
```

**Cómo abre vroom el agente** (patrón de worktrunk: el binario del agente con el
prompt como argumento, en el directorio del proyecto):

- `herdr` — abre un **pane/tab nuevo** (`pane split --cwd <proyecto>` + `pane run`)
  y ejecuta el agente ahí; vroom sigue vivo. Requiere correr vroom dentro de herdr
  (`HERDR_ENV=1`).
- `inline` — suspende vroom y corre el agente en primer plano; al salir, vuelves
  al dashboard.
- `custom` — tu plantilla de shell con placeholders: `{dir}` (proyecto),
  `{agent}` (nombre) y `{cmd}` (comando completo, quoteado). Ejemplo con tmux:
  `tmux new-window -c {dir} -n vroom-{agent} -- {cmd}`.
- `auto` (default) — herdr si está disponible; si no, inline.

Agentes built-in (solo se muestran los instalados en PATH; con uno solo se salta
el picker):

| Agente | Invocación |
|---|---|
| opencode | `opencode --prompt "<prompt>"` |
| pi | `pi "<prompt>"` |
| hermes | `hermes chat -q "<prompt>"` (en TTY la sesión queda interactiva) |
| jcode | `jcode run "<prompt>"` (one-shot: responde y termina; su TUI no acepta prompt inicial) |

**Prefill del prompt**: al abrir el input de ask, vroom asume que la petición va
sobre la app seleccionada y prellena el template `[ask] prompt` con los
placeholders `{name}` (proyecto), `{dir}` (ruta) y `{logs}` (directorio del
servicio con `stdout.log`/`stderr.log`); el cursor queda al final para que
escribas tu petición. El input es multi-línea: arranca grande, crece con el
contenido hasta un cap y luego hace scroll interno. Con `prompt = ""` el input
queda vacío.

Un config malformado no rompe nada: vroom aplica defaults y notifica el error al arrancar.

## Integración con mise (opcional)

**vroom no requiere mise.** Todo lo esencial (start/stop, logs, threads) funciona
solo con `.vroom.toml`. La integración existe en dos puntos, y ambos son opt-in:

1. **`b` (build) e `i` (install)** ejecutan el comando que tú pongas en el
   manifiesto, con `sh -c` en el directorio del proyecto. Si prefieres mise,
   escribes `command_build = "mise run build"`; si prefieres pnpm,
   `command_build = "pnpm build"`.
   vroom nunca añade `mise run` por su cuenta.
2. **`t` (tasks)** lista los tasks de la sección `[tasks.*]` del `mise.toml` del
   proyecto (parseo directo del fichero; listar **no** necesita el binario). Al
   elegir uno se ejecuta `mise run <task>` — ahí sí hace falta tener mise
   instalado en el PATH. Si el proyecto no tiene `mise.toml`, la tecla solo
   notifica `no mise.toml`.

Ejemplo de un frontend con mise:

```toml
# mise.toml
[tasks.install]
run = "pnpm install"

[tasks.build]
description = "Build de producción"
run = "pnpm build"

[tasks.serve]
run = "pnpm dev"
```

```toml
# .vroom.toml
name = "web-frontend"
command_start = "pnpm dev"          # o "mise run serve"
command_install = "mise run install" # tecla i
command_build = "mise run build"     # tecla b
port = 5173
```

La salida de build/install/tasks va con un banner a los logs del proyecto y se ve
en la pestaña Console; al terminar notifica `build ok (3.2s)` o
`build failed (exit 1)`. Solo puede correr un job por proyecto a la vez; los jobs
sobreviven al cierre de la TUI (misma semántica que los servicios).

## Estado y logs

```
~/.local/state/vroom/services/{hash}/   # hash = 8 hex de SHA-256 del path del proyecto
├── meta.json    # nombre, pid, pgid, puerto, estado...
├── pid, pgid    # credenciales del proceso (se limpian al detener)
├── stdout.log   # stdout del servicio
└── stderr.log   # stderr del servicio (se conservan como histórico)
```

Los servicios arrancan con `setsid` (nuevo session leader): cierra la TUI y siguen vivos;
al reabrirla se re-adjunta al estado y verifica procesos con protección anti PID-reuse.

## Desarrollo

```bash
go test ./...        # unit + integración
go vet ./...
```

Spec funcional completa: `docs/planning/0001-feature-vroom-tui/`.
