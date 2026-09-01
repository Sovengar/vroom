# Proposal: TUI para gestión de proyectos y servicios

## Intent

Desarrollar una TUI en Go con Bubbletea que permita al desarrollador gestionar servicios de múltiples proyectos desde un solo punto. El problema: al trabajar con muchos proyectos (Java, Go, JS, Python, etc.), no hay una forma unificada de ver qué servicios están corriendos, iniciarlos/detenerlos, y ver logs sin perder contexto. La TUI escanea el directorio actual (CWD) con 2 niveles de recursividad, detecta proyectos, y ofrece control centralizado de servicios daemonizados que sobreviven al cierre de la terminal.

## Scope

### In Scope
- Escaneo de proyectos desde CWD con 2 niveles de recursividad (no rutas fijas)
- Detección automática de lenguaje por marcadores (pom.xml, go.mod, package.json, etc.)
- Daemonización de servicios con setsid/PGID que sobreviven al cierre de TUI
- Estado persistente en `~/.local/state/svc/` con clave hash de ruta
- Acciones: iniciar, detener (SIGTERM→SIGKILL con timeout), reiniciar, ver logs
- Modo descubrimiento: proyectos sin manifiesto visibles como "sin configurar"
- Vista de logs en tiempo real dentro de la TUI

### Out of Scope
- Edición de manifiestos desde la TUI
- Gestión de dependencias entre servicios
- Dashboard de métricas o monitoreo avanzado
- Soporte para múltiples workspaces simultáneos
- Autenticación o permisos especiales para servicios
- Soporte oficial para Windows en v1 — **pero el diseño no lo bloquea** (ver "Consideraciones cross-platform")

## Capabilities

### New Capabilities
- `manifest-parsing`: Parsing de manifiestos `.svc.toml` con schema en inglés, validación de campos requeridos/opcionales, y defaults
- `project-scanning`: Escaneo recursivo desde CWD con detección de lenguaje por marcadores de proyecto
- `process-management`: Daemonización con setsid, gestión de PID/PGID, graceful shutdown (SIGTERM→SIGKILL), verificación de procesos vivos
- `state-persistence`: Estado persistente en disco con clave hash de ruta, estructura de directorios para servicios, logs, y metadata
- `tui-core`: TUI base con Bubbletea v2, Lipgloss, Bubbles — layout principal, navegación, estados visuales
- `service-actions`: Iniciar, detener, reiniciar servicios con estados intermedios en la UI
- `log-viewing`: Vista de logs con tail en tiempo real y opción de abrir externamente

### Modified Capabilities
None — proyecto nuevo.

## Approach

### Arquitectura de módulos

```
svc/
├── cmd/svc/main.go              # Entry point, inicialización
├── internal/
│   ├── manifest/                # Parsing y validación de .svc.toml
│   │   ├── manifest.go          # Structs, parse, validate
│   │   └── manifest_test.go
│   ├── scanner/                 # Escaneo de directorios
│   │   ├── scanner.go           # Walker recursivo, detección de lenguaje
│   │   └── scanner_test.go
│   ├── process/                 # Daemonización y gestión de procesos
│   │   ├── daemon.go            # setsid, spawn, process groups
│   │   ├── signals.go           # SIGTERM, SIGKILL, timeout
│   │   └── detect.go            # PID alive, puerto, pgrep
│   ├── state/                   # Estado persistente
│   │   ├── state.go             # Lectura/escritura de estado
│   │   ├── hash.go              # Hash de ruta para claves
│   │   └── state_test.go
│   ├── group/                   # Lógica de agrupación
│   │   └── group.go             # Agrupación por campo group
│   └── tui/                     # Capa de presentación
│       ├── app.go               # Modelo principal, update, view
│       ├── projectlist.go       # Lista de proyectos
│       ├── serviceview.go       # Detalle de servicio
│       ├── logview.go           # Vista de logs
│       └── styles.go            # Estilos Lipgloss
└── go.mod
```

**Flujo de dependencias:**
```
tui → scanner, manifest, process, state, group
scanner → manifest (para parsear .svc.toml encontrado)
process → state (para registrar/verificar PID)
state → hash (para generar claves de servicio)
```

### Diagrama de arquitectura

```mermaid
graph TD
    A[main.go] --> B[tui/app.go]
    B --> C[scanner/scanner.go]
    B --> D[process/daemon.go]
    B --> E[state/state.go]
    B --> F[group/group.go]
    C --> G[manifest/manifest.go]
    D --> E
    D --> H[process/detect.go]
    E --> I[state/hash.go]
    
    subgraph "Estado en disco"
        J[~/.local/state/svc/services/{hash}/]
        K[pid, pgid, meta.json, logs]
    end
    
    E --> J
    J --> K
```

## Consideraciones cross-platform (Linux-first, Windows-ready)

**Objetivo**: v1 solo Linux, pero sin code paths que hard-bloqueen Windows. Añadir Windows después debe ser implementar una clase nueva, no reescribir.

**Estrategia: interfaz + build tags.**

1. `internal/process` define una interfaz `ProcessManager` (spawn daemonizado, stop, liveness). La implementación concreta vive en ficheros con build tags:
   - `daemon_unix.go` (`//go:build unix`): `SysProcAttr{Setsid: true}`, `kill(-pgid, SIGTERM/SIGKILL)`, lectura de starttime en `/proc/{pid}/stat`
   - `daemon_windows.go` (`//go:build windows`): placeholder documentado para v2 con `CREATE_NEW_PROCESS_GROUP + DETACHED_PROCESS`, kill de árbol con `taskkill /T /F` o Job Objects, graceful stop best-effort (CTRL_BREAK_EVENT; no hay SIGTERM en Windows)
2. **`gopsutil` (pure Go, sin cgo)** para liveness y creation-time del proceso: funciona igual en Linux y Windows → la verificación anti PID-reuse es portable sin duplicar lógica. Este motivo justifica la dependencia (en un escenario Linux-only se usaría `/proc` con stdlib).
3. **Directorio de estado multiplataforma**: resolver en runtime — Linux: `$XDG_STATE_HOME/svc` (default `~/.local/state/svc`); Windows (futuro): `%LOCALAPPDATA%\svc\state`.
4. **Lo ya portable de serie**: Bubbletea/Lipgloss/Bubbles (soportan Windows Terminal), scanner (`filepath.WalkDir`), TOML, check de puerto (`net.DialTimeout`).

**Semántica de stop por plataforma** (documentada, solo implementada en unix para v1):

| Paso | Linux (v1) | Windows (futuro) |
|------|------------|------------------|
| Graceful | SIGTERM al PGID, timeout 5s | CTRL_BREAK_EVENT al grupo, timeout |
| Forzado | SIGKILL al PGID | `taskkill /T /F` o TerminateJobObject |

## Schema del manifiesto `.svc.toml`

| Campo | Tipo | Requerido | Default | Descripción |
|-------|------|-----------|---------|-------------|
| `name` | string | Sí | — | Nombre legible del servicio |
| `group` | string | No | `""` | Grupo al que pertenece (vacío = proyecto único) |
| `command` | string | Sí | — | Comando a ejecutar (ej: `go run main.go`, `./start.sh`) |
| `port` | integer | No | `0` | Puerto para detección (0 = no verificar) |
| `process_pattern` | string | No | `""` | Patrón para pgrep (vacío = no verificar) |

**Ejemplo:**
```toml
name = "vsocial-api"
group = "vsocial"
command = "go run main.go"
port = 8080
process_pattern = "vsocial-api"
```

**Validaciones:**
- `name` y `command` son obligatorios; falta cualquiera de los dos = error de parsing
- `port` debe ser 0 (deshabilitado) o rango válido (1-65535)
- `group` vacío significa proyecto sin agrupación
- Campos no reconocidos se ignoran (extensible sin breaking changes)

## Modelo de procesos

### Daemonización
1. Parsear `command` del `.svc.toml`
2. Ejecutar via `sh -c "{command}"` para soporte de pipes/redirecciones
3. Llamar `setsid()` en el proceso hijo para crear nuevo session leader
4. Redirigir stdout/stderr a ficheros de log en el directorio de estado
5. Registrar PID y PGID en `meta.json`
6. Proceso queda vivo e independiente de la TUI

### Shutdown
1. Enviar SIGTERM al PID del proceso
2. Esperar timeout configurable (default: 5s)
3. Si el proceso sigue vivo, enviar SIGKILL al PGID (mata todo el grupo)
4. Actualizar estado en disco

### Supervivencia al cierre de TUI
- El proceso hijo hereda el session leader via `setsid()`
- No tiene referencia al proceso padre de la TUI
- Continúa ejecutándose independientemente

### Re-adjunta al reabrir TUI
1. Leer directorio de estado `~/.local/state/svc/services/`
2. Para cada servicio registrado:
   - Leer PID de `pid` file
   - Verificar si el proceso está vivo (`/proc/{pid}/status` o `kill -0`)
   - Verificar puerto si está configurado (`net.DialTimeout`)
   - Verificar patrón de proceso si está configurado (`pgrep`)
3. Actualizar estado visual según verificación

## Detección de estado

**Orden de confianza (de mayor a menor):**
1. **PID vivo + starttime**: Verificar que el PID existe Y que su starttime coincide con el registrado (evita PID reuse)
2. **Puerto activo**: Si `port > 0`, verificar con `net.DialTimeout` que el puerto responde
3. **pgrep pattern**: Si `process_pattern` no está vacío, verificar que `pgrep` encuentra el patrón

**Lógica de determinación:**
- Si PID no existe → `stopped`
- Si PID existe pero starttime no coincide → `stopped` (PID reutilizado)
- Si PID válido + (port OK o pattern OK) → `running`
- Si PID válido pero port y pattern fallan → `unknown` (mostrar advertencia)

## Layout del directorio de estado

```
~/.local/state/svc/
├── services/
│   ├── {hash}/                          # Hash corto del path absoluto (8 chars sha256)
│   │   ├── meta.json                    # {"name": "vsocial-api", "project_path": "/home/user/dev/vsocial", "group": "vsocial", ...}
│   │   ├── pid                          # PID del proceso principal
│   │   ├── pgid                         # PGID para kill por grupo
│   │   ├── stdout.log                   # stdout capturado
│   │   └── stderr.log                   # stderr capturado
│   └── ...
├── config/
│   └── state.json                       # Estado global de la TUI
└── logs/
    └── svc-tui.log                      # Log de la propia TUI
```

**Clave de servicio = hash de ruta:**
- Problema: dos proyectos pueden llamarse igual (ej: `api/` en `/home/user/dev/work/api/` y `/home/user/dev/personal/api/`)
- Solución: clave = primeros 8 chars de SHA-256 del path absoluto del proyecto
- Ejemplo: `/home/user/dev/vsocial` → hash `a3f8c2e1` → directorio `services/a3f8c2e1/`
- `meta.json` almacena el path completo y nombre legible para inspección manual
- Colisión astronómica: con 8 hex chars (4 bytes) hay 2^32 posibles claves → suficiente para uso personal

## Stack y dependencias Go

| Dependencia | Versión | Uso |
|-------------|---------|-----|
| `github.com/charmbracelet/bubbletea` | v2 | Framework TUI |
| `github.com/charmbracelet/lipgloss` | latest | Estilos y layout |
| `github.com/charmbracelet/bubbles` | latest | Componentes (viewport, list, etc.) |
| `github.com/BurntSushi/toml` | v1 | Parsing de `.svc.toml` |
| `github.com/shirou/gopsutil` | v3 | Liveness + creation-time del proceso, portable Linux/Windows (pure Go, sin cgo) |

**Manejo de puertos:**
```go
conn, err := net.DialTimeout("tcp", fmt.Sprintf(":%d", port), 500*time.Millisecond)
if err != nil {
    return false  // puerto no disponible
}
conn.Close()
return true
```

**Verificación de PID (con gopsutil — NUNCA `os.FindProcess` a secas, que en Linux siempre tiene éxito aunque el proceso no exista):**
```go
// gopsutil: liveness + creation-time, portable Linux/Windows
p, err := proc.NewProcess(int32(pid))
if err != nil || !isRunning(p) {
    return stopped  // PID libre o inexistente
}
ctime, _ := p.CreateTime() // ms epoch
if ctime != registeredCreationTime {
    return stopped  // PID reciclado: proceso distinto ocupa el PID
}
return alive
```

## Riesgos y mitigaciones

| Riesgo | Probabilidad | Mitigación |
|--------|--------------|------------|
| PID reuse: proceso muerto, PID reciclado por otro | Media | gopsutil: comparar creation-time del PID contra el registrado en `meta.json` (portable) |
| Zombies: proceso hijo termina pero padre no hace wait | Baja | Usar `setsid()` para desacoplar; el init del sistema reapa zombies |
| Servicios que hacen fork de hijos | Media | Matar por PGID (group kill), no solo PID |
| Permisos de escritura en `~/.local/state/svc/` | Baja | Crear directorios al inicio con permisos 0755; error claro si falla |
| Puerto en uso por otro servicio | Media | Verificar puerto antes de start; mostrar warning si conflict |
| Manifiesto malformado | Alta | Parsing con defaults razonables; mostrar error en TUI sin crashear |
| Path muy largo para hash (overflow de directorio) | Baja | SHA-256 siempre produce 64 hex chars; truncar a 8 es seguro |

## Alternativas consideradas y descartadas

| Alternativa | Por qué se descartó |
|-------------|---------------------|
| Symlink de lockfiles | No resuelve daemonización; solo previene doble-start |
| Dependencia de systemd units | Requiere systemd; no portable a macOS; complejidad innecesaria |
| cgo para procesos | Complejidad de compilación; CGO_ENABLED=1 rompe cross-compile |
| Clave de servicio = nombre directo | Colisiones con proyectos homónimos en diferentes paths |
| SQLite para estado | Overkill; JSON en disco es suficiente y más fácil de inspeccionar |
| Config centralizada por workspace | Contradice el diseño descentralizado (cada proyecto tiene su .svc.toml) |

## Plan de verificación

### Tests con teatest
- **Unit tests**: Parsing de manifiestos, generación de hash, lógica de detección de estado
- **Integration tests**: Flujo completo scan → daemonizar → verificar estado → detener
- **Edge cases**: servicio que crashea inmediatamente, puerto en uso, manifiesto incompleto, permisos denegados, reapertura tras crash

### Smoke test manual
1. Crear proyecto de prueba con `.svc.toml`
2. Ejecutar `svc` desde el directorio padre
3. Verificar que el proyecto aparece en la lista
4. Iniciar servicio → verificar que aparece como `running`
5. Cerrar TUI → verificar que el proceso sigue vivo (`ps aux | grep`)
6. Reabrir TUI → verificar que se re-adjunta correctamente
7. Detener servicio → verificar limpieza de PID y logs

## Affected Areas

| Area | Impacto | Descripción |
|------|---------|-------------|
| `cmd/svc/` | Nuevo | Entry point de la aplicación |
| `internal/manifest/` | Nuevo | Parsing y validación de `.svc.toml` |
| `internal/scanner/` | Nuevo | Escaneo recursivo de proyectos |
| `internal/process/` | Nuevo | Daemonización y gestión de procesos |
| `internal/state/` | Nuevo | Estado persistente en disco |
| `internal/group/` | Nuevo | Lógica de agrupación por campo group |
| `internal/tui/` | Nuevo | Capa de presentación con Bubbletea |
| `~/.local/state/svc/` | Nuevo | Directorio de estado en tiempo de ejecución |

## Rollback Plan

1. Detener todos los servicios daemonizados: `svc stop --all` (o kill manual por PID)
2. Eliminar directorio de estado: `rm -rf ~/.local/state/svc/`
3. El código fuente permanece en `feat/svc-tui`; la rama main no se ve afectada
4. No hay dependencias externas que limpiar (binario autocontenido)

## Success Criteria

- [ ] La TUI detecta proyectos desde CWD con 2 niveles de recursividad
- [ ] Cada proyecto muestra: nombre, lenguaje, grupo, estado (running/stopped/sin configurar)
- [ ] Los servicios se daemonizan y sobreviven al cierre de la TUI
- [ ] Al reabrir la TUI, se re-adjunta y verifica procesos vivos (con protección contra PID reuse)
- [ ] Se puede iniciar un servicio y ver su log en tiempo real
- [ ] Se puede detener un servicio con SIGTERM→SIGKILL tras timeout
- [ ] El hash de ruta evita colisiones entre proyectos homónimos
- [ ] Tests unitarios cubren parsing, hash, y detección de estado
- [ ] Smoke test manual pasa con un proyecto real (ej: vsocial)
