# Proposal: Filtro de árbol con "/" (barra inline)

## Intent

Con muchos proyectos el árbol deja de ser navegable a ojo: hay que recorrerlo
con `j/k` para encontrar un repo. Este cambio añade un filtro estilo fzf: la
tecla `/` abre una barra de texto en la primera línea de la columna del árbol
y el árbol se filtra en vivo mientras se escribe. El match es substring
case-insensitive contra el nombre del proyecto Y los nombres de grupo
(primary/secondary), de modo que filtrar `backend` muestra todos los proyectos
de ese secundario (lo que la spec 0006 ya anticipaba en Out of Scope).

Cambio: `0007-feature-tree-filter`
Base: 0002 (extiende R18 render del árbol; modifica R30 esc), 0006 (opera sobre
el árbol anidado R36-R39); numeración continúa desde 0006 (última R39). Incluye
el fix del render de `treeTop` (R44): hoy el render dibuja siempre `tree[0:]`
ignorando la ventana de scroll que `navigate` mantiene (S18.5), así que con
listas largas el cursor sale de pantalla.

## Scope

### In Scope
- `internal/tui/app.go`: estado del filtro (`filterOpen`, `filterInput`,
  `filterText`), tecla `/`, manejo de teclas del box (`filterKey`),
  `applyFilter` (re-filtra proyectos → `group.Arrange` → `buildTree`),
  `filterMatch`, ruta de `esc` con filtro aplicado, help.
- `internal/tui/dashboard.go`: barra inline en la primera línea de la columna
  del árbol (input al abrir, indicador `⌕ texto · n` con filtro aplicado),
  línea `no matches` cuando el árbol queda vacío filtrando, render rebanado
  por `treeTop`, alto visible del árbol (`treeVis`) que la `navigate` y el
  clamp de resize usan.
- `internal/tui/projectlist.go`: `filterMatch` (predicado puro).
- Tests (`app_test.go`): fixture con secundario, ciclo completo del filtro.
- `README.md`: fila `/` en Keybindings.

### Out of Scope
- Navegación con flechas/j/k mientras el box está abierto (primero `enter`).
- Persistencia del filtro entre sesiones (memoria, como `collapsed`).
- Match sobre otros campos (puerto, comando, rama, ruta).
- Filtro en la consola o en threads (solo el árbol).

## Capabilities

### New Capabilities
- `tree-filter`: barra inline de filtrado en vivo del árbol con `/`, match
  por nombre y grupos, ciclo enter aplica / esc limpia.

### Modified Capabilities
- `services-dashboard` (0002 R18): la columna de árbol reserva su primera
  línea para la barra cuando el filtro está abierto o aplicado (el árbol
  pasa a ocupar `bodyH-1`).
- `quit-esc` (0002 R30): con filtro aplicado, `esc` limpia el filtro y sale
  del modo filtro en lugar de cerrar la TUI; sin filtro, `esc` sigue saliendo.
- `tree-scroll` (0002 S18.5): el render pasa a rebanar por `treeTop` (fix);
  la ventana visible ahora es `treeVis()` (respeta la barra).

## Approach

### Decisiones

- **Barra inline, no modal** (decisión del usuario): la barra vive en la
  primera línea de la columna del árbol (ancho 30), estilo fzf; el resto del
  dashboard no cambia de layout. El input usa `textinput` de bubbles v2
  (prompt `/`, placeholder `filter…`), ya usada vía textarea en el ask.
- **Match por nombre + grupos** (decisión del usuario): substring
  case-insensitive contra `p.Name`, `group.PrimaryOf(p)` y
  `group.SecondaryOf(p)`. Filtrar un nombre de grupo trae todos sus miembros,
  aunque el texto no aparezca en ningún nombre de proyecto.
- **Re-arrange, no recorte del árbol**: `applyFilter` re-ejecuta
  `group.Arrange` sobre los proyectos filtrados y `buildTree` encima. Así los
  headers de grupo solo aparecen si tienen miembros que matchean y no hay que
  duplicar lógica de ocultado en `buildTree`.
- **Filtrado en vivo, cierre explícito**: cada tecla recalcula el árbol
  (sin debounce: el Arrange es O(n) sobre decenas de proyectos). `enter`
  cierra el box conservando el filtro; `esc` cierra limpiando (árbol
  completo). Con el filtro aplicado, la barra sigue visible en dim con
  `⌕ texto · n` (n = proyectos matcheados) para que el árbol recortado
  siempre se explique a sí mismo.
- **esc contextual**: dentro del box limpia y cierra; con filtro aplicado
  limpia y sale del modo filtro (NO cierra la app: sería facilísimo cerrar
  vroom sin querer tras filtrar); sin filtro activo, esc sale como hoy (R30).
- **Cursor a 0 al filtrar**: tras cada `applyFilter` el cursor y `treeTop`
  vuelven al inicio (el árbol cambia de composición; conservar el índice
  apuntaría a otra fila).
- **Fix `treeTop` incluido** (decisión del usuario): el render pasa a dibujar
  `tree[treeTop:]` — hasta hoy dibujaba siempre desde 0 y el auto-scroll de
  S18.5 no tenía efecto visual. Es requisito de facto del filtro: la ventana
  visible cambia (`bodyH-1` con barra) y hay que rebanar coherentemente.

## Impact

- Sin cambios de datos ni de schema (`meta.json`, manifiesto, config: nada).
- Sin comandos/teclas nuevos salvo `/`. Las demás teclas se comportan igual;
  solo `esc` gana un significado contextual.
- Riesgo bajo: todo el efecto se limita a recomponer `entries`/`tree` y al
  render de la primera línea de la columna del árbol.
