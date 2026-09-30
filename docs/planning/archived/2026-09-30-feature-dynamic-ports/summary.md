# Summary: dynamic-ports

## Metadata
- **Completed:** 2026-09-30 20:15
- **Duration:** ~9 hours (10:52 → 19:58, 15 commits)
- **Plan Number:** 0001
- **Branch:** `feat/dynamic-ports` → `main`
- **History shape:** kept as-is, 15 atomic commits (reshape declined on purpose, see Next Step)

## Scenarios
| Feature (behavior.feature) | Scenarios | Status |
|----------------------------|-----------|--------|
| Stop por linaje no deja procesos huerfanos ni mata procesos ajenos | 5 | ✅ Passed |
| Puertos dinamicos por worktree con el puerto real como unica verdad | 36 | ✅ Passed |
| Eleccion determinista del puerto principal cuando hay varios listeners | 5 | ✅ Passed |
| **Total** | **46** | ✅ Passed |

Todos los 46 escenarios de `behavior.feature` estan implementados y en verde.
`behavior-portless.feature` (8 escenarios) queda **sin implementar a proposito**:
slice 4 diferido por el usuario hasta su upgrade global a Node 24+. No hay stubs
ni campos de placeholder para el.

## Commits
15 commits atomicos sobre `main`, lineales, sin squash. La historia se conserva
integra (cuerpos incluidos) por decision explicita; ver Next Step.

- `chore: add dynamic-ports plan`
- `docs: registra la investigacion de puertos dinamicos y portless`
- `fix(process): parar por linaje y fallar cerrado sin prueba de propiedad`
- `feat(ports): puertos dinamicos por worktree con el puerto real como verdad`
- `feat(ports): desambiguacion multi-puerto determinista R1/R2/R3`
- `refactor(startsvc): extract resolveDynamicPort`
- `fix(ports): reserva de puerto concurrente dentro del proceso`
- `fix(orchestrate): un servicio sin puerto no tumba el stack`
- `fix(ports): el puerto sin resolver no es un servicio sin puerto`
- `fix(orchestrate): puerto sin resolver es un tercer resultado no fatal`
- `fix(ports): la reserva tiene ciclo de vida, se devuelve al parar`
- `fix(cli): el JSON solo emite puertos que vroom ha confirmado`
- `test: guard de higiene que falla si un helper sobrevive a la suite`
- `fix(process): Stop con solo PID ya no es un no-op`
- `fix(test): el self-check del guard no debe depender de que sh haga exec`

## Files
42 files changed, +7625 / -169.

- **Created (product):** `internal/process/{dynamic.go,dynamic_unix.go,lineage_unix.go}`,
  `internal/startsvc/startsvc.go`
- **Created (tests):** `internal/process/{dynamic_unix,disambiguate_unix,hygiene_unix,stop_lineage,stop_pid}_test.go`,
  `internal/startsvc/{startsvc,hygiene,helper}_test.go`,
  `internal/orchestrate/{health,hygiene,noport}_test.go`,
  `internal/cli/{port,portjson}_test.go`, `internal/manifest/portmode_test.go`
- **Modified (product):** `internal/{cli/cli.go,config/config.go,manifest/manifest.go,state/state.go}`,
  `internal/orchestrate/{engine.go,health.go}`, `internal/process/{daemon_unix.go,detect.go,process.go}`,
  `internal/tui/{app.go,dashboard.go,outputtabs.go,serviceview.go}`, `README.md`
- **Created (docs):** `docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md`,
  `docs/proposal-dynamic-ports.md`, `docs/planning/0001-feature-dynamic-ports/**`

## Tests
- **Added:** 103 test functions across 14 new test files
- **Suite:** ✅ Passed — `make test` (`go test -race -count=1 -cover ./...`), 18/18 packages,
  el mas lento `internal/startsvc` 49.9 s, `internal/orchestrate` 39.0 s
- **Lint:** ✅ 0 issues (golangci-lint v2.13.2, `make lint`)
- **Build:** ✅ `go build ./...` + `go vet ./...`
- **CI en el tip:** Build / Lint / Test / Mutation calibration ✅ SUCCESS

### Pruebas rojas que fixearon cada commit de review
- `ReservePort` no era seguro ante concurrencia: **7/8 colisiones** medidas en una etapa
  de stack en paralelo antes, **0/8** despues.
- Un servicio sin puerto abortaba el stack entero y derribaba a los hermanos sanos.
- Un puerto que vroom no pudo confirmar era indistinguible de un servicio sin puerto,
  tanto en el badge de la TUI como en el JSON.
- `Stop` con PID valido y sin PGID era un **no-op completo**: `StopSpec.Pid` era
  decorativo como objetivo de muerte.
- El self-check del guard de higiene fallaba en CI (ubuntu-24.04, `/bin/sh` = dash) y no
  en local (bash): afirmaba sobre el PID del *wrapper* de `sh -c`, que solo es nuestro
  si la shell hace `exec`. Refutado con evidencia, no por supuesto.

## Documentation
- **ADR:** ✅ `docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md`
- **Changelog:** ✅ `CHANGELOG.md` (seccion `[Unreleased]`)
- **Docs de producto:** `README.md` actualizado, `docs/proposal-dynamic-ports.md`

## Code Review Issues
- **Critical Found:** 1 (`Stop` con PID sin PGID = no-op; `StopSpec.Pid` decorativo)
- **High Found:** 3 (reserva de puerto no concurrente; servicio sin puerto tumba el stack;
  puerto no confirmable indistinguible de "sin puerto")
- **User Decision:** Approved to continue — las dos rondas de review cerradas, todas las
  criticas y altas fijadas con su prueba en rojo.

## Limitations (aceptadas, no son regresiones)
- La ventana de gracia de 16 s del discovery no es configurable.
- Residuo de reserva cross-process: un `SIGKILL` que impida el `defer` de devolucion
  deja la ranura ocupada hasta que el puerto se libera por el SO.
- Divergencia CLI/TUI para el caso `no_port`.
- `port_verified: false` en un servicio fijo sano es veraz pero puede leerse mal.

## Next Step
Infra de `swe-shell.sh`, separada y ya en cola del usuario (fuera de este PR):
1. `--base` obligatorio, para que `SWE_INTEGRATION_BASE` deje de depender del export.
2. `shape-change-history apply` destruye los cuerpos de commit. El editor scripteado
   (`git-forge.sh:195`) hace `printf '%s\n' "$first" > "$msgfile"` sobre el mensaje
   completo, y los miembros se fusionan con `fixup`. El invariante de seguridad
   (`mechanism_content_tree`) solo compara el **arbol**, asi que reporta `ok` mientras
   los mensajes desaparecen: un guard que no ve lo que guarda. En esta rama habria
   costado 14 cuerpos y 374 lineas de analisis de causa raiz.
   Ademas la agrupacion es por *concern* (= el scope entre parentesis), asi que ningun
   valor de `.change-shape` puede preservar mas commits que concerns distintos; la
   unica via para conservar la historia es no ejecutar el verbo.
