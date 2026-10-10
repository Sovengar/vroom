# ADR-0013 — vroom registers routes in portless without owning the proxy

- Status: accepted
- Date: 2026-09-30
- Feature: `0001-feature-portless-alias-routes`
- Closes: `adr-0012` limitation 6
- Superseded in part by: `adr-0014-url-generation-single-axis.md`

> **PARTIALLY SUPERSEDED by `adr-0014-url-generation-single-axis.md`.** The
> `route_mode` enum and its `off` / `auto` / `named_with_auto_fallback` vocabulary
> were replaced by `url_generation`'s hostname values, and `port_mode` was retired
> as a manifest field. The route **mechanics** described below — vroom only
> registers the route, ownership grant and revocation, read-back, live
> verification, reconciliation, fail-closed cleanup — remain in force unchanged.
> Read the mode names below as their `url_generation` equivalents: `auto` →
> `by_workspace_hostname`, `named_with_auto_fallback` → `by_hostname_or_workspace`.

## Context

With slices 1–3 (`adr-0012`), vroom owns the port: it reserves it, injects it
as `PORT`, discovers and verifies the real one, and persists it. The app process is
its own. That is resolved.

What remains open is the **address**. The port is still ephemeral, so
any external reference to the service —OAuth callback, CORS rule, README,
bookmark, another app's proxy config— is tied to a number that changes on
every startup. `adr-0012` closed this as «separate slice (S4)» and as *«`portless`
absent or incompatible is a normal, non-fatal condition»*.

That slice arrives with two constraints already decided by the user, which are not
relitigated here:

- **vroom only registers aliases.** It does not start, manage, monitor, or display
  the proxy. Both alternatives were explicitly rejected: vroom auto-starting the
  proxy, and managing it as a visible service in the TUI — which also reintroduces
  the Stop-by-lineage recursion, because the proxy's `setsid` is exactly what slice 1
  fixed.
- **The absence of portless degrades to a warning, never an error.** It is the same
  fail-closed posture that governs dynamic port changing: when vroom cannot prove
  something, it does not assert it.

What was missing was evidence. No line of code in vroom mentioned portless.
Everything below is **measured** against portless 0.15.6 / Node 24.15.0, partly
on an **isolated** proxy (`PORTLESS_STATE_DIR` in `/tmp`, `PORTLESS_PORT=1399`,
`PORTLESS_HTTPS=0`, `PORTLESS_SYNC_HOSTS=0`) so as not to touch the user's real
proxy. **This ADR accepts no risk**: every hazard is measured or eliminated by
design.

## Measured facts

| # | Fact | Why it matters |
|---|---|---|
| M1 | **`alias` is a pure write to the state file: it never contacts the proxy.** With the proxy down it exits `exit 0` and writes the route anyway | `exit 0` does **not** prove that the URL resolves. It changes the entire design |
| M2 | Reading `routes.json` back only proves that we wrote | Verification must be **against the live proxy** |
| M3 | The route **survives a proxy restart**: `kill -TERM` → `curl 000` → `proxy start` → `routes.json` unchanged, `doctor`: `ok Routes: 1 active route` | Registering even when the proxy is stopped is **persistent and correct** |
| M4 | Writing an alias **does not harm live routes** from `portless run`: the route with its own `pid` remains present, its process remains alive, it keeps serving | vroom can share a proxy with portless |
| M5 | **`prune` does not touch alias routes** (`pid: 0`, counted as `active`) | **vroom is the only thing that can clean them** → Mandatory reconciliation |
| M6 | **`proxy.port` only exists while the proxy runs**; stopping it makes it disappear | Its absence **is** the signal that there is no proxy. Never assume `1355` |
| M7 | A name with a dot is accepted and preserved literally | The `<worktree>.<project>` scheme works as-is |
| M8 | **Unconditional upsert**: same name + different port → `exit 0`, silently overwrites. `--force` changes nothing observable. No conflict detection | Reading back is mandatory, not polish |
| M9 | The destination port does **not** have to be listening; the proxy responds `502` until it appears | Registering **after** discovering costs nothing |
| M10 | `--remove` of a nonexistent name → `exit 1` | **Benign**: a repeated stop is not an error |
| M11 | An app that **fails** under `portless run` prints the URL but **does not register a route** | The exact mirror of correct behavior |
| M12 | `portless get` **prefixes the current git branch** | Useless for verification |
| M13 | portless derives its worktree prefix from the **branch**, not the directory | The native convention is unstable under `git branch -m` |
| M14 | `env -i PATH=/usr/bin:/bin` does **not** resolve `portless` (via mise shims) | A bare `portless` cannot be assumed |
| M15 | There is no HTTP administration API (`/`, `/health`, `/api/routes`, `/routes`, `/status` → `404`) | The proxy port is read from `proxy.port` |
| M16 | **`$PORTLESS_HOME` does not exist**: the CLI honors `$PORTLESS_STATE_DIR` and not `$PORTLESS_HOME` | The seam must resolve the same directory as the binary, or vroom reads `proxy.port` from one place and the binary writes `routes.json` in another |
| M17 | A hostname with underscore, space, colon, or accents is **rejected** (exit 1), and one with a slash is **silently truncated** (`Feat/My_Branch.proj` → `feat.localhost`) | vroom must sanitize the derived name: a git branch is full of underscores and slashes |
| M18 | The proxy responds **404 to a host it does not know** and **502 to one it routes with the backend down** | 404 and 502 are not the same category; 404 contradicts routing, 502 proves it |

## Decision

1. **vroom registers; the proxy routes.** The mechanism is
   `portless alias <name> <real port>`. vroom does **not** pass `PORTLESS_APP_PORT`:
   that variable is consumed by `portless run <cmd>`, where portless starts the child and
   owns the process — the model the user rejected, and which also breaks
   slice 1's Stop-by-lineage.

2. **`route_mode = "off" | "auto" | "named_with_auto_fallback"`, default `off`,
   plus `route_name`.** Pure addition, with the same form and treatment as
   `port_mode` + `port`: the first
   field has a single meaning, the second retains its own, and a manifest
   that declares nothing behaves exactly as today — with `off`, vroom does not even
   look for the binary. `route_mode != "off"` requires a port in some mode;
   `route_name` without `named_with_auto_fallback` is rejected.

3. **The name is derivable and configurable.** `auto` gives a URL derived from the
   **branch** without writing anything; `named_with_auto_fallback` claims the
   **stable** URL that an OAuth callback or a CORS rule demands and, only when another
   worktree already holds it, falls back to the branch-derived one. Both are needed
   because portless's native convention (M13) derives from the branch and changes with
   a `git branch -m`.

   > **REAL SCOPE OF `auto`.** This text said "own URL for each worktree",
   > and that is false: `DeriveName` receives the branch and the project, **not**
   > the worktree path. `auto` is **branch** scope. Two worktrees on the same branch
   > derive the same name —two clones on `main`, or a
   > `git worktree --force` over an already-used branch— and the second does NOT get a
   > second address but a **clean conflict**, with the first one's route intact
   > (§5, decision 5). The ladder mode is the answer, and not an aesthetic option:
   > its first rung is unique by construction. The ADR was already honest in citing M13; the
   > overassertion was in the code comment and the README.

   > **CORRECTION (claim ladder).** The mode was originally specified as `named`, and
   > its second claimant got a clean `route_conflict` and published **no** URL at all:
   > fail-closed, and precisely backwards for the case the mode exists to serve — two
   > worktrees running at once, of which only one can hold the stable name. The mode is
   > now `named_with_auto_fallback` and a conflict **advances** to a second candidate,
   > `<branch>.<project>`; only when both candidates are held does the old fail-closed
   > degradation remain. Start order decides who owns the stable name (starting a
   > worktree first is how you choose), and the fallback warning names the **port**
   > holding it, so the swap is visible rather than silent. Two consequences that are
   > part of this correction, not follow-ups: §8's rename test becomes *membership in
   > the candidate set* (a persisted fallback name is not a rename, and deleting it
   > would destroy the route this very start is about to reuse), and a service that
   > switches rungs **retires the name it walked away from** — stop only revokes the
   > name actually held, so without that retirement the old route would outlive the
   > handle and block the next worktree falling back to it.

4. **Registration happens after discovery and before persisting the final `Meta`.** Per
   **M9**, registering earlier only buys a `502` window; registering later
   also guarantees that a route is never published for a `port_unresolved`
   or `no_port` service. The single hook point is
   `internal/startsvc`, through which TUI, CLI, and stacks already pass.

5. **Reading back is mandatory — and happens BEFORE writing.** Per **M8** the
   registration is an unconditional upsert, so writing first and reading after
   is a **tautology**: the table would compare the newly written port against itself
   and could not distinguish our own route from another's. That is why
   the name is **queried before writing** and, if it is held by a port that is
   neither ours nor the persisted one, **nothing is written for that candidate**: it
   yields a `route_conflict` (which the ladder of §3 may take to its next rung) and
   the foreign route remains intact. It is the same fail-closed
   that governs cleanup, applied to registration.

   After registration it is read back as well, to cover the window between the
   query and the write.

   > **CORRECTION (review).** This decision said "after registering, vroom reads the
   > route", and that was a tautology in the common case. The R1 in `plan.md` was
   > marked *eliminated* and was not: the evidence for R1 is the **order**, not
   > the read-back.

   The distinction between "owned by another" and "ours, and the app restarted on a
   different port" is given by `portless.Ownership`: not a port, but the fact that
   the route is still OURS.

   > **CORRECTION (review): `prevPort` was a capability with no expiry.**
   > The first version passed the persisted port and compared it like this:
   > `found && existing != port && existing != prevPort`. A persisted number does not
   > expire —stop did not clean `RouteName`/`RoutePort`—, so the grant
   > survived the route that had authorized it. The sequence:
   >
   > ```
   > Apply("main.proj", 4321, 0)       -> registered, persisted 4321
   > Remove("main.proj")               -> the route disappears
   > another owner registers main.proj  -> 4321
   > Apply("main.proj", 5000, 4321)    -> registered, and the other's route moves to 5000
   > ```
   >
   > that is: another's route destroyed and no conflict. What was missing was not the
   > port but **whether we are still the owner**. Now the Meta persists
   > `RouteOwned`, which is granted on registration and **revoked on removal**, and the
   > predicate is `!prev.Authorises(existing)`.
   >
   > **Named residual, because an unnamed residual is how this HIGH was born:** the
   > handle (`RouteName`/`RoutePort`) is deliberately preserved even when ownership is
   > revoked. That way, if a removal fails, reconciliation still has somewhere to
   > look; and if the name is later taken by another, ownership is already revoked and
   > authorizes nothing. Handle and ownership do not contradict each other: the handle
   > says WHERE to look, ownership says WHETHER it can be overwritten. What would
   > NOT be done is cleaning the handle on removal: it would turn an orphan route into
   > something only startup reconciliation could recover.

6. **And furthermore, it is verified against the live proxy.** This is the decision
   that M1 and M2 force to be written and one this slice could not do without:

   - The proxy port is read from `proxy.port`. Per **M6**, if the file **does not
     exist**, that **is** the warning that there is no proxy → it degrades. It
     **never** falls back to an assumed port.
   - **One** request is sent to the proxy port, with the `Host` (and TLS SNI) set to
     the route's hostname, over the loopback address — without depending on name
     resolution. **Any** HTTP response proves that the proxy routes that route,
     **including `502`**: a `502` says "the proxy routes and the service behind does
     not respond", which is different information from "the proxy does not serve the
     route". Only a connection failure or timeout means there is no proxy serving.
   - If nothing responds: `degraded`, and **no `url` is published**. Per **M11** it is
     the same criterion portless applies when an app fails.

   Per **M3**, registering with the proxy down is persistent and correct: the route is
   served when the proxy returns. Verification decides **what is published**, not
   whether it is **registered**.

7. **The URL scheme is determined by probing, not assuming.** `https` is tried and,
   if it does not respond, `http`; the one that responded is published. **This way
   the TLS / port 443 case is not a risk**: the design contains no assumption that
   TLS could refute, and it did not need to be measured. The cost is one probe.

8. **Reconciliation on every startup is mandatory.** Per **M5**, `prune` does not
   touch alias routes, so **vroom is the only thing that can clean them**. On
   every startup vroom takes the routes that **it itself** persisted and, for each
   one, reads it against the live proxy:

   - the persisted name ≠ the one derived now → if the old one does not respond, it
     is removed and the new one is registered (covers `git branch -m`);
   - the persisted route no longer responds → it is removed (covers what a vroom that
     died without stopping the service left behind);
   - responds with a different port → conflict, a warning is issued, **nothing is
     registered on top**;
   - responds and is ours → it is left alone (idempotence).

   > **CORRECTION (claim ladder, §3).** "The one derived now" is the whole candidate
   > set: a persisted name that is still a candidate is a rung this start may reuse,
   > not a rename, so membership — not inequality — is the removal test. And retiring
   > the candidate the ladder walked away from is a separate step (§3), because it may
   > have to remove a **live** route, which the fail-closed rules of this section
   > deliberately never do.

   **A route that responds and is not ours is not removed: a warning is issued.** The
   fail-closed that governs all port changing also applies to cleanup: deleting
   something foreign is worse than leaving an extra route.

   This is what makes "a vroom that dies leaves routes pointing at dead ports" not a
   limitation but a mechanism with recovery.

9. **Neither the state dir nor the `PATH` is hardcoded.** State dir: `$PORTLESS_STATE_DIR`
   → `$XDG_STATE_HOME/portless` → `$HOME/.portless`. Binary: `$PORTLESS_BIN` →
   `exec.LookPath` → known shim directories. Per **M14** a bare
   `portless` cannot be assumed; and vroom runs under a service manager whose
   environment is not the user's login shell, so a path that works in the user's
   shell and fails in the daemon is a bug, not a configuration.

   > **MEASURED CORRECTION (M16).** This text said `$PORTLESS_HOME` as the first
   > step. That is incorrect: against portless 0.15.6, the CLI **ignores**
   > `$PORTLESS_HOME` and honors `$PORTLESS_STATE_DIR`. Implementing the order
   > documented here produces two different views of the same state — vroom reads
   > `proxy.port` from one directory and the binary writes `routes.json` in another—
   > and the symptom is a route that gets registered and then **cannot be removed**. A
   > seam that resolves a path the tool does not resolve is not an advantage: it is a
   > silent failure mode. The correct order is the one above.

10. **Every call is bounded by a timeout.** An unbounded `exec` against a hung binary
    hangs startup, and that would indeed be a loss of availability — degradation can
    never be worse than not having the feature.

11. **The seam is injected and the suite is hermetic.** The CI runner does not have
    portless, nor Node 24, nor a proxy. Integration lives behind a seam, in the
    same form as the `procRoot` already used by `/proc` reads. There are
    **three** integration tests, marked with `VROOM_PORTLESS_INTEGRATION=1` and
    skipped without real portless: register/remove against the real CLI, M3 (the
    route survives a real proxy restart, stopped by PID) and M4 (writing an alias
    does not evict a live app from `portless run`).

    > **CORRECTION (review).** This text said "at most one integration test". The
    > growth is fine —the three cases that cannot be observed with a double are the
    > ones that most deserved a test—; what was wrong was the document, which
    > described a ceiling that no longer exists.

    > **CORRECTION (CI integration job).** Two claims above stopped being true.
    > *"The CI runner does not have portless, nor Node 24, nor a proxy"* now
    > describes only the **Test** job: a dedicated **Integration** job installs
    > Node 24 and `portless@0.15.6` — pinned, the exact version every MEASURED
    > fact in this ADR was taken against — and runs the gated tests against a real
    > proxy in an isolated `PORTLESS_STATE_DIR`. And the count is **five**, not
    > three: the removal one (`TestReleaseIntegrationRemovesForReal`) and, since
    > the claim ladder, `TestLadderAgainstARealPortlessProxy`, which is the only
    > place the ladder meets the tool that has to execute it: the conflict comes
    > out of the real `routes.json` (proving `portless alias` would have silently
    > overwritten the first worktree's route), the fallback publishes its own
    > verified URL while the stable one keeps answering, and `Retire` deletes the
    > abandoned name for real.
    >
    > Hermeticity did not change: without `VROOM_PORTLESS_INTEGRATION=1` every one
    > of them skips, and no test ever touches `~/.portless`. What is new is
    > `VROOM_PORTLESS_INTEGRATION_STRICT=1`, set only by that job, which turns an
    > environment skip (no binary, a proxy that never opened its port, a live app
    > that never got served) into a **failure** — a green job that silently skipped
    > every integration test tests nothing. The job also reaps the proxy it
    > started by PID, because an isolated proxy outliving its test is a leaked
    > process, not a fixture.

12. **The JSON contract has three states, and the absent one is also one.**
    `route_mode` is the **intention**. `route` (`*RouteInfo`) is the
    **result**: `name` (hostname *intended*, whether or not it succeeds), `status`
    (`registered` | `degraded`), `url` (**only if `registered`**, and only with the
    verified scheme) and `reason` (only if `degraded`). Lesson from
    `port_verified`, applied in full: **a URL that no one verified is not published.**

13. **Removal on stop, alongside `ReleasePort`, in all three paths** (TUI, CLI,
    stacks engine). Per **M10**, `--remove` of a nonexistent name is `exit 1`
    and is **benign**: a repeated stop is not an error.

    > **CORRECTION (review, two rounds).** The first draft of this note said "each
    > path has its test" and "an already-dead service on stop removed nothing". Both
    > were inaccurate and commit `fa7f3e8` repeated them:
    >
    > - There are **6 call sites** for removal in 3 packages (cli 1, tui 1, engine
    >   3 + 1 shared). The first round verified **4**; the two engine call sites for
    >   an ALREADY-DEAD service remained uncovered — and one of them called
    >   `portless.Release(nil, …)` with the real client hardcoded, unobservable for
    >   any test. Now all **6** are covered: the engine has a single
    >   `releaseRouteOnStop` through which all three pass, and each branch has its
    >   test (neutralizing any of the three turns the suite red).
    > - "An already-dead service removed nothing" was only true if also
    >   `Port == 0`. A simply-stopped service has `Port != 0`, so the guard was
    >   already satisfied and `stopProcess` already ran. The real gap was the meta
    >   `Pid=0, Pgid=0, Port=0` with `RouteName != ""`, reachable because
    >   `resolveDynamicPort` only registers a route with `State==running && Port>0`
    >   but the inherited fields remain persisted.
    >
    > And the bug the injection uncovered is still real: the removal was INSIDE the
    > process guard.

14. **"No response" in reconciliation means "there is no one behind", not just "the
    proxy does not respond".** A route from a dead vroom **is still routed**: the
    proxy returns `502` because the port is no longer listened on by anyone, and
    reading that `502` as "alive" leaves it forever — which is exactly what §8 exists
    to prevent. `liveRoute` therefore distinguishes three states: not served (`404`
    or no proxy), routed-without-backend (`502`/`504`), and alive. And it is only
    removed when `held.Authorises(published)` — the persisted ownership, not the
    port— so that fail-closed continues to apply to cleanup.

    > **CORRECTION (review).** This text said it was removed when ownership could be
    > proven "by the name and port this service persisted". That was exactly what it
    > did **not** do: `Reconcile` was still receiving a raw port. And since
    > revocation deliberately preserves the handle —so that reconciliation has
    > somewhere to look—, that raw port was a **real deletion authority** over a name
    > vroom had already discarded. Sequence: `Apply(x,4321)` → stop and Release
    > (revokes, handle alive) → another owner takes `x` on 4321 with the backend down
    > (`502`) → branch renamed → `Reconcile` sees `prev != current` and **deletes
    > the foreign route**.
    >
    > The same grant from §5, reused as-is: `Reconcile` now receives `Ownership`
    > and requires `Authorises` before deleting. Preserving the handle is still
    > correct; what was wrong was the predicate.

    The same `502` changes meaning depending on the question: in registration it
    **proves** routing and that is why the route is published; in cleanup it means
    there is no one behind. Treating it the same in both places is an error in one
    of the two.

15. **`Result.Registered` is the grant signal, and it grants by looking at the
    result.** Registering IS granting ownership, but only if the write occurred:
    `Apply` sets `Registered` right after `Register` succeeded, and `applyRoute`
    grants with `res.Registered`.

    > **CORRECTION (review, HIGH-A).** `applyRoute` set `meta.RouteOwned = true`
    > **unconditionally**, without looking at `res`. A registration that never
    > happened —due to conflict, or without a binary— still minted a granting
    > capability, with the requested port. It was the only grant point in the tree, so
    > the defect propagated to everything else.
    >
    > And the fix is NOT to grant with `Succeeded()`: that means registered **and**
    > verified, so it would *under-grant*. A route written with the proxy stopped is
    > genuinely ours and must keep its handle; without it, a restart with the port
    > moved would collide with its own route, which is an invented conflict.
    > Degrading the state does not undo the fact.

16. **Ownership site accounting.** The three findings from round 4 were the same
    shape: a datum written in one place and read in another without anyone verifying
    that the two matched. The complete list, with its role:

    | Site | Role | Before | Now |
    |---|---|---|---|
    | `startsvc.applyRoute` | **grant** | unconditional, without looking at `res` | `res.Registered` |
    | `cli.releaseRouteOnStop` | **revoke** | correct | correct |
    | `tui.releaseRoute` | **revoke** | correct | correct |
    | `engine.releaseRouteOnStop` | **revoke** | correct (3 call sites) | correct |
    | `startsvc.Start` (inheritance) | **read** (transfer) | copied name and port, **not** ownership | copies all three or none |
    | `startsvc.applyRoute` → `Apply` | **read** (decision) | read all three | all three |
    | `startsvc.applyRoute` → `Reconcile` | **read** (decision) | **read name and port, not ownership** | receives `Ownership` |
    | `cli.buildProjectInfo` | **read** (JSON) | read name and port | no change: surface, not decision |
    | — | **clear** | no site clears name+port | none, on purpose |

    The inheritance site was not in the review's list and was the same pattern:
    copying two of three facts leaves the grant silently lost on every startup.

17. **`RemoveAbsent` only revokes with exit 0 or with M10's benign one.**
    `requires Node >= 24`, `EACCES`, and a corrupt `routes.json` all exit with
    exit 1, and a `default → nil` revoked them all: a failed removal declared the
    route as *not ours* while the route could still be there, and without ownership
    no one could claim it. It is a fail-open labeled as closed. Now every exit other
    than the "does not exist" one propagates as an error, and the revoker decides.
    `Remove` keeps its benign contract.

18. **In `routeUnknown` the authority is `held.Owned`, not `Authorises`.** When
    the live port cannot be read, `liveRoute` always returns `published == 0`,
    so comparing ports cannot decide anything: `Authorises(0)` requires `Port > 0`
    and is always false. The only evidence left is the unrevoked grant on that name.

    > **CORRECTION (review, BLOCKING).** The previous guard was
    > `!held.Authorises(published) && published != 0`: **dead code**, because the two
    > conditions are unsatisfiable in that branch. `Remove` ran without checking
    > ownership. Reproduced against real portless in its two trigger states —proxy
    > stopped, and proxy running that responds `404`—: the foreign route was deleted
    > and there was no warning. And `alias --remove` is a pure write (M1), so the
    > deletion succeeded with the proxy down.
    >
    > Granting with `!held.Owned` does NOT disable orphan cleanup: it is precisely
    > what enables it, because the case "our orphan with the proxy stopped" has
    > `Owned=true` and `published` unreadable.

    > **BEHAVIOR CHANGE, DELIBERATELY WRITTEN.** A `meta.json` written by a version
    > prior to `route_owned` does not have that field, so `Owned=false`. Consequence:
    > **orphans from vroom versions prior to the update stop auto-cleaning on
    > startup.** It is a real change and fail-closed: the route is preserved, a
    > warning is issued, and the handle is kept, so it remains recoverable. It is
    > documented here and not discovered in production. Anyone who updates and sees a
    > "no longer owns it" warning is seeing this.

## Consequences

- Positive: a service's health **never** depends on its route existing; the
  slice is shippable without portless installed; the suite is hermetic; the state that
  agents read always distinguishes intention from result, and **verified** from
  **written**.
- Negative / trade-offs: one more external dependency at startup, though
  **bounded, derived, and degradable**; and since `exit 0` is no longer a signal,
  **every** startup pays for a read-back **and** a live verification. It is
  the price of not being able to assert a false address.
- Per **M4**, vroom can share the user's proxy with `portless run` sessions without
  evicting anything foreign: they do not manage each other.
- **No assumed limitations.** The two that existed in this ADR's draft —"a vroom
  that dies leaves permanent routes" and "the TLS scheme is not measured"— are
  **eliminated by design**: the first by mandatory reconciliation (§8), the second by
  determining the scheme by probing (§7).
- Residual cost, not product risk: registering a route collects stale entries from
  the user's `routes.json`. It is harmless (they are dead PIDs) and it is the tool's
  behavior.

## Alternatives considered

- **Passing `PORTLESS_APP_PORT` when starting the app** (what the archived plan
  says). Rejected **by measurement, not by taste**: that variable is consumed by
  `portless run <cmd>`, where **portless** starts the child and owns the process.
  It is the model the user rejected, and it breaks slice 1's Stop-by-lineage. An
  executor that followed the archived plan to the letter would write code that does
  not work.
- **Writing `~/.portless/routes.json` directly** instead of calling the CLI.
  Rejected: it is the proxy's internal state file, without a versioned contract, and
  it reintroduces the type of coupling that slices 1–3 eliminated by making vroom own
  the port and nothing else.
- **Scraping `portless doctor` for the proxy port.** Rejected: the output is for
  humans and changes between versions. `proxy.port` is a state file with the bare
  number. There is no HTTP API to query (M15).
- **Assuming the default port (`1355`, or `80`/`443`).** Rejected: it contradicts
  **M6** and the TLS case. An assumed port that can also be moved is exactly the
  type of constant this slice must not introduce. Nothing is assumed: it is read, and
  if it is not there, it degrades.
- **Verifying with `portless get`.** Rejected by measurement (M12): it prefixes the
  current git branch, so it returns a name that is not the one that was registered.
- **Reading `routes.json` back as sufficient verification.** Rejected by
  **M1** and **M2**: `alias` does not contact the proxy, so the file only proves that
  we wrote. A route in the file with the proxy stopped would appear available and is
  not. **Verification is against the live proxy.**
- **Trusting `exit 0` from `alias` as proof of ownership.** Rejected by measurement
  (M8): unconditional upsert without conflict detection. It is the alternative that
  would have produced lying addresses in silence.
- **Assuming the URL scheme from configuration.** Rejected: there is no need to know
  it. The schemes are probed and the one that responds is published; that way the TLS
  case is not measured **and does not matter**, because there is no assumption that
  TLS could refute.
- **Name derived from the branch only** (the native convention). Rejected because it
  is not stable under `git branch -m` (M13) and a stable URL is exactly what OAuth and
  CORS need. It is kept as `auto`, not as the only option.
- **A single field `route = "" | "auto" | "my-name"`.** Rejected: an ambiguous string
  enum documents its own type in the schema and breaks the symmetry with the preceding
  `port_mode` + `port`.
- **Leaving orphan route cleanup for later / for `portless prune`.** Rejected by
  **M5**: `prune` does not touch `pid: 0`. Waiting for an external sweep is waiting
  for something that will not come. This is what makes reconciliation mandatory and
  not a future improvement.
- **Removing a route that responds but is not ours** (aggressive cleanup). Rejected:
  it contradicts `adr-0012`'s fail-closed. A warning is issued. Deleting something
  foreign is the harm this set of ADRs exists to prevent.
- **Starting or monitoring the proxy from vroom.** Rejected by the user, and by §3.4
  of `adr-0012`: the proxy's `setsid` is the orphan that slice 1 fixed, and making it
  a visible service reintroduces the recursion.
- **Fail-open when portless is not present** (startup error). Rejected: it
  contradicts `adr-0012` limitation 6 and the posture of the entire port changing.
