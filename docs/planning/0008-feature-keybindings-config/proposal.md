# Proposal: Keybindings configurables en config.toml

## Intent

Los atajos de la TUI viven hardcodeados en `handleKey`: teclas literales en un
switch (`internal/tui/app.go:752-811`). gitdash ya resuelve esto igual al resto
de su config: sección `[keybindings]` en `config.toml` que mapea nombre de
acción → tecla, con merge sobre defaults (el usuario solo overridea lo que
cambia) y barra de ayuda derivada de los bindings activos. Este cambio replica
ese patrón en vroom: 12 acciones remapeables, teclas de navegación/especiales
universales (no remapeables), validación estricta en `Validate()` y help que
refleja lo configurado.

Cambio: `0008-feature-keybindings-config`
Base: 0004 (extiende la config global R32); modifica el disparador de las
acciones 0001 R14/R15/R24 (s), S14.2 (R), 0003 R26 (b/i), 0003 R28 (t), 0004
R31 (C), 0004 R32 (a), S19.3 (c), S19.4 (g/G), S22.1 (l), S22.2 (r) — el
efecto de cada acción NO cambia; numeración continúa desde 0007 (última R44).

## Scope

### In Scope
- `internal/config/config.go`: campo `Keybindings` (tag `toml:"keybindings"`),
  `DefaultKeybindings()` (12 acciones), `KeyFor(action)`, `KeyByAction()` (mapa
  inverso tecla→acción), validación (reservadas, colisiones, formato, acción
  desconocida).
- `internal/tui/app.go`: `handleKey` resuelve la acción vía el mapa inverso en
  lugar de teclas literales; `dashboardHelp` derivada de los bindings activos.
- Tests: `config_test.go` (defaults, override parcial, reservada→err,
  colisión→err, formato→err, acción desconocida→err), `app_test.go` (remap con
  `newTestModelWithConfig`; defaults → tests existentes intactos).
- `README.md`: sección `[keybindings]` en el ejemplo de config.
- `~/.config/vroom/config.toml`: sección comentada (estilo gitdash).

### Out of Scope
- Keybinds de los modales internos (picker `t`/`a`, filtro, ask prompt): sus
  handlers (`pickerKey`, `filterKey`, `askKey`) quedan como están.
- Teclas universales: `q`/`ctrl+c` (quit), `esc`, `enter` (plegar grupo),
  `tab` (ciclar pestañas), `j`/`k`/`up`/`down` (navegar), `pgup`/`pgdown`
  (scroll consola), `1`/`2` (pestañas directas), `/` (filter) y `!` (shell
  interactivo, feature futura) — NO remapeables.
- El alias `o` de `logs` queda fijo (no entra en el mapa).
- Mouse (rueda de consola) y `home`/`end` en el árbol.
- Sección `[commands]` de gitdash (aquí los comandos viven en cada
  `.vroom.toml`, no aplica).

## Capabilities

### New Capabilities
- `keybindings-config`: remapeo de 12 acciones vía `[keybindings]` con merge,
  validación estricta y help derivada.

### Modified Capabilities
- `config-global` (0004 R32): `Validate()` gana las reglas de keybindings; un
  config inválido sigue degradando a defaults + notify (mismo patrón).
- Disparadores de acciones (0001/0003/0004/0005): cambian de tecla literal a
  acción resuelta; con defaults el comportamiento es idéntico al actual.

## Approach

### Decisiones

- **12 acciones remapeables, no 13**: el usuario fijó que `/` (filter) y `!`
  (futuro shell) son keybinds permanentes, junto a la navegación y los
  especiales. `filter` sale del mapa configurable.
- **Validación estricta (mejora sobre gitdash)**: gitdash resuelve la tecla
  iterando un map en `actionForKey` (orden no determinista) y acepta cualquier
  valor sin validar. vroom valida en `Validate()`: (1) tecla reservada →
  error, (2) colisión entre dos acciones → error, (3) formato — 1 rune,
  `ctrl+<rune>` o nombre especial conocido (`space`, `home`, `end`, `delete`,
  `backspace`, `left`, `right`) → si no, error, (4) acción desconocida
  (typo) → error. Un config inválido degrada a defaults + notify como hoy.
- **Mapa inverso precalculado**: `KeyByAction()` construye tecla→acción una
  vez (en `New`, junto a la carga de config) y `handleKey` hace switch sobre
  la acción; determinista y O(1), sin iterar maps por tecla pulsada.
- **Help derivada, no hardcodeada** (decisión del usuario): `dashboardHelp`
  compone la línea con las teclas resueltas (labels fijos para las
  universales, key+label para las configurables), con las variantes full y
  compacta actuales. Con defaults reproduce el texto de hoy; si se remapea,
  la barra nunca miente.
- **Sin sección `[commands]`**: los comandos que ejecutan `b`/`i`/`t` ya son
  config por-proyecto (`.vroom.toml`); no hay nada que mover al config global.

## Impact

- Sin cambios de datos ni de schema (`meta.json`, manifiesto: nada).
- Con defaults, la TUI se comporta exactamente igual que hoy (los tests
  existentes de teclas no cambian).
- Riesgo bajo: la única lógica nueva es la resolución tecla→acción y la
  validación; el efecto de cada acción queda intacto.
