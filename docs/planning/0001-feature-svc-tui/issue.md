# Implementar TUI para gestión de proyectos y servicios

## User Story

**Como** desarrollador que trabaja con múltiples proyectos en distintos lenguajes,
**Quiero** una TUI que muestre los proyectos del directorio actual con su estado y me permita iniciar/detener servicios,
**Para** gestionar eficientemente mi entorno de desarrollo sin perder contexto al cambiar de proyecto.

## Alcance

**Qué entra:**
- Escaneo de proyectos con 2 niveles de recursividad
- Detección automática de lenguaje (Java, Go, JS, Python, Rust, etc.)
- Gestión de servicios daemonizados que sobreviven al cierre de la TUI
- Estado persistente en disco (~/.local/state/svc/)
- Acciones: iniciar, detener (SIGTERM → SIGKILL), reiniciar, ver logs
- Modo descubrimiento para proyectos sin configurar

**Qué NO entra:**
- Edición de manifiestos desde la TUI
- Gestión de dependencias entre servicios
- Dashboard de métricas o monitoreo avanzado
- Soporte para múltiples workspaces simultáneos

## Acceptance Criteria

- [ ] La TUI detecta proyectos en ~/dev/{projects,personal,work,learning} con 2 niveles de recursividad
- [ ] Cada proyecto muestra: nombre, lenguaje, grupo (si pertenece a uno), estado (running/stopped)
- [ ] Los servicios se ejecutan daemonizados y sobreviven al cierre de la TUI
- [ ] Al reabrir la TUI, se re-adjunta al estado persistente y verifica procesos vivos
- [ ] Se puede iniciar un servicio y ver su log en tiempo real dentro de la TUI
- [ ] Se puede detener un servicio por SIGTERM y forzar con SIGKILL tras timeout
- [ ] Proyectos sin manifiesto .svc.toml se muestran como "sin configurar" pero son visibles
- [ ] El layout del directorio de estado permite recuperar logs e histórico de servicios

## Decisiones Técnicas Cerradas

| Decisión | Valor | Justificación |
|----------|-------|---------------|
| Lenguaje | Go + Bubbletea v2 + Lipgloss + Bubbles | Stack probado para TUIs, ecosistema rico |
| Ubicación | ~/dev/projects/svc (rama feat/svc-tui) | Ya creado, repo inicializado |
| Ejecución | Híbrida daemonizada (setsid / process group propio) | Servicios sobreviven al cierre de TUI |
| Estado | ~/.local/state/svc/ (PID/PGID + metadatos + logs) | XDG compliant, persistente |
| Detección | PID/PGID vivo + check de puerto/patrón de proceso | Cubre servicios iniciados fuera de la TUI |
| Stop | SIGTERM → SIGKILL con timeout | Graceful shutdown con fallback |
| Logs | Tail de fichero + acción para abrir externamente | Balance entre integración y flexibilidad |

## Propuestas para Puntos Abiertos

### 1. Manifiesto por proyecto: `.svc.toml`

**Propuesta:** Fichero `.svc.toml` en la raíz del proyecto.

**Justificación:**
- Consistente con cdx-rs (usa TOML)
- Formato legible y popular en el ecosistema Go
- Extensible sin breaking changes

**Campos mínimos:**
```toml
nombre = "mi-servicio"
grupo = "vsocial"  # vacío si es proyecto único
comando = "go run main.go"  # o "./start.sh"
puerto = 8080  # para detección (opcional)
patron_proceso = "mi-servicio"  # pgrep pattern (opcional)
logs_dir = "./logs"  # relativo al proyecto
```

### 2. Expresión de grupos: cadena de grupo en ambos manifiestos

**Propuesta:** Cada proyecto declara su grupo en `grupo = "nombre_grupo"`. Si el valor es el mismo en ambos, se agrupan automáticamente.

**Justificación:**
- Simple y descentralizado
- No requiere fichero de configuración central
- Permite que un proyecto pertenezca a múltiples grupos en el futuro
- Cada manifiesto es autocontenido

### 3. Proyectos sin manifiesto: modo descubrimiento

**Propuesta:** Mostrar todos los proyectos detectados, marcando los sin `.svc.toml` como "sin configurar" (estilo gris/deshabilitado).

**Justificación:**
- No penaliza la exploración inicial
- Permite al usuario ver qué proyectos existen
- Invita a crear manifiestos gradualmente
- Respeta el principio de mínima sorpresa

### 4. Multi-módulo Maven: manifiesto en raíz del padre

**Propuesta:** Un único `.svc.toml` en la raíz del proyecto padre (ej: vsocial/), con campos que describan el conjunto.

**Justificación:**
- Evita duplicación de configuración
- Refleja la realidad: un proyecto, múltiples módulos
- El usuario gestiona "vsocial" como unidad, no cada módulo por separado
- Los módulos se tratan internamente pero la interfaz es por proyecto

### 5. Layout del directorio de estado

**Propuesta:**
```
~/.local/state/svc/
├── services/
│   ├── {service-name}/
│   │   ├── pid          # PID del proceso principal
│   │   ├── pgid         # PGID para kill por grupo
│   │   ├── meta.json    # nombre, grupo, puerto, fecha_inicio
│   │   ├── stdout.log   # stdout capturado
│   │   └── stderr.log   # stderr capturado
│   └── ...
├── config/
│   └── state.json       # estado global de la TUI
└── logs/
    └── svc-tui.log      # log de la propia TUI
```

**Justificación:**
- Estructura clara por servicio
- Separa logs de servicio de logs de TUI
- Permite recuperación limpia tras crashes
- Fácil de inspeccionar manualmente

### 6. Ejecución en otra shell (modelo híbrido daemonizado)

**Propuesta:** El comando del manifiesto se ejecuta via `sh -c "{comando}"` en un proceso desacoplado (setsid). El proceso queda daemonizado con logs capturados.

**Flujo:**
1. Parsear comando del `.svc.toml`
2. Crear proceso hijo con setsid() para nuevo session leader
3. Redirigir stdout/stderr a ficheros de log
4. Registrar PID/PGID en estado
5. Proceso sigue vivo al cerrar TUI

**Script .sh como alternativa:** Si el comando termina en `.sh`, se ejecuta directamente. El efecto es el mismo: daemonizado con logs.

## Task Breakdown

1. **Modelo de datos y parsing de manifiestos** — Definir structs Go para proyecto/servicio, implementar parser TOML para `.svc.toml`

2. **Escaneo de directorios con recursividad** — Implementar walker que detecte proyectos por marcadores de lenguaje (pom.xml, go.mod, package.json, etc.) con 2 niveles de profundidad

3. **Estado persistente y detección de procesos** — Sistema de PID/PGID en `~/.local/state/svc/`, verificación de procesos vivos, check de puerto/patrón

4. **Daemonización de servicios** — Implementar spawn desacoplado con setsid, gestión de process groups, captura de logs

5. **TUI base con Bubbletea** — Layout principal, lista de proyectos, navegación, estado visual (running/stopped/sin configurar)

6. **Acciones de servicio** — Iniciar, detener (SIGTERM→SIGKILL), reiniciar, con estados intermedios en la UI

7. **Vista de logs** — Viewport dentro de la TUI con tail de log, acción para abrir en editor externo

8. **Tests con teatest** — Unit tests para lógica de negocio, tests de integración para daemonización

## Testing Considerations

- **Unit tests:** Parsing de manifiestos, detección de lenguaje, lógica de PID/PGID
- **Integration tests:** Flujo completo: scan → daemonizar → verificar estado → detener
- **Edge cases:** 
  - Servicio que crashea inmediatamente
  - Puerto ya en uso
  - Manifiesto con campos incompletos
  - Directorio sin permisos de escritura
  - Reabrir TUI tras crash del sistema

## Assumptions

- El usuario tiene permisos de escritura en `~/.local/state/svc/`
- Los proyectos marcados con Build (Java), Build (JS), Requirements (Python), etc. son válidos para detección
- El formato TOML es aceptable para el manifiesto (consistente con cdx-rs)
- Los servicios daemonizados no requieren autenticación o permisos especiales

---

**Nota:** Esta issue define el "qué" y el "por qué". El "cómo" funcional se detalla en proposal.md y spec.md (requisitos + escenarios Gherkin), que constituyen el contrato de implementación; el agente ejecutor decide los detalles de código.
