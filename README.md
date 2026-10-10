# vroom

[![CI](https://github.com/Sovengar/vroom/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/Sovengar/vroom/actions/workflows/ci.yml)

TUI in Go + Bubbletea for managing services across multiple projects from a single point.
Uses [fd](https://github.com/sharkdp/fd) to quickly find `.vroom.toml` in the
directory tree (configurable depth). Allows starting/stopping daemonized services that
**survive terminal close**, with real-time console
and status detection (PID + port + process pattern).

## Dashboard

A single dashboard in the style of IntelliJ's "Services" panel, composed of four
sections with rounded border and title on the border: `Projects` (left,
full height), `Details` (top-right), `Output` (bottom-right) and
`Keybinds` (bottom, full width; the status message appears as a legend
on its bottom-right border).

- **Project tree** (`Projects` box, left, fixed width, auto-scroll):
  selectable and collapsible groups with `enter` (collapsed shows
  `group (running/total)`), compact glyph + name rows (⚠ = no
  manifest); `s` on a group starts/stops all its members.
- **Details panel** (`Details` box, right, fixed, two columns): on the left
  path, language, git branch, group, port, pattern, PID and logs; on the
  right the manifest commands (`start`/`stop`/`install`/`build`,
  with `—` for unconfigured ones).
- **`Output` panel with tabs**:
  - `Console` — stdout + stderr merged in real time (incremental
    tail every 400ms, auto-follow, pauses on scroll-up). `c` toggles merged/
    stdout/stderr.
  - `Threads` — OS-level process threads (`/proc/<pid>/task`): name,
    TID, status and CPU% sorted by consumption. Works for any
    language without a debugger (Java exposes thread names, Go goroutines).
  - `Metrics` — process resources sampled each tick: CPU% (tick
    delta), RSS, open file descriptors and thread count.
  - `Git` — branch, status (clean/number of changes) and recent commits of the
    project's repo.
  - `Env` — process environment (`/proc/<pid>/environ`), sorted.
  - `Timeline` — operational session events (start/stop/restart/build/
    install/task/stack) with time and duration, most recent on top.
  - `Health` — HTTP GET to the service's **real port** (`health_path`,
    default `/`) with short timeout: code, latency, content-type and
    first lines.

## Installation

```bash
go build -o ~/.local/bin/vroom ./cmd/vroom
```

**Runtime dependency:** [fd](https://github.com/sharkdp/fd) (>= v8.0)

```bash
# Arch Linux
sudo pacman -S fd

# macOS
brew install fd

# Ubuntu/Debian
sudo apt install fd-find

# Fedora
sudo dnf install fd-find
```

fd is used to find `.vroom.toml` quickly and efficiently. Without fd, vroom will not work.

Other requirements: Linux (v1), POSIX shell.

### Optional dependency: portless

Only needed for the hostname generations — `by_hostname`, `by_workspace_hostname` or
`by_hostname_or_workspace` (see [`url_generation`](#url_generation-how-the-service-is-addressed)).
With `by_port` or `none` vroom **does not even look for the binary**.

portless is a Node CLI and **requires Node >= 24**. It is resolved in this order:
`$PORTLESS_BIN` → `exec.LookPath` → known mise shim directories (vroom runs
under a service manager whose environment is not your login shell, so a `portless`
"node" in PATH is not guaranteed).

Without portless, without a running proxy, or with too old Node: vroom warns **once**
and the service **starts anyway**. The health of a service never depends on its route existing.

## Quick start — playground

The repo includes `playground/` with 8 dummy projects ready to test the full cycle:

```bash
cd playground
vroom
```

| Project | Language | Group | Port | Command |
|---|---|---|---|---|
| products-api-java | Java | store | 8081 | `java src/main/java/com/example/Main.java` |
| orders-api-springboot | Java (Spring Boot) | store | 8084 | `mvn spring-boot:run` |
| billing-api-go | Go | — | 8082 | `go run main.go` |
| inventory-api-python | Python | — | 8083 | `python3 app.py` |
| web-frontend | JavaScript | store | 5173 | `node server.js` |
| search-api-python | Python | — | 8090 | `python3 -m http.server 8090` |
| auth-api-go | Go | — | 8091 | `go run main.go` |
| nginx-proxy | other | — | 8080 | `docker run --rm -p 8080:80 nginx:alpine` |

Notes:
- `orders-api-springboot` requires **JDK 17+ and Maven**; the first run downloads
  dependencies (you'll see the full Spring startup log in the logs view).
- `nginx-proxy` requires Docker.
- `web-frontend` includes `commands.install`/`commands.build` in its manifest and a `mise.toml` with
  tasks (and one hidden) to test `b`, `i` and the `t` picker without configuring anything.
- Each project defines its service in a `.vroom.toml` — this is how you configure yours:

```toml
name = "my-service"
primary_group = "store"           # top-level grouping (optional)
secondary_group = "backend"       # inner level, only with primary_group (optional)
commands.start.run = "go run main.go"
commands.start.hooks.pre_run = "fuser -k 5005/tcp || true"  # fail-fast hook, before start (optional)
port = 8080                       # default app port (0 = disabled)
url_generation = "by_port"        # "by_port" | "by_hostname" | "by_workspace_hostname" | "by_hostname_or_workspace" | "none"
                                  # default: by_port when port > 0, else none
process_pattern = ""              # pgrep pattern (optional)
commands.install.run = "npm install"    # one-shot with the i key (optional)
commands.build.run = "mise run build"  # one-shot with the b key (optional)
commands.stop.run = "docker stop x"    # graceful stop with the s key (optional)
health_path = "/healthz"          # Health tab probe path (default "/")
route_name = ""                   # stable hostname (required by by_hostname / by_hostname_or_workspace)

[worktrees]
url_generation = "by_hostname_or_workspace"  # optional per-copy override for worktrees (absent = inherit)
```

**Everything vroom runs lives under `[commands]`** — `start`, `build`, `install`
and `stop`, each with a `run`, plus `start.hooks` for the hooks that belong to
the start. The dotted keys above and the table form decode to exactly the same
manifest:

```toml
[commands.start]
run = "mise run start"

  [commands.start.hooks]
  pre_run = "fuser -k 5005/tcp || true"
```

Two rules for the table form: TOML table headers end the top-level table, so a
`[commands.*]` header goes **after** the top-level keys (or keep using the
dotted form anywhere); and `run` is required for `start`. vroom enforces both
loudly — a swallowed top-level key (`port` written after the header) is a parse
error naming it, and the pre-`[commands]` keys (`command_start`,
`command_build`, `command_install`, `command_stop`, `command_pre_start`) no
longer parse: the error names their new location, there is no alias.

Grouping is hierarchical: with `primary_group` + `secondary_group` the TUI
shows two levels of collapsible headers (e.g. `store` → `backend`/
`frontend`); `secondary_group` without `primary_group` is ignored, and with only
`primary_group` projects go directly under their header (enter toggles
header collapse or, on a project, its innermost container).

`commands.stop.run` is for services where killing the process group is not enough (the
child process survives the kill, e.g. a Docker container): when pressing `s`, vroom
runs that command first (with banner, visible in the console) and then applies
the usual cleanup shutdown (SIGTERM → 5s → SIGKILL to the process group **and
its descendants**, including those that did `setsid`).

`commands.start.hooks.pre_run` is the mirror image on the way up: an optional hook run with
`sh -c` in the project directory just before `commands.start.run` spawns, for
preparation the app command itself must not carry (freeing a stale debug port,
bringing a dependency up). It is **fail-fast**: a non-zero exit aborts the start
before any process exists — nothing spawned, nothing persisted — and the error
names the hook and its exit status while its output stays in the service log
(banner `── vroom ▶ pre_run: … ──`, first entry of that run). It runs on every
start path (`vroom start`, `s`, `R`, `vroom launch`) and never on
`already_running`. Since the hook is plain `sh -c`, a best-effort step ends with
`|| true` (`fuser` exits non-zero when nobody holds the port).

`commands.start.hooks.post_run` is the counterpart that needs the service **already
up**: it runs at the very end of the start — state persisted, port resolved,
route registered — for seeding, warming or notifying. It is **warn-only** on
purpose: the service is alive, so a non-zero exit never fails the start nor
kills it; the failure names the hook and its exit status among the start
warnings (Console + `vroom logs`) and its output stays in the service log behind
the `post_run` banner.

### `url_generation`: how the service is addressed

`port` is not the detection mechanism: it is the **default port of your app**,
the same value as `PORT=${PORT:-8080}`. `url_generation` is the single key that
decides what vroom does with it — the port and the address are two faces of one
choice, so they are no longer declared separately (the former `port_mode` +
`route_mode` pair is gone).

| `url_generation` | What vroom does |
|---|---|
| `by_port` | Binds the declared `port`; the address is `localhost:<port>`. Publishes no portless route. The start is **refused while the port is occupied**. |
| `by_hostname` | Ephemeral port (injected as `PORT`, with `HOST=127.0.0.1`) + registers `route_name` in portless. Refused **only if a proven foreign holder keeps that name**; the service reclaims its own stale route. |
| `by_workspace_hostname` | Ephemeral port + `<branch>.<project>` hostname; no `route_name` needed. |
| `by_hostname_or_workspace` | Claim ladder: `route_name` first; if another worktree holds it, falls back to `<branch>.<project>` instead of failing. |
| `none` | Headless: no port, no URL. vroom does not even look for portless. |

The port vocabulary is **derived** from the generation: `by_port` binds a fixed
port, the hostname generations get an ephemeral one (their address is the
hostname), `none` has none — there is no separate port mode to keep in sync.

**Backwards compatible when absent:** a manifest that declares no
`url_generation` keeps the historical behavior — a declared `port > 0` means
`by_port`, `port = 0` (or absent) means `none`.

**Per worktree:** `[worktrees].url_generation` overrides the top-level value for a
worktree copy, so one committed manifest serves `main` one way and every worktree
another; absent means the worktree inherits the top-level value.

```toml
url_generation = "by_hostname"               # main checkout: the stable URL
[worktrees]
url_generation = "by_hostname_or_workspace"  # each worktree: stable name, else branch
```

#### Ports: fixed vs ephemeral

With a hostname generation the same `.vroom.toml` works for N worktrees at once:
each one
starts on its own port and the UI, the JSON and the health probe show
**that same number**. The contract with your app is one line:

```bash
PORT=${PORT:-8080} node server.js
```

If the app **ignores** `PORT` and binds to its own fixed port, startup
**does not fail**: vroom discovers the real port, persists it and emits a
visible warning. If the app opens no TCP port (only UDP, a worker…), it is
registered as "no port" and the service remains operable.

Discovery is bounded by deadline and process liveness: a service
that dies on startup is reported in ~1s, not after exhausting the timeout.
A service that takes 3.5s to bind keeps its port and is not reported
as "no port".

If the app opens **multiple** listeners, vroom only has to guess: the
reserved port wins if the app took it; otherwise the one that best responds on
`health_path` (200 > 2xx/3xx > 5xx > 404) wins.

**Not a guarantee:** an app that besides not being HTTP ignores `PORT` leaves
vroom with no way to know which of its listeners is the main one. In that case the
lowest-numbered one is chosen (deterministic) and the service is marked as **"port
not verified"** (`port_verified: false` in the JSON, with visible warning). See `docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md`
for the complete port ownership contract.

#### Routes: a stable URL for the changing port

With ephemeral ports, any external reference — an OAuth callback, a CORS rule, a
README, a bookmark — is tied to a number that changes on every startup. The
hostname generations give the service a **stable name** in portless.

```toml
url_generation = "by_hostname"    # claim route_name exactly
route_name = "my-api"             # required by by_hostname / by_hostname_or_workspace
```

`by_workspace_hostname` derives the name from the **branch**: `<branch>.<project>`,
without writing anything.

`by_workspace_hostname` separates distinct **branches** of the same repo, which is
what prevents two running branches from sharing an address. Watch the scope: the
name comes from the branch, not from the worktree folder, so **two worktrees on the
same branch derive the same name** (for example, two clones both on `main`, or a
`git worktree --force` on an already-used branch). When that happens, the second
service does not get a second address but a **conflict warning**: the first
one's route stays intact and this service keeps working on its port.

`by_hostname_or_workspace` is the mode for URLs that **cannot depend on a branch**
(`redirect_uri`, origin list): the first worktree to start claims `route_name`, so
starting a worktree first is how you choose which one owns the stable URL. The
worktrees that start later no longer degrade to nothing — they fall back to
`<branch>.<project>`, with a warning naming the **port that holds the stable
name** (that port tells you which worktree won it). The fallback is re-tried on
every start, so a worktree that later finds `route_name` free claims it and its
old branch-derived route is retired in the same start. Only when **both** names
are taken does it degrade like before: no URL published, the other routes intact,
the service healthy on its port.

`by_hostname` claims `route_name` and nothing else: a name kept by another holder
is a **refusal** (there is no branch rung to fall back to — that is what the ladder
exists for). And `by_port` refuses a start while its port is occupied, before
anything spawns.

**vroom only registers the route. It does not start, manage, supervise or show the
proxy.** portless is an *optional* dependency (see
[Installation](#optional-dependency-portless)): if there is no `portless`, or it is not in the
service's `PATH`, or its proxy is not
running, or its Node is too old: vroom warns **once** and the
service **starts anyway, stays healthy, and lives on its port**. The health of a
service never depends on its route existing — a route is an address, not a
dependency.

When stopping the service, its route disappears. And routes left by a vroom that
died without stopping it are cleaned by **the next startup's reconciliation**, because
`portless prune` does **not** touch alias routes. If you rename a branch in
`by_workspace_hostname` mode, the old route is removed and the new one is
registered; in `by_hostname_or_workspace` the same retirement happens when the
ladder switches names, because stop only ever revokes the name the service actually
holds. Switching a service to `by_port` or `none` retires the route a previous
hostname start left behind.

In the JSON, `url_generation` is the **intention** (the recorded choice, else the
manifest's) and `route` is the **result**:

```json
"url_generation": "by_hostname_or_workspace",
"route": { "name": "my-api", "status": "registered", "url": "https://my-api.localhost", "port": 4321 }
```

A degraded route **never** publishes `url`, and always says why:

```json
"route": { "name": "my-api", "status": "degraded", "reason": "proxy_not_running" }
```

vroom does not publish a URL it has not seen working. `portless alias` writes the
route even if the proxy is off and exits successfully anyway, so **writing the
route does not prove the URL resolves**: that is why, after registering it, vroom reads it back
to confirm it belongs to that service and checks it against the live proxy. A `502`
counts as "the proxy routes the route and your service does not respond"; only a
refused connection or a timeout mean there is no proxy. `by_port` and `none`
publish none of this.

See `docs/adr/adr-0014-url-generation-single-axis.md` and
`docs/adr/adr-0013-vroom-registers-portless-routes.md`.

## Keybindings

| Key | Action |
|---|---|
| `j`/`k` or arrows | Navigate the tree (cyclic, auto-scroll) |
| `/` | **Filter**: live filter bar for the tree (matches name and `primary_group`/`secondary_group` groups, case-insensitive); `enter` applies and closes, `esc` clears and closes |
| `enter` | Collapse/expand the selected group |
| `s` | **Start/stop** (contextual toggle; on a group, all its members). On a **stopped** service it **arms** a start-generation selector: `p` = `by_port`, `u` = `by_hostname`, `w` = `by_workspace_hostname`, `f` = `by_hostname_or_workspace`, any other key cancels |
| `R` | Restart (stop → start with timeout) |
| `b` | **Build**: one-shot manifest command (`commands.build.run = "..."`) |
| `i` | **Install**: one-shot manifest command (`commands.install.run = "..."`) |
| `t` | **Tasks**: `mise.toml` task picker (see [mise](#mise-integration-optional)) |
| `a` | **Ask AI**: ask an agent (opencode/pi/hermes/jcode) with your prompt → new chat; the input is prefilled with app context ([global config](#global-config-ask-ai)); dispatch is configurable |
| `C` | **Clear**: clears the in-memory console (files keep history) |
| `1` … `7` | Output panel tabs: Console / Threads / Metrics / Git / Env / Timeline / Health (`tab` cycles, `shift+tab` back) |
| `c` | Console mode: merged → stdout → stderr |
| `pgup`/`pgdn`, `g`/`G` | Console scroll with keyboard (pauses follow; `G` reactivates it) |
| mouse wheel | Console scroll (3 lines per click; reaching the end reactivates follow) |

Log lines longer than the panel wrap (soft wrap): the full
content is visible and color is preserved on continuation lines.
| `l` | Open both logs in the editor (`$VISUAL`/`$EDITOR`, default nvim, vertical split) — `o` alias |
| `r` | Forced refresh |
| `q`/`Esc` | Quit (`Esc` closes the prompt/picker first; with filter applied, clears the filter before quitting) |

The details panel is fixed: shown whenever there is space and has no toggle.

Action keys are configurable via `[keybindings]` (see
[global config](#global-config-ask-ai)); navigation and
special keys (`q`, `Esc`, `enter`, `tab`/`shift+tab`, `j`/`k`, arrows, `pgup`/`pgdn`,
`1`…`7`, `/`, `!`) are universal and cannot be remapped.

## Global config (ask AI)

`~/.config/vroom/config.toml` (respects `$XDG_CONFIG_HOME`; override with
`$VROOM_CONFIG`). Without a file, everything works with defaults.

```toml
[ask]
launcher = "auto"     # auto | herdr | inline | custom
direction = "right"   # herdr split: right | down
target = "pane"       # pane | tab
focus = false         # false = --no-focus (vroom keeps focus)
# launcher_cmd = "tmux new-window -c {dir} -n vroom-{agent} -- {cmd}"  # custom only
# prompt = "Given the app {name} with logs in {logs}, "  # input prefill; "" disables

# ── Keybindings ───────────────────────────────────────
# Maps action name → key; any missing action keeps its
# default. Remapping onto a universal key, duplicating a key across
# actions or using an unknown action invalidates the config (defaults
# + warning on startup). The help bar reflects what is configured.
[keybindings]
start_stop = "s"  # toggle start/stop (also on groups)
restart    = "R"  # restart (stop → start)
build      = "b"  # one-shot build
install    = "i"  # one-shot install
tasks      = "t"  # mise task picker
ask        = "a"  # ask AI
clear      = "C"  # clear in-memory console
stream     = "c"  # mode merged → stdout → stderr
top        = "g"  # scroll to top (pauses follow)
bottom     = "G"  # scroll to bottom (reactivates follow)
logs       = "l"  # logs in editor (fixed alias: o)
refresh    = "r"  # forced refresh
```

**How vroom opens the agent** (worktree pattern: the agent binary with the
prompt as argument, in the project directory):

- `herdr` — opens a **new pane/tab** (`pane split --cwd <project>` + `pane run`)
  and runs the agent there; vroom stays alive. Requires running vroom inside herdr
  (`HERDR_ENV=1`).
- `inline` — suspends vroom and runs the agent in the foreground; on exit, you return
  to the dashboard.
- `custom` — your shell template with placeholders: `{dir}` (project),
  `{agent}` (name) and `{cmd}` (full command, quoted). Example with tmux:
  `tmux new-window -c {dir} -n vroom-{agent} -- {cmd}`.
- `auto` (default) — herdr if available; if not, inline.

Built-in agents (only those installed in PATH are shown; with only one the picker is
skipped):

| Agent | Invocation |
|---|---|
| opencode | `opencode --prompt "<prompt>"` |
| pi | `pi "<prompt>"` |
| hermes | `hermes chat -q "<prompt>"` (in TTY the session becomes interactive) |
| jcode | `jcode run "<prompt>"` (one-shot: responds and exits; its TUI does not accept an initial prompt) |

**Prompt prefill**: when opening the ask input, vroom assumes the request is
about the selected app and prefills the `[ask] prompt` template with the
placeholders `{name}` (project), `{dir}` (path) and `{logs}` (service
directory with `stdout.log`/`stderr.log`); the cursor is placed at the end so you
can type your request. The input is multi-line: starts large, grows with
content up to a cap and then scrolls internally. With `prompt = ""` the input
stays empty.

A malformed config breaks nothing: vroom applies defaults and notifies the error on startup.

## Integration with mise (optional)

**vroom does not require mise.** Everything essential (start/stop, logs, threads) works
with just `.vroom.toml`. The integration exists at two points, and both are opt-in:

1. **`b` (build) and `i` (install)** execute the command you put in the
   manifest, with `sh -c` in the project directory. If you prefer mise,
   you write `commands.build.run = "mise run build"`; if you prefer pnpm,
   `commands.build.run = "pnpm build"`.
   vroom never adds `mise run` on its own.
2. **`t` (tasks)** lists the tasks from the `[tasks.*]` section of the project's
   `mise.toml` (direct file parsing; listing does **not** need the binary). When
   selecting one, `mise run <task>` is executed — there you do need mise
   installed in PATH. If the project has no `mise.toml`, the key only
   notifies `no mise.toml`.

Example of a frontend with mise:

```toml
# mise.toml
[tasks.install]
run = "pnpm install"

[tasks.build]
description = "Production build"
run = "pnpm build"

[tasks.serve]
run = "pnpm dev"
```

```toml
# .vroom.toml
name = "web-frontend"
commands.start.run = "pnpm dev"          # or "mise run serve"
commands.install.run = "mise run install" # i key
commands.build.run = "mise run build"     # b key
port = 5173
```

Build/install/tasks output goes with a banner to the project logs and is seen
in the Console tab; on completion it notifies `build ok (3.2s)` or
`build failed (exit 1)`. Only one job per project can run at a time; jobs
survive TUI close (same semantics as services).

## State and logs

```
~/.local/state/vroom/services/{hash}/   # hash = 8 hex of SHA-256 of the project path
├── meta.json    # name, pid, pgid, port, url_generation, status...
├── pid, pgid    # process credentials (cleared on stop)
├── stdout.log   # service stdout
└── stderr.log   # service stderr (kept as history)
```

Services start with `setsid` (new session leader): close the TUI and they stay alive;
on reopen it re-attaches to state and verifies processes with PID-reuse protection.

## Development

```bash
make check           # build + lint + test: the local Lint/Test gate (Mutation: make mutate-diff)

go test ./...        # unit + integration (the real-portless tests skip)
go vet ./...

# The tests that need a REAL portless and its proxy isolate PORTLESS_STATE_DIR in a
# temp dir (they never touch ~/.portless) and skip when the binary is missing:
VROOM_PORTLESS_INTEGRATION=1 go test ./internal/portless/

# CI runs exactly that in its `Integration` job — which installs Node 24 and
# portless@0.15.6 (pinned) — plus VROOM_PORTLESS_INTEGRATION_STRICT=1, turning an
# environment skip (no binary, proxy that never opens its port) into a failure, so
# a green job can never mean "every integration test was skipped".
```
