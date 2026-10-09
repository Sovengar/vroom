# Proposal — Dynamic ports and stable URLs for parallel worktrees

- Status: **proposal** (for review; not implemented)
- Date: 2026-09-30
- Scope: `vroom` (core) + convention in the user's project `.vroom.toml` files

> This document is a **problem / need / proposal report** designed for
> another agent to review critically and implement if deemed correct. All
> numerical data in the "Evidence" section was **measured** on this machine,
> not estimated. Points where the proposal is debatable are marked.

---

## 1. Problem

The user manages their projects with **git worktrees** and the worktrees are
**copies of the same repo**, each with its own `.vroom.toml`. Since the
copies are identical, **they declare the same fixed ports**.

Direct consequence: **it is not possible to bring up the same app in two
worktrees at once.** And the conflict is not a clean failure: vroom has
behaviors that turn the collision into incorrect data and the killing of
foreign processes (detail in §3.3).

## 2. Need

That vroom allow:

1. Bringing up the same application in **N worktrees simultaneously**, each
   with its own real port, without editing `.vroom.toml` per worktree.
2. Being able to **reference each service by a stable name** and not by a
   port number, because the number changes on every start. Without this, any
   project configuration (OAuth URLs, CORS, redirect URIs, README links,
   bookmarks) is tied to an ephemeral value.
3. Keeping manual startup outside vroom intact (`npm run dev`, `go run .`,
   `mise run dev`): the default ports **must not change** for those who do
   not use vroom.

## 3. Current state (verified in the code)

### 3.1 `StartSpec` has no `Port` field

`internal/process/process.go:26-31`. Startup is **blind**: vroom does not
validate port availability, does not reserve anything, and does not verify
anything after starting. The manifest's port is **copied literally** to
`state.Meta.Port` (`internal/tui/app.go:543`, `internal/cli/cli.go:469`).

### 3.2 The manifest's port has exactly 4 uses

| # | Use | Location | Nature |
|---|-----|-----------|-----------|
| 1 | State liveness signal | `internal/process/daemon_unix.go:122-126` | read |
| 2 | Release the port on stop (`fuser -k`) | `internal/process/daemon_unix.go:104-105` | **action** |
| 3 | Stage health gate (`WaitForPort`) | `internal/orchestrate/engine.go:302,340` | read |
| 4 | Health tab HTTP probe + display | `internal/tui/outputtabs.go:329,387`; `serviceview.go`, `dashboard.go`, `app.go:2142` | read |

Critical detail of (1): if `port > 0` and the PID is alive but the port does
not respond, the state is `unknown`, **not** `running`. And in the
"restarted externally" fallback, `PortOpen` + `PortOwnerPID` with a different
`creation_time` ⇒ `stopped`, precisely so as not to report `running` because
of another service's process.

### 3.3 Consequences measured with two worktrees

Two services declaring `port = 8080`:

- **Duplicate hard bind** (Go/Python with literal `:8080`): the second
  process **dies**. vroom detects it in ~300 ms.
- **Auto-increment** (Next.js/Vite, `EADDRINUSE` → 8081): the second
  service comes up on 8081, but vroom still believes 8080. Since 8080
  responds (worktree A has it), `Evaluate` reports worktree B as **`running`
  and "healthy"** when in reality it is listening elsewhere. Silent false
  positive.
- **`Stop` kills the twin.** `killPortHolder` runs `fuser -k` on
  `meta.Port` **without checking ownership** (`daemon_unix.go:218-229`).
  Stopping worktree A kills worktree B's process.

### 3.4 Preexisting Stop bug, independent of ports

`Stop` (`daemon_unix.go:89-108`) kills by **process group**. Any
`command_start` that daemonizes itself or calls `setsid` leaves children
**outside the group** that survive the stop. Measured with portless:

```
portless  PID/PGID/SID = 1066968
backend   PID/PGID/SID = 1067797   ← another group, survives kill(-1066968)
```

After the group's `kill -9`, the backend is left **orphaned, reparented to
init, and still listening** (confirmed with `ss`). Today this already
affects `nohup`, `setsid`, `pm2`, `docker run -d`, etc. It is a `Stop` bug,
not a ports bug, but it is a **prerequisite** for any piece that wraps the
child (see §6.1).

---

## 4. Evidence (measured, not estimated)

### 4.1 Available primitives

- `gopsutil v3.24.5` is already in `go.mod`; `PortOwnerPID` already uses
  `gopsnet.ConnectionsPid("tcp", pid)` (`internal/process/detect.go:42-52`).
- **`gopsutil v3 does not expose `Pgid()` on any OS** (verified in
  `process/process.go`): one must read `/proc/<pid>/stat` field 5 or use
  `ps`.

### 4.2 Discovery behavior (A)

Tests with `sh -c` + `setsid`, probing every 300 ms:

| Scenario | Result |
|---|---|
| Startup with error, immediate exit | detected at **300 ms** |
| Bind and death at 200 ms | detected at 300 ms, port already dead (verified with dial) |
| Slow bind (JVM/Spring, 3.5 s) | detected at 3.5 s |
| Immediate bind (grandchild `sh` → server) | detected at 300 ms |
| **UDP-only, never opens TCP** | **never detected** |
| Two listeners (metrics and http) | both detected |

**The risk of "waiting forever for a port" does not materialize, but only
because the loop checks the group's liveness on each iteration.** A loop
that only waits for "a port appears" stays until the timeout is exhausted.
It is a design requirement, not a detail.

### 4.3 Multi-port ambiguity

If the service opens `metrics` before `http` (measured: 1.5 s difference),
taking the **first port seen** returns the wrong one. Resolved in §6.4.

### 4.4 Cost per call

| Primitive | Cost |
|---|---|
| `gopsutil.Processes()` | 54–62 ms |
| `gopsutil.Connections("tcp")` | 29 ms |
| discovery by pgid (complete) | 61 ms |
| discovery by ppid lineage (complete) | 129 ms |
| **direct `/proc` snapshot (pid→ppid)** | **11 ms** |
| **`/proc/net/tcp` (LISTEN)** | **1.7 ms** |
| **complete discovery with direct `/proc`** | **13 ms** |

⇒ **Discovery must live in `start` / explicit refresh, NEVER in the TUI
tick.** With `gopsutil`, 20 projects at 1 Hz would be **2.5 s of CPU per
second**. If implemented, parse `/proc` by hand instead of `gopsutil`.

### 4.5 portless with non-Node backend

`portless` **is not a frontend tool**: it is a generic reverse proxy.
Verified with a Python backend:

```
portless api-a python3 server.py
  → injects PORT=4042, HOST=127.0.0.1, PORTLESS_URL=http://api-a.localhost:1355
  → route: http://api-a.localhost:1355 -> localhost:4042
```

Furthermore, it **auto-detects the git worktree and prefixes the branch as
a subdomain** (`https://fix-ui.myapp.localhost`) with no configuration.
Requires **Node 24+**.

### 4.6 How the route is registered: `portless alias` (corrected)

**CORRECTED MECHANISM.** This section used to say `PORTLESS_APP_PORT`; **it
is the wrong mechanism**, and the difference is not stylistic:

`PORTLESS_APP_PORT` is consumed by `portless run <cmd>`, where **portless
starts the child and owns the process** — the model the user rejected, and
which also breaks the lineage-based Stop of slice 1. The correct mechanism,
measured against portless 0.15.6, is to register the alias:

```
portless alias <name> <real port>     # add / upsert
portless alias --remove <name>          # removal (exit 1 if not found: benign)
portless list                           # read-back
```

```
portless alias fix-ui.api 39677
  → route: fix-ui.api.localhost -> localhost:39677
```

That is: **portless is used as a pure proxy**, without deciding the port or
starting anything. The overlap between "vroom reserves the port" and
"portless assigns it" disappears because portless no longer has anything to
assign.

**The process topology does NOT change**: the backend is started by vroom
and continues to run in its own process group. The lineage-based `Stop` fix
remains mandatory, and it is exactly what this mechanism preserves.

See `adr-0013-vroom-registers-portless-routes.md`.

### 4.7 Reservation race

`bind(127.0.0.1:0)` + `close` leaves the port free again before the child
starts: there is a **TOCTOU window** (verified). In the range 4000–4999 the
collision is improbable, but it must be documented and/or retried.

---

## 5. Decisions already made

1. **`portless` is kept.** Its proxy provides HTTPS with a local CA,
   subdomain-based names, CORS/cookies across subdomains, and HMR
   (websockets) — pieces that would have to be rewritten and that would
   also clash with vroom's `Stop` model.
2. **vroom owns the port and the lifecycle; `portless` is the proxy in
   front.** `PORTLESS_APP_PORT` is passed to it and `portless` does not
   decide.
3. **The user modifies their apps** to read `PORT` with a fallback to the
   default port. Validated pattern:
   ```bash
   PORT=${PORT:-8080}      # reads if vroom injects it, otherwise the default
   ```
4. **Node 24.15.0 will be installed globally** (today the `portless` shim
   does not resolve a version: mise's default is 20.19.0).
5. **The manifest schema is free.** Whatever is needed can be changed. The
   concrete proposal is in §6.2 and is **not** an open decision.
6. **`portless` is kept at full capacity.** It is the piece that provides
   stable URL, HTTPS with a local CA, and subdomain-based names. Replacing
   it with a native vroom proxy is not contemplated; it is noted in §8.3
   only as a future escape hatch.

---

## 6. Proposal

Three pieces, in this order. **The first is a prerequisite of the third.**

### 6.1 Piece 1 — `Stop` by lineage (prerequisite, and independent bug fix)

**What:** on stopping, `kill(-pgid)` is not enough. Build the set of
descendant PIDs of the registered PID (map `pid → ppid` from `/proc`) and
signal the group **and** the descendants that have re-sid. Iterate until the
lineage is empty or the timeout expires.

**Why:** today `Stop` leaves orphans of children that call `setsid` (`nohup`,
`pm2`, `portless`, `docker run -d`). It is a bug that already exists without
changing anything about ports.

**Side effect:** it makes lineage-based discovery possible and removes the
dependency on assuming "the server is in my pgid".

**Constraint:** the current case (normal group, no re-sid) must keep working
without regression.

### 6.2 Piece 2 — Dynamic ports: reservation + injection + discovery

**What:**

- `StartSpec` gains an `Env map[string]string` field (does not exist today).
- On starting in dynamic mode: reserve a free port in the range 4000–4999,
  inject `PORT` (and `HOST=127.0.0.1`) into the child's environment, and
  register the route with `portless alias <name> <real port>` if the
  manifest declares `route_mode` (§6.3). `PORTLESS_APP_PORT` is **not**
  passed: that variable is consumed by `portless run <cmd>`, where portless
  owns the process.
- **Discover and verify** the real port after startup, and persist it in
  `state.Meta.Port` (the field that the 4 uses of §3.2 already consume).
- The discovery loop **must** be bounded by: (a) deadline, (b) lineage
  liveness (fast failure ~300 ms if the process dies with an error), and
  (c) **stabilization window** before accepting a port (see §6.4).
- If after the deadline there is no TCP port, explicitly record **"no
  port"**, not silently exhaust the timeout (covers UDP-only, §4.2).
- Read `/proc` directly (11 ms) instead of `gopsutil.Processes()` (54 ms).
- Disambiguate the multi-port case per §6.4.

**Why:** it is the half that portless cannot do for us (discover and verify
the real port) and the one that makes the reservation work without
collisions.

**Additional care:** `killPortHolder` (`daemon_unix.go:218`) must **validate
ownership** before killing: check that the PID owning the port
(`PortOwnerPID`) has a coherent `creation_time` or belongs to our lineage.
With dynamic ports the risk of killing a recycled foreign process increases.

#### 6.2.1 Manifest schema (decided)

Today `port` (`manifest.go:43`) is overloaded: it means both *"the port the
app uses"* and *"the signal vroom uses to know if the service is alive"*, and
`0` means "disabled" (validation in `manifest.go:90`). That is what makes the
port *load-bearing*.

**Proposal: separate the two things, without breaking backward
compatibility.**

```toml
port = 8080                 # app's default port (fallback). int, same as today.
port_mode = "dynamic"       # "fixed" (default, current) | "dynamic" | "none"
```

- `port` retains **a single meaning**: the port the app uses when vroom does
  not start it. It is exactly the value the app uses in `PORT=${PORT:-8080}`,
  so the §5.3 pattern and the manifest are aligned.
- `port_mode` is a **pure addition**: without `port_mode` the behavior is
  today's, zero regression. `fixed` = vroom does not touch ports. `dynamic` =
  reserve + inject + discover. `none` = replaces the current `port = 0`
  (service without a port, e.g. a queue worker).
- No union type (`int` \| `string`) or custom TOML unmarshalling is needed.

**Compatibility:** the current `port = 0` is equivalent to
`port_mode = "none"`. It can be kept as a silent alias or warned via log;
implementation decision.

### 6.3 Piece 3 — `portless` as a pure proxy (decided, DELIVERED)

**Delivered** in `adr-0013-vroom-registers-portless-routes.md`.

**What:** integrate `portless` as a naming and TLS layer **in front**,
without letting it assign ports or start anything. On each `start`, vroom
registers the route `<branch>.<project>.localhost → 127.0.0.1:<REAL port
that vroom already knows>` with `portless alias`, **reads it back** to
confirm it is its own, and **verifies it against the live proxy** before
publishing it.

**Why:** it provides stable URL, HTTPS with a local CA, and subdomain-based
name resolution — which is what breaks CORS/OAuth/HMR if the user accesses
via `localhost:<port>`. With `route_mode = "named_with_auto_fallback"` the URL is truly
stable, which is what an OAuth callback or a CORS rule demands.

**What the measurement changed about this text:**
- vroom does **not** start the proxy. Explicitly rejected: starting it, or
  managing it as a visible service, reintroduces the `setsid` orphan from
  §3.4. If no proxy is reachable, vroom warns **once** and the service
  stays alive on its port.
- The proxy's port (1355) is **not** assumed: it is read from `proxy.port`,
  which only exists while it is running. Its absence **is** the signal that there is no proxy.
- There is **no** `prune` that cleans vroom's routes: `prune` does not touch
  alias routes (`pid: 0`, counted as active). That is why **reconciliation
  on every start is mandatory**: vroom is the only thing that can clean them.
- The add is an **unconditional upsert**: two vrooms with the same name
  silently overwrite each other. That is why the read-back is mandatory,
  not a nicety.
- The absence of portless is **never** a startup failure.

**Dependencies and risks:** Node 24+ (an old Node degrades to a warning, and
vroom does not touch the user's node configuration).

**No-goal:** that `portless` manages the app process's lifecycle, or the
proxy's. Its `setsid` is exactly what breaks vroom's `Stop` (§3.4). vroom
starts it and kills it; portless only routes.

### 6.4 Piece 4 — Multi-port disambiguation (decided, with evidence)

**Why it must be resolved:** a service can open multiple listeners — Spring
with `management.server.port`, a Prometheus exporter, the JVM debug port,
gRPC alongside HTTP, multiprocess workers. Measured: if the service opens
`metrics` **before** the main port, "first port seen" chooses the wrong one
(§4.3).

**Observation that simplifies the problem:** the ambiguity only exists when
vroom *guesses*. If the app honors `PORT` (§5.3 commitment), vroom already
knows in advance which the port is and only has to **verify it**. That is
why the ambiguity is resolved on the reservation path, not the main one.

**Rule, in order of preference:**

| Rule | Condition | Result |
|---|---|---|
| **R1** | the reserved port is among the listeners | that is the port. Deterministic, no heuristic. |
| **R2** | multiple listeners, the reserved one is not among them | probe `health_path` (which **already exists** in the manifest) on each candidate; the best response wins (200 > 2xx/3xx > 5xx > 404) |
| **R3** | multiple, tie in R2 (includes "not HTTP") | the **lowest port number** wins, deterministically. Mark the service as *unverified port*. |

**Measured evidence:**

| Case | Candidates | Result |
|---|---|---|
| Honors `PORT`, metrics opens **first** | `[41501, 42501]` | R1 → **41501** (reserved) OK |
| Honors `PORT`, main opens first | `[41502, 42502]` | R1 → **41502** (reserved) OK |
| **Ignores `PORT`**, metrics 404 on `/health`, main 200 | `[41510, 42510]` | R2 → **41510** OK |
| Tie: both 200 on `/health` | `[41520, 42520]` | R3 → **41520**, deterministic OK |
| **Not HTTP** (gRPC-like, raw sockets) | `[41530, 42530]` | R3 → fallback. **vroom cannot know which is primary** |

**Honest limitation:** for services that are not HTTP and also ignore `PORT`,
vroom has no way to know which listener is the primary one. Heuristic
resolution is a coin toss and **must not be presented as certainty**. Since
`health_path` already covers the HTTP case (the majority), and apps that
respect the §5.3 commitment are covered by R1, **no extra manifest field is
added** until a real case that needs it appears.

**Bug found while implementing the loop** (relevant for whoever writes it):
the first version of the probe closed with "no port" as soon as a sample
came back empty, that is **before the process had time to bind**. The
conclusion "there are no ports" is only valid (a) if the lineage is dead, or
(b) after a sufficient observation window without changes. Without that
second guard, a slow service is reported without a port.

---

## 7. Points that remain open

1. **What to do if the project does not accept `PORT`.** Discovery still
   works (§6.4), but the real port may differ from the reserved one. Define
   whether that is a startup error or just an informational warning.
   *Recommendation: warning, not error* — the service works, it simply did
   not seize the port that was offered to it.
2. **Reservation scope.** Fixed range 4000–4999 (proposed) or any free
   port. The range reduces the TOCTOU probability and facilitates manual
   debugging. *Not blocking: one can start with the range and expand later.*
3. **`portless` proxy lifecycle.** If vroom starts it, it must be decided
   whether it is managed as one more vroom service (appears in the list) or
   stays hidden as infrastructure. *Not blocking.*

None of this prevents starting with Piece 1, which is independent of ports.

---

## 8. Implementation context

### 8.1 Repository constraints

- Single local gate: **`make check`** (build + lint + test). Must be green.
- `go build ./... && go vet ./... && go test ./...` before considering
  anything done.
- CI runs `Test` with `-race`; the scanner assumes `fd` is installed.
- **The user's binary is `~/.local/bin/vroom`, not the repo's.** After any
  change it must be deployed: `go build -o ~/.local/bin/vroom ./cmd/vroom`
  (or `make install`). Without this, the TUI the user tests is still the
  old version.
- ADR convention in `docs/adr/` (see `adr-0011` for the format).

### 8.2 Touched code areas

- `internal/process/process.go` — `StartSpec.Env`, `StopSpec`
- `internal/process/daemon_unix.go` — lineage-based `Stop`,
  `killPortHolder` with ownership validation
- `internal/process/detect.go` — new discovery primitives
- `internal/manifest/manifest.go` — `port_mode`
- `internal/state/state.go` — `Meta.Port` becomes the real port
- `internal/cli/cli.go`, `internal/tui/*` — show real port + URL
- `internal/orchestrate/health.go` — `WaitForPort` with the real port

### 8.3 Emergency escape hatch (not chosen)

If Node were to become problematic in the future, a native proxy in vroom
(Caddy-style) would be the substitute: ~400 lines, same contract
(`name → port`). The rest of the design —reservation, injection, discovery,
`Meta.Port`— would not change, because vroom is always the owner of the
port. Noted so the decision is reversible.

---

## 9. Acceptance criteria

- [ ] Two worktrees of the same repo with the same `.vroom.toml` coexist,
      each with a distinct port, both `running` and both with their own
      stable URL.
- [ ] A service that dies with an error on startup is reported
      `stopped`/failure in bounded time (~<1 s), not after exhausting the
      timeout.
- [ ] A service that only opens UDP does not hang startup (marked "no
      port").
- [ ] A slow service (bind at 3.5 s) is reported with a port, not as "no
      port" (stabilization guard, §6.4).
- [ ] A service with two listeners chooses the primary one deterministically
      (R1 if it honors `PORT`; R2 by `health_path` if not).
- [ ] `Stop` leaves no orphaned processes, not even when the child calls
      `setsid` or `portless` re-sids it.
- [ ] Stopping a worktree does **not** kill its twin's process.
- [ ] Manual startup outside vroom keeps using the default port.
- [ ] An existing manifest without `port_mode` behaves exactly as today.
- [ ] `make check` green; binary deployed to `~/.local/bin/vroom`.

---

*Report generated after investigation with empirical validation (probes in
`/tmp/opencode/portprobe*` and `/tmp/opencode/mpprobe`). No repository code
was modified.*
