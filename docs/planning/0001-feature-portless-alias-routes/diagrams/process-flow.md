# Process flow — planificación de `feat/portless-alias-routes`

Slice 4 de 4. Los slices 1–3 están mergeados y **no se tocan**.

```mermaid
flowchart TD
    S([orchestrator]) --> R["resolve --branch feat/portless-alias-routes<br/>worktree + planning_dir"]

    R --> IDX{Código indexado y fresco?}
    IDX -->|viejo: d48da40 / feat/dynamic-ports| CG["codegraph init + sync<br/>96 ficheros · 1794 nodos"]
    IDX -->|fresco| EV
    CG --> EV{{"EVIDENCIA: 9 Alias de portless<br/>mediada, no asumida"}}

    EV --> F1{{"1 · alias NUNCA toca el proxy<br/>exit 0 con el proxy caido<br/>=> verificar contra el VIVO"}}
    F1 --> F2{{"2 · prune destruye alias?<br/>NO · pid:0 · vroom es el unico<br/>que puede limpiarlas"}}
    F2 --> F3{{"3 · ruta sobrevive reinicio proxy?<br/>SI · routes.json se recarga"}}
    F3 --> F4{{"4 · alias daña rutas vivas?<br/>NO · coexisten con portless run"}}
    F4 --> F5{{"5 · proxy.port condicional?<br/>SOLO existe corriendo<br/>su ausencia ES la señal"}}
    F5 --> F6{{"6 · upsert exit 0<br/>no prueba propiedad"}}

    F5 --> CP1{{"CHECKPOINT 1 · issue"}}
    CP1 -->|aprobada| B

    B["behavior.feature<br/>degradación de primera clase"] --> CP2{{"CHECKPOINT 2 · behavior"}}
    CP2 -->|aprobado| ADR

    ADR["ADR-0013<br/>vroom registra; el proxy enruta"] --> PL["plan.md + context.md"]
    PL --> VER["make check<br/>+ make install"]
    VER --> EX([swe-executor])

    subgraph CORR["Correcciones que este slice arrastra a propósito"]
      C1["El plan archivado dice PORTLESS_APP_PORT<br/>MECANISMO EQUIVOCADO<br/>lo consume portless run: portless sería dueño del proceso"]
      C2["behavior-portless.feature dice v0.13.0 no resuelve por shim<br/>DESACTUALIZADO: 0.15.6 y sí resuelve"]
      C3["La propuesta §6.3 asume 80/443 + sudo<br/>INFORMATIVO en esta máquina: 1355, HTTP, sin sudo"]
    end

    subgraph DEC["Decisiones fijadas, no a relitigar"]
      D1["vroom SÓLO registra alias<br/>nunca arranca ni gestiona el proxy"]
      D2["state dir: PORTLESS_HOME → XDG → HOME<br/>binario: PORTLESS_BIN → LookPath → shims<br/>nada de /home/buble hardcodeado"]
      D3["Suite hermética por seam inyectado<br/>CI no tiene portless ni node 24 ni proxy"]
    end

    subgraph LIMIT["Eliminado por diseño, no diferido"]
      L1["vroom que muere deja rutas<br/>RECOGIDAS en el siguiente arranque<br/>reconciliación obligatoria"]
      L2["esquema TLS / puerto 443<br/>SE PRUEBA, no se supone<br/>no hay supuesto que el TLS refute"]
    end

    style EV fill:#d1ecf1,stroke:#17a2b8
    style LIMIT fill:#f8d7da,stroke:#dc3545
    style CORR fill:#fff3cd,stroke:#ffc107
    style DEC fill:#d4edda,stroke:#28a745
    style CP1 fill:#e2e3e5,stroke:#6c757d
    style CP2 fill:#e2e3e5,stroke:#6c757d
```

## Decisiones de este cambio, con lo descartado

| Decisión | Descartado | Por qué |
|---|---|---|
| Mecanismo: `portless alias <name> <port>` | `PORTLESS_APP_PORT` | Lo consume `portless run`, donde portless es dueño del proceso |
| Conocer el puerto del proxy: `proxy.port` + guarda de fichero ausente | raspar `doctor`; API HTTP; asumir `1355` | `doctor` cambia entre versiones; no hay API (todo `404`); `proxy.port` sólo existe corriendo |
| Verificar: leer de vuelta **y** una sonda al proxy vivo | sólo leer `routes.json`; sólo `exit 0`; `portless get` | `alias` no toca el proxy, luego el fichero sólo prueba que escribimos; `get` prefija la rama |
| Esquema de la URL: se prueba (`https` luego `http`) | suponerlo de la configuración | No hay supuesto que el TLS pueda refutar; no hizo falta medirlo |
| Limpieza: reconciliación obligatoria en cada arranque | `portless prune`; un `vroom route prune` aparte; borrar en silencio | `prune` no toca `pid:0`; borrar una ruta viva ajena contradice el fallo cerrado |
| Nombre: `route_mode = off\|auto\|named` + `route_name` | un solo `route = ""\|auto\|"nombre"` | La rama no es estable ante `git branch -m`; un enum ambiguo documenta su tipo en el schema |
| State dir y binario: derivados del entorno | `/home/buble/.portless`; `portless` a pelo | vroom corre bajo un gestor de servicios que no es el shell de login |
| Falta de portless: aviso | error de arranque | Contradice `adr-0012` limitación 6 |