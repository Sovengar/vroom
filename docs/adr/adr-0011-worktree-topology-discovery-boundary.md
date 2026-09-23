# ADR-0011 — Límite del descubrimiento de topología de worktrees

- Estado: aceptada
- Fecha: 2026-09-23
- Feature: `0011-feature-worktree-nesting`

## Contexto

Hasta ahora `internal/gitinfo` leía la rama de un proyecto **solo de disco**
(parseando `HEAD`), sin spawnar el binario git. Ese principio daba lecturas
instantáneas y testeables, y cubría repos normales y worktrees.

Para mostrar worktrees anidados y bare repos hace falta saber **qué
worktrees registra un repo** y **cuál es su main checkout**. Esa información
no está en el disco del proyecto: solo la conoce `git worktree list`. Además,
un bare repo no tiene `.vroom.toml`, por lo que el escaneo por manifiestos
nunca lo encontraría.

Esto rompe el principio de `gitinfo` e introduce una dependencia de entorno
(el binario git) y un coste de proceso en el camino de scan.

## Decisión

1. **Capa dedicada `internal/worktree`.** Es el único punto del proyecto
   autorizado a spawnar git. Expone datos planos: `List(dir)` (parser puro de
   `git worktree list --porcelain`) e `IsBareRepo(dir)` (heurística). `gitinfo`
   **no se modifica** y sigue siendo disk-only.

2. **Contrato del scanner plano y anotado.** El scanner no construye ninguna
   estructura anidada: anota el `[]scanner.Project` existente con campos
   aditivos (`RepoRoot`, `IsWorktree`, `IsBareContainer`, `WorktreeErr`). Los
   consumidores (`group`, `tui`, `cli`, `orchestrate`) siguen recibiendo un
   slice plano. El anidado visual es una preocupación de presentación en
   `tui.buildTree`.

3. **Degradación por repo, no todo-o-nada.** `List` devuelve un sentinel
   (`ErrGitUnavailable`) si git falta, y error si git falla o la salida es
   inválida. La invocación se acota con timeout (`exec.CommandContext`) y está
   gated por la presencia de un repo git real. El scanner registra el motivo en
   `Project.WorktreeErr` y continúa: ningún proyecto se oculta ni la TUI cae.

4. **Heurística de bare repo reforzada.** Se exige la conjunción
   `HEAD` + `objects/` + `refs/`, la ausencia de `.git` y el marcador
   autoritativo `core.bare = true` que escriben `git init --bare` /
   `git clone --bare`.

5. **Bare-container y worktrees fuera del root.** El scanner sintetiza filas
   contenedoras sin manifiesto para bare repos y para worktrees in-root sin
   `.vroom.toml`. Un worktree cuyo main checkout está fuera del scan root se
   anota igual (`RepoRoot` fuera); la TUI sintetiza la fila contenedora al
   construir el árbol.

## Consecuencias

- Positivas: una sola frontera para la dependencia de git, testeable en
  aislamiento (parser y heurística puros); contrato plano preservado para todos
  los consumidores; degradación controlada.
- Negativas / tradeoffs: el scan ahora spawna git por repo (coste y dependencia
  de entorno). Mitigado por timeout, gating y degradación.
- Limitaciones documentadas: un worktree prunable/ausente o agregado durante la
  sesión no se refresca hasta el próximo scan. La topología se descubre desde
  repos in-root; un worktree sin manifiesto cuyo repo no tiene ningún proyecto
  in-root no se detecta.

## Alternativas consideradas

- **Leer `.git` a mano para derivar el main checkout.** El `.git` file de un
  worktree apunta al gitdir, no al main checkout; reconstruir la relación
  requeriría parsear `commondir` y duplicaría lógica frágil de git.
- **Tipo anidado en el scanner (`Repo{Worktrees []Project}`).** Rompería a todos
  los consumidores y los mapas por path; se descartó por coste y acoplamiento.
- **Anidar dentro de `group.Arrange`.** La relación repo→worktree es ortogonal a
  `primary_group`/`secondary_group`; mezclarlas habría fusionado dos conceptos
  y roto la agrupación existente (option B).
