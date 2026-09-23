# Plan — 0011 Worktrees de git anidados

`adr_required: true`
`adr_reason: se introduce una dependencia del binario git en el camino de scan (subprocess), rompiendo el principio "no spawnear git" de internal/gitinfo; requiere decidir el límite (paquete dedicado) y la política de degradación por repo.`
`adr_title: adr-0011-worktree-topology-discovery-boundary`

## Resultado esperado

Un repo con worktrees se ve como **una sola fila expandible** (colapsada por
defecto). El repo normal es el main checkout ejecutable; al expandir aparecen
sus worktrees indentados, cada uno operable. Un bare repo se detecta en el scan
y se muestra como fila **contenedora** no ejecutable. Los worktrees dejan de ser
entradas top-level. La agrupación existente (option B) no cambia: la fila del
repo conserva su grupo y posición; los grupos propios de un worktree son inertes.
Además, la CLI y las stacks dejan de ser ambiguas ante `Manifest.Name` repetido.

## Enfoque (alto nivel)

- **Descubrimiento**: una capa nueva dedicada a la topología de repos
  (`git worktree list --porcelain` + detección de bare repo), separada de
  `gitinfo` (que sigue leyendo HEAD solo de disco). El scanner la usa y **anota**
  los proyectos; nunca construye una estructura anidada.
- **Contrato del scanner**: se mantiene **plano** (`[]scanner.Project`) con
  anotaciones de relación repo/worktree/bare. Esto evita tocar a todos los
  consumidores (`group`, `tui`, `cli`, `orchestrate`), que siguen recibiendo un
  slice plano.
- **Anidado visual**: es una preocupación de presentación, resuelta al construir
  el árbol de la TUI. `group.Arrange` queda intacto; el toggle y la indentación
  viven en la fila del repo.
- **Degradación**: por repo, no todo-o-nada. Si git falta o falla, el repo se
  muestra sin hijos con un aviso; nunca se ocultan proyectos ni se cae la TUI.
- **CLI**: el path pasa a ser el direccionador canónico (el nombre se mantiene
  para el caso único). El error de ambigüedad ya existente se vuelve accionable.
- **Stacks**: la resolución deja de ser last-wins silencioso; ante duplicados
  falla de forma explícita y estable.

## Decisiones clave

1. **Modelo plano anotado, sin tipo anidado.** Se agregan campos de relación a
   `scanner.Project`; los worktrees siguen siendo proyectos normales (con su
   propio manifiesto) y el contenedor bare es una fila sintética sin manifiesto.
   Un tipo recursivo rompería todos los consumidores y los mapas por path.
2. **La topología vive en una capa propia.** Aísla la única dependencia del
   binario git, es testeable en aislamiento, y mantiene cohesiva la detección de
   forma de repo (bare vs normal vs worktree). `gitinfo` no se contamina.
3. **Anidado en la TUI, no en el agrupamiento.** La relación repo→worktrees es
   un eje ortogonal a `primary_group`/`secondary_group`; mezclarlos en `group`
   habría fusionado dos conceptos independientes y roto option B.
4. **Colapso por repo con namespace propio y default invertido.** El estado de
   colapso de repos no debe colisionar con las claves de grupo ni heredar su
   default (grupos expandidos por defecto; repos colapsados por defecto).
5. **Path-first en CLI.** El path es único; el nombre no. Se mantiene la
   búsqueda por nombre para el caso no ambiguo y se agrega direccionamiento
   explícito por path, con la metadata de relación expuesta en el JSON para que
   un consumidor (IA) pueda desambiguar sin adivinar.
6. **Duplicados en stacks = error explícito.** Con paths ordenados de forma
   estable. Es lo mínimo que cumple "determinista o falla ruidosamente" sin
   introducir sintaxis nueva en el compose file.
7. **Bare repo: heurística reforzada.** A la conjunción `HEAD` + `objects/` +
   `refs/` y ausencia de `.git` se recomienda sumar el marcador autoritativo que
   escribe `git init --bare` para eliminar falsos positivos baratos de detectar.

## Riesgos y mitigaciones

- **Subprocess de git en el scan (tradeoff asumido).** Rompe el principio de
  `gitinfo`; añade dependencia de entorno y coste de proceso. Mitigación:
  invocación acotada por repo, con timeout, gated por presencia de `.git`, y
  degradación controlada si git falta. Es la decisión que motiva el ADR.
- **Last-wins silencioso en stacks.** Un worktree arbitrario ganaba sin avisar.
  Mitigación: error explícito ante duplicados.
- **Misma clase de bug en la TUI.** El cálculo de estado de stack de la TUI
  también resuelve por primer match de nombre; si no se alinea con el engine,
  TUI y CLI discreparán. Mitigación: un único criterio de resolución compartido.
- **Falsos positivos de bare repo.** Mitigación: conjunción completa + marcador
  de bare + ausencia de `.git`.
- **Worktrees fuera del scan root.** El main checkout puede no estar entre los
  proyectos escaneados; hay que sintetizar la fila contenedora para no perder el
  anidado.
- **Ciclo de vida del worktree.** Un worktree prunable/ausente o agregado durante
  la sesión no se refresca hasta el próximo scan; se documenta como limitación.
- **Back-compat del JSON de `vroom list`.** El array debe seguir siendo plano;
  los campos de relación son aditivos.

## Orden de trabajo (grueso)

1. Capa de descubrimiento de topología + detección de bare repo (con su
   degradación y tests unitarios de parser/heurística).
2. Scanner: anotaciones de relación y ensamblado del slice plano (incluida la
   fila contenedora sintética).
3. TUI: anidado, toggle, indentación y clave de colapso por repo; preservar
   `group.Arrange` y el render de grupos.
4. CLI: direccionamiento por path + metadata en el JSON + error accionable.
5. Orquestación: resolución determinista/explícita y alineación de la TUI.
6. Documentación/ADR y verificación end-to-end.

## Fuera de alcance

Cambiar la semántica de agrupación, elegir un worktree canónico por heurística,
renombrar manifiestos, y UI para crear/eliminar worktrees.
