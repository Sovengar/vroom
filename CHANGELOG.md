# Changelog

Todos los cambios notables de este proyecto se documentan en este archivo.

El formato está basado en [Keep a Changelog](https://keepachangelog.com/es/1.0.0/),
y este proyecto sigue [Semantic Versioning](https://semver.org/lang/es/).

## [Unreleased]

### Added
- Anidar los worktrees de git bajo la fila de su repo: el repo se muestra como
  una única fila colapsable (colapsada por defecto) y sus worktrees aparecen
  indentados al expandir, cada uno operable como cualquier proyecto.
- Detectar bare repos durante el scan y mostrarlos como fila contenedora no
  ejecutable, con sus worktrees anidados.
- Direccionar proyectos por path en la CLI (posicional o `--path`) y exponer la
  relación repo/worktree (`repo_root`, `is_worktree`, `bare_container`) en la
  salida JSON de `vroom list`, manteniendo el array plano por compatibilidad.
- Resolver los stacks de orquestación de forma determinista: ante un
  `Manifest.Name` duplicado falla explícitamente listando los paths candidatos,
  en lugar de elegir arbitrariamente el último.

### Fixed
- Consultar los bare repos para descubrir sus worktrees (antes se omitían).
- Mostrar el error de topología de la fila de repo aunque el repo tenga hijos.
- No contar ni plegar los worktrees anidados al agregar el estado de un grupo.
- Detectar bare repos durante el scan sin un recorrido adicional del árbol.
- Consultar git una sola vez por repo en lugar de una vez por proyecto.
- Acotar el tiempo real de ejecución de git mediante un timeout efectivo.
- Un worktree marcado como prunable por git pero cuyo directorio sigue
  existiendo ya no se muestra como entrada top-level: se anida con normalidad.
- La fila de un repo sin worktrees ya no muestra el glifo de expansión.
- La detección de bare repos solo considera `core.bare` dentro de la sección
  `[core]` del config, evitando falsos positivos.
- Exponer el error de topología del repo (`worktree_error`) en el JSON de
  `vroom list`.
- Separar la salida de git de stderr, para que un warning no corrompa el
  parseo, e ignorar bloques de salida con ruta vacía.

### Changed
- El parseo de `--path` es predecible: acepta `--path <valor>` y
  `--path=<valor>`, y falla con un error claro ante valor ausente, vacío o
  duplicado (antes se ignoraba en silencio).
- Al direccionar un proyecto por path se resuelven los symlinks, de modo que
  un path enlazado apunta al proyecto correcto.
- La validación de stacks itera los nombres en orden estable, con mensajes y
  errores reproducibles.

### Removed
- Código muerto: `quickCheck`, `stacksEmitted`, el parámetro `services` de
  `StopStack` y el tipo `ServiceStatus`.

[Unreleased]: https://github.com/Sovengar/vroom/commits/HEAD
