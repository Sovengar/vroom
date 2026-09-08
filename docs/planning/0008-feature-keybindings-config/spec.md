# Spec: Keybindings configurables en config.toml

Cambio: `0008-feature-keybindings-config`
Base: 0004 (extiende config R32); modifica el disparador de las acciones
0001 R14/R15/R24 (s), S14.2 (R), 0003 R26 (b/i), 0003 R28 (t), 0004 R31 (C),
0004 R32 (a), S19.3 (c), S19.4 (g/G), S22.1 (l), S22.2 (r); numeración
continúa desde 0007 (última R44)

## Requirements

### R45: Sección [keybindings] con merge sobre defaults

El config global SHALL soportar la sección opcional `[keybindings]`: cada
entrada mapea nombre de acción → tecla (String() de la tecla). Cualquier
acción ausente SHALL conservar su default (merge: el usuario solo overridea
lo que cambia). La resolución SHALL exponerse como `KeyFor(action)` (acción →
tecla) y `KeyByAction()` (mapa inverso tecla → acción, precalculado y
determinista).

#### S45.1: sin sección, defaults

- GIVEN un config sin `[keybindings]`
- WHEN se carga
- THEN `KeyFor("start_stop") = "s"` y el resto de acciones tienen sus
  defaults (R46), sin `cfg.Err`

#### S45.2: override parcial

- GIVEN `[keybindings]` con solo `start_stop = "x"`
- WHEN se carga
- THEN `KeyFor("start_stop") = "x"`, `KeyFor("build") = "b"` (default
  intacto) y sin `cfg.Err`

#### S45.3: mapa inverso

- GIVEN la config cargada (defaults o override)
- WHEN se consulta `KeyByAction()`
- THEN cada tecla activa resuelve a su acción y dos acciones NUNCA comparten
  tecla (R48)

### R46: Acciones configurables y defaults

Las acciones remapeables SHALL ser exactamente estas, con estos defaults
(comportamiento vigente):

| Acción | Default | Efecto (sin cambios) |
|---|---|---|
| `start_stop` | `s` | toggle start/stop (también nodo de grupo) |
| `restart` | `R` | restart (stop → start) |
| `build` | `b` | one-shot build |
| `install` | `i` | one-shot install |
| `tasks` | `t` | picker de tasks de mise |
| `ask` | `a` | ask AI |
| `clear` | `C` | limpiar consola |
| `stream` | `c` | ciclar merged → stdout → stderr |
| `top` | `g` | goto top consola (pausa follow) |
| `bottom` | `G` | goto bottom (reactiva follow) |
| `logs` | `l` | abrir logs en editor (alias fijo `o`) |
| `refresh` | `r` | refresh forzado |

#### S46.1: defaults completos

- GIVEN defaults
- WHEN se cargan
- THEN las 12 acciones resuelven a las teclas de la tabla

#### S46.2: remap preserva el efecto

- GIVEN `start_stop = "x"` en el config
- WHEN se pulsa `x` sobre un servicio stopped
- THEN se dispara el toggle de start (mismo efecto que `s` hoy) y `s` NO
  dispara ninguna acción

### R47: Teclas reservadas no remapeables

Las siguientes teclas SHALL estar reservadas y NO SHALL poder asignarse a
ninguna acción configurable: `q`, `ctrl+c`, `esc`, `enter`, `tab`, `j`, `k`,
`up`, `down`, `pgup`, `pgdown`, `1`, `2`, `/` (filter) y `!` (shell
interactivo, feature futura). Un `[keybindings]` que asigne cualquiera de
ellas SHALL producir config inválida: defaults + `cfg.Err` notificado al
arrancar (patrón 0004).

#### S47.1: reservada → error

- GIVEN `[keybindings]` con `ask = "q"`
- WHEN se carga
- THEN `cfg.Err != nil`, `KeyFor("ask") = "a"` (defaults) y la TUI notifica
  el error al arrancar

#### S47.2: "/" y "!" no entran en el mapa

- GIVEN el config con defaults
- WHEN se intenta definir `filter` o `shell` en `[keybindings]`
- THEN la acción es desconocida → config inválida (R49 S49.4)

### R48: Colisión entre acciones

Dos acciones configuradas a la MISMA tecla SHALL producir config inválida
(defaults + `cfg.Err`): el mapa inverso debe ser inyectivo.

#### S48.1: colisión → error

- GIVEN `[keybindings]` con `build = "x"` y `install = "x"`
- WHEN se carga
- THEN `cfg.Err != nil` y ambas acciones conservan sus defaults (`b`, `i`)

### R49: Formato válido de tecla y acción conocida

El valor de una entrada SHALL ser: una sola rune, `ctrl+<rune>` (con
`<rune> != c`, reservada) o un nombre especial de la lista `space`, `home`,
`end`, `delete`, `backspace`, `left`, `right`. Cualquier otro valor
(multi-rune, vacío, `ctrl+c`) SHALL producir config inválida. El nombre de
acción SHALL ser una de las 12 de R46; una acción desconocida (typo, p.ej.
`filter`) SHALL producir config inválida.

#### S49.1: rune simple válida

- GIVEN `restart = "z"`
- WHEN se carga
- THEN sin `cfg.Err`, `KeyFor("restart") = "z"`

#### S49.2: nombre especial válido

- GIVEN `stream = "space"`
- WHEN se carga
- THEN sin `cfg.Err`, `KeyFor("stream") = "space"`

#### S49.3: valor inválido → error

- GIVEN `[keybindings]` con `ask = "abc"` (multi-rune) o `ask = ""`
- WHEN se carga
- THEN `cfg.Err != nil` y defaults

#### S49.4: acción desconocida → error

- GIVEN `[keybindings]` con `filter = "f"` (acción inexistente)
- WHEN se carga
- THEN `cfg.Err != nil` y defaults

### R50: Resolución de teclas en la TUI

`handleKey` SHALL resolver la tecla → acción vía el mapa inverso precalculado
(al construir el Model) y despachar sobre la acción. Las teclas universales
(R47) y el alias `o` de `logs` SHALL seguir resolviéndose literalmente antes
del mapa. Los modales (picker, filtro, ask) SHALL capturar las teclas como
hoy, sin pasar por el mapa. Con defaults, el comportamiento SHALL ser
idéntico al actual.

#### S50.1: sin remap, comportamiento intacto

- GIVEN defaults
- WHEN se pulsan `s`, `b`, `i`, `t`, `a`, `C`, `c`, `g`, `G`, `l`, `r`, `R`
- THEN cada tecla dispara su acción actual (los tests existentes pasan sin
  cambios)

#### S50.2: remap efectivo y tecla vieja libre

- GIVEN `tasks = "m"` en el config
- WHEN se pulsa `m` sobre un servicio con mise.toml
- THEN se abre el picker de tasks; cuando se pulsa `t` NO se abre nada
  (`t` dejó de estar asignada)

#### S50.3: modales intactos

- GIVEN el picker abierto (vía la tecla configurada)
- WHEN se pulsa `j`/`k`/`enter`/`esc`
- THEN el picker navega/ejecuta/cierra como hoy (sin pasar por el mapa)

#### S50.4: universales intactas

- GIVEN `start_stop = "x"` en el config
- WHEN se pulsan `q`, `esc`, `enter`, `tab`, `j`, `k`, `1`, `2`, `/`
- THEN se comportan como hoy (quit, esc, plegar, pestañas, navegar, filtro)

### R51: Barra de ayuda derivada de los bindings

La línea de ayuda del dashboard SHALL derivarse de los bindings activos:
labels fijos para las universales (`j/k move`, `/ filter`, `enter collapse`,
`1/2 tabs`, `q quit`) y `key label` para cada acción configurable resuelta,
en orden canónico. SHALL mantener las variantes responsive actuales (full y
compacta, truncado al ancho). Con defaults SHALL reproducir el texto vigente;
tras un remap SHALL mostrar la tecla nueva.

#### S51.1: help con defaults

- GIVEN defaults
- WHEN se renderiza la help full
- THEN reproduce el texto vigente: `j/k move · / filter · enter collapse ·
  s start/stop · R restart · b build · i install · t tasks · a ask · C clear ·
  1/2 tabs · c stream · l logfile · r refresh · q quit`

#### S51.2: help tras remap

- GIVEN `start_stop = "x"` en el config
- WHEN se renderiza la help full
- THEN el segmento de start/stop muestra `x start/stop` y NO contiene
  `s start/stop`

#### S51.3: responsive sin cambios

- GIVEN un ancho menor que la help full
- WHEN se renderiza
- THEN se usa la variante compacta y se trunca al ancho (como hoy)
