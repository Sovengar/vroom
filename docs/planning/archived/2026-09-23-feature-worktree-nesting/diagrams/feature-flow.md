# Feature flow — 0011 Worktrees de git anidados

Flujo de comportamiento derivado de `behavior.feature`. Muestra el ciclo
scan → anidado → operación, y los dos caminos de ambigüedad (CLI y stacks).

```mermaid
flowchart TD
    Start([scan root]) --> Manifest["walk/fd: buscar .vroom.toml"]
    Manifest --> Projects["[]scanner.Project plano"]
    Projects --> RepoCheck{"¿dir es repo<br/>con .git?"}
    RepoCheck -->|sí| List["git worktree list --porcelain"]
    RepoCheck -->|no| BareCheck{"¿bare?<br/>HEAD+objects/+refs/<br/>sin .git"}
    BareCheck -->|sí| Bare["fila contenedora<br/>(no ejecutable)"]
    BareCheck -->|no| Plain["proyecto normal"]

    List --> GitOK{"¿git disponible<br/>y salida válida?"}
    GitOK -->|no| Degrade["repo sin hijos<br/>+ WorktreeErr<br/>(no crashea)"]
    GitOK -->|sí| Annotate["anotar: RepoRoot /<br/>IsWorktree / IsBareContainer"]
    Annotate --> BuildTree

    Plain --> BuildTree["TUI buildTree<br/>(group.Arrange intacto)"]
    Bare --> BuildTree
    Degrade --> BuildTree

    BuildTree --> RepoRow["1 fila de repo<br/>colapsada por defecto"]
    RepoRow --> Expand{"enter sobre la fila"}
    Expand -->|expandir| Children["worktrees indentados<br/>bajo la fila del repo"]
    Expand -->|colapsar| RepoRow
    Children --> Operate["start/stop/build/install<br/>sobre el worktree"]

    RepoRow -.->|option B| Group["conserva primary_group<br/>y posición; grupos de<br/>worktrees inertes"]

    Children --> CLI{"CLI: nombre<br/>duplicado?"}
    CLI -->|no| NameRes["resuelve por nombre"]
    CLI -->|sí, sin path| AmbErr["error: ambiguous<br/>+ paths + sugerir --path"]
    CLI -->|sí, con path| PathRes["resuelve por path<br/>(posicional o --path)"]

    Children --> Stack{"stack referencia<br/>nombre duplicado?"}
    Stack -->|no| StackOK["resolución determinista"]
    Stack -->|sí| StackErr["error explícito<br/>paths ordenados<br/>(sin last-wins)"]

    Operate --> State["estado por path<br/>(PathKey, sin migración)"]
    NameRes --> State
    PathRes --> State
```

## Notas de comportamiento

- **Colapsado por defecto**: a diferencia de los grupos (expandidos), las filas
  de repo nacen colapsadas; el estado se persiste con clave con namespace propio.
- **Option B**: la fila del repo mantiene su grupo y posición; un worktree que
  declare su propio `primary_group` **no** se reposiciona.
- **Degradación**: la ausencia de git no oculta proyectos ni rompe el scan.
- **Ambigüedad**: el nombre deja de ser identidad; el path es el direccionador
  canónico, tanto en CLI como en la resolución de stacks.
