# Changelog

Todos los cambios notables de este proyecto se documentan en este archivo.

El formato está basado en [Keep a Changelog](https://keepachangelog.com/es/1.0.0/),
y este proyecto sigue [Semantic Versioning](https://semver.org/lang/es/).

## [Unreleased]

### Added
- Puertos dinámicos por worktree con `port_mode = "fixed" | "dynamic" | "none"`
  en `.vroom.toml`. En `dynamic` vroom reserva un puerto del rango 4000-4999,
  inyecta `PORT` y `HOST` en el entorno del proceso, descubre el puerto real
  listening y lo usa como única verdad en la TUI, en la salida JSON y en el
  gate de salud del stack. Un manifiesto sin `port_mode` se comporta
  exactamente como hasta ahora.
- Elección determinista del puerto principal cuando un servicio abre varios
  listeners: primero el puerto reservado, después el que responde al
  `health_path` de la sonda, y como último recurso el menor, marcado como no
  verificado.
- `port_verified` en la salida JSON de `vroom list`, para distinguir un puerto
  confirmado por vroom de uno todavía pendiente de resolver.
- ADR-0012 documentando el contrato de propiedad de puertos y el diseño de
  puertos dinámicos.
- Panel `Output` con cinco pestañas nuevas además de Console y Threads:
  `Metrics` (CPU%, RSS, FDs e hilos), `Git` (rama, estado y últimos commits),
  `Env` (entorno del proceso), `Timeline` (eventos de la sesión con duración)
  y `Health` (probe HTTP al puerto del manifiesto).
- Campo opcional `health_path` en `.vroom.toml` para la ruta del probe de la
  tab Health (default `/`).
- Anidar los worktrees de git bajo la fila de su repo: el repo se muestra como
  una única fila colapsable (colapsada por defecto) y sus worktrees aparecen
  indentados al expandir, cada uno operable como cualquier proyecto.
- Detectar bare repos durante el scan y mostrarlos como fila contenedora no
  ejecutable, con sus worktrees anidados.
- Direccionar proyectos por path en la CLI (posicional o `--path`) y exponer la
  relación repo/worktree (`repo_root`, `is_worktree`, `bare_container`) en la
  salida JSON de `vroom list`, manteniendo el array plano por compatibilidad.
- Resolver los stacks de orquestación de forma determinista: ante un
  `Manifest.Name` duplicado falla explícitamente listando los paths candidatos,
  en lugar de elegir arbitrariamente el último.
- CI en GitHub Actions con tres gates que corren en cada PR y en cada push a
  `main`: `Build` (`go build ./...` + `go vet ./...`), `Lint` (`make lint`,
  golangci-lint v2.13.2) y `Test` (`go test -race` de todo el suite, con `fd`
  instalado en el runner para los tests del scanner). El merge a `main` queda
  gobernado por la ruleset `protect-main`: PR obligatorio, checks verdes
  requeridos y bloqueo de force-push/borrado, con bypass de admin (deliberado).
  Dependabot actualiza GitHub Actions de forma semanal y el README muestra el
  badge de estado del workflow.

### Fixed
- Detener un servicio ya no deja procesos huérfanos: los hijos que cambian de
  SID al arrancar (`nohup`, `setsid`, `pm2`, `docker run -d`) se detienen
  siguiendo el linaje, y el puerto solo se libera cuando la propiedad es
  demostrable, nunca a ciegas.
- `Stop` con un PID válido y sin PGID ya no es una operación nula: el PID es un
  objetivo de muerte por sí mismo y su linaje se señala proceso a proceso.
- La reserva de puertos es segura ante arranques concurrentes, y el puerto
  vuelve al pool al detener el servicio.
- Un servicio sin puerto ya no aborta el stack entero ni derriba a sus
  hermanos sanos.
- Un puerto que vroom no pudo confirmar se distingue de un servicio sin puerto,
  tanto en el badge de la TUI como en la salida JSON.
- El entorno del proceso hijo ya no se trunca al inyectar `PORT` y `HOST`: se
  fusiona con el entorno existente.
- Consultar los bare repos para descubrir sus worktrees (antes se omitían).
- Mostrar el error de topología de la fila de repo aunque el repo tenga hijos.
- No contar ni plegar los worktrees anidados al agregar el estado de un grupo.
- Detectar bare repos durante el scan sin un recorrido adicional del árbol.
- Consultar git una sola vez por repo en lugar de una vez por proyecto.
- Acotar el tiempo real de ejecución de git mediante un timeout efectivo.
- Un worktree marcado como prunable por git pero cuyo directorio sigue
  existiendo ya no se muestra como entrada top-level: se anida con normalidad.
- La fila de un repo sin worktrees ya no muestra el glifo de expansión.
- La detección de bare repos solo considera `core.bare` dentro de la sección
  `[core]` del config, evitando falsos positivos.
- Exponer el error de topología del repo (`worktree_error`) en el JSON de
  `vroom list`.
- Separar la salida de git de stderr, para que un warning no corrompa el
  parseo, e ignorar bloques de salida con ruta vacía.

### Changed
- El dashboard se compone de cuatro secciones con borde redondeado y título
  (`Projects`, `Details`, `Output`, `Keybinds`) y ya no muestra la cabecera
  `vroom — projects in …`, desfasada al ser configurable el root de escaneo.
- El panel `Console` pasa a llamarse `Output` (también alberga Threads y las
  nuevas pestañas); la navegación pasa a `1`…`7`, con `tab`/`shift+tab` para
  ciclar.
- Las pestañas inactivas usan gris claro legible (antes el ANSI 8 quedaba
  casi invisible en temas oscuros).
- El parseo de `--path` es predecible: acepta `--path <valor>` y
  `--path=<valor>`, y falla con un error claro ante valor ausente, vacío o
  duplicado (antes se ignoraba en silencio).
- Al direccionar un proyecto por path se resuelven los symlinks, de modo que
  un path enlazado apunta al proyecto correcto.
- La validación de stacks itera los nombres en orden estable, con mensajes y
  errores reproducibles.

### Removed
- Código muerto: `quickCheck`, `stacksEmitted`, el parámetro `services` de
  `StopStack` y el tipo `ServiceStatus`.

[Unreleased]: https://github.com/Sovengar/vroom/commits/HEAD
