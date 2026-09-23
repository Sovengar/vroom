# Process flow — planificación de 0011-feature-worktree-nesting

Flujo de planificación real para esta feature, con las decisiones tomadas.

```mermaid
flowchart TD
    WT["worktree feat/worktree-nesting<br/>base 0c09653"] --> Index["codebase-index/vroom<br/>codegraph: ready"]
    Index --> Issue["issue.md<br/>✅ aprobada"]
    Issue --> B{"brainstormer + architect<br/>(paralelo)"}
    B --> Model["modelo plano anotado<br/>NO tipo anidado"]
    B --> Seam["seam: internal/worktree<br/>git subprocess aislado"]
    B --> Nest["anidado en tui.buildTree<br/>group.Arrange intacto"]
    Model --> Behavior["behavior.feature<br/>23 escenarios<br/>✅ aprobada"]
    Seam --> Behavior
    Nest --> Behavior
    Behavior --> Interp["interpretación confirmada:<br/>fila repo conserva grupo;<br/>grupos de worktrees inertes"]
    Interp --> Plan["plan.md<br/>adr_required: true"]
    Plan --> ADR["ADR propuesto:<br/>adr-0011-worktree-<br/>topology-discovery-boundary<br/>(subprocess de git + degradación)"]
    Plan --> Ctx["context.md<br/>codebase-researcher<br/>freshness 0c09653<br/>factible, sin blocker"]
    Ctx --> Diagrams["diagrams/<br/>feature-flow + process-flow"]
    Diagrams --> Commit["commit docs/planning/0011-*"]
    Commit --> Executor["swe-executor"]

    Plan -.->|riesgos| R1["subprocess git<br/>(tradeoff asumido)"]
    Plan -.->|riesgos| R2["last-wins en stacks<br/>→ error explícito"]
    Plan -.->|riesgos| R3["TUI y CLI divergen<br/>→ criterio compartido"]
    Plan -.->|riesgos| R4["bare false-positive<br/>→ heurística reforzada"]
    Plan -.->|riesgos| R5["worktrees fuera de root<br/>→ fila contenedora sintética"]
```

## Decisiones registradas

| Decisión | Valor |
| --- | --- |
| Modelo | `[]scanner.Project` plano + anotaciones (`RepoRoot`, `IsWorktree`, `IsBareContainer`, `WorktreeErr`) |
| Descubrimiento | nuevo `internal/worktree`; `gitinfo` intacto; degradación por repo |
| Anidado | `tui.buildTree` + `itemRepo`; clave de colapso con namespace, default colapsado |
| Bare repo | heurística `HEAD` + `objects/` + `refs/` sin `.git`, + marcador de bare |
| CLI | path-first (posicional + `--path`); JSON aditivo y plano |
| Stacks | error explícito ante duplicados; criterio compartido TUI/CLI |
| ADR | `adr_required: true` — `adr-0011-worktree-topology-discovery-boundary` |

## Checkpoints

`issue` ✅ → `behavior` ✅ → `plan` ✅ → context pass ✅ → diagramas ✅ → commit
