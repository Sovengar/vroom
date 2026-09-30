# Feature flow — vroom registra rutas en portless

Derivado de `../behavior.feature`. El recuadro verde es el **único** camino donde
el servicio obtiene una dirección verificada; todo lo demás degrada sin tocar la
salud.

```mermaid
flowchart TD
    A[Arranque de un servicio] --> B{port_mode?}

    B -->|fixed / none| Z[Sin registro de ruta<br/>comportamiento de hoy]
    B -->|dynamic| C[Reservar puerto + inyectar PORT]

    C --> D[DiscoverPort acotado<br/>deadline · liveness · estabilización]
    D --> E{Puerto real confirmado?}

    E -->|no: unresolved| U[port_unresolved<br/>NINGUNA ruta]
    E -->|no: sin listener| N[no_port<br/>NINGUNA ruta]
    E -->|sí| REC

    REC["RECONCILIAR las rutas que vroom<br/>persistio, contra el proxy vivo"]
    REC --> RC{Resultado}
    RC -->|nombre distinto, no responde| RD[retirar la antigua]
    RC -->|no responde: vroom murio antes| RH[retirar la huerfana]
    RC -->|responde con otro puerto| CF[conflicto · avisar<br/>NO registrar encima]
    RC -->|responde y es nuestra| RJ[no tocar · idempotente]

    RD --> F{route_mode = off?}
    RH --> F
    F -->|off| Z
    F -->|auto / named| G[Derivar nombre de la ruta]

    G --> H{State dir<br/>PORTLESS_HOME → XDG → HOME}
    H -->|ilegible| D1[degraded: portless_missing]
    H -->|ok| I{proxy.port existe?}

    I -->|NO · M6| D2[degraded: proxy_not_running<br/>NUNCA suponer 1355]
    I -->|sí| J{Proxy acepta conexion?}

    J -->|no| D3[degraded: proxy_unreachable]
    J -->|sí| K{Resolver binario<br/>PORTLESS_BIN → LookPath → shims}

    K -->|no encontrado| D4[degraded: portless_missing]
    K -->|ok| L["portless alias &lt;name&gt; &lt;port real&gt;<br/>acotado · M1: nunca toca el proxy"]

    L --> M{exit 0?}
    M -->|no| D5[degraded: register_failed]
    M -->|sí| N1[LEER DE VUELTA<br/>comparar puerto publicado]

    N1 --> N2{El puerto es el nuestro?}
    N2 -->|no| D6[degraded: conflict<br/>NO se reporta exito]
    N2 -->|sí| V1[VERIFICAR CONTRA EL PROXY VIVO<br/>un request · Host + SNI · loopback]

    V1 --> V2{Responde?}
    V2 -->|cualquier HTTP, incluso 502| V3[proxy enruta la ruta<br/>el servicio responde o no]
    V2 -->|error de conexion o timeout| D7[degraded: proxy_unreachable<br/>NO url]
    V2 -->|ningun esquema responde| D7

    V3 --> O[(Ruta registrada Y verificada<br/>url publicada<br/>esquema comprobado)]

    O --> P[SaveMeta con la ruta<br/>estado running]

    U --> Q
    N --> Q
    D1 --> Q
    D2 --> Q
    D3 --> Q
    D4 --> Q
    D5 --> Q
    D6 --> Q
    D7 --> Q
    CF --> Q
    Z --> Q
    P --> Q[[El servicio esta running<br/>en TODOS los caminos]]

    Q --> R{Stop}
    R --> S[Retirar ruta<br/>junto a ReleasePort<br/>3 caminos: TUI · CLI · stacks]
    S --> T{--remove exit != 0?}
    T -->|si: no existia| U2[Benigno · stop repetido<br/>NO es error]
    T -->|no| V[Ruta fuera]
    U2 --> W
    V --> W[[Servicio parado]]

    style Q fill:#d4edda,stroke:#28a745
    style O fill:#d4edda,stroke:#28a745
    style P fill:#d4edda,stroke:#28a745
    style V3 fill:#d4edda,stroke:#28a745
    style D6 fill:#fff3cd,stroke:#ffc107
    style D7 fill:#fff3cd,stroke:#ffc107
    style CF fill:#fff3cd,stroke:#ffc107
```

## Las dos verificaciones, y por qué son dos

Por **M1**, `alias` es una escritura pura del fichero de estado: **nunca contacta
con el proxy**. Con el proxy parado sale `exit 0` y escribe la ruta igual. Por eso:

- `N1` (leer de vuelta) existe por **M8**: `alias` es un upsert incondicional sin
  detección de conflictos, así que `exit 0` no prueba propiedad. Sin `N1`, `D6` es
  inalcanzable y vroom publicaría una url ajena.
- `V1` (verificar contra el proxy vivo) existe por **M1/M2**: leer el fichero sólo
  prueba que escribimos. Sin `V1`, `D7` es inalcanzable y una ruta muerta se
  reportaría como disponible.

## Por qué `502` es éxito

En `V2`, un `502` cae en la rama verde. Un `502` significa "el proxy enruta esta
ruta y el servicio de detrás no responde", que es información **distinta** de "el
proxy no sirve la ruta". Sólo un fallo de conexión o un timeout significa que no
hay proxy. Confundir los dos convierte una comprobación en una afirmación falsa.

## Por qué la reconciliación es obligatoria

Por **M5**, `prune` no toca las rutas de alias (`pid: 0`, contadas como `active`).
**vroom es lo único que puede limpiarlas.** Sin `REC`, las rutas de un vroom que
murió serían permanentes; con `REC`, se recogen en el siguiente arranque.

Y la línea que no se negocia: `CF` **avisa y no borra**. Una ruta que responde y no
es nuestra puede ser de otro vroom vivo. El fallo cerrado que gobierna todo el
cambio de puertos aplica también a la limpieza.

## Lo que este diagrama no tiene

Ninguna arista sale de `A` hacia un fallo de arranque, y ninguna flecha de
degradación toca el estado del servicio. Una ruta es una dirección, no una
dependencia.