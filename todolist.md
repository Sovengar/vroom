# todolist.md — cobertura de tests de 72.9% → ≥98%

Baseline medido el 2026-10-02 con paridad exacta de CI:

```bash
go test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
```

**4409 statements, 1199 sin cubrir → 72.9%.** Idéntico con y sin `-race`.
544 tests en 60 archivos. 18 paquetes.

---

## 0. La matemática del objetivo (léase antes de aceptar el 98%)

| | statements | sin cubrir |
|---|---|---|
| `internal/tui` | 1978 | 558 |
| resto (17 paquetes) | 2431 | 641 |

Para 98% total hacen falta **≤88 statements sin cubrir**. Dos hechos
condicionan todo el plan:

1. **El 98% total depende íntegramente del TUI.** Si los otros 17 paquetes
   llegan al 98% (≈49 sin cubrir) y el TUI se queda en 71.8%, el total queda en
   **86.2%**. Con el TUI al 90% → 94.4%. Con el TUI al 95% → 96.6%. El TUI tiene
   que llegar a ~98% para que el total llegue a 98%. No hay atajo: no es "subir la
   cobertura de los paquetes fáciles".

2. **Aun así es alcanzable, y por un motivo estructural**: el 90% de
   `internal/tui` son funciones de render puras con receptor por valor que
   devuelven `[]string` (`metricsLines`, `gitLines`, `treeLines`,
   `detailsLines`, `stackDetailsLines`, `rightColumnLines`…). No son un Bubbletea
   `Program` que haya que conducir. Son funciones testeares con table-driven y
   golden, sin seam nuevo. La parte que sí es un `Program` (`Init`, `Update`) es
   una fracción del peso.

Por eso el plan es ejecutable. La Fase 4 es la cara, las Fases 1–3 son las
baratas.

---

## 1. Doctrina del repo — obligatoria, no negociable

Extraída de los comentarios ya existentes en el código. Un PR que la contradiga
debería rechazarse.

- **Probar la cosa real, no un stub.** `removeabsent_test.go`: *"Estos casos NO
  pasan por Release: se exertan contra el RemoveAbsent REAL, con el exec
  inyectado, porque Release delega en un stub y un test que sólo mira el stub no
  probaría nada del código bajo prueba."*
- **No introducir variables globales intercambiables como seam.** `apply.go`
  documenta `IsTestBinary()` como *"No depende de una variable que un test pueda
  no poner."* Por eso la Fase 3 usa **subproceso real**, no `var exit = os.Exit`.
- **Table-driven** para casos múltiples, `t.Run(tt.name, ...)`, nombrado por
  escenario y no por mecánica del input.
- **`t.TempDir()`** para todo lo que toque disco. Nunca el home real.
- **Integración con `testing.Short()`** cuando ejecute comandos externos.
- **Golden determinista**, actualizable solo por un path `-update` del repo, y
  re-verificado sin `-update` después.
- **Commenté en español, explica el porqué** (es la convención dominante del
  repo; los tests existentes documentan el bug que previenen).

---

## Fase 0 — Infraestructura de test (bloquea Fases 3 y 4)

Sin esto no se puede escribir ni un test de CLI ni de TUI. **Cero delta de
cobertura**, es enabling work.

- [ ] **0.1 Harness de golden en `internal/tui`.**
  Helper `assertGolden(t, name, lines []string)` que compare contra
  `internal/tui/testdata/<name>.golden`. Flags `-update` para regenerar.
  Determinismo: **ninguna línea puede contener reloj, PID real ni ruta
  absoluta** — los `*Lines` reciben `time.Time`/pid del Model como dato, así que
  los tests fijan el tiempo con `t.Setenv`/campos del Model, no con `time.Now()`
  dentro del render. Si un golden resulta no determinista, el culpable es un
  `time.Now()` en el render y hay que inyectarlo, no sanear el golden.
  Aceptación: golden inexistente falla el test con un mensaje que dice cómo
  generarlo; `-update` regenera y el rerun sin `-update` pasa.

- [ ] **0.2 Helper de subprocess con cobertura de hijo.**
  `internal/testsub` (paquete nuevo, solo test): Given un comando, lo ejecuta y
  recoge su cobertura vía `GOCOVERDIR`. Requiere compilar el binario de vroom
  con `go build -cover -o <tmp>/vroom ./cmd/vroom` (soportado desde Go 1.20; el
  repo está en go 1.26.3) y ejecutar con `GOCOVERDIR` apuntando a un dir
  temporal. Aceptación: el test ve statements de `main()` y de `outputError`,
  que hoy son inalcanzables in-process.

- [ ] **0.3 Fusión de perfiles en el informe.**
  Fusión (`go tool covdata`) de los perfiles de los procesos hijos dentro del
  perfil del padre, para que `coverage.out` dé el total real y no solo el del
  proceso de test. Sin esto el gate de la Fase 6 mide mal.

- [ ] **0.4 Objetivo de tiempo de suite documentado.** Hoy el perfil es de
  60s (`internal/startsvc`). Las Fases 3 y 4 suben eso. Medir y dejar escrito el
  presupuesto; si se dispara, la respuesta es `testing.Short()`, no subir el
  timeout de CI a ciegas.

---

## Fase 1 — Funciones puras al 0%, sin infraestructura (barata)

Todo esto es table-driven sobre funciones puras. **~197 statements.**
Esperado: **72.9% → ~77.3%**

- [ ] **1.1 `internal/tui/bordered` (82 sin cubrir, el peor ratio: 42.7%).**
  `parseAnsiSegments`, `wrapLine`, `isResetStyle` — las tres al 0%. Funciones de
  texto puras y deterministas sobre el renderer: cero excusas. Casos:
  secuencias SGR válidas, secuencia malformada a mitad de línea, reset
  `ESC[0m` vs `ESC[m`, texto sin escapes, línea más ancha que el ancho ( wrap en
  punto de palabra y en punto duro), ancho 0 y ancho 1, tabulador y caracteres
  de ancho doble (CJK) en el wrap. **Ojo: es el renderer, un bug aquí es
  corrupción visual en la TUI, y hoy no lo detecta nadie.**
  Aceptación: ≥98% del paquete.

- [ ] **1.2 `internal/tui/outputtabs` — los `apply*` al 0%.**
  `applyMetrics`, `applyGit`, `applyEnv`, `firstLine`, `probeHealth`,
  `recordStackEventByName`. Los tests existentes (`outputtabs_test.go`) afirman
  `metricsLines`/`gitLines` **pre-poblando la caché del Model**: se prueba la
  mitad pura del sistema y se ignora la mitad transición. Estos tests pasan
  aunque el pipeline que llena la caché esté roto. Tests: cada `cmd` (42.9%)
  inyectando el seam de exec y disparando el `tea.Msg` resultante; cada `apply*`
  con msg de éxito, msg de error, y msg de servicio que ya no existe.
  Aceptación: la caché se llena probando el `cmd`, no)a mano.

- [ ] **1.3 `internal/state` (68.9%).**
  `NewStore` y `Base` al 0% porque los tests construyen el Store directamente y
  saltan el constructor. Cubrir `NewStore` con `t.Setenv("HOME", tmp)` +
  `t.TempDir()`: creación de dir base, permisos, error si el base dir es un
  fichero. `DefaultBaseDir` (33.3%) con y sin `HOME`. `EnsureServiceDir`,
  `SaveMeta`, `RegisterPid`, `ClearPid`, `SaveCollapsed` están en 62–75%: cubrir
  sus ramas de error (dir no escribible, meta corrupto, pid ya registrado).
  Aceptación: ≥98%.

- [ ] **1.4 `internal/portless` — los huecos reales, no los stubs.**
  `Warn` está al **18.2%** y es el agujero de verdad. Los 0% de `RemoveAbsent`
  (adaptador `ReleaserFunc`), `InertReleaser`, `IsTestBinary` y `ClientFor` son
  deliberados: son guardas de seguridad, no deuda. Cubrir `Binary`, `Default`,
  `StateDir` con `t.Setenv("PATH")` y `HOME` redirecidos (y **verificar con
  `IsTestBinary` que el test no puede tocar el portless real del desarrollador**),
  y `withReason`.
  Aceptación: ≥98%, o el resto de 0% documentado en un comentario como
  intencionado.

- [ ] **1.5 Stubs al 0% de un statement en paquetes ya altos.**
  `manifest.Exists`, `orchestrate.LaunchAsync`, `scanner.IsNestedRow`,
  `agents` (2), `group` (1), `worktree` (5), `gitinfo` (3), `tail` (5),
  `config` (7), `launcher` (10), `startsvc` (10). Todo table-driven, bajo
  riesgo, sube el total casi gratis.

- [ ] **1.6 `internal/process` — lo alcanzable.**
  `warnf` (2), `lineageDesc` (2). `ReadEnviron`/`ReadMetrics` (82.9%) leen
  `/proc`: o se cubren contra el `/proc` real del propio proceso de test (que
  siempre está y es determinista en Linux), o se marca como integración con
  `testing.Short()`.

---

## Fase 2 — Barrido de render del TUI con golden (la mayor de las baratas)

Cierra `*Lines`, `*ContentLines`, `fitLines`, `clipLines`, `padLines`,
`frameBoxLines`, `treeLines`, `rightColumnLines`, `detailsContentLines`,
`consoleContentLines`, `groupDetailsLines`, `allDetailsLines`, `threadsLines`,
`timelineLines`, `dashboard.go`, `projectlist.go`, `serviceview.go`
(`stackDetailsLines` al 0%), `tui/route.go` (63.6%), `tui/term.go` (87.7%).

**~300 statements.** Esperado acumulado: **~84.1%**

- [ ] **2.1 Un golden por estado del Model, no por función.** El error a evitar
  es un golden por cada una de las ~20 funciones `*Lines`: eso no prueba nada,
  porque las funciones se llaman unas a otras y el mismo estado se repetiría 20
  veces. Lo correcto es un golden por **escenario de pantalla**: servicio
  parado / corriendo / arrancando /Parando / con error, con y sin grupo, con y
  sin stack, servicio seleccionado y no seleccionado, log vacío y log con
  líneas, viewport con scroll y sin scroll, ancho mínimo y ancho amplio. Eso
  ejercita las ~20 funciones a la vez y cubre de verdad.
- [ ] **2.2 Casos límite de layout**, que es donde se Concentran las ramas sin
  cubrir: ancho 0/1/2, altura 0/1, líneas más largas que el panel, truncado con
  elipsis, texto multibyte, panel oculto (`full` toggles), resize.
- [ ] **2.3 `tui/route.go` (63.6%)** — `tuiRouteReleaser` al 40%: es la
  integración TUI↔portless, es decir donde se decide la propiedad de una ruta.
  Mismo rigor que en `internal/portless`: stub no vale, hay que ejercer el
  liberador real.

---

## Fase 3 — `internal/cli`: de 30.1% a ≥98% (288 statements)

El paquete tiene **un solo `os.Exit`**: `outputError` (cli.go:152). Todo lo
demás son funciones que escriben en `os.Stdout` por `outputJSON`. Los 4 test
files actuales (`cli_test.go`, `portjson_test.go`, `port_test.go`,
`route_test.go`, `route_release_test.go`) solo cubren helpers de búsqueda de
proyecto y de puerto.

**Esperado acumulado: ~90.2%**

- [ ] **3.1 `outputJSON` / `outputError`.** El formato JSON **es la interfaz
  pública para agentes** (el propio repo se describe como "JSON interface for
  AI agents"). Hoy está al 0% y nadie lo verifica: un cambio de nombre de campo
  rompe a cada consumidor y CI sigue verde. Probar el shape exacto del JSON
  (indentación de 2 espacios, `omitempty` de `Stdout`/`Stderr`, clave `error`)
  decodificando a `map[string]any` y comparando contra el struct, no contra
  string literal.
- [ ] **3.2 `resolveRoot` (0%).** Rama `~` con `UserHomeDir`, rama
  relativa→absoluta con `filepath.Join`, y la config vacía. La expansión de `~`
  es lógica de ruta y un bug ahí manda al scanner al sitio equivocado en
  silencio.
- [ ] **3.3 `Run` — tabla de dispatch por subcomando.**
  `list`/`status`, `start`, `stop`, `build`, `install`, `oneshot`, `logs`,
  `help`, `launch`, y el caso `len(args)==0 → false` (que significa "lanza la
  TUI"). Verificar dos cosas distintas y ambas importantes: **a qué
  subcomando enruta**, y **qué devuelve** (true = manejado, false = TUI). Sin
  este test, un typo en el `switch` enruta `stop` a `start` y nada falla.
- [ ] **3.4 Los nueve `cmd*` (todos al 0%).** Casos por subcomando: happy path,
  proyecto inexistente, **proyecto ambiguo** (el `findProject` ya devuelve
  ambigüedad y el mensaje es accionable — hay que probarlo), `--path`
  desambiguando, flags desconocidos, y argumentos de menos (cada uno debe dar
  el `usage:` correcto, no un panic).
- [ ] **3.5 `runLogged`.** Es el camino que captura stdout/stderr de un comando
  one-shot y lo mete en `appendLine`. El log es lo que el usuario va a leer
  cuando algo falle: necesita caso de éxito, de comando que falla, de output
  interleaving, y de log que no se puede abrir.
- [ ] **3.6 `cmd/vroom/main.go` (0%, 14 statements).** inalcanzable in-process
  por los tres `os.Exit`. Solo se cubren con el harness de subprocess de 0.2:
  caso TUI (sin args) no es automatizable — se documenta la exclusión —, pero
  los tres caminos de error sí son (`NewStore` fallando, `Getwd` fallando,
  `tea.Run` fallando) con `HOME`/cwd manipulados.
- [ ] **3.7 `loadConfig`.** Trivial pero hoy es un 0% dentro de un paquete al
  30%; entra solo.

---

## Fase 4 — `internal/tui/app.go`: transiciones (la fase cara)

558 statements sin cubrir en el TUI, la mayoría aquí. **Esta fase es la que
decide si el total llega a 98%.**

**Esperado acumulado: ~95.1%**

- [ ] **4.1 `Model.Init` (0%)** y `tickCmd`/`consoleTickCmd`/`threadsCmd` (0%):
  los `tea.Cmd`. Probar el `cmd` con el seam de exec inyectado y afirmar el
  `tea.Msg` que produce; `Init` se despacha y se afirma el batch inicial.
- [ ] **4.2 `Update` está al 36.5%** — es un solo `switch` enorme y es el
  mayor bloque sin cubrir de todo el repo. Table-driven: **un caso por `tea.Msg`
  y por tecla**, afirmando la transición de estado, no el render. Mensajes que
  faltan: ticks, resultados de `metricsCmd`/`gitCmd`/`envCmd`/`healthCmd`,
  `consoleMsg`, `threadsMsg`, `editLogs`. Teclas: navegación, `q`/esc,
  shortcuts de vista, resize, cada toggle.
- [ ] **4.3 Los toggles al 0%:** `toggleStack`, `toggleComposers`,
  `markStackStopping`, `scrollDetails`. Los toggles son exactamente donde un
  estado queda desincronizado: el síntoma es "la TUI muestra algo que ya no es
  cierto", que no lo detecta ningún otro test.
- [ ] **4.4 `refreshBatch` (0%)** — es elnamen de la carga inicial del
  dashboard. Un 0% aquí significa que **la primera pantalla después de abrir
  vroom no tiene test**. Prioridad máxima dentro de la fase.
- [ ] **4.5 `stateOfMeta` (0%)** — traduce `Meta` a estado de UI. Si se
  equivoca, la UI miente sobre un servicio real.
- [ ] **4.6 `scrollDetails` + viewport.** Scroll con logs más largos que el
  panel, saturación en los extremos, resize que invalida el offset.

---

## Fase 5 — Colas de los paquetes ya altos (144 statements)

`process` (69), `scanner` (27), `orchestrate` (28), `launcher` (10),
`startsvc` (10).

**Esperado acumulado: ~97.6%**

- [ ] **5.1 `internal/process`** es el más grande: `daemon_unix.go` (83.1%),
  `dynamic_unix.go` (91.5%), `lineage_unix.go` (87.0%), `threads_unix.go`
  (84.0%), `metrics_unix.go`. Usa procesos reales y `/proc` real. Lo no
  alcanzable de forma determinista (lectura de `/proc` de un proceso ya
  muerto, carrera entre `kill` y `wait`) va a `testing.Short()` o se declara
  excluido, pero **cada rama de error real se prueba**.
- [ ] **5.2 `internal/scanner`** (86.7%): fallback de `WalkDir` cuando falta
  `fd` — es una ruta que CI ejercita en un runner sin `fd` instalado y en local
  nunca, así que hoy está verde sin ejecutarse de verdad. Probar ambas rutas.
- [ ] **5.3 `internal/orchestrate`** (89.7%): `engine.go` (87.7%),
  `compose.go`, `health.go`. Estos tests tardan 38s: son los que peor relación
  coste/beneficio dan de todo el plan, por eso van los últimos.

---

## Fase 6 — Gate: que el 98% no se deshaga

- [ ] **6.1 Umbral en CI.** Hoy `.github/workflows/ci.yml` **solo reporta**
  cobertura en el step summary; no bloquea nada. Con el gate de mutación
  existente (`mutation.yml`, bloquea supervivientes nuevos en el diff) hay una
  asimetría: la mutación previene que la cobertura **baje**, pero no sube el
  piso. Añadir umbral destatements y, preferiblemente, **no bajar del valor
  registrado en el propio repo** (`coverage.floor` en un fichero versionado), no
  un número fijo en el YAML que se queda viejo sin que nadie lo note.
- [ ] **6.2 Reproducibilidad del número.** Documentar en `Makefile` el comando
  exacto (`make cover`) que produce el perfil, e incluir los perfiles de
  subproceso (Fase 0.3). Un gate que mide distinto a como se midió el baseline
  es un gate que miente.
- [ ] **6.3 Integrar con el gate de mutación.** Cobertura por statements **y**
  supervivencia de mutantes son dos señales distintas: la primera mide cuánto
  código se ejecuta, la segunda mide si algún test lo verifica de verdad. Un
  suite puede tener 98% de cobertura y cero poder de detección — todo código
  ejecutado, nada afirmado. Los dos gates juntos, no uno.

---

## 7. La cola final: los últimos ~2%

Llegar a 98% exige ≤88 statements sin cubrir de 4409. Los últimos son, por
naturaleza, ramas de sistema: `/proc` de un proceso en carrera, un `syscall`
que solo falla bajo permisos concretos, un `os.Exit` que solo se alcanza con un
`HOME` corrupto.

**Regla, para que esto no se convierta en una pelea de números:**

- Cada exclusión se documenta en el propio punto del código, con un comentario
  que diga **por qué no es alcanzable**, como ya se hace en `apply.go` con
  `IsTestBinary`. Sin razón escrita, no es exclusión: es deuda.
- Si al terminar las Fases 0–5 el total queda entre 96% y 98%, **el número se
  publica tal cual y el objetivo se revisa con datos**, no se maquilla
  bajando el umbral ni subiendo el número con exclusiones infladas. Un 96%
  honesto con exclusiones justificadas vale más que un 98% con hueco.

---

## Orden de ejecución y por qué en este orden

1. **Fase 0** — no produce cobertura, pero sin 0.1 no hay golden y sin 0.2 no hay
   CLI ni `main`. Todo lo demás depende de esto.
2. **Fase 1** — la más barata: funciones puras ya al 0%, sin seam nuevo, riesgo
   bajo. Baja el número ya y valida que el harness de golden de 0.1 funciona
   (1.1 es el banco de pruebas de 0.1).
3. **Fase 2** — el mayor volumen y el menor riesgo, gracias a que los renders
   son puros. Sube el TUI de 71.8% a ~85%.
4. **Fase 3** — el CLI, que es superficie pública para agentes y está al 30%.
   Depende de 0.2.
5. **Fase 4** — la cara. Se hace con la confianza de que 1–3 ya están verdes y
   el golden harness está probado.
6. **Fase 5** — colas de los paquetes que ya están altos: es donde queda menos
   por ganar, así que va el último.
7. **Fase 6** — el gate, cuando el número ya es defendible. Poner el umbral
   antes solo provoca que se bajen las exclusiones para que pase.

Cada fase es un PR. La regla de `AGENTS.md` se aplica **al terminar cada uno**:
`make check` y después `make install`, que verifica el sello de revisión del
binario desplegado.