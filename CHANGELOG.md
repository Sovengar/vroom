# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **One start axis: `url_generation` replaces `port_mode` + `route_mode`** in
  `.vroom.toml`. The five values are `by_port` (bind the declared port, no route;
  refuses an occupied port), `by_hostname` (ephemeral port + `route_name`; refuses
  only a proven foreign holder), `by_workspace_hostname` (ephemeral port +
  `<branch>.<project>`), `by_hostname_or_workspace` (the claim ladder: `route_name`
  first, branch hostname when taken) and `none` (headless). The port vocabulary is
  derived from the generation, so the port can no longer contradict the address. A
  manifest without the key keeps the historical behavior: a declared `port > 0`
  means `by_port`, `port = 0` means `none`.
- `[worktrees].url_generation` overrides the top-level value for a worktree copy
  (absent = inherit), so one committed manifest serves `main` one way and every
  worktree another.
- The TUI `s` on a **stopped** service now **arms** a start-generation selector
  (`p` = `by_port`, `u` = `by_hostname`, `w` = `by_workspace_hostname`,
  `f` = `by_hostname_or_workspace`; any other key cancels) instead of starting
  outright. The chosen generation is recorded in the service state and outranks the
  manifest on every later start (TUI `R`, `vroom start`, stacks).
- `url_generation` in the `vroom list` / `vroom status` JSON: the recorded choice,
  else the worktree-aware manifest default. Switching a service to `by_port` or
  `none` retires the route a previous hostname start left behind (only a clean
  retirement clears the ownership handle, so a later start can still reconcile).
- ADR-0014 documenting the single-axis decision, its five values, the derived port
  vocabulary and the partial supersession of ADR-0013's `route_mode` vocabulary.
- Stable URL for the changing port via the hostname generations of
  `url_generation` (`by_hostname`, `by_workspace_hostname`,
  `by_hostname_or_workspace`) and `route_name` in `.vroom.toml`. vroom registers an
  alias route in portless per service and removes it on stop; `by_port` and `none`
  do not look for the binary, and no existing manifest changes behavior.
  `by_workspace_hostname` derives the name from the branch (`<branch>.<project>`), so
  two branches of the same repo do not share an address; two worktrees on the same
  branch receive a conflict warning instead of overwriting each other.
  `by_hostname_or_workspace` is a claim ladder: `route_name` (what a `redirect_uri`
  or a CORS origin list requires) is claimed first, and only when another worktree
  already holds it does the service fall back to `<branch>.<project>` — with a
  warning naming the port that holds the stable name — so whoever starts first owns
  the stable URL and every other worktree still gets an address instead of no URL at
  all. A service that later claims the stable name retires its old branch-derived
  route in the same start.
- **vroom only registers the route: it does not start, manage, supervise or show the
  proxy.** If there is no `portless`, it is not in `PATH`, its proxy is not running or its Node
  is too old, vroom warns once and the service starts anyway, healthy and on
  its port. The health of a service never depends on its route existing: a route
  is an address, not a dependency.
- Two-step verification when registering a route, because `portless alias` only
  writes the state file and exits successfully even if the proxy is off: a
  read-back confirms the route belongs to that service, and a probe against the
  live proxy confirms the URL responds. A degraded route never publishes `url` and
  always says why; a `502` counts as "the proxy routes the route and your service
  does not respond", and only a refusal or a timeout mean there is no proxy.
- Route reconciliation on startup, and route removal when stopping the
  service from the CLI or the TUI. `portless prune` does not touch alias routes, so
  vroom is the only thing that can clean them: routes left by a vroom that
  died without stopping it, and the old route of a renamed branch, are removed on their own.
- `url_generation` (intention) and `route` (result) in the `vroom list` JSON, so
  an agent can distinguish what the service will start as from what it achieved, and
  route ownership persisted in the service state to be able to
  reconcile it without touching foreign routes.
- ADR-0013 documenting the route ownership model, the eleven measured
  facts of portless 0.15.6 that forced the design, and why verification costs two
  steps instead of one.
- README: portless is documented as an **optional dependency** (with its Node >= 24
  and how vroom resolves it), separated from `fd`, which is mandatory.
- Ephemeral ports per worktree in the hostname generations of `url_generation`
  (`by_hostname`, `by_workspace_hostname`, `by_hostname_or_workspace`).
  vroom reserves a port from the 4000-4999 range,
  injects `PORT` and `HOST` into the process environment, discovers the real
  listening port and uses it as the single source of truth in the TUI, in the JSON output and in the
  stack health gate. A manifest without `url_generation` behaves
  exactly as before.
- Deterministic selection of the main port when a service opens multiple
  listeners: first the reserved port, then the one that responds to the
  probe's `health_path`, and as a last resort the lowest one, marked as not
  verified.
- `port_verified` in the `vroom list` JSON output, to distinguish a port
  confirmed by vroom from one still pending resolution.
- ADR-0012 documenting the port ownership contract and the design of
  dynamic ports.
- `Output` panel with five new tabs besides Console and Threads:
  `Metrics` (CPU%, RSS, FDs and threads), `Git` (branch, status and recent commits),
  `Env` (process environment), `Timeline` (session events with duration)
  and `Health` (HTTP probe to the manifest port).
- Optional `health_path` field in `.vroom.toml` for the probe path of the
  Health tab (default `/`).
- Nest git worktrees under their repo's row: the repo is shown as
  a single collapsible row (collapsed by default) and its worktrees appear
  indented on expand, each operable like any project.
- Detect bare repos during the scan and show them as a non-executable
  container row, with their worktrees nested.
- Address projects by path in the CLI (positional or `--path`) and expose the
  repo/worktree relationship (`repo_root`, `is_worktree`, `bare_container`) in the
  `vroom list` JSON output, keeping the flat array for compatibility.
- Resolve orchestration stacks deterministically: on a
  duplicate `Manifest.Name` it fails explicitly listing the candidate paths,
  instead of arbitrarily choosing the last one.
- CI on GitHub Actions with four gates that run on every PR and on every push to
  `main`: `Build` (`go build ./...` + `go vet ./...`), `Lint` (`make lint`,
  golangci-lint v2.13.2), `Test` (`go test -race` on the whole suite, with `fd`
  installed on the runner for the scanner tests) and `Integration` (a real
  portless, see below). The merge to `main` is
  governed by the `protect-main` ruleset: PR mandatory, green
  checks required and force-push/deletion blocked, with admin bypass (deliberate).
  Dependabot updates GitHub Actions weekly and the README shows the
  workflow status badge.
- Integration tests against a **real portless**, now running in CI. A dedicated
  `Integration` job installs Node 24 and `portless@0.15.6` (pinned — the version
  every measured fact in the ADR was taken against) and runs the gated tests in
  `internal/portless` against a live proxy in an isolated state dir, never
  `~/.portless`. New among them: an end-to-end test of the claim ladder against
  that real proxy — the conflict comes out of the real `routes.json` (proving
  `portless alias` would have silently overwritten the first worktree's route), the
  second worktree publishes its own fallback URL while the stable one keeps
  answering on the first worktree's port, and the upgrade retires the abandoned
  name for real. `VROOM_PORTLESS_INTEGRATION_STRICT=1`, set only by that job,
  turns an environment skip (no binary, a proxy that never opens its port) into a
  failure, so a green job can no longer mean "every integration test was skipped".
  Each run now also reaps the proxy it started by PID: it used to leak a live
  process per run.

### Fixed
- The portless state directory now matches what the CLI writes: `XDG_STATE_HOME` is
  ignored (measured on 0.15.6), so vroom no longer reads `proxy.port` from a directory
  that never exists and reports every route as `proxy_not_running`.
- Stopping a service no longer leaves orphan processes: children that change
  SID on startup (`nohup`, `setsid`, `pm2`, `docker run -d`) are stopped
  following the lineage, and the port is only released when ownership is
  demonstrable, never blindly.
- `Stop` with a valid PID and no PGID is no longer a no-op: the PID is a
  death target by itself and its lineage is signaled process by process.
- Port reservation is safe against concurrent startups, and the port
  returns to the pool when stopping the service.
- A service without a port no longer aborts the entire stack nor takes down its
  healthy siblings.
- A port that vroom could not confirm is distinguished from a service without a port,
  both in the TUI badge and in the JSON output.
- The child process environment is no longer truncated when injecting `PORT` and `HOST`: it
  merges with the existing environment.
- Query bare repos to discover their worktrees (previously omitted).
- Show the repo row topology error even if the repo has children.
- Do not count or collapse nested worktrees when aggregating a group's status.
- Detect bare repos during the scan without an additional tree traversal.
- Query git only once per repo instead of once per project.
- Bound the real execution time of git via an effective timeout.
- A worktree marked as prunable by git but whose directory still
  exists is no longer shown as a top-level entry: it nests normally.
- A repo row without worktrees no longer shows the expansion glyph.
- Bare repo detection only considers `core.bare` within the
  `[core]` section of the config, avoiding false positives.
- Expose the repo topology error (`worktree_error`) in the
  `vroom list` JSON.
- Separate git output from stderr, so a warning does not corrupt
  parsing, and ignore output blocks with empty path.

### Changed
- **BREAKING (`.vroom.toml`): `port_mode` and `route_mode` are gone, replaced by
  the single `url_generation` key.** This is a hard rename with no aliases: a
  manifest that still declares `port_mode` or `route_mode` must migrate. Remove both
  keys and declare `url_generation`: `fixed` → `by_port`,
  `dynamic` with no route → `by_workspace_hostname`, `auto` →
  `by_workspace_hostname`, `named_with_auto_fallback` →
  `by_hostname_or_workspace`, `off` → drop the key. A service that used to be `none`
  (via `port_mode = "none"` or `port = 0`) must now declare
  `url_generation = "none"` explicitly if it wants that stated rather than inferred.
- The dashboard is composed of four sections with rounded border and title
  (`Projects`, `Details`, `Output`, `Keybinds`) and no longer shows the
  `vroom — projects in …` header, which was out of sync since the scan root became configurable.
- The `Console` panel is renamed `Output` (it also hosts Threads and the
  new tabs); navigation is now `1`…`7`, with `tab`/`shift+tab` to
  cycle.
- Inactive tabs use readable light gray (before, ANSI 8 was
  almost invisible on dark themes).
- `--path` parsing is predictable: accepts `--path <value>` and
  `--path=<value>`, and fails with a clear error on missing, empty or
  duplicate value (before it was silently ignored).
- When addressing a project by path, symlinks are resolved, so that
  a linked path points to the correct project.
- Stack validation iterates names in stable order, with reproducible
  messages and errors.

### Removed
- The `port_mode` and `route_mode` manifest fields and their `vroom list` JSON
  counterparts; `url_generation` is the single axis that replaces them.
- Dead code: `quickCheck`, `stacksEmitted`, the `services` parameter of
  `StopStack` and the `ServiceStatus` type.

[Unreleased]: https://github.com/Sovengar/vroom/commits/HEAD
