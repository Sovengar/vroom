# Especificación: vroom TUI

## Propósito

TUI en Go + Bubbletea v2 que escanea el CWD (2 niveles), detecta proyectos por marcadores, gestiona servicios daemonizados que sobreviven al cierre, y muestra estado/acciones/logs. Linux-first, Windows-ready.

---

## Capability: manifest-parsing

### R1: Schema del manifiesto `.vroom.toml`

El sistema SHALL parsear ficheros `.vroom.toml` con campos en inglés: `name` (string, requerido), `group` (string, default `""`), `command` (string, requerido), `port` (integer, default `0`), `process_pattern` (string, default `""`). Campos no reconocidos se ignoran.

#### S1.1: Parsing exitoso

- GIVEN un fichero `.vroom.toml` válido con name y command
- WHEN el sistema lo parsea
- THEN devuelve un manifest con todos los campos tipados y defaults aplicados

#### S1.2: Campo requerido faltante

- GIVEN un fichero `.vroom.toml` sin campo `name` o sin campo `command`
- WHEN el sistema lo parsea
- THEN retorna error de validación con campo faltante identificado, sin crashear

#### S1.3: Puerto fuera de rango

- GIVEN un `.vroom.toml` con `port = 99999`
- WHEN el sistema valida
- THEN retorna error indicando que port debe ser 0 o 1-65535

#### S1.4: Campos vacíos opcionales

- GIVEN un `.vroom.toml` con `group = ""` y `process_pattern = ""`
- WHEN el sistema lo parsea
- THEN aplica defaults vacíos sin error

---

## Capability: project-scanning

### R2: Escaneo recursivo desde CWD

El sistema SHALL escanear desde el CWD con 2 niveles de recursividad usando `filepath.WalkDir`. Detecta proyectos por marcadores de lenguaje.

#### S2.1: Detección por marcador

- GIVEN un directorio con `go.mod` en CWD/subdir
- WHEN el walker lo encuentra
- THEN lo registra como proyecto con lenguaje "Go"

#### S2.2: Profundidad máxima

- GIVEN un proyecto a 3 niveles de profundidad desde CWD
- WHEN el walker lo encuentra
- THEN lo ignora (no lo registra)

#### S2.3: Sin marcadores conocidos

- GIVEN un directorio sin ningún marcador conocido
- WHEN el walker lo encuentra
- THEN lo registra como proyecto con lenguaje "otro" (visible pero sin opción de start)

### R3: Tabla de marcadores de lenguaje

El sistema SHALL reconocer los siguientes marcadores:

| Marcador | Lenguaje mostrado |
|----------|-------------------|
| `pom.xml` | Java |
| `build.gradle` / `build.gradle.kts` | Java |
| `go.mod` | Go |
| `package.json` | JavaScript |
| `pyproject.toml` | Python |
| `requirements.txt` | Python |
| `Cargo.toml` | Rust |
| (ninguno) | otro |

#### S3.1: Marcador duplicado

- GIVEN un directorio con `go.mod` y `package.json`
- WHEN el walker lo detecta
- THEN muestra el primer marcador encontrado en orden de la tabla como lenguaje primario

---

## Capability: state-persistence

### R4: Directorio de estado

El sistema SHALL persistir estado en `~/.local/state/vroom/services/{hash}/` donde hash = primeros 8 hex chars de SHA-256 del path absoluto del proyecto.

#### S4.1: Colisión de nombres

- GIVEN dos proyectos llamados `api/` en paths distintos
- WHEN se generan sus hashes
- THEN producen directorios de estado distintos (colisión astronómica aceptada con 2^32 posibles claves)

#### S4.2: Creación de directorio

- GIVEN un proyecto nuevo al primer start
- WHEN se crea su directorio de estado
- THEN contiene: `meta.json`, `pid`, `pgid`, `stdout.log`, `stderr.log`

### R5: Schema de `meta.json`

El sistema SHALL escribir `meta.json` con el siguiente schema:

```json
{
  "name": "string",
  "project_path": "string (absoluto)",
  "group": "string",
  "port": 0,
  "process_pattern": "string",
  "command": "string",
  "pid": 0,
  "pgid": 0,
  "creation_time_ms": 0,
  "started_at": "string (RFC3339)",
  "state": "running|stopped|unknown"
}
```

#### S5.1: Lectura de meta.json corrupto

- GIVEN un `meta.json` con JSON inválido
- WHEN el sistema lo lee
- THEN marca el servicio como `stopped` sin crashear y logs un warning

### R6: Eliminación de `config/state.json`

El sistema SHALL NOT usar `config/state.json` para estado global de la TUI. El estado por servicio ya cubre la información necesaria. Preferencias de UI (ordenación, servicio seleccionado) se persistirán en `~/.config/vroom/config.toml` (futuro, fuera de alcance v1).

---

## Capability: process-management

### R7: Daemonización con setsid

El sistema SHALL ejecutar servicios via `sh -c "{command}"` en un proceso hijo con `setsid()` para crear nuevo session leader. Stdout y stderr se redirigen a ficheros de log en el directorio de estado.

#### S7.1: Supervivencia al cierre de TUI

- GIVEN un servicio daemonizado
- WHEN el usuario cierra la TUI
- THEN el proceso sigue ejecutándose verificable con `ps`

#### S7.2: Re-adjunta al reabrir TUI

- GIVEN servicios daemonizados y la TUI cerrada
- WHEN el usuario reabre la TUI
- THEN lee el directorio de estado, verifica PID vivo con gopsutil (liveness + creation_time), y muestra estado correcto

### R8: Graceful shutdown con SIGTERM al PGID

El sistema SHALL enviar SIGTERM al PGID (no solo al PID) para graceful shutdown de todo el process group, esperar timeout (default 5s), y si el proceso sigue vivo enviar SIGKILL al PGID.

#### S8.1: Stop exitoso graceful

- GIVEN un servicio en estado `running`
- WHEN el usuario ejecuta stop
- THEN envía SIGTERM al PGID, espera, el proceso termina, y el estado cambia a `stopped`

#### S8.2: Stop forzado tras timeout

- GIVEN un servicio que no responde a SIGTERM en 5s
- WHEN el timeout expira
- THEN envía SIGKILL al PGID, mata todo el grupo, y el estado cambia a `stopped`

#### S8.3: Stop de servicio ya detenido

- GIVEN un servicio en estado `stopped`
- WHEN el usuario ejecuta stop
- THEN no envía señales y muestra mensaje "ya detenido"

### R9: Verificación de PID anti-reuse

El sistema SHALL usar gopsutil para verificar liveness + `creation_time` del PID contra el registrado en `meta.json`. Si la creation_time no coincide → `stopped` (PID reutilizado por otro proceso).

#### S9.1: PID reutilizado

- GIVEN un servicio registrado con PID 1234 y creation_time T1
- WHEN el PID 1234 ahora pertenece a otro proceso con creation_time T2 ≠ T1
- THEN el estado se marca como `stopped`

#### S9.2: Verificación de puerto

- GIVEN un servicio con `port = 8080`
- WHEN se verifica estado
- THEN usa `net.DialTimeout` para confirmar que el puerto responde

#### S9.3: Estado unknown

- GIVEN un servicio con PID vivo pero puerto y pattern no verificables
- WHEN se verifica estado
- THEN se muestra como `unknown` con indicador visual de advertencia

---

## Capability: grouping

### R10: Agrupación por campo `group`

El sistema SHALL agrupar proyectos que compartan el mismo valor de `group` en el manifiesto. Proyectos sin grupo (group = `""`) se muestran sin agrupación.

#### S10.1: Agrupación visual

- GIVEN dos proyectos con `group = "vsocial"` y uno sin grupo
- WHEN la TUI muestra la lista
- THEN los dos del grupo aparecen juntos bajo un separador "vsocial", el otro aparece suelto

#### S10.2: Grupo único

- GIVEN un proyecto con `group = "backend"` como único miembro
- WHEN se muestra la lista
- THEN aparece bajo el header "backend" (sin otros miembros)

---

## Capability: tui-core

### R11: Layout principal

La TUI SHALL mostrar una lista de proyectos con: nombre del proyecto, badge de lenguaje, grupo (si aplica), y estado visual (running=verde, stopped=gris, unknown=amarillo, sin configurar=gris atenuado).

#### S11.1: Proyectos con y sin manifiesto

- GIVEN proyectos con `.vroom.toml` y proyectos sin él
- WHEN la TUI muestra la lista
- THEN los sin manifiesto aparecen como "sin configurar" con estilo deshabilitado

#### S11.2: Acción start deshabilitada

- GIVEN un proyecto sin manifiesto seleccionado
- WHEN el usuario pulsa la tecla de start
- THEN no ejecuta acción y muestra mensaje "Proyecto sin manifiesto — crea un .vroom.toml para habilitar"

### R12: Navegación por teclado

La TUI SHALL soportar: flechas/j/k para navegar, Enter para seleccionar, q/Esc para salir, r para refresh forzado.

#### S12.1: Navegación cíclica

- GIVEN la lista de proyectos
- WHEN el usuario pulsa flecha abajo en el último elemento
- THEN vuelve al primer elemento

### R13: Refresco periódico de estado

La TUI SHALL verificar liveness de servicios visibles cada 2 segundos (polling). Además, la tecla `r` SHALL forzar un refresh inmediato.

#### S13.1: Servicio que crashea mientras la TUI está abierta

- GIVEN un servicio `running` verificado hace 3s
- WHEN el polling detecta que el PID ya no existe
- THEN el estado cambia a `stopped` en la siguiente ciclo de refresco

---

## Capability: service-actions

### R14: Estados intermedios en la UI

La TUI SHALL mostrar estados intermedios: `starting` (durante spawn), `stopping` (durante SIGTERM timeout). Estos estados son transitorios y se resuelven en el siguiente ciclo de polling.

#### S14.1: Start con feedback

- GIVEN un servicio en estado `stopped`
- WHEN el usuario pulsa start
- THEN el estado cambia a `starting`, se daemoniza, y al completar cambia a `running`

#### S14.2: Restart

- GIVEN un servicio en estado `running`
- WHEN el usuario pulsa restart
- THEN ejecuta stop (con timeout) y luego start, mostrando `stopping` → `starting` → `running`

### R15: Prevención de doble start

El sistema SHALL verificar si el servicio ya está `running` antes de intentar start.

#### S15.1: Start de servicio ya corriendo

- GIVEN un servicio en estado `running`
- WHEN el usuario pulsa start
- THEN muestra mensaje "ya está ejecutándose" sin crear segundo proceso

---

## Capability: log-viewing

### R16: Vista de logs conmutables

La TUI SHALL mostrar una vista de logs con un viewport que hace tail del fichero de log. El usuario SHALL poder alternar entre stdout y stderr con una tecla (default: Tab). El log mostrado SHALL ser conmutable, no fusionado.

#### S16.1: Alternar a stderr

- GIVEN la vista de logs mostrando stdout
- WHEN el usuario pulsa Tab
- THEN cambia a mostrar stderr.log

#### S16.2: Log vacío

- GIVEN un servicio recién iniciado sin output
- WHEN se abre la vista de logs
- THEN muestra "Sin logs disponibles" sin crashear

### R17: Abrir logs externamente

La TUI SHALL ofrecer una acción (tecla `o`) que suspende la TUI y abre ambos ficheros de log del servicio (`stdout.log` y `stderr.log`) en el editor del usuario (`$VISUAL`, `$EDITOR`, default `nvim`) en split vertical (`-O`), con el foco en el stream seleccionado en la vista de logs. Al cerrar el editor, la TUI SHALL restaurarse intacta. Para editores no tipo vim el flag `-O` se omite.

#### S17.1: Editar logs en el editor

- GIVEN un servicio con logs en `~/.local/state/vroom/services/{hash}/`
- WHEN el usuario pulsa `o`
- THEN la TUI se suspende, se abre el editor con ambos ficheros en split vertical (foco según stream activo), y al salir la TUI se restaura intacta

---

## Capability: test-playground

### R18: Playground de proyectos ficticios

El repositorio SHALL incluir un playground versionado en `playground/` (raíz del repo) con proyectos ficticios, cada uno con su `.vroom.toml`. Sirve como fixture para el smoke test manual, para tests de integración (scanner/manifest sin spawn) y como demo.

**Estructura:**

```
playground/
├── apps/
│   ├── products-api-java/        # Java: pom.xml (marker), stdlib com.sun.net.httpserver
│   │                             # command = "java src/main/java/com/example/Main.java" (single-file, Java 11+)
│   │                             # port = 8081, group = "tienda"
│   ├── orders-api-springboot/    # Java/Spring Boot: pom.xml (spring-boot-starter-web)
│   │                             # command = "mvn spring-boot:run" (requiere JDK 17+ y Maven; primera
│   │                             # ejecución descarga dependencias)
│   │                             # port = 8084, group = "tienda"
│   ├── billing-api-go/           # Go: go.mod, net/http hello
│   │                             # command = "go run main.go", port = 8082, group = ""
│   ├── inventory-api-python/     # Python: requirements.txt (marker), app.py con http.server stdlib
│   │                             # command = "python3 app.py", port = 8083, group = ""
│   └── web-frontend/             # Node: package.json, server.js con http module (sin npm install)
│                                 # command = "node server.js", port = 5173, group = "tienda"
├── servers/
│   ├── search-api-python/        # Python: requirements.txt, command = "python3 -m http.server 8090", port = 8090
│   └── auth-api-go/              # Go: go.mod, command = "go run main.go", port = 8091, group = ""
└── infra/
    └── nginx-proxy/              # Docker: sin marcador de lenguaje (lenguaje "otro"), command = "docker run --rm -p 8080:80 nginx:alpine"
                                  # port = 8080, group = ""
```

**Qué ejercita el playground:**

| Aspecto validado | Fixture |
|------------------|---------|
| Marcadores Java/Go/Python/JS | products-api-java, billing-api-go, inventory-api-python, web-frontend |
| Spring Boot real (logs de arranque, Maven) | orders-api-springboot |
| Proyecto sin marcador conocido ("otro") pero configurable | nginx-proxy |
| Agrupación backend+frontend (group = "tienda") | products-api-java + orders-api-springboot + web-frontend |
| Comandos heterogéneos: binario directo, `go run`, script python, node, **maven**, **docker** | todos |
| Detección por puerto | todos (8080-8084, 5173, 8090-8091) |
| Servicio dentro de Docker | nginx-proxy |

**Restricciones de los fixtures:**
- Cero dependencias externas de build, **excepto** `orders-api-springboot` (Spring Boot requiere Maven y descarga de dependencias en la primera ejecución — excepción explícita aprobada)
- Cada proyecto tiene `.vroom.toml` con `name`, `command`, `port` y (cuando aplica) `group`
- Los puertos (8080-8084, 5173, 8090-8091) no deben colisionar entre sí

#### S18.1: Listado correcto del playground

- GIVEN la TUI abierta con CWD = `testdata/playground/`
- WHEN se completa el escaneo
- THEN se listan los 8 proyectos con su lenguaje correcto (Java, Go, Python, JavaScript, otro), y api-java + web-frontend aparecen agrupados bajo "tienda"

#### S18.2: Ciclo completo multi-servicio

- GIVEN el playground y la TUI abierta
- WHEN el usuario inicia todos los servicios configurados, cierra la TUI, verifica con `ps`/`curl` que siguen vivos, reabre la TUI y los detiene todos
- THEN cada fase muestra los estados correctos (starting → running → alive tras cierre → running al reabrir → stopping → stopped)

#### S18.3: Servicio Docker

- GIVEN nginx-proxy con `command = "docker run --rm -p 8080:80 nginx:alpine"` y Docker instalado
- WHEN el usuario inicia y luego detiene el servicio desde la TUI
- THEN el puerto 8080 responde durante `running` (verificable con `curl`), y al detener el contenedor se elimina (`--rm`) tras el stop

---

## Escenarios transversales

### S-T1: Manifiesto malformado

- GIVEN un `.vroom.toml` con sintaxis TOML inválida
- WHEN el scanner lo encuentra
- THEN registra el proyecto como "sin configurar" con warning en log de la TUI, sin crashear

### S-T2: Puerto en uso por otro servicio

- GIVEN un servicio con `port = 8080` que otro proceso ya usa
- WHEN el usuario inicia el servicio
- THEN el servicio arranca (el puerto puede ser legítimo del propio servicio) pero se muestra advertencia si la verificación de puerto falla post-start

### S-T3: Servicio que crashea inmediatamente

- GIVEN un servicio cuyo comando termina en <1s
- WHEN se inicia y se verifica estado
- THEN se muestra como `stopped` (PID no existe) en el siguiente ciclo de polling

### S-T4: Reapertura tras crash del sistema

- GIVEN servicios registrados en disco pero el sistema se reinició
- WHEN se abre la TUI
- THEN lee meta.json, verifica PID (inexistente), muestra todos como `stopped`

### S-T5: Permisos denegados en directorio de estado

- GIVEN `~/.local/state/vroom/` sin permisos de escritura
- WHEN la TUI intenta crear directorio de servicio
- THEN muestra error claro y sugiere verificar permisos, sin crashear

### S-T6: Refresh manual

- GIVEN la TUI abierta con servicios en estado desconocido
- WHEN el usuario pulsa `r`
- THEN re-verifica liveness de todos los servicios visibles inmediatamente

---

## Criterios de aceptación (ligados al smoke test)

| # | Criterio | Verificable con |
|---|----------|-----------------|
| AC1 | Proyectos detectados desde CWD con 2 niveles | Smoke test pasos 1-3 |
| AC2 | Cada proyecto muestra nombre, lenguaje, grupo, estado | Smoke test paso 3 |
| AC3 | Servicios daemonizados sobreviven al cierre | Smoke test pasos 4-5 |
| AC4 | Re-adjunta al reabrir con protección PID reuse | Smoke test paso 6 |
| AC5 | Log en tiempo real dentro de la TUI | Smoke test paso 4 |
| AC6 | Stop SIGTERM→SIGKILL tras timeout | Smoke test paso 7 |
| AC7 | Hash de ruta evita colisiones | Unit test de hash |
| AC8 | Tests unitarios cubren parsing, hash, detección | `go test ./...` |
| AC9 | Playground completo: 8 proyectos detectados con lenguajes/grupos correctos, ciclo completo start→cerrar→reabrir→stop, y nginx en Docker arrancado/detenido | Smoke test sobre `testdata/playground/` (S18.1-S18.3) |

---

## Revisiones

### R1 (post-implementación v1.1)

- **R14/R15/S8.3/S15.1 — tecla `s` contextual**: la UI usa un único keybind `s` como toggle (start si stopped, stop si running/unknown) en lugar de teclas separadas de start/stop. El mensaje de S15.1 ("ya está ejecutándose") queda inalcanzable vía UI: el guard anti doble-start es estructural (el toggle nunca arranca un servicio running). S8.3 ("ya detenido") igualmente no es alcanzable vía UI; el no-op seguro permanece a nivel de `stopCmd`/Manager. `R` (restart) y `r` (refresh) no cambian.
- **R17 — `o` abre el editor**: sustituye xdg-open del directorio por apertura de ambos ficheros de log en `$VISUAL`/`$EDITOR` (default nvim) con split vertical; ver R17 reescrita arriba.
- **S18.1/AC9 — "8 proyectos"**: la estructura original de R18 definía 7 fixtures pero S18.1/AC9 hablaban de 8. **Resuelto**: se añadió `orders-api-springboot` (grupo `tienda`, port 8084) a petición del usuario → 8 fixtures reales. El playground se movió a `playground/` (raíz del repo) y los fixtures renombrados a nombres descriptivos (`<dominio>-api-<lenguaje>`). Excepción documentada a la regla cero-deps: el fixture Spring Boot requiere Maven.
