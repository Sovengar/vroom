# Spec: Agrupación jerárquica primary_group/secondary_group

Cambio: `0006-feature-nested-groups`
Base: 0001 (reemplaza R10; extiende R1/R5), 0002 (extiende R24); numeración
continúa desde 0005 (última R35)

## Requirements

### R36: Claves del manifiesto primary_group/secondary_group

El manifiesto `.vroom.toml` SHALL aceptar `primary_group` y `secondary_group`
(string, opcionales, default `""`). La clave `group` SHALL desaparecer: un
manifiesto que solo defina `group` SHALL NO agrupar (reemplazo duro, sin
alias de compatibilidad). La semántica SHALL ser:

| primary_group | secondary_group | efecto |
|---|---|---|
| definido | definido | dos niveles de headers |
| definido | vacío | un nivel: proyectos directos bajo el primario |
| vacío | definido | el secondary SHALL ignorarse → proyecto sin grupo |
| vacío | vacío | sin agrupar (comportamiento R10) |

El struct `Manifest` SHALL exponer `PrimaryGroup`/`SecondaryGroup` (en lugar
de `Group`). El meta.json (0001 R5) SHALL dejar de almacenar `group` (el
campo `Meta.Group` se elimina: solo se escribía, nunca se leía).

#### S36.1: dos niveles definidos

- GIVEN un manifiesto con `primary_group = "vsocial"` y
  `secondary_group = "backend"`
- WHEN se parsea
- THEN PrimaryGroup="vsocial", SecondaryGroup="backend"

#### S36.2: `group` ya no agrupa

- GIVEN un manifiesto con solo `group = "backend"` (clave vieja)
- WHEN se descubre y renderiza
- THEN la fila aparece sin grupo, sin header `backend` (la clave se ignora)

#### S36.3: secondary sin primary se ignora

- GIVEN un manifiesto con solo `secondary_group = "infra"`
- WHEN se agrupa
- THEN SecondaryGroup se descarta y la fila va inline (sin agrupar)

#### S36.4: primario sin secundario

- GIVEN un manifiesto con solo `primary_group = "misc"`
- WHEN se agrupa
- THEN la fila va directamente bajo el header `misc`, sin header secundario

#### S36.5: defaults vacíos

- GIVEN un manifiesto mínimo válido (name + command_start)
- WHEN se parsea
- THEN PrimaryGroup y SecondaryGroup son `""` (sin agrupación)

### R37: Arrangement jerárquico de dos niveles

`Entry` SHALL ser `{Primary, Secondary string, Project}`. Tras el orden
actual (sort por ruta), SI existe algún proyecto con `primary_group` no
vacío, `Arrange` SHALL aplicar el patrón de bloque contiguo en dos niveles:

- el bloque de un primario SHALL emitirse completo en la posición de su
  primer miembro (orden de aparición; patrón R10/R16);
- dentro de un primario, el bloque de cada secundario SHALL emitirse
  contiguo en la posición de SU primer miembro (orden de primera aparición
  dentro del primario);
- los proyectos con primario pero sin secundario SHALL conservar su posición
  de sort dentro del primario, sin header propio (sin pseudo-header
  `(general)`);
- los proyectos sin primario SHALL seguir inline en su posición, como hoy
  (vroom NO usa la sección `(ungrouped)` de gitdash).

`IsGroupHeader` SHALL sustituirse por: `IsPrimaryHeader(i)` = `Primary != ""`
y (i==0 o cambia el primario respecto de i-1); `IsSecondaryHeader(i)` =
`Secondary != ""` y (i==0, o cambia el primario O el secundario respecto de
i-1).

#### S37.1: bloques anidados contiguos

- GIVEN filas ordenadas [A(p=vsocial,s=backend), B(sin primario),
  C(p=vsocial,s=infra), D(p=vsocial,s=backend)]
- WHEN se agrupa
- THEN el orden es A, D (bloque backend), C (bloque infra), B; B inline
  después, sin sección ungrouped

#### S37.2: primario en posición del primer miembro

- GIVEN filas [X(p=otros), Y(p=vsocial), Z(p=otros)]
- WHEN se agrupa
- THEN X, Z quedan contiguos desde la posición de X (bloque otros) y Y forma
  su propio bloque después; los bloques conservan el orden de primera
  aparición (X antes de Y)

#### S37.3: mezcla con y sin secundario

- GIVEN un primario con proyectos backend/frontend y un proyecto sin
  secundario
- WHEN se agrupa
- THEN el proyecto sin secundario aparece bajo el primario en su posición de
  sort, sin header secundario que lo envuelva

#### S37.4: secundario contiguo en posición de su primer miembro

- GIVEN dentro del primario vsocial los miembros en orden de aparición
  [m1(s=b2), m2(s=b1), m3(s=b2), m4(sin s)]
- WHEN se agrupa
- THEN dentro del bloque: m1, m3 (bloque b2), m2 (bloque b1), m4

#### S37.5: predicados de header

- GIVEN las entradas de S37.1
- WHEN se evalúan los predicados
- THEN IsPrimaryHeader es true solo en A (primero del bloque vsocial) e
  IsSecondaryHeader es true en A (abre backend), C (cambia a infra) y D
  (vuelve a backend), y false en B (sin primario)

### R38: Headers de dos niveles y plegado

El árbol SHALL componerse de tres tipos de fila: header primario, header
secundario y proyecto. El header primario SHALL dibujarse en columna 0
(`▾ nombre` expandido, `▸ nombre (running/total)` colapsado, convención R24);
el header secundario SHALL dibujarse indentado con 2 espacios extra
(`▾ secundario`). El conteo del primario SHALL incluir a todos sus
secundarios. El plegado SHALL alternarse con `enter` (interacción R24):

- `enter` sobre un header alterna su propio nivel (primario o secundario);
- `enter` sobre un proyecto SHALL plegar el contenedor más interno al que
  pertenece (su secundario si tiene, si no su primario);
- plegar un secundario oculta solo sus proyectos;
- plegar un primario oculta TAMBIÉN sus headers secundarios.

El estado plegado SHALL usar claves únicas en el map `collapsed`: primario =
su nombre; secundario = `primario/secundario` (compuesta, evita colisión
entre primarios distintos con el mismo secundario). Persistencia solo en
sesión. `tab` sigue ciclando pestañas (R22).

#### S38.1: render de headers

- GIVEN el primario vsocial con el secundario backend dentro
- WHEN se renderiza el árbol
- THEN el primario está en columna 0 (`▾ vsocial`) y el secundario con 2
  espacios extra (`  ▾ backend`), ambos antes de sus miembros

#### S38.2: plegado de secundario

- GIVEN el header secundario backend de vsocial seleccionado con 6 miembros
- WHEN se pulsa `enter`
- THEN sus 6 proyectos se ocultan, el header pasa a `▸ backend (r/6)` y el
  primario sigue abierto con sus otros secundarios

#### S38.3: plegado de primario oculta secundarios

- GIVEN vsocial abierto con backend/frontend/infra visibles
- WHEN se pliega el primario
- THEN desaparecen sus proyectos Y sus 3 headers secundarios; solo queda
  `▸ vsocial (r/14)`

#### S38.4: enter sobre proyecto pliega el contenedor interno

- GIVEN el cursor sobre un proyecto con secundario backend (primario
  vsocial)
- WHEN se pulsa `enter`
- THEN se pliega `vsocial/backend`, no `vsocial`; GIVEN el cursor sobre un
  proyecto sin secundario dentro de vsocial WHEN se pulsa `enter` THEN se
  pliega `vsocial`

#### S38.5: conteo del primario suma secundarios

- GIVEN vsocial con backend (6), frontend (4), infra (4)
- WHEN se colapsa el primario
- THEN el header muestra el total 14 (y sus running) sumando los tres
  secundarios

#### S38.6: claves de plegado sin colisión

- GIVEN dos primarios `a` y `b`, ambos con secundario `backend`
- WHEN se pliega `a/backend`
- THEN `b/backend` permanece desplegado

#### S38.7: el header conserva su posición al plegar

- GIVEN cualquier nivel de header seleccionado
- WHEN se alterna el plegado
- THEN el cursor queda sobre el mismo header (su índice se conserva al
  reconstruir el árbol; los miembros ocultos están después)

### R39: Selección y acciones de grupo a dos niveles

Las interacciones de grupo de R24 SHALL funcionar sobre ambos niveles: la
selección de un header (primario o secundario) SHALL mostrar el placeholder
de consola y el resumen en el panel de detalles (conteo y miembros del nodo
seleccionado); `s` sobre un primario SHALL aplicar el toggle contextual a
TODOS sus miembros (incluidos los de todos sus secundarios); `s` sobre un
secundario SHALL aplicarlo solo a los miembros de ese secundario. El panel
de detalles del proyecto SHALL mostrar en la fila `group:` el compuesto
`primario/secundario` (o solo el primario si no hay secundario; la fila se
omite si no hay ninguno).

#### S39.1: resumen de secundario en detalles

- GIVEN el header secundario `vsocial/backend` seleccionado
- WHEN se renderiza el panel de detalles
- THEN muestra el conteo running/total y los miembros de ese secundario

#### S39.2: s sobre primario

- GIVEN un primario con miembros stopped y running mezclados a través de
  varios secundarios
- WHEN se pulsa `s` sobre el header primario
- THEN el toggle aplica a todos sus miembros: se arrancan los stopped (o se
  paran los running si no hay stopped)

#### S39.3: s sobre secundario

- GIVEN el secundario backend de vsocial con miembros stopped y running
- WHEN se pulsa `s` sobre su header
- THEN el toggle aplica solo a los miembros de backend

#### S39.4: detalles con compuesto

- GIVEN el cursor sobre un proyecto con primary=vsocial y secondary=backend
- WHEN se renderiza el panel de detalles
- THEN la fila `group:` muestra `vsocial/backend`; con solo primario muestra
  el primario; sin ninguno la fila se omite

#### S39.5: placeholder de consola en secundario

- GIVEN un header secundario seleccionado
- WHEN la consola está visible
- THEN muestra el placeholder `group selected — pick a service to view its
  console`

## Revisiones

- **R36**: reemplazo duro de `group` sin alias (decisión del usuario,
  replicada de gitdash R18); implica migrar manifiestos del playground,
  tests (`group_test`, `app_test`, `manifest_test`, `scanner_test`,
  `state_test`) y la tabla del README.
- **R36**: `secondary_group` sin `primary` se ignora (decisión del usuario):
  el secundario solo tiene sentido dentro de un primario. Desviación
  consciente de gitdash, donde caía en `(ungrouped)`: en vroom los sin
  primario van inline.
- **R37**: sin sección `(ungrouped)` (decisión del usuario): vroom no adopta
  el bloque final plegable de gitdash R19; los proyectos sin primario siguen
  inline en su posición (comportamiento R10/R16 actual).
- **R37**: proyectos con primario y sin secundario conservan su posición de
  sort dentro del bloque, sin pseudo-header `(general)` (decisión del
  usuario; menos ruido visual).
- **R38**: el conteo usa la convención vroom de R24 — `(running/total)`
  visible al colapsar — en lugar del `(n)` siempre visible de gitdash S20.1;
  el usuario pidió "como hoy".
- **R38**: la tecla de plegado es `enter` (R24), no `tab`: en vroom `tab`
  cicla pestañas Console/Threads (R22). La regla gitdash "plegar sobre un
  proyecto el contenedor más interno" se aplica con `enter`.
- **R38**: clave de plegado compuesta `primario/secundario` para evitar
  colisión de nombres de secundarios entre primarios distintos (decisión del
  usuario, igual que gitdash R20).
- **Búsqueda/filtro**: el requisito de gitdash S18.5 (que el filtro matchee
  el secondary) no aplica: vroom no tiene búsqueda/filtro hoy. Queda
  documentado como requisito para un futuro filtro.
