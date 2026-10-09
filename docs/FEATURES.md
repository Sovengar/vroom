# FEATURES.md

Concise reference of vroom's features. Detailed contracts live in `../README.md` and `adr/`.

---

## Feature inventory

| Feature | What it does | Where |
|---|---|---|
| [Service lifecycle](#service-lifecycle) | Start/stop/restart daemonized services that survive the terminal | `s`, `R`; `vroom start/stop`; `command_pre_start` |
| [One-shot jobs](#one-shot-jobs) | `command_build`, `command_install`, `mise` tasks | `b`, `i`, `t`; `vroom build/install` |
| [Logs & console](#logs--console) | Live tail of stdout/stderr, open both logs in `$EDITOR` | `l`/`o`, `c`, `C`, `g`/`G`; `vroom logs` |
| [Output tabs](#output-tabs) | Console, Threads, Metrics, Git, Env, Timeline, Health | `1`–`7`, `tab` |
| [Ask AI](#ask-ai) | Send a prompt to an agent with app context prefilled | `a` |
| [Orchestration stacks](#orchestration-stacks) | Staged launch/stop of several services | `vroom launch`; `Composers` group |
| [Scanning, groups & filter](#scanning-groups--filter) | Find manifests, group them, filter the tree | `/`, `enter` |
| [Worktree topology](#worktree-topology) | Show a repo's worktrees and bare repos as one row | `enter` on a repo row |
| [Embedded terminal](#embedded-terminal) | A shell inside the TUI, in the selected project | `!` |
| [Status detection](#status-detection) | PID + creation time + port + pattern → a trustworthy badge | — |
| [CLI JSON contract](#cli-json-contract) | Every subcommand answers JSON for agents | `vroom …` |
| [Config & keybindings](#config--keybindings) | XDG config, remappable actions | — |
| [State & logs layout](#state--logs-layout) | Where vroom keeps meta, PIDs and log files | — |
| [Dynamic ports + routes](#dynamic-ports-for-worktrees-with-portless-integration-for-transparent-hostname-via-proxy) | Per-worktree ephemeral port + stable hostname via proxy | `port_mode`, `route_mode` |

---

## Service lifecycle

A service is the `command_start` of a `.vroom.toml`, run under `sh -c` as its own
session leader (`setsid`) with stdout/stderr redirected to files. Closing the TUI
— or the terminal — does **not** kill it: vroom re-attaches to the state on the
next run.

- **`s`** (start/stop) is contextual: on a project it toggles that service; on a
  **group** it starts every stopped member (or stops them all if none is stopped);
  on a **stack** row it launches/stops the whole stack; on a repo container it
  refuses and asks you to expand it first.
- **`R`** (restart) = stop → start, and only from `running` (anything else gets
  *"only a running service can be restarted"*).
- **Stopping is a ladder, not a single signal:** `command_stop` first (for
  services where the kill cannot reach, e.g. `docker stop`), then `SIGTERM` to
  the process group **and** the whole lineage read from `/proc` before signaling
  (so descendants that called `setsid` are reached too), then `SIGKILL` after 5 s.
  If a descendant survived `SIGKILL`, that is a **warning in the service log**,
  not a silent success.
- **Stop also cleans up after itself:** the reserved port returns to the pool and
  the portless route is revoked (only if vroom owns it).
- Starting is idempotent on the CLI side: a service already running answers
  `{"action":"already_running"}` instead of spawning a twin.
- **`command_pre_start`** is an optional **fail-fast hook** run in the project
  directory right before `command_start` spawns — environment prep the app
  command itself must not carry (e.g. `fuser -k 5005/tcp || true` to free a stale
  debug port). Its banner, output and footer land at the top of that run's log;
  a non-zero exit **aborts the start** (no process, no state) and the error names
  the hook, the command and the exit status. It never runs on `already_running`,
  nor on stop/build/install.

**Why it matters:** the same `s` works for one service, a whole group or a stack,
and "stop" provably means *nothing of this service is left listening* — worst
case it says so out loud.

---

## One-shot jobs

- **`b`** runs `command_build`, **`i`** runs `command_install` — both with
  `sh -c` in the project directory, **synchronous** in the CLI (`exit_code` +
  `elapsed` in the JSON).
- **`t`** opens a picker with the `[tasks.*]` of the project's `mise.toml`
  (parsed directly from the file; listing needs no `mise` binary, running one
  does: it executes `mise run <task>`).
- Output goes to the project's log files behind a banner
  (`── vroom ▶ build: … ──`), so it shows up in the Console tab and in
  `vroom logs`. On completion you get `build ok (3.2s)` or `build failed (exit 1)`.
- **Only one job per project at a time** — a second `b` while one runs answers
  *"<kind> already running in <name>"*.
- No command configured → a warning that names the missing manifest key, never a
  crash. Stacks do not support these actions.

**Why it matters:** build/install are one-shot *commands*, not services: they
never get a PID, a port or a badge — they run, they log, they report.

---

## Logs & console

- **Console tab** (default): stdout + stderr merged, read incrementally every
  400 ms from the log files (no process attached), auto-follow that pauses when
  you scroll up and reactivates with `G`. Long lines **soft-wrap** — nothing is
  truncated.
- **`c`** cycles the stream: merged → stdout → stderr. **`C`** clears the
  in-memory console only (the files keep the whole history).
- **`l`** (alias **`o`**) opens **both** logs in `$VISUAL`/`$EDITOR` (default
  `nvim`) — `nvim`/`vim`/`vi` get `-O` for a vertical split, and the stream you
  are on decides which file opens first.
- **`g`/`G`**, `pgup`/`pgdn` and the **mouse wheel** (3 lines/click) scroll the
  console; reaching the end reactivates the follow.
- **CLI:** `vroom logs <name|path> [--tail N --stream merged|stdout|stderr]`
  answers `{project, stdout, stderr}` as JSON. An unreadable log yields `""`, not
  an error: a query command must still answer.

**Why it matters:** the console is a *view over files*, not a pipe — that is what
makes it survive the TUI closing, and what lets `vroom logs` answer the same
question from a script.

---

## Output tabs

Seven tabs on the bottom-right panel: `1`…`7` jump directly, `tab` cycles
forward and `shift+tab` backward.

| # | Tab | Content |
|---|---|---|
| 1 | **Console** | merged stdout/stderr, live |
| 2 | **Threads** | OS threads of the service (`/proc/<pid>/task`): name, TID, status, CPU% — works for any language, no debugger needed |
| 3 | **Metrics** | per tick: CPU% (delta), RSS, open file descriptors, thread count |
| 4 | **Git** | branch, status (clean / N changes) and recent commits of the project |
| 5 | **Env** | the process environment (`/proc/<pid>/environ`), sorted — this is where you *see* the injected `PORT` |
| 6 | **Timeline** | operational events (start / stop / restart / build / install / task / stack) with time and duration, newest first (last 100) |
| 7 | **Health** | HTTP GET against the service's **real** port + `health_path` (default `/`), 1.5 s timeout: status code, latency, content-type and the first lines of the body |

**Why it matters:** the answer to "what is this process doing" is one key away
without leaving the TUI, and Health probes the port vroom *verified* — never the
declared one, which could belong to a twin worktree.

---

## Ask AI

**`a`** opens a prompt prefilled with the selected app's context.

- **Agents** (only those found in `PATH`, so the picker shows what is actually
  installed; with exactly one it is skipped): `opencode`, `pi`, `hermes`,
  `jcode` — all overridable in `[ask.agents]` of the config.
- **Prefill** comes from `[ask] prompt` with `{name}` (project), `{dir}` (path)
  and `{logs}` (the service's log directory); `prompt = ""` disables it. The
  input is multi-line and grows with the content.
- **Launcher** (`[ask] launcher`): `auto` (default: `herdr` when
  `HERDR_ENV=1`, otherwise `inline`), `herdr` (new pane/tab, vroom keeps focus
  unless `focus = true`), `inline` (suspends vroom, foreground agent, returns on
  exit) or `custom` (`launcher_cmd` with `{dir}`, `{agent}`, `{cmd}` — e.g. a
  `tmux new-window` template).

**Why it matters:** the prompt already knows which app, which directory and
where its logs are — you ask about the bug, not about the setup.

---

## Orchestration stacks

`.vroom-compose.toml` (in the working directory) declares named stacks of
**stages**: stages run sequentially, the services inside a stage in parallel,
each stage with its own `timeout` (default 30 s) and a health gate.

```bash
vroom launch --list             # stacks found in the file
vroom launch <stack> [--dry]    # or <group>/<stack> to disambiguate
vroom launch <stack>            # starts them, staged
```

- In the TUI the stacks appear under a **`Composers`** secondary group (their
  own concept — they never touch `primary_group`/`secondary_group` of the
  services) and **`s` on the stack row** launches or stops all of them.
- A name that exists in two groups is an **ambiguity error**, not a guess:
  use `group/stack`.
- Each stage waits for its services' ports/health; a failing stage stops the
  launch instead of half-starting everything.
- Ports and routes behave exactly as in a single start: dynamic ports are
  discovered and persisted, routes are registered, and stopping the stack
  releases ports and revokes routes.

**Why it matters:** "start my whole backend" is one command or one key, and the
staging guarantees infrastructure is up before the services that need it.

---

## Scanning, groups & filter

- vroom scans the **scan root** (CWD by default; `[scanner] root` in the config
  overrides it, `~` and relative paths supported) with **`fd`** — falling back to
  a `filepath.WalkDir` walk when `fd` is not in `PATH` — for `.vroom.toml` files,
  up to `[scanner] depth` (default 4). Hidden directories and
  `node_modules`/`vendor`/`target`/`dist`/`build` are skipped.
- **`primary_group` / `secondary_group`** build a two-level tree with
  collapsible headers; collapsed state **persists** across sessions
  (`collapsed.json`). A group header shows `group (running/total)`.
- **`/`** opens a live filter over names *and* group names (case-insensitive):
  `enter` applies and closes, `esc` clears and closes (and only quits when there
  is no filter to clear).
- A row with **⚠** has no manifest (or an invalid one): it is visible on purpose,
  so a broken manifest is a warning, not a disappearance.

**Why it matters:** the tree is the index — you find a service by name or group,
and a bad manifest can never hide a project from you.

---

## Worktree topology

vroom asks `git worktree list` once per repo (the `internal/worktree` layer is the
one place that spawns git for topology) and
annotates each project with `RepoRoot`, `IsWorktree`, `IsBareContainer` and, on
failure, `WorktreeErr`. The TUI then shows a **repo container row** with its
worktrees nested underneath, plus synthesized rows for **bare repos** — which
have no manifest and would otherwise be invisible.

`enter` toggles a repo's collapse; `s` on the container itself refuses with
*"repository container — expand it to operate its worktrees"*.

**Why it matters:** N worktrees of one repo read as one entry with N services —
which is also what makes the [dynamic ports](#dynamic-ports-for-worktrees-with-portless-integration-for-transparent-hostname-via-proxy)
feature meaningful: each of those rows gets its own port.

---

## Embedded terminal

**`!`** opens a shell **inside the TUI**, with the selected project's directory
as cwd (the scan root when nothing is selected). The PTY gets `setsid` +
`Setctty`, so it has its own session and its whole tree can be shut down
together.

- **`ctrl+q` hides the modal and leaves the session running** — reopening `!`
  reattaches to the same shell instead of restarting it (and resizes it).

**Why it matters:** a quick `curl` or `ps` next to the logs, without losing the
dashboard or the service you are debugging.

---

## Status detection

The badge is not "is the PID alive": vroom combines, in order,

1. **PID + creation time** — a recycled PID never counts as our service;
2. **port open** (with the *owner* of that port checked against our lineage — an
   ambiguous or foreign owner degrades to `unknown` instead of `running`);
3. **`process_pattern`** via `pgrep -f` (treated as a **regex**, unquoted).

yielding `running`, `starting`, `stopping`, `unknown`, `stopped` plus the three
named port states `port_pending`, `port_unresolved` and `no_port` — never an
optimistic guess. Only a *verified* port is shown or probed.

**Why it matters:** with two worktrees of the same app, "the port is open" is
exactly the wrong question; `unknown` is a real answer and it is never silently
upgraded to healthy.

---

## CLI JSON contract

Every subcommand prints **one JSON object on stdout**; errors are JSON on
**stderr** with exit code 1. There is no plain-text mode — an agent should never
have to parse prose.

```bash
vroom list                      # alias: vroom status — full state of every project
vroom start <name|path> [--path <path>]
vroom stop  <name|path> [--path <path>]
vroom build <name|path>         # command_build, synchronous (exit_code, elapsed)
vroom install <name|path>       # command_install, synchronous
vroom logs  <name|path> [--tail N --stream merged|stdout|stderr]
vroom launch --list | <stack> [--dry]
vroom help                      # the command list, as JSON
```

- **`--path`** (or an absolute path as the name) disambiguates a manifest name
  that exists in several worktrees; without it you get
  `ambiguous project name … use --path`.
- `route_mode` / `route` and `port_mode` / `port` / `declared_port` /
  `port_verified` ride on the `list` rows (see the sections above).
- `vroom` with no arguments — or an unknown one — launches the TUI.

**Why it matters:** the same state the TUI paints is queryable, so a script or
an agent can start, wait for a port and read the route URL without a screen.

---

## Config & keybindings

`~/.config/vroom/config.toml` (`$XDG_CONFIG_HOME`; `$VROOM_CONFIG` overrides the
path). **A broken config never aborts**: vroom falls back to defaults and shows
the error on startup.

```toml
[scanner]
root = "~/dev"          # scan root (default: CWD)
depth = 4               # max directory depth

[ask]
launcher = "auto"       # auto | herdr | inline | custom
prompt = "Given the app {name} with logs in {logs}, "   # "" disables the prefill

[keybindings]           # action = "key"; missing actions keep their default
start_stop = "s"
restart    = "R"
build      = "b"
install    = "i"
tasks      = "t"
ask        = "a"
clear      = "C"
stream     = "c"
top        = "g"
bottom     = "G"
logs       = "l"
refresh    = "r"
```

Navigation and special keys (`q`, `esc`, `enter`, `tab`, `j`/`k`, arrows,
`1`…`7`, `/`, `!`) are **universal and not remappable**; binding an action to a
reserved key, duplicating a key across actions or naming an unknown action is an
**invalid config**: vroom falls back to the defaults (for the whole file, not
just the offending section) and reports the error on startup. The help bar
always shows what is *configured*, not what is hardcoded.

**Why it matters:** the key you read in the bottom bar is the key that works —
and a typo in the config cannot brick the TUI.

---

## State & logs layout

```
~/.local/state/vroom/services/{8-hex-sha256-of-project-path}/
├── meta.json     # name, pid, pgid, port + its states, reserved port, route handle
├── pid, pgid     # process credentials (cleared on stop)
├── stdout.log    # service stdout (also build/install/task output)
└── stderr.log    # service stderr, vroom's own warnings, kept as history
collapsed.json    # which groups are collapsed
```

`$XDG_STATE_HOME` is honoured. The key is a **hash of the project path**, which
is what gives every worktree its own independent state. Everything is written
atomically (tmp + rename).

**Why it matters:** state is per path and on disk — that is the single fact that
makes worktrees independent, restarts resumable and `vroom list` reproducible.

---


## Dynamic ports for worktrees with portless integration for transparent hostname via proxy

vroom gives every worktree its own ephemeral port and — through portless's reverse
proxy — a stable, portless hostname. N copies of the same repo run side by side
with no manifest edits and nothing to remember.

### Background

- **vroom** is a CLI + TUI that scans a root directory (the working directory by
  default, `[scanner] root` to change it) for
  `.vroom.toml` manifests and daemonizes services (`setsid` + `nohup`) so they
  survive the terminal closing. It returns JSON on stdout for AI agents to consume.
- **portless** is an external tool that provides portless routes: aliases like
  `http://feature-a.portless-demo.localhost:1355` that point to a real port on
  localhost. vroom neither starts nor owns that proxy; it only registers aliases in it.
- **Git worktrees** are copies of the same repo on different branches, each with its
  own working directory. vroom treats them as independent services.
- **Walkthrough prerequisites:** portless proxy running on `:1355`, vroom binary
  updated (`make install` done), and `~/dev/portless-demo/` already created.

**Two manifest fields drive the layers below.** Both are enums with a
backward-compatible default, so a manifest that declares neither behaves exactly as
it always did:

| Field | Values | Default | Decides |
|---|---|---|---|
| `port_mode` | `fixed` \| `dynamic` \| `none` | `fixed` | *who* picks the port: the manifest (`fixed`), vroom (`dynamic`), or nobody — the service has no port by design (`none`) |
| `route_mode` | `off` \| `auto` \| `named_with_auto_fallback` | `off` | *whether* the service is published under a hostname, and how that name is derived |

`port` is not part of `port_mode`: it is always the app's own default
(`PORT=${PORT:-8080}`), in every mode — `port_mode` only decides who overrides it.
`port = 0` is a silent alias of `none`.

### Choosing the mode at start time (TUI)

Pressing `s` on a stopped project does not start it outright: it **arms** a port-mode
selector (the same pattern as gitdash's `p`). The second key chooses:

- **`ss`** — start **fixed** (the manifest's `port`).
- **`sd`** — start **dynamic** (vroom reserves a port and injects `PORT`).
- **any other key / `esc`** — cancels the arm and continues normally.

The choice is recorded in the service's state, so `vroom list` shows it as `port_mode`
and every later start (TUI `R`, CLI `vroom start`, stacks) inherits it until changed.
The manifest's `port_mode` is the default only when nothing has been chosen yet.

### Layer 1 — Worktrees

vroom looks for `.vroom.toml` and asks git for each repo's worktrees, grouping them
under a single repo row. Each worktree is a full, independent service: its own
manifest copy, its own state (indexed by path), its own process and port. One repo,
two branches → two running instances.

**Why it matters:** before, two worktrees of the same project collided on port and
on name; now they coexist without stepping on each other.

### Layer 2 — Dynamic ports (`port_mode = "dynamic"`)

On start, and **only for the process it is about to spawn**: vroom picks a free
port from its own pool (4000–4999) and puts `PORT` (and `HOST=127.0.0.1`) into that
child's environment — nothing is exported globally, nothing is written to the
manifest, and no other service sees that value. The child is spawned as its own
session leader; vroom then discovers the real port its process tree is listening on
and persists it as the truth.

Discovery is bounded, it never hangs:

| Situation | Outcome |
|---|---|
| the process dies | fast failure (~300 ms) |
| no TCP port | `no_port` (unverified, port health checks disabled) |
| slow binds | stabilization window before accepting a sample |
| several listeners | deterministic resolution: reserved port wins → else the best `health_path` response → else the lowest, marked `port_verified: false` |

The manifest `port` is untouched by all this (see Background), so running the app by
hand still binds the declared port. On stop, the reserved port returns to the pool.

**Why it matters:** you can have five worktrees running at once without editing any
manifest or remembering which port each one had.

### Layer 3 — Routes without a port (`route_mode`)

**Problem:** with dynamic ports, any external reference — a frontend proxy config, an
OAuth callback, a CORS rule, a README — is tied to a number that changes on every
startup.

**Solution:** vroom registers a hostname in portless
(`portless alias <name> <real-port>`) after discovering and verifying the real port,
and publishes the URL **only if it answered a live probe**. Consumers talk to the
hostname; the proxy forwards to whatever port the service has today.

How the two sides actually talk, concretely:

- **vroom → portless:** it runs the portless CLI as a subprocess —
  `portless alias <name> <real-port>` to register, `portless alias --remove <name>`
  to revoke. There is no API and no shared library; the CLI *is* the contract.
- **Where portless stores it:** portless appends the pair to its own state file
  `~/.portless/routes.json` (one `{hostname, port}` entry per alias). The proxy reads
  that file to know where to forward; vroom never writes it directly.
- **Who knows the hostname:** vroom does — it *derives the name itself* (`auto` →
  `<branch>.<project>`, `named_with_auto_fallback` → your `route_name` first, then
  `<branch>.<project>`) and hands it to the CLI.
  portless does not invent or return a name for aliases; it only echoes what it was
  given. So the URL `http://<name>.localhost` is knowable before, during and after
  registration.

#### Values

| `route_mode` | Hostname | Scope |
|---|---|---|
| `off` (default) | none | vroom does not even look for the portless binary |
| `auto` | `<branch>.<project>` | per **branch**: changes on `git branch -m`, collides between two worktrees on the same branch |
| `named_with_auto_fallback` | `route_name`, else `<branch>.<project>` | **claim ladder**: `route_name` is global and stable (start a worktree first to give it the stable URL); the branch rung is per branch and is only tried when the stable name is already held |

#### Semantics

- **Claim on start only:** the name is registered when the service starts (after port
  discovery + verification) and released on `stop`. No queue, no handoff.
- **Claim ladder (`named_with_auto_fallback`):** `route_name` is tried first; only a
  `route_conflict` on it advances to `<branch>.<project>`, and the warning names the
  **port** holding the stable name — that port is how you tell which worktree won it.
  The rung that succeeds is the `route.name` in the JSON. A fallback that later finds
  `route_name` free claims it and **retires its old branch-derived route in the same
  start** (stop only revokes the name the service holds, so a name the ladder walked
  away from would otherwise be orphaned forever).
- **Mutual exclusion, fail-closed:** a second service claiming the same name while it
  is held gets `route_conflict` — no URL published, first route stays intact, the
  second keeps serving on its raw port with a warning. vroom never overwrites a
  foreign route. With the ladder this only happens when **both** rungs are held (e.g.
  a third worktree on the same branch as two already-running ones).
- **Health never depends on the route:** no portless binary, proxy down, old Node →
  one warning, the service starts and stays healthy anyway.
- **Verified URLs only:** `portless alias` exits 0 even with the proxy down, so vroom
  reads the route back (proof of ownership), probes the live proxy (any HTTP response —
  including `502` — proves routing) and probes the scheme; an unverified URL is never
  published.
- **Revocation on stop = the entry leaves `~/.portless/routes.json`:** `vroom stop`
  (or the TUI `s`, or a stack stop) runs `portless alias --remove <name>`, so the
  row disappears from the proxy's table and the hostname stops resolving — but only
  when vroom still owns it (`RouteOwned`); a foreign route is never deleted. Alias
  routes are not covered by `portless prune`, so orphans left by a crashed vroom are
  cleaned by vroom itself on the next startup (reconciliation); a route that
  responds and is not ours is warned about, never deleted.

#### JSON contract

`route_mode` = intention (manifest), `route` = result:

```json
"route_mode": "named_with_auto_fallback",
"route": { "name": "api-dev", "status": "registered", "url": "http://api-dev.localhost", "port": 4321 }
```

Degraded never publishes `url`, always says why:

```json
"route": { "name": "api-dev", "status": "degraded", "reason": "proxy_not_running" }
```

Field by field — the top-level `port` and `route.port` look redundant but answer
different questions:

| Field | Question it answers |
|---|---|
| `port` | where does the **process** listen right now? (vroom's own discovery; falls back to the manifest's declared port when nothing runs) |
| `port_verified` | did vroom *confirm* that port, or just pick the best guess? |
| `route.port` | where does the **alias** point? (the number vroom handed to `portless alias`) |
| `route.status` / `url` / `reason` | is that alias live and verified against the proxy, and if not, why |

They coincide right after a successful start — same registration, same number. They
diverge the moment one side changes: a **stopped** row keeps the last alias target
while `port` reverts to the declared one:

```json
{"name":"wt-b","status":"stopped","port":8080,
 "route":{"name":"feature-b.portless-demo","status":"registered","port":4001}}
```

So `port` tracks the process, `route.port` tracks the proxy's table; reading one
instead of the other answers a different question.

#### Use cases

| Case | Recommended setting |
|---|---|
| Frontend (any worktree) pointing at a backend with dynamic ports | `named_with_auto_fallback` + fixed `route_name`; frontend hardcodes the host once |
| OAuth callback / CORS origin list / README link | `named_with_auto_fallback` — the URL must not depend on a branch; start the worktree that must own it first |
| Two worktrees, only one backend running at a time (shared name) | `named_with_auto_fallback` + same `route_name`: whoever starts claims it; the frontend is unaware of which worktree serves |
| Two backends running **simultaneously** on different branches | `auto` — each branch gets its own host (`feat-x.api.localhost`), or the ladder: the second worktree falls back to exactly that host |
| Two backends simultaneously on the **same** branch | `named_with_auto_fallback`: the first claims `route_name`, the second falls back to `<branch>.<project>`; a **third** on that branch would need a distinct `route_name` (edit the `.vroom.toml` per working copy) |
| No proxy / no portless installed | any value works: degrades to a warning, service unaffected |

#### Requirements

- portless installed and its **proxy running** (vroom never starts or manages it).
- `route_mode != "off"` requires `port > 0` in some port mode.
- `route_name` only with `named_with_auto_fallback`; hostname-safe characters only
  (lowercase letters, digits, hyphens, dots).
- The whole route contract — including the claim ladder against a **real**
  portless behind a live proxy — is re-run on every PR by the `Integration` CI job
  (Node 24 + `portless@0.15.6`, pinned).

See `adr/adr-0013-vroom-registers-portless-routes.md`.

### Where you see it

- TUI details panel: the `url:` row (route URL) plus the badge with the live port.
- `vroom list` JSON: `port`, `port_verified`, `route{name,status,url}`.
- `portless list`: what the proxy serves right now.

Two footnotes:

- **Where `:1355` comes from:** not the app's port and not vroom's — it is the port
  the portless **proxy itself** listens on, which portless records in
  `~/.portless/proxy.port` while it runs (on a default install the proxy is on
  443/80, so no port appears in the URL at all). This walkthrough runs it with
  `--no-tls -p 1355`, hence every URL needs `:1355` appended. vroom's published
  `url` never includes it: vroom does not know or manage the proxy's port.
- **A stopped row is a snapshot, not a live check:** `vroom list` keeps the last
  route result (`name`, `status`, `port`, even `url`) in its meta after `stop`;
  the authoritative check of "registered right now" is `portless list`, which reads
  the proxy's table directly.

---

## Walkthrough: `portless-demo`

```
~/dev/portless-demo/
├── repo/   → branch main       → http://main.portless-demo.localhost:1355
├── wt-a/   → branch feature-a  → http://feature-a.portless-demo.localhost:1355
└── wt-b/   → branch feature-b  → http://feature-b.portless-demo.localhost:1355
```

The three share the same `.vroom.toml` (`port_mode = "dynamic"`,
`route_mode = "auto"`, a python server honoring `${PORT:-8080}`); each branch serves
its own `<h1>` marker so you can tell them apart in the browser. The proxy runs on
`:1355` and the binary is up to date (`make install` done).

### 0 — Baseline

```bash
portless list        # expect only your two routes: main.api.localhost, api.localhost
vroom list | jq -c '.projects[] | select(.path|contains("portless-demo")) | {name,status,route}'
# repo/wt-a/wt-b → stopped
```

### 1 — Start both worktrees

```bash
vroom start wt-a
vroom start wt-b
# each → {"ok":true,...,"action":"started","pid":...}
```

### 2 — See what vroom decided

```bash
vroom list | jq -c '.projects[] | select(.name=="wt-a" or .name=="wt-b") | {name,status,port,port_verified,route}'
# both "running", ports in 4000–4999 (e.g. 4000/4001), port_verified:true,
# route:{status:"registered", url:"http://feature-a.portless-demo.localhost", ...}
# top-level port = where the process listens; route.port = where the alias points:
# same number right after start, they are not the same field (see JSON contract).
```

### 3 — Same app, two addresses

```bash
curl -s http://feature-a.portless-demo.localhost:1355/   # <h1>portless-demo — worktree A (feature-a)</h1>
curl -s http://feature-b.portless-demo.localhost:1355/   # <h1>portless-demo — worktree B (feature-b)</h1>
portless list                                            # both aliases → their real ports
```

Open them in the browser too — different content, side by side, with no port engineering.

### 4 — The TUI view

```bash
vroom
```

Find the `demo` group / the `portless-demo` repo row → expand it → two worktrees. The
details panel shows the real port and the `url:` row. Default keys: `s` start/stop,
`R` restart, `r` refresh, `q` quit (also useful for the steps below).

### 5 — Isolation: stop one, the twin lives

```bash
vroom stop wt-a
curl -s -o /dev/null -w '%{http_code}\n' http://feature-b.portless-demo.localhost:1355/   # 200 — intact
portless list                                                                             # among the demo routes, only feature-b remains
```

wt-a's URL stops serving (alias revoked) and its port is released; wt-b's
process/port/route are untouched.

### 6 — Restart: the route follows the port

```bash
vroom start wt-a
vroom list | jq -c '.projects[] | select(.name=="wt-a") | {port,route}'   # registered again, pointing at the real (possibly new) port
```

### 7 — (Optional, the key promise) Health never depends on the route

```bash
vroom stop wt-b
portless proxy stop
vroom start wt-b          # starts anyway — just a warning
vroom list | jq -c '.projects[] | select(.name=="wt-b") | {status,port,route}'   # running, route degraded/proxy_not_running, NO url
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:4001/                  # 200 direct (use its real port from step 2)
portless proxy start --no-tls -p 1355
vroom stop wt-b && vroom start wt-b    # with the proxy back, the route registers again
```

### 8 — Adopt it in a real project

```toml
port = 4321                # the app's own default, for manual runs
port_mode = "dynamic"      # vroom reserves + injects PORT and discovers the real one
route_mode = "auto"        # stable URL <branch>.<name>.localhost; use "named_with_auto_fallback" + route_name for OAuth/CORS
```

App side: read `PORT` with a fallback — `PORT=${PORT:-4321}` — that is the only change
your apps need.

### 9 — Cleanup

```bash
vroom stop wt-a; vroom stop wt-b   # the routes revoke themselves
rm -rf ~/dev/portless-demo         # repo + both worktrees at once
portless proxy stop                # only if you don't want the proxy running
```

---

## Why these three layers together

| Previous problem | Solution | Guarantee |
|---|---|---|
| Two worktrees collide on port/name | Layer 1 + 2: state per path, reserved port | Real isolation, zero manifest edits |
| Remembering/enumerating ports by hand | Layer 2 + 3: discovery + stable alias | Fixed URL per branch, the port may change |
| Proxy down = "broken" service | Layer 3: route decoupled from health | The service stays healthy; the route degrades and reconciles |

Things to keep in mind:

- The 4000–4999 ports are a **pool**; if an external process lands in that range,
  vroom skips it when reserving.
- `auto` routes depend on the **branch name** — renaming the branch changes the URL.

---

## Portless: with vs without

| Aspect | With portless (`route_mode != "off"`) | Without portless (`route_mode = "off"`) |
|---|---|---|
| **URL** | Stable hostname (e.g. `api-dev.localhost`) that survives restarts | None — consumers must track the raw port |
| **Port changes** | Transparent — the proxy forwards to whatever port the service has today | Opaque — every restart may change the port; all references break |
| **OAuth / CORS / bookmarks** | Work once, forever | Must be updated on every restart |
| **External dependency** | Node >= 24 + `portless` binary + proxy running | None |
| **Startup cost** | Read-back + live HTTP probe against the proxy | None |
| **Failure mode** | Degrades to warning; service stays healthy | N/A — nothing to fail |
| **Route cleanup** | vroom reconciles on startup (orphans, renames, dead vrooms) | N/A |
| **Shared proxy** | vroom can share the proxy with `portless run` sessions (M4) | N/A |

**When to use it:** when any external system (frontend proxy, OAuth provider, CORS
rule, README, bookmark) needs a stable address for a service whose port changes on
every start.

**When to skip it:** when only the port matters (e.g. `curl localhost:<port>`) or
when the service has a fixed port and no external consumers.
