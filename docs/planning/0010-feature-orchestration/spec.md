# Spec: Orquestación de arranque con `.vroom-compose.toml`

Cambio: `0010-feature-orchestration`
Base: 0001 (reutiliza scanner, process, state), 0006 (extiende group)
Numeración: continúa desde 0009 (última R56)

---

## Capability: compose-parsing

### R50: Schema del compose file `.vroom-compose.toml`

El sistema SHALL parsear ficheros `.vroom-compose.toml` con la siguiente
estructura TOML:

- `[[stack]]` — array de tables, cada entrada es un stack
  - `name` (string, requerido) — nombre del stack para CLI y display
  - `primary_group` (string, requerido) — primary group donde aparece en la TUI
  - `[[stack.stage]]` — array de tables dentro del stack
    - `name` (string, requerido) — nombre de la etapa
    - `services` ([]string, requerido) — nombres de servicios
    - `timeout` (string, opcional, default `"30s"`) — timeout de health check

El fichero se busca en CWD. Si no existe, el sistema retorna error.

#### S50.1: Parsing exitoso

- GIVEN un `.vroom-compose.toml` válido con un stack y dos stages
- WHEN el sistema lo parsea
- THEN devuelve un `ComposeFile` con el stack y sus stages correctos

#### S50.2: Múltiples stacks

- GIVEN un `.vroom-compose.toml` con dos `[[stack]]`
- WHEN se parsea
- THEN ambos stacks están disponibles con sus stages independientes

#### S50.3: Campo requerido faltante (name)

- GIVEN un `.vroom-compose.toml` con `[[stack]]` sin campo `name`
- WHEN se parsea
- THEN retorna error de validación: `"missing required field: name"`

#### S50.4: Campo requerido faltante (primary_group)

- GIVEN un `.vroom-compose.toml` con `[[stack]]` sin campo `primary_group`
- WHEN se parsea
- THEN retorna error de validación: `"missing required field: primary_group"`

#### S50.5: Stage sin servicios

- GIVEN un stage con `services = []`
- WHEN se valida
- THEN retorna error: un stage debe tener al menos un servicio

#### S50.6: Timeout default

- GIVEN un stage sin campo `timeout`
- WHEN se parsea
- THEN timeout es `"30s"`

#### S50.7: Compose file no encontrado

- GIVEN CWD sin `.vroom-compose.toml`
- WHEN se intenta parsear
- THEN retorna error: `"no .vroom-compose.toml found in current directory"`

---

## Capability: service-resolution

### R51: Resolución de servicios

El sistema SHALL resolver cada nombre de servicio en `services` contra
los proyectos escaneados por el scanner (`scanner.Scan()`). La resolución
usa `Manifest.Name` del `.vroom.toml` de cada proyecto.

Si un nombre no coincide con ningún proyecto configurado, el sistema
SHALL retornar error ANTES de iniciar cualquier arranque.

#### S51.1: Servicio encontrado

- GIVEN un servicio `"db"` en el compose y un proyecto con `Manifest.Name = "db"` en `.vroom.toml`
- WHEN se resuelven los servicios
- THEN el proyecto se asocia al servicio

#### S51.2: Servicio no encontrado

- GIVEN un servicio `"xyz"` en el compose que no matchea ningún `Manifest.Name`
- WHEN se resuelven los servicios
- THEN retorna error: `"service 'xyz' not found in scanned projects"`

#### S51.3: Servicio duplicado en stages

- GIVEN un servicio `"api"` en dos stages diferentes del mismo stack
- WHEN se resuelven los servicios
- THEN es válido: el servicio arranca en su primera etapa; en la segunda
  se verifica que sigue corriendo (no se arranca de nuevo)

---

## Capability: orchestration-engine

### R52: Ejecución secuencial de etapas

El sistema SHALL ejecutar las etapas en orden secuencial (primera → última).
Dentro de cada etapa, SHALL arrancar todos los servicios en paralelo usando
goroutines. No se procede a la siguiente etapa hasta que TODOS los servicios
de la etapa actual estén sanos (health check pasado).

#### S52.1: Etapas en orden

- GIVEN un stack con stage `"infra"`, `"api"`, `"spa"`
- WHEN se lanza
- THEN `"infra"` se ejecuta primero, `"api"` después, `"spa"` al final

#### S52.2: Servicios paralelos dentro de etapa

- GIVEN un stage con `["db", "cache"]`
- WHEN se ejecuta el stage
- THEN ambos servicios arrancan simultáneamente (goroutines)

#### S52.3: Espera a completar etapa

- GIVEN un stage con `["api-1", "api-2"]`
- WHEN ambos arrancan
- THEN se espera a que ambos pasen health check antes de continuar

---

### R53: Health check por puerto

El sistema SHALL verificar que cada servicio esté sano esperando a que su
puerto esté abierto (usando `process.PortOpen()` de `internal/process/detect.go:30`).
El timeout por defecto es 30s, configurable por stage con `timeout`.

Si un servicio no tiene `port` definido en su `.vroom.toml` (port = 0), el
sistema SHALL esperar el timeout sin verificar puerto (asume que arranca rápido).

#### S53.1: Puerto abre a tiempo

- GIVEN un servicio con `port = 8080`
- WHEN arranca y el puerto abre en 5s
- THEN health check pasa, se continua

#### S53.2: Puerto no abre (timeout)

- GIVEN un servicio con `port = 8080` y `timeout = "10s"`
- WHEN el puerto no abre en 10s
- THEN health check falla, se aborta la orquestación

#### S53.3: Servicio sin puerto

- GIVEN un servicio con `port = 0`
- WHEN se verifica health
- THEN se espera el timeout (default 30s) y se asume sano

---

### R54: Abort on failure

Si CUALQUIER servicio falla (start error o health check timeout), el
sistema SHALL:

1. Detener TODOS los servicios arrancados en esta sesión de orquestación
2. Retornar error con el nombre del servicio que falló
3. NO ejecutar etapas siguientes

Los servicios que ya estaban corriendo ANTES de la orquestación NO se
detienen (solo los arrancados por esta sesión).

#### S54.1: Fallo en stage 1

- GIVEN stage 1 con `["db", "cache"]` y `db` falla al arrancar
- WHEN se detecta el fallo
- THEN `cache` se detiene (si arrancó), se retorna error, stage 2 no se ejecuta

#### S54.2: Fallo en stage 2

- GIVEN stage 1 OK y stage 2 con `["api"]` que falla health check
- WHEN se detecta el fallo
- THEN `api` se detiene, los servicios de stage 1 NO se detienen (ya existían),
  se retorna error

#### S54.3: Servicio ya corriendo

- GIVEN un servicio que ya está running cuando se inicia la orquestación
- WHEN se procesa su stage
- THEN se salta el start, se verifica health check, se continua

---

## Capability: stack-tui-integration

### R55: Stacks como entradas en la TUI

Los stacks SHALL aparecer en la TUI como entradas dentro del `secondary_group`
fijo `"Composers"` dentro de su `primary_group`. El header `Composers` SHALL
aparecer al FINAL del bloque primario, después de todos los secondary_groups
regulares.

#### S55.1: Posición del header Composers

- GIVEN un `primary_group` con secondary_groups `"backend"` y `"frontend"`,
  y un stack con `primary_group` igual
- WHEN se renderiza la TUI
- THEN el header `Composers` aparece después de `"frontend"`

#### S55.2: Indicador visual

- GIVEN un stack en la lista
- WHEN se renderiza
- THEN la fila muestra `"🎵 <nombre> [stack] (<running>/<total>)"`

#### S55.3: Estilo del stack

- GIVEN un stack en la lista
- WHEN se renderiza
- THEN usa `styleStack` (foreground color 5, magenta)

#### S55.4: Colapso del header Composers

- GIVEN el header `Composers` visible con stacks dentro
- WHEN se presiona `enter` sobre el header
-THEN se colapsa ocultando todos los stacks del primario

---

### R56: Acciones sobre stacks en la TUI

La tecla `s` (start_stop) sobre un stack SHALL ejecutar la acción
correspondiente según el estado:

- Si el stack está **stopped** (o no tiene servicios running): lanzar la orquestación
- Si el stack está **running** (todos los servicios running): parar todos los servicios
- Si el stack está en **transición** (starting/stopping): ignorar la acción

#### S56.1: Lanzar stack desde TUI

- GIVEN un stack stopped seleccionado
- WHEN se presiona `s`
- THEN se ejecuta la orquestación completa (mismo comportamiento que `vroom launch`)

#### S56.2: Parar stack desde TUI

- GIVEN un stack running seleccionado
- WHEN se presiona `s`
- THEN se paran todos los servicios del stack

#### S56.3: Enter sobre stack

- GIVEN un stack seleccionado
- WHEN se presiona `enter`
- THEN no hace nada (stacks no se pliegan)

#### S56.4: Acciones no disponibles para stacks

- GIVEN un stack seleccionado
- WHEN se presiona `b` (build), `i` (install) o `l` (logs)
- THEN se muestra mensaje: `"not available for stacks"`

---

### R57: Detalles del stack en la TUI

Cuando se selecciona un stack, el panel de detalles SHALL mostrar:
- Nombre + sufijo `[stack]` + estado
- Tipo: `"orchestration stack"`
- Número de etapas
- Número de servicios (running/total)
- Lista de etapas con sus servicios

#### S57.1: Panel de detalles con stack seleccionado

- GIVEN un stack `"vsocial-full"` con 3 etapas y 7 servicios (3 running)
- WHEN se selecciona el stack
- THEN el panel muestra:
  ```
  🎵 vsocial-full [stack]  ● running
  type:        orchestration stack
  stages:      3
  services:    7 (3 running)
  group:       vsocial
  ```

---

## Capability: stack-launch (CLI)

### R58: Comando CLI `vroom launch`

El sistema SHALL soportar los siguientes modos:

| Comando | Descripción |
|---------|-------------|
| `vroom launch --list` | Lista todos los stacks del compose file |
| `vroom launch <name>` | Lanza un stack específico |
| `vroom launch <name> --dry` | Muestra el plan sin ejecutar |

Todos los comandos retornan JSON en stdout. Errores en stderr con formato
`{"error":"..."}` (patrón existente de 0001).

#### S58.1: Listar stacks

- GIVEN un `.vroom-compose.toml` con 2 stacks
- WHEN se ejecuta `vroom launch --list`
- THEN retorna JSON con ambos stacks y sus stages

#### S58.2: Lanzar stack

- GIVEN un stack `"vsocial-full"` válido
- WHEN se ejecuta `vroom launch vsocial-full`
- THEN ejecuta la orquestación y retorna JSON con resultado

#### S58.3: Dry run

- GIVEN un stack `"vsocial-full"`
- WHEN se ejecuta `vroom launch vsocial-full --dry`
- THEN retorna JSON con el plan (stages y servicios) sin ejecutar nada

#### S58.4: Compose file no encontrado

- GIVEN CWD sin `.vroom-compose.toml`
- WHEN se ejecuta `vroom launch --list`
- THEN retorna error: `"no .vroom-compose.toml found in current directory"`

#### S58.5: Stack no encontrado

- GIVEN un compose con stacks `"a"` y `"b"`
- WHEN se ejecuta `vroom launch c`
- THEN retorna error: `"stack 'c' not found"`

---

## Capability: compose-scanning

### R59: Descubrimiento de compose files

El scanner SHALL descubrir ficheros `.vroom-compose.toml` durante el escaneo
y retornarlos en `ScanResult` junto con los proyectos.

#### S59.1: Compose file en el root del grupo

- GIVEN un directorio con `.vroom-compose.toml` y subdirectorios con `.vroom.toml`
- WHEN se ejecuta `scanner.Scan()`
- THEN el ScanResult contiene tanto los projects como el ComposeFile

#### S59.2: Múltiples compose files

- GIVEN dos directorios con `.vroom-compose.toml` a diferentes profundidades
- WHEN se ejecuta `scanner.Scan()`
- THEN ambos compose files están en el ScanResult

#### S59.3: Compose file malformado

- GIVEN un `.vroom-compose.toml` con sintaxis TOML inválida
- WHEN se parsea
- THEN se omite con warning; los demás compose files y projects se procesan normalmente
