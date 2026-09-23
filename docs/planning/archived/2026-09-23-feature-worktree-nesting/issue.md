# Worktrees de git anidados en el árbol de proyectos

## User Story

Como desarrollador que trabaja con múltiples worktrees de un mismo repo, quiero
ver cada repo con sus worktrees como **una sola fila expandible** en la lista de
proyectos, para que los worktrees dejen de aparecer como entradas top-level
sueltas y no colisionen por `Manifest.Name` duplicado. Quiero poder operar cada
worktree (start/stop/build) igual que cualquier proyecto, y que los comandos por
nombre, las stacks de orquestación y el scanner no queden ambiguos cuando hay
nombres repetidos.

Estado actual: cada worktree aparece como entrada top-level separada, los `name`
duplicados producen `ambiguous project name "X"` en CLI y last-wins silencioso en
resolución de stacks.

## Alcance

### Qué entra

- Detección en el scanner de repos con worktrees y de **bare repos**.
- Fila de repo **expandible/colapsable**, colapsada por defecto.
- Anidado visual de los worktrees indentados bajo la fila del repo.
- Worktree operables como proyectos (start/stop/build).
- Desambiguación por nombre en CLI (`findProject`).
- Resolución determinista (o fallo explícito) de stacks con nombres duplicados.

### Qué NO entra

- Cambiar la semántica de agrupación (sigue "option B": `primary_group` sin
  cambios; el toggle vive en la fila del proyecto, sin nuevo eje de agrupación).
- Elegir un worktree "canónico" automáticamente por heurística/azar.
- Renombrar manifests.
- UI para crear/eliminar worktrees.

## Acceptance Criteria

- [ ] Un repo con worktrees se muestra como UNA fila expandible, colapsada por
      defecto.
- [ ] Repo normal: la fila es el main checkout (ejecutable: start/stop/build como
      hoy). Al expandir se listan sus worktrees indentados, cada uno operable.
- [ ] Bare repo: la fila es **contenedor** (no ejecutable, sin manifest). Al
      expandir se listan sus worktrees. Se detecta en el scan por heurística: el
      directorio tiene `HEAD` + `objects/` + `refs/` y no tiene `.git`.
- [ ] Los worktrees ya no aparecen como entradas top-level separadas.
- [ ] Opción B preservada: un proyecto con `primary_group` permanece en su grupo
      exactamente como hoy; el toggle de worktrees vive en su fila.
- [ ] Descubrimiento de worktrees vía `git worktree list --porcelain`
      (subprocess).
- [ ] `findProject` permite direccionar un worktree específico sin ambigüedad
      (path-based / desambiguación explícita); comportamiento definido y testeado.
- [ ] `ResolveServices` resuelve stacks de forma determinista o falla
      explícitamente ante nombres duplicados (sin last-wins silencioso).

## Decisiones Técnicas Cerradas

| Decisión | Valor | Justificación |
| --- | --- | --- |
| Modelo de presentación | Repo = una fila expandible; worktrees indentados; colapsado por defecto | Evita duplicación top-level y colisiones de nombre |
| Repo normal | La fila del repo es el main checkout, ejecutable | Preserva el comportamiento actual de start/stop/build |
| Bare repo | Fila contenedora, NO ejecutable, sin manifest | No hay working tree que operar |
| Detección de bare repo | Heurística: `HEAD` + `objects/` + `refs/` y sin `.git` | Detectable en scan sin depender de git |
| Descubrimiento de worktrees | `git worktree list --porcelain` (subprocess) | Listado autoritativo de git; tradeoff aceptado |
| Agrupación | Option B sin cambios; el toggle vive en la fila del proyecto | No introduce un nuevo eje de agrupación |
| CLI por nombre | Path-based / desambiguación explícita | Un `name` puede repetirse entre worktrees |
| Stacks | Resolución determinista o fallo explícito | Elimina last-wins arbitrario |

**Tradeoff asumido (subprocess):** `internal/gitinfo` evita deliberadamente
spawnear git. Esta feature lo rompe al invocar `git worktree list --porcelain`.
Se acepta por ser la fuente autoritativa, a costa de: dependencia del binario git
en runtime, coste de proceso hijo en el scan, y posible fallo si git falta.

## Riesgos

- **Subprocess de git en `internal/gitinfo`:** contradice la regla de no spawnear
  git. Impacto: latencia de scan y dependencia de entorno. Mitigación: invocación
  única por repo, timeout, y degradación controlada si git no está disponible.
- **Last-wins silencioso en stacks (`index[Manifest.Name] = p`):** un worktree
  arbitrario gana sin avisar. Impacto: arranque de servicios incorrectos.
  Mitigación: detección de duplicados y error explícito.
- **Ambigüedad CLI con nombres duplicados:** `ambiguous project name` ya ocurre
  hoy. Mitigación: direccionamiento por path.
- **Detección heurística de bare repo:** falsos positivos en directorios que
  casualmente tengan `HEAD` + `objects/` + `refs/`. Mitigación: validar
  conjunción completa y ausencia de `.git`.
- **Git binario ausente:** el scan de worktrees falla o queda vacío. Mitigación:
  error claro y repo mostrado sin hijos.

## Testing Considerations

**Unitarias**
- Parser de `git worktree list --porcelain` (incluye worktree detached).
- Detección de bare repo por heurística (positivo y negativos).
- Construcción del árbol repo → worktrees indentados.
- Desambiguación en `findProject` (nombre único vs duplicado, path explícito).
- `ResolveServices`: determinismo y error ante nombres duplicados.

**Integración**
- Scan de un repo normal con worktrees → una fila + hijos.
- Scan de un bare repo → fila contenedora + worktrees.
- start/stop/build sobre un worktree anidado.
- Stack que referencia un nombre duplicado → error explícito, no last-wins.

**Edge cases**
- Bare repo.
- Repo sin worktrees (fila no expandible / sin hijos).
- Worktree detached.
- Nombres duplicados entre worktrees.
- Paths de worktrees anidados fuera del root del scan.
- `git` binario ausente o no ejecutable.
