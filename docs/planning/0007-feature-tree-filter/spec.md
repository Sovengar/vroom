# Spec: Filtro de árbol con "/" (barra inline)

Cambio: `0007-feature-tree-filter`
Base: 0002 (modifica R18/R30; extiende S18.5), 0006 (opera sobre el árbol
R36-R39); numeración continúa desde 0006 (última R39)

## Requirements

### R40: Tecla "/" abre el modo filtro (barra inline)

Al pulsar `/` la TUI SHALL abrir el modo filtro: una barra de texto en la
PRIMERA línea de la columna del árbol (ancho 30), con prompt `/` y placeholder
`filter…`. Mientras la barra está abierta (`filterOpen`):

- Las teclas imprimibles (incluidas `q`, `s`, `b`, `/`, `1`, `2`) SHALL
  insertarse en el input; la TUI SHALL NO ejecutar sus acciones globales.
- Cada cambio del texto SHALL recalcular el árbol en vivo (sin confirmar).
- `ctrl+c` SHALL seguir saliendo de la TUI.
- El cursor del árbol SHALL resetearse a 0 en cada cambio de filtro.

Reabrir con `/` tras aplicar un filtro SHALL conservar el texto previo.

#### S40.1: abrir con "/"

- GIVEN la TUI con el árbol completo
- WHEN se pulsa `/`
- THEN `filterOpen=true`, la barra aparece en la línea 1 del árbol con el
  placeholder y el filtro está vacío (árbol intacto)

#### S40.2: las teclas van al input

- GIVEN el modo filtro abierto
- WHEN se pulsa `q`
- THEN la TUI NO sale; el input contiene `q`

#### S40.3: filtrado en vivo

- GIVEN el modo filtro abierto
- WHEN se teclea `tienda`
- THEN el árbol se recalcula tras cada tecla y solo contiene los proyectos
  que matchean (tienda-api, tienda-web) con su header

#### S40.4: reabrir conserva el texto

- GIVEN un filtro `tienda` aplicado (box cerrado)
- WHEN se pulsa `/`
- THEN el box se abre con el input prellenado `tienda`

### R41: Criterio de match y recomposición del árbol

El texto del filtro SHALL matchear (substring, case-insensitive) contra el
nombre del proyecto, su `primary_group` y su `secondary_group`. Sobre los
proyectos que matchean SHALL re-ejecutarse `group.Arrange` (patrón 0006 R37)
y `buildTree`: los headers de grupo SHALL aparecer SOLO si tienen miembros
que matchean. Si ningún proyecto matchea, el árbol SHALL quedar vacío y la
columna SHALL mostrar una línea `no matches` en dim.

#### S41.1: match por nombre, case-insensitive

- GIVEN los proyectos tienda-api, tienda-web y suelto
- WHEN se filtra por `TIENDA`
- THEN matchean tienda-api y tienda-web (no suelto)

#### S41.2: match por primary_group

- GIVEN tienda-api/tienda-web con `primary_group = "tienda"`
- WHEN se filtra por `tiend`
- THEN matchean ambos (por nombre y por grupo)

#### S41.3: match por secondary_group

- GIVEN tienda-web con `primary_group = "tienda"` y
  `secondary_group = "frontend"`, y tienda-api solo con primary
- WHEN se filtra por `front`
- THEN SOLO matchea tienda-web (el texto no aparece en ningún nombre ni en
  el primario); el árbol muestra `tienda ▸ frontend ▸ tienda-web`

#### S41.4: headers solo con miembros que matchean

- GIVEN el fixture con secundario frontend
- WHEN se filtra por `api`
- THEN SOLO matchea tienda-api; el header secundario frontend NO aparece
  (quedó sin miembros)

#### S41.5: sin matches

- GIVEN el árbol completo
- WHEN se filtra por `zzz`
- THEN el árbol queda vacío y la columna muestra `no matches`

### R42: Ciclo de cierre y teclas con filtro aplicado

- `enter` con el box abierto SHALL cerrarlo manteniendo el filtro actual
  (texto no vacío) o simplemente cerrarlo si está vacío.
- `esc` con el box abierto SHALL cerrarlo LIMPIANDO el filtro: el árbol
  vuelve al estado completo.
- Con filtro aplicado y box cerrado:
  - `esc` SHALL limpiar el filtro (árbol completo) y NO SHALL salir de la
    TUI.
  - `q` y el resto de atajos (`s`, `b`, `i`, `t`, `a`, `1`, `2`, `tab`,
    `j`/`k`, `enter`) SHALL seguir operando, ahora sobre el árbol filtrado.
- Sin filtro activo, `esc` SHALL salir de la TUI (0002 R30, sin cambios).

#### S42.1: enter aplica y cierra

- GIVEN el box abierto con texto `tienda` y el árbol ya filtrado en vivo
- WHEN se pulsa `enter`
- THEN `filterOpen=false`, el árbol sigue filtrado y la barra muestra el
  indicador persistente

#### S42.2: esc dentro del box limpia

- GIVEN el box abierto con texto `tienda`
- WHEN se pulsa `esc`
- THEN el box se cierra, `filterText` queda vacío y el árbol vuelve a las
  3 filas completas (grupo tienda + 2 miembros + suelto)

#### S42.3: esc con filtro aplicado no sale

- GIVEN filtro `tienda` aplicado (box cerrado)
- WHEN se pulsa `esc`
- THEN el filtro se limpia (árbol completo) y la TUI NO emite `tea.Quit`

#### S42.4: esc sin filtro sale (sin cambios)

- GIVEN sin filtro activo
- WHEN se pulsa `esc`
- THEN la TUI emite `tea.Quit` (0002 R30)

### R43: Indicador persistente y reset del cursor

Con el filtro aplicado (box cerrado, texto no vacío) la primera línea de la
columna del árbol SHALL mostrar `⌕ <texto> · <n>` en dim (n = nº de
proyectos matcheados), truncado al ancho de la columna. En cada
recálculo del filtro (abrir/teclear/limpiar) el cursor del árbol y `treeTop`
SHALL resetearse a 0.

#### S43.1: indicador con conteo

- GIVEN filtro `tienda` aplicado con 2 matches
- WHEN se renderiza
- THEN la línea 1 del árbol contiene `⌕ tienda · 2`

#### S43.2: cursor reset al filtrar

- GIVEN el cursor sobre la última fila del árbol
- WHEN se aplica un filtro
- THEN `cursor=0` y `treeTop=0`

### R44: Render del árbol respeta treeTop (fix) y alto con barra

El render de la columna de árbol SHALL dibujar las líneas desde `treeTop`
(fix: hoy dibuja siempre desde 0 y el auto-scroll de S18.5 no tiene efecto
visual). Cuando la barra de filtro es visible (box abierto o filtro
aplicado) el árbol visible pasa a ocupar `bodyH-1` líneas: la ventana de
scroll (`navigate`, clamp de resize) SHALL usar ese alto (`treeVis`).

#### S44.1: render rebanado por treeTop

- GIVEN un árbol más alto que el cuerpo y el cursor movido al final
  (treeTop > 0)
- WHEN se renderiza
- THEN la primera línea del árbol visible es la fila treeTop del árbol (la
  fila del cursor entra en la ventana)

#### S44.2: la barra consume una línea del árbol

- GIVEN la barra visible
- WHEN se renderiza
- THEN la línea 0 del árbol es la barra y las filas del árbol se dibujan a
  partir de la línea 1 (alto visible bodyH-1)

#### S44.3: sin barra el layout no cambia

- GIVEN sin filtro abierto ni aplicado
- WHEN se renderiza
- THEN el árbol ocupa bodyH líneas como hoy (R18)
