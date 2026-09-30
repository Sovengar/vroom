# Process flow — planificación de `dynamic-ports`

Decisiones reales que se tomaron para ESTE cambio. No es boilerplate.

```mermaid
flowchart TD
    START(["resolve --branch feat/dynamic-ports"]) --> IDX

    IDX{"Índice Engram<br/>codebase-index/vroom"} -->|"obs #2209 a 0c09653<br/>stale (ancestro de HEAD)"| REFRESH
    IDX -->|"fresh"| ISSUE

    REFRESH["codegraph init (faltaba en el worktree)<br/>+ sync → 1389 nodos<br/>mem_save @ d48da40"] --> ISSUE

    ISSUE["idea-refiner → issue.md<br/>DELEGACIÓN NO DISPONIBLE<br/>(límite de uso) → inline"]
    ISSUE --> CK1

    CK1{{"CHECKPOINT 1 — issue<br/>APROBADO"}} --> BRAIN

    BRAIN["brainstormer + architect → INLINE<br/>delegas fallaron, no se reintentó"]
    BRAIN --> BEH

    BEH["behavior.feature escrito<br/>27 escenarios, slices 1-3"] --> CK2
    BEH -.->|"frontera real:<br/>PR separado"| BEHP["behavior-portless.feature<br/>8 escenarios, slice 4"]

    CK2{{"CHECKPOINT 2 — behavior<br/>APROBADO con 2 condiciones"}} --> ADJ

    ADJ["Restricción dura añadida:<br/>el bloqueo de stage aplica<br/>SOLO a port_mode dynamic"] --> PLAN

    PLAN["plan.md<br/>adr_required = TRUE"] --> CK3
    PLAN --> CTX

    CK3{{"CHECKPOINT 3 — plan<br/>APROBADO"}} --> CTX

    CTX["context.md<br/>riesgos 5-17 + convenciones verificadas"] --> DIAG
    DIAG["diagrams"] --> COMMIT
    COMMIT["git add planning_dir<br/>commit"] --> DONE(["status: success"])
```

## Orden de slices decidido

```mermaid
flowchart LR
    S1["S1 Stop por linaje<br/>+ guard fail-closed"] --> S2["S2 Puertos dinámicos<br/>núcleo"]
    S2 --> S3["S3 R1/R2/R3"]
    S3 -.->|"PR INDEPENDIENTE<br/>no bloquea S1-S3"| S4["S4 portless<br/>proxy puro"]

    S1 -.->|"sin S4"| OK1["verde y publicable"]
    S2 -.->|"sin S4"| OK2["verde y publicable"]
    S3 -.->|"sin S4"| OK3["verde y publicable"]

    S4 --> DEG["Degrada a hoy<br/>si el shim falla<br/>(hoy: node global 20.19.0)"]
```

## Por qué S1 va primero

```mermaid
flowchart TD
    RISK["Riesgo 7 VERIFICADO:<br/>Stop de un servicio YA parado<br/>ejecuta fuser -k<br/>(guard: Pgid>0 OR Port>0;<br/>los callers ponen Pgid=0 pero<br/>preservan Port)"]
    RISK --> NOW["Hoy el puerto es FIJO:<br/>daño limitado al instante"]
    RISK --> LATER["En S2 el puerto es EFÍMERO:<br/>el mismo bug pasa a matar<br/>procesos no relacionados"]

    NOW --> ORDER1["Por eso S1 ANTES que S2:<br/>el guard fail-closed aterriza<br/>antes de que los puertos<br/>se vuelvan efímeros"]
    LATER --> ORDER1
    ORDER1 --> COST["Coste aceptado por el usuario:<br/>la capacidad titular llega en S2,<br/>no en S1"]
```

## Disposición de riesgos 5-17 (13 guards, 1 aceptación)

```mermaid
flowchart TD
    AUDIT["Auditoría READ-ONLY<br/>riesgos 5-17"]

    AUDIT --> G5["5 · Env merge<br/>G"]
    AUDIT --> G6["6 · spawn→SaveMeta<br/>G"]
    AUDIT --> G7["7 · Stop ya parado<br/>G (S1)"]
    AUDIT --> G8["8 · WaitForPort(0)<br/>G, solo dynamic"]
    AUDIT --> G9["9 · ownerless → running<br/>G"]
    AUDIT --> G10["10 · dual-stack<br/>G"]
    AUDIT --> G11["11 · Meta.State write-only<br/>G"]
    AUDIT --> G12["12 · pending = vivo<br/>G"]
    AUDIT --> G13["13 · port==0 en 7 sitios<br/>G"]
    AUDIT --> G14["14 · constantes duplicadas<br/>G"]
    AUDIT --> G15["15 · Validate no aplica<br/>G"]
    AUDIT --> G16["16 · tripwire test<br/>G"]
    AUDIT --> D17["17 · puerto cambia entre ticks<br/>D ACEPTADO<br/>+ sub-guard: nunca mezclar generaciones"]

    RULING["Descartados por auditoría<br/>(no gastar presupuesto)"] --> X1["daemon_windows.go es stub<br/>→ StartResult es seguro"]
    RULING --> X2["PathKey hashea la ruta<br/>→ worktrees ya separados"]
```

## Divergencia proposal → realidad (corregida)

```mermaid
flowchart TD
    P["El proposal afirma"] --> P1["'meta.Port es el campo<br/>que los usos ya consumen'"]
    P1 --> X["FALSO<br/>10+ sitios leen Manifest.Port<br/>solo 3 leen meta.Port"]
    X --> IMP["La migración display/JSON<br/>es TRABAJO EN SCOPE,<br/>no un extra"]

    P2["'WaitForPort en engine.go'"] --> X2["FALSO<br/>está en orchestrate/health.go"]
    P2 --> X3["'ConnectionsPid tcp, pid'"] --> X4["FALSO<br/>es ConnectionsPid('tcp', 0)<br/>0 = TODOS los pids"]
    P2 --> X5["'discovery nunca en el tick'"] --> X6["YA se viola hoy<br/>PortOpen en cada tick de 2 s"]
```

## Decisiones del usuario en los checkpoints

```mermaid
flowchart TD
    U1["killPortHolder falla cerrado:<br/>ownership no probado → no matar + aviso"]
    U2["Puerto pendiente ≠ sin puerto:<br/>y pendiente NO satisface el gate de stage<br/>SCOPED A port_mode dynamic"]
    U3["App ignora PORT → AVISO, no error"]
    U4["Rango 4000-4999 fijo ahora,<br/>ampliable después"]
    U5["Orden de slices CONFIRMADO"]
    U6["make install por slice:<br/>el usuario corre el binario instalado"]
    U7["Riesgo 17 aceptado:<br/>sin re-resolución por tick"]
```