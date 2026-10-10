# ADR-0014 — One `url_generation` axis for how a service is addressed

- Status: accepted
- Date: 2026-10-10
- Feature: `0014-feature-url-generation`
- Supersedes: `adr-0013-vroom-registers-portless-routes.md`

## Context

`adr-0012` introduced `port_mode` (`fixed` | `dynamic` | `none`) + `port`; `adr-0013`
added `route_mode` (`off` | `auto` | `named_with_auto_fallback`) + `route_name`. On
paper the two axes were orthogonal — *who* picks the port, and *whether* the service
is published under a hostname — but together they described **one** decision: how the
started service is addressed. Only five combinations make sense, and the two fields
could express combinations that do not:

- `port_mode = "fixed"` + `route_mode = "auto"` is meaningless: there is no changing
  port for a stable name to hide.
- `port_mode = "none"` + `route_mode != "off"` is contradictory: there is no port for
  the route to point at.
- Validation had to encode the exclusions by hand: `route_mode != "off"` requires a
  port in some mode, and `route_name` is rejected unless
  `route_mode = "named_with_auto_fallback"`.

The friction had a visible symptom. The TUI's `s` menu (gitdash's arm-then-choose
pattern) offered start modes the manifest could not state, while the manifest could
state combinations the menu could not reach. And the `route_mode = "off"` default kept
a second vocabulary alive — "the portless world" vs "the non-portless world" — that
leaked into the port semantics, even though whether portless is involved is a
*consequence* of the address choice, not a choice of its own.

The port vocabulary is fully determined by the address choice: bind a declared port,
take an ephemeral port behind a hostname, or have no port at all. Keeping it as a
second declared axis is exactly what allowed the two fields to drift apart.

## Decision

1. **One top-level key replaces both axes: `url_generation`.** It is a hard rename
   with no aliases. The values are exactly:

   | `url_generation` | What vroom does |
   |---|---|
   | `by_port` | Binds the manifest's declared `port`; the address is `localhost:<port>`. Publishes no portless route. The start is **refused while the port is occupied**. |
   | `by_hostname` | Ephemeral port (injected as `PORT`, with `HOST=127.0.0.1`) + registers `route_name` in portless. The start is refused **only if a proven foreign holder keeps that name**; the service reclaims its own stale route. |
   | `by_workspace_hostname` | Ephemeral port + `<branch>.<project>` hostname; no `route_name` needed. |
   | `by_hostname_or_workspace` | Claim ladder: `route_name` first; if another worktree holds it, `<branch>.<project>` instead of failing. |
   | `none` | Headless: no port, no URL. vroom does not even look for the portless binary. |

2. **`route_name` stays a top-level key.** It is the stable hostname OAuth callbacks
   and CORS rules need, and it is required by `by_hostname` and by
   `by_hostname_or_workspace` — the two generations whose address *is* the name. A
   manifest that declares `route_name` without one of those generations is rejected at
   parse time, because it would otherwise be either ignored or applied by accident.

3. **The port vocabulary is derived, never declared apart.** `by_port` is the fixed
   mode (bind the declared port); the three hostname generations are the dynamic mode
   (vroom reserves an ephemeral port, injects it as `PORT` with `HOST=127.0.0.1`, and
   discovers the real one before returning control); `none` has no port. The internal
   port-mode vocabulary survives only as the language the port-wait and display code
   already speak; it is no longer a manifest field.

4. **`[worktrees].url_generation` overrides the top-level value for a worktree copy.**
   Absent means a worktree inherits the top-level value. This is what lets one
   committed manifest serve `main` one way (for example `by_hostname`) and every
   worktree another (the ladder) without editing a per-copy file.

5. **Absent `url_generation` keeps the historical behavior.** A declared `port > 0`
   means `by_port`; a `port` of 0 (or absent) means `none`. A manifest that never heard
   of this key behaves exactly as it did before the two axes existed. An *explicit*
   non-`none` generation with `port = 0` is rejected, because the declared port is the
   app's `PORT=${PORT:-N}` fallback and the generation promises an address that depends
   on it.

6. **The TUI start arms a selector; the choice is recorded and outranks the manifest.**
   Pressing `s` on a **stopped** service does not start it: it arms the
   start-generation selector. The second key picks — `p` = `by_port`, `u` =
   `by_hostname`, `w` = `by_workspace_hostname`, `f` = `by_hostname_or_workspace`; any
   other key cancels. The chosen generation is recorded in the service's state, and the
   resolution order at every later start is: explicit per-start override (the armed
   choice) → the recorded choice → the worktree-aware manifest default. So `R` in the
   TUI, `vroom start`, and stack launches all agree with the last TUI choice, and the
   manifest's value is the default only until a choice is recorded.

7. **Switching to a generation that publishes nothing retires the route.** A service
   that moves from a hostname generation to `by_port` or `none` revokes the route a
   previous start registered. Retirement is fail-closed and no-ops without an unrevoked
   ownership lease, so a name another worktree now holds is never deleted. Only a
   **clean** retirement clears the ownership handle; a warning leaves it in place so a
   later start can reconcile.

8. **Refusals happen before anything spawns, and only on proof.** `by_port` refuses an
   occupied port in preflight — the neighbour's listener is not ours, and mistaking it
   for the service is the bug that made two projects read as running. `by_hostname`
   refuses a name kept by a **proven** foreign holder: its URL must not move, and
   falling back is exactly what `by_hostname_or_workspace` exists for. A broken or
   missing portless makes the holder **unprovable**, so it degrades to a warning and
   never fails the start.

9. **The CLI JSON carries the one axis.** Each `vroom list` / `vroom status` row
   publishes `url_generation` — the recorded choice, else the worktree-aware manifest
   default — and the former `port_mode` / `route_mode` fields are gone. `route` remains
   the *result* of a hostname generation (`name`, `status`, verified `url`), and is
   absent for `by_port` and `none`, which neither claim nor deny a route.

## Supersession

This ADR supersedes `adr-0013` in its **manifest-axis decisions**: the `route_mode`
enum and its `off` / `auto` / `named_with_auto_fallback` vocabulary are replaced by
the hostname values of `url_generation`, and the `port_mode` axis of `adr-0012` is
retired as a manifest field. The route **mechanics** of `adr-0013` remain in force
unchanged — vroom only registers the route and never starts or manages the proxy;
registration is an ownership grant, revocation happens on stop, reading back and live
verification are mandatory, reconciliation on startup cleans orphans, and cleanup is
fail-closed. Only the names the user writes, and the fact that the port and the address
are now one decision, change.

## Consequences

- Positive: one vocabulary for the manifest, the TUI menu, the persisted state and the
  JSON; the port can no longer contradict the address, because it is derived from it;
  the TUI can offer exactly the modes the manifest can express, and vice versa; the
  two-worlds split disappears.
- Negative / trade-offs: **a breaking `.vroom.toml` rename.** Every manifest that used
  `port_mode` or `route_mode` must be migrated (see the migration table below). A
  service that used to be headless must now state it: `port = 0` still resolves to
  `none`, but a manifest that leaned on `port_mode = "none"` has no port-based fallback
  to lean on.
- A service that used `route_mode = "auto"` maps to `by_workspace_hostname`;
  `named_with_auto_fallback` maps to `by_hostname_or_workspace`; `fixed` maps to
  `by_port`; `dynamic` without a route maps to `by_workspace_hostname` — an ephemeral
  port still needs an address, and the branch hostname is the one that needs no
  `route_name`.

## Migration

| Before | After |
|---|---|
| `port_mode = "fixed"` (+ `port`) | `url_generation = "by_port"` (or drop the key: a declared port already means `by_port`) |
| `port_mode = "dynamic"`, no route | `url_generation = "by_workspace_hostname"` |
| `port_mode = "none"` / `port = 0` | `url_generation = "none"` |
| `route_mode = "auto"` | `url_generation = "by_workspace_hostname"` |
| `route_mode = "named_with_auto_fallback"` (+ `route_name`) | `url_generation = "by_hostname_or_workspace"` (+ `route_name`) |
| `route_mode = "off"` | drop the key (or `url_generation = "by_port"` / `"none"`) |

The rule of thumb: remove `port_mode` and `route_mode`, declare the single
`url_generation`, and remember that a service that used to be `none` must declare
`url_generation = "none"`.

## Alternatives considered

- **Keep both axes and tighten validation.** Rejected: validation can only forbid the
  contradictory combinations after the fact; it cannot remove the two-worlds split, and
  the TUI menu would still not map one-to-one onto the manifest.
- **Add `url_generation` as an alias while keeping `port_mode` / `route_mode`.**
  Rejected: aliases are exactly how two worlds survive — two spellings of one decision,
  one of them always slightly out of sync. The rename is hard on purpose, and the parse
  error names the new key.
- **Keep `port_mode` and derive `route_mode` (or vice versa).** Rejected: whichever
  field survives has to encode the address anyway, so the derived one becomes
  redundant — and the redundant field is free to drift.
- **A single string enum `url = "" | "auto" | "my-name"`.** Rejected in `adr-0013` and
  still rejected: an ambiguous string enum documents its own type in the schema, and
  `route_name` is a separate concern (a name, not a mode) that the ladder needs as its
  first rung.
- **A separate `[worktrees]` section per generation rather than one override key.**
  Rejected as over-configuration: the only per-copy difference the design needs is
  which generation a worktree uses, and one key states exactly that.
