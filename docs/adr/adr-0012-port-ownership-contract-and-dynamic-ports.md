# ADR-0012 — Port ownership contract and dynamic ports per worktree

- Status: accepted
- Date: 2026-09-30
- Feature: `0011-feature-dynamic-ports`

## Context

A service's port was tied to the `.vroom.toml`, and the manifest is a
snapshot read once at scan time. That works while each project has its own
directory. With **N worktrees of the same repo** they all declare the same
`port`, only one starts and the rest collide with `EADDRINUSE` or, worse,
`Evaluate` considers them alive because "the port is open" — the twin's.

Added to that are two preexisting problems that the ephemeral port case
makes a wrong `fuser` much more dangerous:

1. `Stop` signaled `kill(-pgid)`, which **does not reach descendants that
   called `setsid`** (`nohup`, `pm2`, `docker run -d`, `portless`): they
   remain in another process group and keep listening.
2. `Stop`'s last resort was `fuser -k` on the port, without checking
   ownership, and the guard was `Pgid > 0 || Port > 0` with all four call
   sites preserving `Port` with `Pgid = 0`. Stopping an already-stopped
   service killed whoever held that port — normally the twin.

## Decision

1. **`port_mode` is an additive three-state field**, with `fixed` as the
   default. Absent means `fixed`, and `port = 0` remains a silent alias for
   `none` so as not to break any existing manifest.

2. **`port` retains a single meaning**: the app's default port, the same
   value as `PORT=${PORT:-N}`. Manifest and app are aligned by construction.

3. **In `dynamic`, vroom is the sole owner of the port.** It reserves a free
   one in `4000–4999`, injects it as `PORT` (along with `HOST=127.0.0.1`),
   and discovers and verifies the real port before returning control. The
   reservation carries a mutex and an in-memory set of already-issued ports:
   **within a single vroom process** two concurrent reservations never
   collide. Between distinct vroom processes the `bind`+`close` window
   remains open (see trade-offs).

   **The reservation has a lifecycle: it is acquired at start and returned
   at stop.** The reserved port is persisted separately from the real one
   (`Meta.ReservedPort`), because the two have different lifetimes: the real
   one is what is displayed and probed, the reserved one is what must be
   released. Without persisting it there is no way for stop to know what to
   return. All four stop paths (TUI, CLI, `stopService`, and
   `abortAndCleanup`) release it and set `ReservedPort = 0`, because a
   repeated stop reading a stale reservation would release a port another
   service has already taken.

   The set is **per process and dies with it**: a crash does not shrink the
   range permanently, the next vroom starts with an empty set. And a stale
   entry can only cause a genuinely free port to be skipped, never cause a
   collision, because `bind` is the truth about occupancy.
   Discovery is bounded by three things: deadline, **lineage liveness**
   (fast failure on the order of 1 s if the process dies), and
   **stabilization window**: the set of listeners must remain unchanged for
   500 ms before being accepted. An app that opens metrics in one goroutine
   and the main one in another produces two different samples, and accepting
   the first chooses the wrong listener.

4. **The real port is the only truth.** `meta.Port` becomes the port
   effectively listened on and the six named surfaces read it. What each one
   does with an **unconfirmed** port:

   | Surface | Unconfirmed port |
   |---|---|
   | badge, service view, dashboard | `port_unresolved` does not fall back to the declared one; shows "port unresolved" with no number |
   | Health tab | probes nothing and explains why |
   | stack health gate | `port_unresolved` is a non-fatal result; `port_pending` does fail the stage |
   | **CLI JSON** | `port` is `0`; the declared one is published separately in `declared_port` |
   | `meta.Port` on disk | `0`, with explicit `State` |

   Only when **no service is running** is the declared one used: stopped, it
   is the only information that exists. The declared one also lives in
   `declared_port`, so an agent can query the manifest's intent without it
   being confused with the real port.

5. **The child's environment is merged explicitly** with `os.Environ()`
   before injecting. In Go, `cmd.Env == nil` inherits and any non-nil slice
   **replaces** the entire environment.

6. **`Stop` signals the lineage, not just the group.** The real lineage is
   read from `/proc` and captured **before** signaling: when the root dies
   its children are reparented to init and the relationship is lost.

   And there are **two credible roots**, not one: with PGID the group is
   killed and the re-sids by their lineage; with only PID, with no group to
   kill, the root and its lineage are signaled. Both share the SAME
   escalation ladder, so they cannot diverge in semantics. `StopSpec.Pid`
   is documented as the lineage root: as a kill target it was decorative,
   because the whole block was under `Pgid > 0` and a spec with only PID did
   absolutely nothing. With no credible root, `Stop` remains a no-op and
   the port fallback does not fire: the ownership test is not weakened.

7. **The port ownership guard fails closed.** `fuser -k` only runs if there
   is **a single known owner** and it **belongs to the service's lineage**.
   Zero owners (permissions, unreadable `/proc`), multiple owners (same
   number on IPv4 and IPv6), or a foreign owner: nothing is killed and a
   warning is emitted. The warning travels via `StopSpec.Warn` to the
   service log and the TUI.

8. **Undetermined owner does not resolve to "alive".** `PortOwnerPID`
   returns 0 on ambiguity and `Evaluate` degrades to undetermined instead of
   applying the optimistic verdict.

9. **Discovery reads `/proc` directly** (11 ms) and crosses
   `/proc/net/tcp{,6}` with `/proc/<pid>/fd` of the lineage.
   `gopsutil.Processes()` costs 54–62 ms measured on this machine, and
   discovery **does not live in the TUI tick**.

10. **"Port pending", "no port", and "port unresolved" are three named
    states** (`port_pending`, `no_port`, `port_unresolved`), declared in both
    `state` and `process`. Pending is not disguised as healthy with the
    generic spinner, and all three remain stoppable.

11. **Discovery deadline expiry does not prove there is no port.** A service
    that takes 12 s to come up and a UDP-only one look the same for 12 s.
    That is why there is a second bounded window
    (`DefaultDynamicUnresolvedGrace`): if the port appears there, it resolves
    as always and does not remain unresolved. Only if not even a single
    listener appears within deadline + grace is `no_port` asserted. And when
    there are listeners but none can be declared primary, the result is
    `port_unresolved`, which is not the same as "has no port".

12. **"Pending" and "unresolved" do not share a verdict.** In-flight
    discovery is still a real gate (`ErrPortPending`): a stage cannot be
    considered good with the port undecided. But `port_unresolved` is
    terminal —discovery has already finished— and it is a **third non-fatal
    result** (`PortUnresolved`), distinct from `PortNone`. Treating it as an
    error chained all the way to `abortAndCleanup`, which shut down healthy
    siblings; and its message said "pending", telling the user to wait for
    something that had already finished.

    Port states also count as "the process is up": a live service with the
    port pending, without a port, or unresolved is NOT restarted on every
    launch. Only `running` was "alive" before these states existed, and
    restarting a healthy service for not having the port closed is a failure
    in itself.

13. **A port vroom has not verified is not a probe target.** With the port
    unresolved, `displayPort` returns 0 instead of falling back to the
    declared port, and the Health tab emits no probe. Probing the declared
    one can reach another worktree's twin port, and presenting it as one's
    own is worse than showing nothing.

## Consequences

### Positives

- The same `.vroom.toml` serves N worktrees at once.
- Stopping a worktree does not touch the twin, and a `Stop` no longer leaves
  orphaned listeners from re-`sid` descendants.
- Failure is visible and local: the ownership warning names the port and the
  process that could not be proven.
- Startup failure is fast and with a distinct cause: a dying service is
  reported in ~1 s instead of exhausting the discovery timeout.

### Negatives / trade-offs

- Startup in `dynamic` is extended by however long the process takes to bind
  (up to ~3.5 s in the test case). The spawn→`SaveMeta` window is no longer
  sub-millisecond.
- **Reservation TOCTOU:** `bind` + `close` returns the port to the pool
  before the child starts. That window is **not** improbable in the dominant
  case. Fixed: vroom-against-vroom collision within the same process is not
  a rare case but the normal one, because `toggleNode` returns `tea.Batch`
  (bubbletea runs commands in parallel) and `Launch` starts each service of
  a stage in its own goroutine. Measured before the fix: **99.5 %** of
  collisions between concurrent reservation pairs, because all of them
  entered through the first free slot in the range. Now there is a mutex and
  an in-memory set of issued ports (`ReservePort` / `ReleasePort`), so that
  **two concurrent reservations in the same vroom process never return the
  same port**, and a failed attempt returns its own.
  What is **not** protected: two **distinct** vroom processes. Each has its
  own set and both do `bind`+`close` on the same kernel pool. That is the
  window that remains open; mitigating it would require socket passing or a
  lock file, and the child is `sh -c`, so there is no one to pass the
  descriptor to. An `EADDRINUSE` in that case is reported by the app, not
  vroom.
- An environment variable is needed in the app (`PORT=${PORT:-8080}`). An
  app that does not honor it works, but there must be a warning and its real
  port must be discovered.
- Failing closed can leave a genuine orphaned listener alive after a stop.
  It is a conscious cost: silent damage between worktrees is worse than a
  visible orphan plus a warning.
- `/proc` is Linux-specific. The `xxxAt(root, pid)` convention leaves the
  reader injectable, but a Windows implementation needs another reader.

### Documented limitations (not guarantees)

1. **Non-HTTP app that also ignores `PORT`:** vroom has no way to know which
   listener is the primary one. The lowest-numbered one is chosen,
   deterministically, and the service is marked as unverified: in the TUI
   the port appears unconfirmed, and in the JSON `port` carries the chosen
   number with `port_verified: false`. That `false` **is emittable** because
   the field is a tri-state `*bool`, not a `bool` with `omitempty` — which
   was precisely what made the mark invisible on the surface that agents
   read.
2. **Listeners that open more than 500 ms apart:** the stabilization window
   covers the typical two-goroutine opening. If the set does not stabilize
   within the deadline, the result is not an invented port but
   `port_unresolved`: there are listeners, it is not known which is primary,
   and that is stated. It is a non-fatal result. An app that opens a primary
   listener seconds after another ends up with listeners that never
   stabilize and therefore with no declared port, not with the wrong one.
3. **UDP-only service:** no discoverable TCP port. "No port" is recorded
   (`no_port`), never a hang. The assertion rests on the absence of
   listeners during deadline + grace (16 s by default), not on a positive
   test: a service that takes longer than that to open its port ends up in
   `port_unresolved` and the user is told, instead of being labeled as "no
   port".
4. **Duplicate hard bind** (the app ignores `PORT` and does a literal bind):
   it remains an app startup failure. vroom reports it faster and better;
   it does not prevent it.
5. **The port can change between ticks.** `p.Manifest` is a scan snapshot and
   `meta.Port` is read from disk on each tick. Accepted. The sub-guard is
   firm: a probe and the state refer to the same process; "alive and
   healthy" is never reported with a port from another generation.
6. ~~**Missing or incompatible `portless`** is a normal, non-fatal
   condition.~~ **CLOSED by
   `adr-0013-vroom-registers-portless-routes.md`.** It remains non-fatal,
   and is now additionally the *implemented* behavior and not an intention:
   vroom registers the route with `portless alias`, verifies it against the
   live proxy, and without portless —or without its proxy, or with a broken
   or hung binary— the service still starts, stays healthy, and the user
   receives a warning. A service's health never depends on its route
   existing.

## Alternatives considered

- **`int | string` union in `port`** (`port = 8080` vs `port = "dynamic"`).
  Rejected: a field with two types breaks the compatibility of every
  consumer that already reads it as `int`, and makes "no port" no longer
  representable. The additive field keeps `port` with a single meaning and
  makes backward compatibility trivial by construction.
- **`gopsutil` for lineage and listener discovery** (54–62 ms per snapshot
  versus 11 ms for direct `/proc`, and `Pgid()` **does not exist in gopsutil
  v3.24.5 on any OS**). Rejected: the tick already pays one `Evaluate` per
  project every 2 s; adding 5× the cost would turn it into per-second work
  with N projects. The repo's injected-root convention is preserved
  (`xxxAt(root, pid)` with `procRoot`) so tests use synthetic fixtures
  instead of real `/proc`.
- **Kill-vs-ask in the ownership guard.** The alternative was to continue
  with unconditional `fuser -k`. Failing closed was chosen: asking the user
  on every stop with a busy port breaks the TUI flow, and killing blindly is
  exactly the damage this ADR eliminates.
- **Re-resolving the port on every tick** to track the change between runs.
  Rejected: the manifest snapshot is cheap and the change between ticks is
  rare; measuring again on the tick would turn an 11 ms per-service
  operation into constant work. Limitation 4 is accepted.
