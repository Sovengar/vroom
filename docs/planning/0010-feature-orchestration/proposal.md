# Proposal: Orquestación de arranque con `.vroom-compose.toml`

## Intent

vroom hoy arranca servicios de forma aislada (`vroom start <name>`). El usuario
trabaja con múltiples proyectos que deben arrancar en un orden específico con
paralelismo dentro de cada etapa. Ejemplo: en `work/vsocial` necesita que DB y
camunda arranquen primero, luego las APIs, luego los SPAs. Este cambio añade
un fichero `.vroom-compose.toml` que define stacks con etapas secuenciales y
servicios paralelos, ejecutables con `vroom launch <stack>`.

Los stacks aparecen en la TUI integrados en el `primary_group` bajo un
`secondary_group` fijo llamado `Composers`, como una opción más ejecutable.

Cambio: `0010-feature-orchestration`
Base: 0001 (reutiliza scanner, process, state), 0006 (extiende group)

## Scope

### In Scope
- Nuevo fichero `.vroom-compose.toml` con múltiples stacks por fichero
- Parsing del compose file con validación de estructura
- Motor de orquestación: etapas secuenciales, servicios paralelos
- Health check por puerto (reutiliza `process.PortOpen` en `internal/process/detect.go:30`)
- Abort on failure: si falla, para todo y limpia
- Integración en TUI: stacks como `secondary_group = "Composers"` dentro del `primary_group`
- Indicador visual `🎵` + `[stack]` para distinguir stacks de apps
- CLI: `vroom launch --list`, `vroom launch <name>`, `vroom launch <name> --dry`
- Tests unitarios y de integración

### Out of Scope
- Health check por comando custom o patrón de proceso
- Watch/restart automático de servicios caídos
- Stop de stacks desde CLI (`vroom launch --stop`)
- Variables de entorno por servicio en el compose
- Más de dos niveles de anidación en grupos (ya limitado por 0006)

## Capabilities

### New Capabilities
- `compose-parsing`: Parsing de `.vroom-compose.toml` con schema TOML,
  múltiples stacks, validación de estructura y nombres
- `orchestration-engine`: Motor que ejecuta etapas secuenciales con
  servicios paralelos, health check por puerto, abort on failure
- `stack-launch`: Comando CLI `vroom launch` con `--list` y `--dry`
- `stack-tui-integration`: Stacks como entradas en la TUI bajo el
  secondary_group `Composers`

### Modified Capabilities
- `cli-commands` (0001): se añade subcomando `launch`
- `grouping` (0006): `Entry` y `Arrange()` aceptan stacks
- `groups-collapsible` (0006): el header `Composers` se puede colapsar

## Approach

### Decisiones

- **Fichero `.vroom-compose.toml`** (homenaje a docker-compose): se busca en
  CWD. Se distingue de `.vroom.toml` por no tener punto al nombre base.
- **Múltiples stacks por fichero**: usando `[[stack]]` TOML array of tables.
  Cada stack es independiente con sus etapas.
- **Secondary_group fijo `Composers`**: todos los stacks de un `primary_group`
  aparecen bajo este secondary_group al final del bloque primario. Esto
  aprovecha el sistema de grupos jerárquicos de 0006 sin modificaciones
  estructurales.
- **Health check por puerto**: reutiliza `process.PortOpen()` existente.
  Si un servicio no tiene `port` en su `.vroom.toml`, se espera un timeout
  fijo (default 30s).
- **Abort on failure**: si cualquier servicio falla (start o health), se aborta
  toda la orquestación y se paran los servicios arrancados en esta sesión.
- **Servicios ya corriendo**: si un servicio ya está running, se salta el start
  pero se verifica su health check antes de continuar a la siguiente etapa.
- **Resolución de servicios**: se escanean todos los proyectos (scanner existente)
  y se buscan por `Manifest.Name` del `.vroom.toml`.

### Arquitectura

```
internal/orchestrate/
├── compose.go           # ComposeFile, Stack, Stage structs + Parse()
├── compose_test.go      # Tests de parsing con fixtures TOML
├── engine.go            # Engine: Launch(), launchStage(), abortAndCleanup()
├── engine_test.go       # Tests con mocks de process.Manager
└── health.go            # WaitForPort()
```

**Flujo de dependencias:**
```
cli → orchestrate (engine)
orchestrate → process (Manager.Start/Stop, PortOpen)
orchestrate → scanner (Scan para encontrar proyectos)
orchestrate → state (Store para persistir meta de servicios arrancados)
tui → orchestrate (status de stacks)
tui → group (Arrange con stacks)
group → orchestrate (Stack type para Entry)
```

### Schema del compose file `.vroom-compose.toml`

```toml
# .vroom-compose.toml — orquestación de arranque
# Se busca en CWD. Puede definir múltiples stacks.

[[stack]]
name = "vsocial-full"
primary_group = "vsocial"  # requerido: dónde aparece en la TUI

  [[stack.stage]]
  name = "infrastructure"
  services = ["db", "camunda-server"]
  # timeout = "30s"  # default: 30s por servicio

  [[stack.stage]]
  name = "backend"
  services = ["actuacions-api", "agenda-api", "formularis-api"]

  [[stack.stage]]
  name = "frontend"
  services = ["valorador-spa", "agenda-spa"]

[[stack]]
name = "minimal"
primary_group = "vsocial"

  [[stack.stage]]
  name = "all"
  services = ["api", "web"]
```

| Campo | Tipo | Requerido | Default | Descripción |
|-------|------|-----------|---------|-------------|
| `stack.name` | string | Sí | — | Nombre del stack (para CLI y display) |
| `stack.primary_group` | string | Sí | — | Primary group donde aparece en la TUI |
| `stack.stage.name` | string | Sí | — | Nombre de la etapa (para display) |
| `stack.stage.services` | []string | Sí | — | Nombres de servicios (matchea `Manifest.Name` de `.vroom.toml`) |
| `stack.stage.timeout` | string | No | `"30s"` | Timeout de health check por servicio (formato Go duration) |

### Integración con el sistema de grupos

Los stacks se integran como `secondary_group = "Composers"` dentro de su
`primary_group`. Esto aprovecha el sistema jerárquico de 0006:

```
▾ vsocial                          ← primary_group header
  ▸ backend (2/3)                  ← secondary_group regular
    ● actuacions-api               ← proyecto
    · agenda-api
    · formularis-api
  ▸ frontend (0/2)                 ← secondary_group regular
    · valorador-spa
    · agenda-spa
  ▸ Composers (0/1)                ← secondary_group fijo para stacks
    🎵 vsocial-full [stack]         ← entrada de stack
```

El header `Composers` aparece al final del bloque primario, después de todos
los secondary_groups regulares. Se puede colapsar con `enter` como cualquier
otro header secundario.

### Cambios en `group.Entry`

```go
// internal/group/group.go
const ComposersGroup = "Composers"

type Entry struct {
    Primary   string
    Secondary string
    Project   scanner.Project  // zero value para stacks
    Stack     *Stack           // nil para proyectos regulares
}
```

`Arrange()` firma actual:
```go
func Arrange(projects []scanner.Project) []Entry
```

Firma nueva:
```go
func Arrange(projects []scanner.Project, stacks []Stack) []Entry
```

La lógica añadida:
1. Ejecutar la agrupación existente de proyectos
2. Para cada stack, crear Entry con `Primary = stack.PrimaryGroup`,
   `Secondary = "Composers"`, `Stack = &stack`
3. Los entries de stacks se insertan al FINAL de su primary_group,
   después de todos los secondary_groups regulares

### Integración con la TUI

**Nuevo `treeItemKind` en `internal/tui/projectlist.go`:**
```go
const (
    itemPrimary   treeItemKind = iota
    itemSecondary
    itemProject
    itemStack     // NUEVO
)
```

**Renderizado en `projectlist.go`:**
```go
func (m Model) stackRow(s *orchestrate.Stack) string {
    r, n := m.stackStats(s)
    label := fmt.Sprintf("🎵 %s [stack] (%d/%d)", s.Name, r, n)
    return styleStack.Render(trunc(label, treeWidth-4))
}
```

**Nuevo estilo en `internal/tui/styles.go`:**
```go
styleStack = lipgloss.NewStyle().Foreground(lipgloss.Color("5")) // magenta
```

**Acciones en `internal/tui/app.go`:**
- `s` sobre un stack → `toggleStack()`: lanza o para la orquestación
- `enter` sobre un stack → no hace nada (stacks no se pliegan)
- `b`, `i`, `l` sobre un stack → mensaje "not available for stacks"

**Detalles en `internal/tui/serviceview.go`:**
Cuando se selecciona un stack, el panel de detalles muestra:
- Nombre + estado (running/stopped)
- Tipo: "orchestration stack"
- Número de etapas y servicios
- Lista de etapas con sus servicios

### CLI

```bash
vroom launch --list                        # listar todos los stacks
vroom launch <stack-name>                  # arrancar stack
vroom launch <stack-name> --dry            # dry run (muestra plan sin ejecutar)
```

**Salida JSON `vroom launch --list`:**
```json
{
  "file": "/home/user/work/vsocial/.vroom-compose.toml",
  "stacks": [
    {
      "name": "vsocial-full",
      "primary_group": "vsocial",
      "stages": [
        {"name": "infrastructure", "services": ["db", "camunda-server"]},
        {"name": "backend", "services": ["actuacions-api", "agenda-api", "formularis-api"]},
        {"name": "frontend", "services": ["valorador-spa", "agenda-spa"]}
      ]
    }
  ]
}
```

**Salida JSON `vroom launch vsocial-full`:**
```json
{
  "ok": true,
  "stack": "vsocial-full",
  "stages": [
    {
      "name": "infrastructure",
      "services": [
        {"name": "db", "action": "started", "pid": 12345},
        {"name": "camunda-server", "action": "started", "pid": 12346}
      ]
    },
    {
      "name": "backend",
      "services": [
        {"name": "actuacions-api", "action": "started", "pid": 12347},
        {"name": "agenda-api", "action": "already_running"},
        {"name": "formularis-api", "action": "started", "pid": 12348}
      ]
    }
  ]
}
```

### Manejo de errores

| Escenario | Comportamiento |
|-----------|----------------|
| `.vroom-compose.toml` no encontrado | Error: `"no .vroom-compose.toml found in current directory"` |
| Stack name no encontrado | Error: `"stack 'xyz' not found in .vroom-compose.toml"` |
| Servicio no encontrado en projects | Error: `"service 'xyz' not found in scanned projects"` |
| Servicio sin port y sin timeout | Usa timeout default (30s) |
| Servicio falla al arrancar | Abort + stop todos los servicios de esta sesión |
| Servicio no pasa health check | Abort + stop todos los servicios de esta sesión |
| Servicio ya corriendo | Skip start, verificar health, continuar |

### Health check

```go
// internal/orchestrate/health.go
func WaitForPort(port int, timeout time.Duration) error {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if process.PortOpen(port) {
            return nil
        }
        time.Sleep(500 * time.Millisecond)
    }
    return fmt.Errorf("port %d not open after %s", port, timeout)
}
```

Reutiliza `process.PortOpen()` de `internal/process/detect.go:30`.

### Ficheros afectados

| Fichero | Tipo | Descripción |
|---------|------|-------------|
| `internal/orchestrate/compose.go` | Nuevo | Parsing de `.vroom-compose.toml` |
| `internal/orchestrate/compose_test.go` | Nuevo | Tests de parsing |
| `internal/orchestrate/engine.go` | Nuevo | Motor de orquestación |
| `internal/orchestrate/engine_test.go` | Nuevo | Tests del motor |
| `internal/orchestrate/health.go` | Nuevo | WaitForPort() |
| `internal/group/group.go` | Modificado | `Entry.Stack`, `ComposersGroup`, `Arrange()` con stacks |
| `internal/group/group_test.go` | Modificado | Tests con stacks |
| `internal/scanner/scanner.go` | Modificado | Escanear `.vroom-compose.toml` en ScanResult |
| `internal/tui/projectlist.go` | Modificado | `itemStack`, `stackRow()` |
| `internal/tui/app.go` | Modificado | `toggleStack()`, `selectedStack()`, nuevos mensajes |
| `internal/tui/serviceview.go` | Modificado | `stackDetailsLines()` |
| `internal/tui/styles.go` | Modificado | `styleStack` |
| `internal/cli/cli.go` | Modificado | `vroom launch` |

### Dependencias Go

No se necesitan dependencias nuevas. Se reutiliza:
- `github.com/BurntSushi/toml` — parsing del compose file (ya en `go.mod`)
- `vroom/internal/process` — `PortOpen`, `Manager.Start/Stop`
- `vroom/internal/scanner` — `Scan` para encontrar proyectos
- `vroom/internal/state` — `Store` para persistir meta
- `vroom/internal/manifest` — `Manifest.Name` para resolver servicios

### Plan de verificación

1. **Tests unitarios compose.go**: parsing TOML, validación, fixtures
2. **Tests unitarios engine.go**: lógica de etapas, abort, cleanup
3. **Tests group_test.go**: Arrange() con stacks bajo Composers
4. **Tests de integración**: flujo completo parse → resolve → mock start
5. **Smoke test manual**:
   - Crear `.vroom-compose.toml` en playground
   - `vroom launch --list` → verificar JSON
   - `vroom launch minimal --dry` → verificar dry run
   - `vroom launch minimal` → verificar arranque paralelo
   - Verificar que el stack aparece en la TUI bajo Composers
   - Ctrl+C durante arranque → verificar cleanup

### Rollback

1. Eliminar `internal/orchestrate/` (paquete nuevo, sin dependencias externas)
2. Revertir cambios en `internal/group/group.go`, `internal/cli/cli.go`,
   `internal/scanner/scanner.go`, `internal/tui/*.go`
3. No hay dependencias nuevas en go.mod
