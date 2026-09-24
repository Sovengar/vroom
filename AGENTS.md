# AGENTS.md — vroom

## Paso crucial tras cualquier cambio de código

**Desplegar el binario** (los tests/smoke con `go run` no actualizan el
instalado; el usuario ejecuta el bin de `~/.local/bin`, no el repo):

```bash
go build -o ~/.local/bin/vroom ./cmd/vroom
```

Sin este paso, cualquier verificación que haga el usuario sobre la TUI usa la
versión vieja. Ejecutarlo SIEMPRE al terminar una tarea de código, después de
la verificación (`go build ./... && go vet ./... && go test ./...`).

## CI y protección de `main`

CI vive en `.github/workflows/ci.yml` y corre en **todo PR** y en **todo push
a `main`** (sin filtros `paths`: un workflow skipeado deja los required checks
en pending para siempre y bloquea todos los PRs). Tres jobs:

- **`Build`**: `go build ./...` y `go vet ./...`.
- **`Lint`**: `make lint` → golangci-lint **v2.13.2** (versión pineada en el
  `Makefile`; no hay `.golangci.yml`, corre el set por defecto).
- **`Test`**: `go test -race -covermode=atomic -coverprofile=… ./...` (suite
  completo, sin `-short`), previa instalación de **fd** en el runner: el
  scanner prefiere `fd --hidden` y los tests de scanner asumen que existe
  (`make test` local también lo da por supuesto).

Reglas de la rama `main` (ruleset **`protect-main`**, reproducible con
`scripts/setup-repo-protection.sh`):

- Merge **solo vía PR**, con los tres checks en verde; force-push y borrado de
  `main` bloqueados.
- Existe **bypass de admin** y es **deliberado** (aprobado por el usuario): un
  admin *podría* pushear directo, pero la intención de trabajo es siempre el
  camino PR. Ningún actor no-admin puede hacerlo.
- `delete_branch_on_merge=true`: GitHub borra la rama remota al mergear.

Ante un merge: verificar que el workflow `push` de `main` quedó verde y que el
badge del README reporta `passing` (el badge cachea unos segundos).
