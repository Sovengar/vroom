# behavior.feature — Worktrees de git anidados (0011)
#
# Fuente única del comportamiento esperado. Es para VERIFICACIÓN HUMANA:
# no está cableado a ningún runner ni tiene step definitions. El executor
# lo usará como fuente de comportamiento para tests BDD/ATDD reales.
#
# Nota de interpretación (a confirmar en el checkpoint behavior): la
# agrupación (option B) se preserva para la FILA DEL REPO (el main
# checkout conserva su primary_group/secondary_group y su posición).
# Los worktrees cuelgan indentados de esa fila y sus propios grupos son
# inertes para el posicionamiento (no se introduce un nuevo eje). Si el
# usuario quiere que un worktree se agrupe por su cuenta, este escenario
# debe iterarse.

Feature: Worktrees de git anidados en el árbol de proyectos

  Background:
    Given un scan root con proyectos .vroom.toml
    And el repositorio en /repo tiene su main checkout y worktrees registrados

  # ------------------------------------------------------------------
  # 1. Fila de repo: una sola fila, colapsada por defecto
  # ------------------------------------------------------------------

  Scenario: Repo con worktrees se muestra como una sola fila colapsada
    Given el repo /repo tiene worktrees /repo-wt/a y /repo-wt/b
    When se construye el árbol de proyectos
    Then /repo aparece como UNA sola fila
    And los worktrees /repo-wt/a y /repo-wt/b NO aparecen como filas top-level
    And la fila de /repo está colapsada por defecto

  Scenario: Repo sin worktrees no es expandible
    Given el repo /solo no tiene worktrees
    When se construye el árbol de proyectos
    Then la fila de /solo se muestra como un proyecto normal
    And no ofrece toggle de expansión

  Scenario: Repo normal: la fila del repo es el main checkout ejecutable
    Given el repo /repo tiene un .vroom.toml válido en su main checkout
    When el cursor está sobre la fila de /repo
    Then start/stop/build/install operan sobre el main checkout
    And el comportamiento es idéntico al de hoy

  # ------------------------------------------------------------------
  # 2. Expandir y operar worktrees anidados
  # ------------------------------------------------------------------

  Scenario: Expandir la fila del repo revela los worktrees indentados
    Given el repo /repo tiene worktrees /repo-wt/a y /repo-wt/b
    And la fila de /repo está colapsada
    When se pulsa enter sobre la fila de /repo
    Then /repo-wt/a y /repo-wt/b se muestran indentados bajo la fila de /repo
    And cada worktree aparece como una fila de proyecto

  Scenario: Un worktree anidado es operable como cualquier proyecto
    Given el worktree /repo-wt/a tiene su propio .vroom.toml
    And la fila de /repo está expandida
    When el cursor está sobre /repo-wt/a y se pulsa start
    Then el servicio se arranca con workdir /repo-wt/a
    And stop/build/install operan sobre /repo-wt/a

  Scenario: El estado de colapso por repo se persiste y se restaura
    Given el repo /repo estaba expandido al cerrar la TUI
    When se reabre la TUI
    Then la fila de /repo se muestra expandida
    And el estado de colapso del repo no colisiona con el de los grupos

  # ------------------------------------------------------------------
  # 3. Bare repos
  # ------------------------------------------------------------------

  Scenario: Bare repo se detecta durante el scan y se muestra como contenedor
    Given un directorio /bare con HEAD + objects/ + refs/ y sin .git
    When se escanea el root
    Then /bare aparece como fila contenedora
    And la fila contenedora no es ejecutable
    And no tiene manifiesto

  Scenario: Bare repo con worktrees los anida al expandir
    Given el bare repo /bare tiene worktrees /bare-wt/a
    When se expande la fila de /bare
    Then /bare-wt/a se muestra indentado bajo /bare
    And /bare-wt/a es operable

  Scenario: Un directorio que no es bare repo no se confunde con contenedor
    Given un directorio /normal con .git (dir o file)
    When se escanea el root
    Then /normal NO se trata como bare repo

  # ------------------------------------------------------------------
  # 4. Variantes de worktree
  # ------------------------------------------------------------------

  Scenario: Worktree detached se muestra con su sha
    Given un worktree detached /repo-wt/det
    When se expande la fila del repo
    Then /repo-wt/det se muestra con nombre basename
    And su rama se muestra como "<sha> (detached)"

  Scenario: Worktree sin manifiesto se lista como no configurado
    Given el worktree /repo-wt/sin-mf no tiene .vroom.toml
    When se expande la fila del repo
    Then /repo-wt/sin-mf se lista como no configurado (⚠)
    And no es operable

  Scenario: Worktree prunable o ausente se omite
    Given git reporta /repo-wt/gone como prunable
    When se construye el árbol
    Then /repo-wt/gone no se muestra como fila operable

  Scenario: Un submodule no se trata como worktree
    Given un directorio con .git file que apunta a modules/...
    When se descubre la topología
    Then no se anida como worktree de su padre

  Scenario: Worktrees fuera del scan root se anidan igual
    Given el main checkout /repo está fuera del root pero su worktree /root/repo-wt/a está dentro
    When se construye el árbol
    Then /root/repo-wt/a se anida bajo una fila contenedora sintetizada de /repo

  # ------------------------------------------------------------------
  # 5. Agrupación (option B) preservada
  # ------------------------------------------------------------------

  Scenario: El repo conserva su grupo exactamente como hoy
    Given el main checkout de /repo declara primary_group "X"
    When se construye el árbol
    Then la fila de /repo aparece en el bloque "X" como hoy
    And el toggle de worktrees vive en esa fila
    And no se introduce un nuevo eje de agrupación

  Scenario: El grupo propio de un worktree no lo reposiciona
    Given el worktree /repo-wt/a declara primary_group "Y"
    When se construye el árbol
    Then /repo-wt/a aparece anidado bajo /repo
    And NO se emite en el bloque "Y"

  # ------------------------------------------------------------------
  # 6. Degradación: git ausente o fallo
  # ------------------------------------------------------------------

  Scenario: Git ausente degrada sin romper el scan
    Given el binario git no está disponible
    When se escanea el root
    Then los repos se muestran sin hijos
    And se notifica el motivo de la degradación
    And ningún proyecto se oculta ni la TUI crashea

  Scenario: git worktree list con salida inválida se trata como sin worktrees
    Given `git worktree list --porcelain` devuelve salida malformada o exit != 0
    When se escanea el root
    Then el repo se muestra sin hijos
    And se registra el error de topología en la fila

  # ------------------------------------------------------------------
  # 7. CLI: direccionamiento por nombre y por path
  # ------------------------------------------------------------------

  Scenario: Nombre único resuelve como hoy
    Given un solo proyecto con manifest name "api"
    When se ejecuta `vroom start api`
    Then se arranca ese proyecto

  Scenario: Nombre duplicado sin path falla con paths accionables
    Given los worktrees /repo-wt/a y /repo-wt/b comparten manifest name "api"
    When se ejecuta `vroom start api` sin path
    Then devuelve error `ambiguous project name "api": found in <paths>`
    And el mensaje lista los paths candidatos
    And sugiere direccionar por path (--path)

  Scenario: Direccionamiento por path posicional
    Given los worktrees /repo-wt/a y /repo-wt/b comparten manifest name "api"
    When se ejecuta `vroom start /repo-wt/a`
    Then se arranca el proyecto en /repo-wt/a
    And no hay ambigüedad

  Scenario: Direccionamiento por flag --path
    Given los worktrees /repo-wt/a y /repo-wt/b comparten manifest name "api"
    When se ejecuta `vroom start api --path /repo-wt/b`
    Then se arranca el proyecto en /repo-wt/b

  Scenario: Un path no escaneado no resuelve
    Given un path que no pertenece a ningún proyecto escaneado
    When se ejecuta `vroom start /no/existe`
    Then devuelve `project not found`

  Scenario: vroom list expone la relación repo/worktree
    Given un repo con worktrees
    When se ejecuta `vroom list`
    Then cada worktree incluye repo_root, is_worktree y (si aplica) bare_container
    And el array de proyectos sigue siendo plano (back-compat)

  # ------------------------------------------------------------------
  # 8. Stacks de orquestación deterministas
  # ------------------------------------------------------------------

  Scenario: Stack con nombre de servicio único resuelve determinísticamente
    Given un stack que referencia "api" y solo un proyecto lo declara
    When se lanza el stack
    Then se arranca exactamente ese proyecto

  Scenario: Stack con nombre duplicado falla explícitamente
    Given dos proyectos comparten manifest name "api"
    When un stack referencia "api"
    Then `ResolveServices` devuelve un error explícito
    And el error lista los paths en orden estable
    And NO hay last-wins silencioso

  Scenario: La TUI y el CLI coinciden en la resolución de stacks
    Given dos proyectos comparten manifest name "api"
    When la TUI calcula el estado de un stack que referencia "api"
    Then la TUI reporta el mismo conflicto que el CLI
    And no elige arbitrariamente el primero
