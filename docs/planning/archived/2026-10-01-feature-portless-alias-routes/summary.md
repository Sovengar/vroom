# Summary: portless-alias-routes

## Metadata
- **Completed:** 2026-10-01 16:22
- **Duration:** ~2 días (2026-09-30 → 2026-10-01), 22 commits atómicos
- **Plan Number:** 0001
- **Slice:** 4 de 4 (los slices 1-3 —puertos dinámicos, Stop-by-lineage, R1/R2/R3 multi-port— ya mergearon como `e9df629`)

## Scenarios
| Scenario (behavior.feature) | Status |
|-----------------------------|--------|
| 39 escenarios Gherkin en `behavior.feature`, agrupados en 10 bloques | ✅ Passed (`make test`) |

Bloques: criterio rector (salud independiente de la ruta) · ausencia total de
portless · proxy ausente · camino feliz verificado · verificación contra el proxy
vivo · lectura de vuelta (exit 0 no prueba propiedad) · reconciliación · ciclo de
vida del stop · compatibilidad hacia atrás · superficie JSON para agentes.

## Commits
22 commits atómicos, **historia preservada tal cual** (no se aplicó
`shape-change-history`; ver "Shape decision"). Detalle:

- `chore: add portless-alias-routes plan`
- `feat(portless): seam inyectable para registrar rutas`
- `test(startsvc): la salud nunca depende de la ruta`
- `feat(cli,tui,orchestrate): superficie de ruta y retirada en los tres stops`
- `feat(portless)` / `fix(portless)` / `refactor(portless)` ×8 (seam, state dir, reconciliación, read-back, concesión, retirada inyectable)
- `test(portless)`, `test(cli,tui)`, `test+docs` — tests de integración, sonda real, ramas muertas del engine
- `docs(adr)` ×2, `docs: corrige el mecanismo a portless alias`, `docs(plan)`, `fix(lint)`

### Shape decision (explícito)
**Keep as-is, atomic commits.** El plan de `shape-change-history` colapsaba 22
commits en 8 unidades (14 cuerpos de commit destroyed) y en este harness su
`GIT_EDITOR` scriptado sobrescribe cada mensaje con una única línea de subject,
mientras `mechanism_content_tree()` sólo resuelve un hash de árbol — no puede
detectar la pérdida y reportaría `ok` igual. Ningún valor de `.change-shape`
preserva los cuerpos porque el agrupamiento es por concern. Ningún verbo del
entrypoint exige el reshape (`integrate-change` no lo invoca), así que se
declinó y se conservaron los 22 commits.

## Files
- **Created:** `internal/portless/` (11 archivos: `apply.go`, `portless.go`, `name.go` + 8 `_test.go`), `internal/cli/route_test.go`, `internal/cli/route_release_test.go`, `internal/manifest/routemode_test.go`, `internal/orchestrate/route_release_test.go`, `internal/startsvc/route_test.go`, `internal/startsvc/route_grant_test.go`, `internal/startsvc/route_ownership_test.go`, `internal/startsvc/helper_test.go`, `internal/tui/route.go`, `internal/tui/route_test.go`, `internal/tui/route_release_test.go`, `docs/adr/adr-0013-vroom-registers-portless-routes.md`, `docs/planning/0001-feature-portless-alias-routes/*`
- **Modified:** `internal/cli/cli.go`, `internal/manifest/manifest.go`, `internal/orchestrate/engine.go`, `internal/startsvc/startsvc.go`, `internal/state/state.go`, `internal/tui/app.go`, `internal/tui/app_test.go`, `internal/tui/serviceview.go`, `README.md`, `docs/adr/adr-0012-…`, `docs/proposal-dynamic-ports.md`
- **Diff total:** 43 archivos, +7809 / -62

## Tests
- **Added:** ~3.600 líneas de tests nuevos en 14 archivos (dominio `internal/portless` al 77.0% de cobertura, `startsvc` 87.8%, `orchestrate` 89.7%)
- **System Tests:** ✅ Passed — `make test` (`go test -race -count=1 -cover ./...`) verde en los 20 paquetes
- **CI en `b9b6db0`:** `Build`, `Vet`, `Lint`, `Test` ✅ · `Mutation` ✅
- **RED real:** el orquestador neutralizó `!held.Owned` en `apply.go:521`, dos tests pasaron a ROJO con los mensajes esperados, y volvieron a verde al restaurar. La prueba RED del último fix es genuina.

## Documentation
- **Changelog:** ✅ Updated (`## [Unreleased]` → `Added`, vroom registra rutas portless)
- **Docs:** `README.md` (sección `route_mode` completa, tabla de modos, alcance de `auto`, contrato de degradación, semántica del JSON), `docs/proposal-dynamic-ports.md`, `docs/adr/adr-0012` (nota de cross-ref)
- **ADR:** ✅ Created — `docs/adr/adr-0013-vroom-registers-portless-routes.md` (18 decisiones, §18 documenta el cambio de comportamiento deliberado)

## Code Review Issues
5 rondas de review, cada una con defectos reales. Todos de la **misma forma**: un predicado que leía un **subconjunto** del estado de propiedad.

Orden de los hallazgos:
1. El read-back corría **después** de la escritura → sólo podía confirmar la escritura de vroom.
2. La propiedad se concedía por una línea que **nunca miraba** el resultado.
3. La revocación **nunca llegaba** a `Reconcile` → huérfanas perpetuas.
4. La guarda de `routeUnknown` era **código muerto insatisfacible** → `Remove` corría sin comprobación de propiedad alguna, y borraba rutas ajenas. (ADR §18, con reproducción contra portless real en sus dos estados de disparo.)
5. El stop retiraba **sólo por handle** → eliminaba rutas ajenas.

Dos los encontró la auditoría propia del ejecutor.

- **Critical Found:** 2 (guarda muerta con borrado de rutas ajenas; stop que borra rutas ajenas)
- **High Found:** 3 (los otros tres)
- **User Decision:** Approved to continue

### Hechos medidos que forzaron el diseño
- **M1:** `portless alias` **nunca contacta con el proxy** — escritura pura del fichero de estado. `exit 0` no prueba ni propiedad ni disponibilidad → dos verificaciones independientes: read-back para propiedad, sonda viva para disponibilidad.
- **M6:** `proxy.port` **sólo existe mientras el proxy corre**; su ausencia *es* la señal. Nunca un puerto por defecto hardcodeado.
- **M5:** `portless prune` **no toca** rutas de alias (`pid: 0`, contadas como activas) → vroom es lo único que puede limpiarlas, lo que hace de la reconciliación en el arranque un requisito de correctitud.
- **Bastardización del plan:** el state dir de portless es `$PORTLESS_STATE_DIR`, **no** `$PORTLESS_HOME`. El plan decía lo contrario; portless 0.15.6 ignora esa variable. Implementar el plan al pie de la letra produjo rutas que nunca se podían quitar.

## Limitaciones (documentadas, no regresiones)
1. Un `meta.json` escrito por un vroom pre-actualización no tiene `route_owned` → las **huérfanas pre-upgrade dejan de auto-limpiarse**. Falla cerrado: ruta preservada, handle retenido y recuperable, aviso emitido. ADR §18.
2. La sonda viva cuesta **una petición HTTP por arranque de servicio**.
3. **Tres tests de integración hacen skip** sin portless real, así que algunos hechos medidos quedan sin verificar en un runner CI pelado.
4. Coste residual inofensivo: registrar una ruta recolecta entradas stale en el `routes.json` del usuario (PIDs muertos).

## Decisión del usuario, innegociable y respetada
**vroom sólo registra alias. Nunca arranca, gestiona, supervisa ni muestra el proxy.**
Sin proxy → avisa **una vez**, el servicio queda sano en su propio puerto. **La salud
de un servicio nunca depende de que exista su ruta.** El usuario rechazó
explícitamente auto-arrancar el proxy y rechazó exponerlo como servicio de la TUI.

`route_mode = "off" | "auto" | "named"` + `route_name`, siguiendo el precedente de
`port_mode`: `off` es el default, **no busca el binario**, y ningún manifiesto
existente cambia de comportamiento. `auto` deriva `<rama>.<proyecto>` y es de scope
**RAMA** (hallazgo de review: el comentario del código y el README afirmaban
scope de worktree, que el código no podía dar; corregido en los tres sitios).

## Next Step
Mergear a `main` por PR, verificar el workflow `push` de `main` en verde, el badge
del README en `passing`, y reconstruir `~/.local/bin/vroom` en la revisión mergeada.
