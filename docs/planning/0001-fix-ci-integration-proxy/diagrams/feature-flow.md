# Feature flow — `fix/ci-integration-proxy`

Derived from `behavior.feature`: what `Apply` does with a freshly written portless route, and the propagation race the fix addresses.

## Verify: state flow (post-fix)

```mermaid
flowchart TD
    A["Apply: write alias, then read-back confirms our port"] --> B{"Proxy port readable?"}
    B -- no --> R1["degraded · proxy_not_running<br/>Registered true"]
    B -- yes --> C["Probe https then http<br/>deadline = now + 3.5 s · poll 250 ms"]
    C --> P{"Probe reply"}
    P -- "any status except 404 (502 included)" --> R2["registered · Url published<br/>routing proven, window ends here"]
    P -- "404: proxy up, cache not caught up yet" --> W{"Deadline reached?"}
    P -- "no reply at all" --> K{"Declared port accepts TCP?"}
    K -- no --> R4["degraded · proxy_unreachable<br/>at once: a dead port never serves"]
    K -- yes --> W
    W -- no --> S["sleep 250 ms"] --> C
    W -- yes --> F{"Any 404 seen during the window?"}
    F -- yes --> R3["degraded · route_not_served · no Url<br/>same as pre-fix, just later"]
    F -- no --> R5["degraded · route_not_served<br/>Registered true"]
    R2 --> OK["Route usable on the stable Url"]
    R1 --> NG["Service keeps running on its own port (warn)"]
    R3 --> NG
    R4 --> NG
    R5 --> NG
```

Reading notes:

- Pre-fix, a first-probe 404 was final → `degraded · route_not_served` (the deterministic CI failure on the inotify-starved runner).
- `WithVerifyWait(0)` collapses the loop to the old single immediate probe (deterministic fakes pin it); `New()` installs the 3.5 s / 250 ms defaults.
- The retry only extends a **404**; any other answer, 502 included, stands as proof of routing and returns the published Url.

## The CI race: apply vs proxy cache

```mermaid
sequenceDiagram
    autonumber
    participant V as vroom Apply
    participant W as routes watcher
    participant P as portless proxy cache
    V->>W: write alias (routes.json)
    Note over W: healthy machine: fs.watch debounce ~100 ms<br/>CI runner: watcher unavailable → 3 s polling fallback
    V->>P: probe GET / (https, then http)
    P-->>V: 404 — cache has not reloaded yet
    loop every 250 ms until the 3.5 s deadline
        V->>P: probe again
    end
    W-->>P: reload routes (within ~3 s on the runner)
    V->>P: probe
    P-->>V: 200 or 502 → routing proven
    Note over V: registered · Url published<br/>pre-fix: the first 404 was final → degraded
```
