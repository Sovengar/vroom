# Proposal: Agrupación jerárquica primary_group/secondary_group

## Intent

La agrupación actual (0001 R10) es plana: un solo eje `group`. El caso de uso
real es tener un proyecto (p. ej. `vsocial`) y dentro agrupar por área
(backend, frontend, infra). Hoy eso obliga a fragmentar en grupos sueltos sin
jerarquía. Este cambio replica en vroom la agrupación jerárquica de gitdash
(0003-feature-nested-groups): dos niveles de headers plegables —primario y
secundario— conservando el patrón vroom ya validado (bloque contiguo en la
posición del primer miembro, R24).

Cambio: `0006-feature-nested-groups`
Base: 0001 (reemplaza R10), 0002 (extiende R24); numeración continúa desde 0005 (última R35)

## Scope

### In Scope
- Manifiesto: `group` se **reemplaza** (duro, sin alias de compatibilidad) por
  `primary_group` y `secondary_group` (strings opcionales, default `""`).
  Campos `Manifest.PrimaryGroup`/`SecondaryGroup`.
- `state.Meta`: se elimina el campo `Group` de meta.json (nada lo lee; lo
  escribía `startCmd` desde `Manifest.Group`).
- `internal/group`: `Entry{Primary, Secondary, Project}`; `Arrange` a dos
  niveles; `IsGroupHeader` se sustituye por `IsPrimaryHeader`/
  `IsSecondaryHeader`.
- TUI (`projectlist.go` + `app.go`): árbol con tres tipos de fila (header
  primario / header secundario / proyecto), skip de dos niveles en el
  plegado, header secundario indentado, claves compuestas de plegado.
- Interacciones de grupo (0002 R24) extendidas a los dos niveles: selección,
  `enter` de plegado, `s` toggle y panel de detalles funcionan sobre headers
  primarios y secundarios.
- Migración de fixtures y tests: `group_test`, `app_test` (~R24),
  `manifest_test`, `scanner_test` (usa `Manifest.Group`) y `state_test`
  (`Meta.Group`).
- `playground/`: migrar los 8 `.vroom.toml` a las claves nuevas y configurar
  primarios con varios secundarios para verificar el anidado a ojo.
- `README.md`: tabla del manifiesto (línea `group = ""`) migrada.

### Out of Scope
- Más de dos niveles de anidación.
- Sección `(ungrouped)` de gitdash: vroom NO la usa; los proyectos sin
  primario siguen inline como hoy (R10).
- Búsqueda/filtro de proyectos: vroom no tiene filtro hoy (gitdash S18.5 no
  aplica); si se añade, deberá matchear el secondary.
- Persistencia del estado plegado entre sesiones (sigue siendo un map en
  memoria).
- Orden custom de grupos (sigue "posición del primer miembro").
- Acciones grupales nuevas (las existentes `s`/`enter` se extienden tal cual).

## Capabilities

### New Capabilities
- `nested-groups`: agrupación jerárquica de dos niveles con headers
  plegables (primario/secondary) sobre el patrón de bloques contiguos.

### Modified Capabilities
- `grouping` (0001 R10): la clave `group` desaparece; el eje pasa a ser
  `primary_group` (+ `secondary_group` opcional).
- `groups-collapsible` (0002 R24): headers seleccionables/colapsables en dos
  niveles con skip anidado y conteo del primario que suma secundarios.
- `manifest-schema` (0001 R1): claves `primary_group`/`secondary_group`
  sustituyen a `group`.

## Approach

### Decisiones

- **Reemplazo duro, sin alias** (decisión del usuario): un manifiesto que
  solo defina `group` no agrupa (los campos desconocidos ya se ignoran).
  Los fixtures/playground/tests migran a las claves nuevas.
- **Arrange recursivo con el algoritmo actual**: el mismo invariante
  "el bloque se emite completo en la posición de su primer miembro" se aplica
  dos veces — nivel primario sobre `Primary`, y dentro de cada bloque
  primario, nivel secundario sobre `Secondary`. Los miembros con primario
  pero sin secundario conservan su posición dentro del primario (sin
  pseudo-header `(general)`). Así no hay lógica de posicionamiento nueva,
  solo la recursión.
- **Predicados de header**: `IsPrimaryHeader(i)` = `Primary != ""` y cambia
  el primario respecto de `i-1`; `IsSecondaryHeader(i)` = `Secondary != ""`
  y (cambia el primario O el secundario respecto de `i-1`).
- **Conteo como hoy (R24), no como gitdash**: vroom muestra el conteo
  `(running/total)` al colapsar (`▸ nombre (3/8)`). Se conserva esa
  convención en ambos niveles; el `total` del primario incluye a todos sus
  secundarios. Gitdash mostraba `(n)` siempre visible; desviación
  documentada en Revisiones.
- **Tecla de plegado `enter`, no `tab`**: en vroom la interacción R24 es
  `enter` (y `tab` cicla pestañas Console/Threads). La decisión de gitdash
  "plegar sobre un proyecto el contenedor más interno" se aplica con
  `enter`: sobre un proyecto alterna su secundario si tiene, si no su
  primario; sobre un header alterna su propio nivel.
- **Claves de plegado sin colisión**: primario = su nombre; secundario =
  `primario/secundario` (compuesta) en el map `collapsed`. Plegar un
  primario oculta también sus headers secundarios (skip de dos niveles en
  `buildTree`).
- **meta.json pierde `group`**: `Meta.Group` solo se escribía, nunca se leía;
  se elimina junto con el campo del manifiesto. Los meta.json viejos
  simplemente dejan de llevar la clave (no hay versionado de meta.json y el
  campo se reescribe en cada start).
- **Playground**: `apps/*` → `primary_group = "tienda"` con
  `secondary_group = "backend"` (products/orders/billing), `"frontend"`
  (web) y sin secundario (inventory); `servers/*` →
  `primary_group = "servers"` con secundarios `go`/`python`;
  `infra/nginx-proxy` → solo `primary_group = "infra"`. Cubre los tres
  casos: anidado completo, mezcla con/sin secundario y un solo nivel.
