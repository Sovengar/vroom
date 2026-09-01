# svc

TUI en Go + Bubbletea para gestionar servicios de múltiples proyectos desde un solo punto.
Escanea el directorio actual (2 niveles de recursividad), detecta proyectos por marcadores
de lenguaje (`pom.xml`, `go.mod`, `package.json`, `pyproject.toml`, `requirements.txt`,
`Cargo.toml`), y permite iniciar/detener servicios daemonizados que **sobreviven al cierre
de la terminal**, con logs en vivo y detección de estado (PID + puerto + patrón de proceso).

## Instalación

```bash
go build -o ~/.local/bin/svc ./cmd/svc
```

Requisitos en runtime: Linux (v1), shell POSIX, `xdg-open` no requerido.

## Uso rápido — playground

El repo incluye `playground/` con 8 proyectos ficticios listos para probar todo el ciclo:

```bash
cd playground
svc
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
- Cada proyecto define su servicio en un `.svc.toml` — así se configura el tuyo:

```toml
name = "mi-servicio"
group = ""                  # vacío = sin agrupar
command = "go run main.go"
port = 8080                 # para detección (0 = deshabilitado)
process_pattern = ""        # patrón pgrep (opcional)
```

## Keybindings

| Tecla | Acción |
|---|---|
| `j`/`k` o flechas | Navegar (cíclico) |
| `enter` | Detalle del servicio |
| `s` | **Start/stop** (toggle contextual) |
| `R` | Restart (stop → start con timeout) |
| `l` | Vista de logs integrada (tail en vivo, `Tab` alterna stdout/stderr) |
| `o` | Abrir ambos logs en el editor (`$VISUAL`/`$EDITOR`, default nvim, split vertical) |
| `r` | Refresh forzado |
| `q`/`Esc` | Salir |

## Estado y logs

```
~/.local/state/svc/services/{hash}/   # hash = 8 hex de SHA-256 del path del proyecto
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

Spec funcional completa: `docs/planning/0001-feature-svc-tui/`.
