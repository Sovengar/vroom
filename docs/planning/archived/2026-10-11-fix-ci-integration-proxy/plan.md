# Plan — `fix/ci-integration-proxy`

adr_required: true
adr: amend `docs/adr/adr-0013-vroom-registers-portless-routes.md` — add a measured-facts row for the async route propagation (fs.watch debounce vs 3 s polling fallback) and correct §6, which claims verify sends "one request" and is stale after this change. Fallback if a new file is preferred: `adr-0015-portless-route-propagation-window`, with a pointer from §6.

## Outcome

The `Integration` job is green on the non-draft PR and on the `push` run to `main`: `Apply` no longer degrades a freshly written portless route the proxy is about to serve, while a genuinely unserved route keeps the same `degraded` / `route_not_served` semantics, just after the bounded window. Every existing gate stays green and the change lands on `main`.

## Approach (high level)

The fix is already committed (`7c1ab05`) and **is the change under test — verify it, do not rewrite it**. It retries the https/http route probe inside `verify()` for a bounded window (default 3.5 s, 250 ms poll; `WithVerifyWait` to override, `0` restoring the old single probe), breaks early when the declared proxy port refuses connections, and the ladder e2e waits for the expected backend body. The repro commit `36ecaf2` forces the CI polling fallback locally. This run reviews that diff as the implementation, closes the doc/gate edges, and lands it.

## Key decisions

- Fix stays as committed: no refactor, no portless pin change, no unrelated CI jobs, mutation-gate files untouched (issue non-goals). Prototype commits are the pipeline's starting point, not a proposal to redo.
- The propagation window is a measured dependency fact of portless 0.15.6 → record it in ADR-0013 (measured row + §6 correction), not only in `docs/FEATURES.md` (already updated by the fix).
- `~/.agents/skills/vroom/SKILL.md`: one clause on the bounded probe (a route the proxy is about to serve now publishes its URL; an unserved route degrades after the window).
- Mutation: expect timing-only survivors around the `answered` flag and the early-break branch in `apply.go`; kill them with a timing-tolerant test (dead port + wait > 0 returns well before the window), never a silent allowlist.
- ADR-0014 (code rejection) is a separate queued run — untouched here.

## Risks

- Thin margin: 3.5 s window vs portless's 3 s polling fallback; Node timer slip under a loaded runner is the only remaining failure mode. Watch the PR `Integration` run; the single knob if slip appears is `DefaultVerifyWait` — do not raise it preemptively (every extra second is paid by every genuinely unserved start).
- A not-yet-served route now takes up to ~3.5 s to degrade; accepted, bounded, and paid at most once per start in the ladder (only a pre-write `route_conflict` advances a rung).
- e2e timing on a loaded runner: suite measured ~31 s forced vs the job's 600 s budget; no threat.

## Ordering (coarse)

1. Treat the prototype diff as the implementation and verify locally: forced-polling repro script + CI-equivalent portless suite.
2. Close the doc edges (ADR-0013 amendment, SKILL.md clause).
3. Run the gates (`make check`, mutation diff, coverage), adjusting tests only if a survivor or coverage gap demands it.
4. PR → `Integration` green → merge → confirm the `main` push run green and `make install` revision check.

## Progress

Phase: close

Close: started 2026-10-11

Review: 2026-08-24 — verdict **fix**: 0 CRITICAL, 0 HIGH, 2 MEDIUM, 8 LOW across five parallel reviews (behavior 2 LOW · code 4 LOW · security 0 · performance 1 MEDIUM + 2 LOW · docs 1 MEDIUM). Verification: passing (Lint, CI fast, Test, Integration green; Mutation skipped).

Fix pass: MEDIUM-1 (verify's retry window is now cancellable — `ApplyContext` + `select` on `ctx.Done()`, commit 2bc5f2a) and MEDIUM-2 (ADR Decision 7 probe cost under the retry window, commit 428991c) resolved. Re-verified: `make check` green, diff coverage 100% (69/69), total 100%, Mutation 4 killed / 0 survived.

Fix pass (round 2): the re-review's two new MEDIUMs resolved — nil-context guard on the exported `ApplyContext` (commit b141ecd) and the production wiring (`RouteRegistrar.ApplyContext` + `Request.Ctx`, signal/command lifetime from CLI `cmdStart` and TUI `runTUI`/`startCmd`, commit c3cc86d); ADR probe count corrected to ~30 (commit e5735d0). Re-verified: `make check` green, diff coverage 100% (99/99), total 100%, Mutation 10 killed / 0 survived.

Fix pass (round 3): HIGH (Ctrl-C now terminates `vroom start` — `runStart` selects the start against the command context, commit 39f1785) and the two MEDIUMs (cancellation reported as reason `cancelled`, commit dccaa0b; `behavior.feature` gains the cancellation scenario). Ctrl-C verified end-to-end against a real child process (SIGINT during discovery → exit in ~2 ms).

Review 2: 2026-08-24 (tip 694b6c7) — verdict **fix**: 0 CRITICAL, 0 HIGH, 2 MEDIUM, 8 LOW (behavior 1 MEDIUM + 3 LOW · code 1 MEDIUM · security 0 · performance 0 MEDIUM + 3 LOW · docs 2 LOW). Both prior MEDIUMs confirmed resolved. New MEDIUMs, both on the `ApplyContext` seam: (1) exported method panics on a nil context (doc-comment-only guard, apply.go:101); (2) no production caller — `startsvc` still routes through `Apply` → `context.Background()`, so the TUI/CLI never benefit from the cancellation they motivated the fix (apply.go:96-101, startsvc.go:292-295). Verification: passing (Lint, CI fast, Test, Integration green; Mutation skipped). Change left unopened.

Review 3: 2026-08-24 (tip e236ea3) — verdict **fix**: 0 CRITICAL, 1 HIGH, 3 MEDIUM, 5 LOW (behavior 1 HIGH + 2 MEDIUM · code 0 · security 0 + 1 LOW · performance 0 + 1 LOW · docs 1 MEDIUM + 3 LOW). Round-2 MEDIUMs resolved (`b141ecd` nil guard; `c3cc86d` lifetime wiring — RouteRegistrar.ApplyContext, Request.Ctx, CLI signal ctx, TUI program ctx) and both prior lenses verify the wiring correct, tested and cheap. But the wiring introduced a HIGH regression: `internal/cli/cli.go:508-551` — `signal.NotifyContext` captures SIGINT for all of cmdStart and nothing checks `ctx.Err()`, so Ctrl-C no longer terminates `vroom start`; it only cuts the propagation window while the start completes and the service launches anyway (exit 0), an uninterruptible span of up to ~8 s+ (port discovery + window). MEDIUMs: (a) the cancellation seam has no behavior.feature scenario; (b) a cancelled window is reported as `route_not_served` with the "proxy does not serve this route" warning — no cancelled reason; (c) the abort behavior is user-visible but absent from docs/FEATURES.md and the vroom SKILL.md (same-change rule), whose window clause is now stale. Verification: passing. Change left unopened; the interrupt-semantics decision (abort-on-cancel vs document the swallow vs drop the CLI signal handler) is the executor's next call.

Review 4: 2026-08-24 (tip 673cdec) — verdict **fix**: 0 CRITICAL, 0 HIGH, 3 MEDIUM, 14 LOW (behavior 1 MEDIUM · code 1 MEDIUM + 4 LOW · security 1 MEDIUM + 3 LOW · performance 0 + 2 LOW · docs 0 + 5 LOW). Round-3 HIGH confirmed fixed (`runStart` select — Ctrl-C aborts with exit 1, pinned by tests); `cancelled` reason plumbed consistently (Warn table, route_reason JSON, Registered, no path reports `route_not_served`); happy path pays nothing and Ctrl-C cuts at once; FEATURES.md + SKILL.md now document the cancellation (round-3 docs MEDIUM's main component closed). New MEDIUMs, all on the cancel path: (1) cli.go:509-525 — the abandoned start goroutine is never joined before `os.Exit(1)`, so `persistOrKill` typically never runs: daemon untracked (no meta/pid), portless route unowned, cancelled warning discarded, next start falls back to another ladder candidate; the TUI path (waits) saves everything, so CLI and TUI disagree on post-cancel state; (2) cli.go:509-525 → startsvc.go:108-120 → logrun.go:35-44 — Ctrl-C during the `command_pre_start` hook orphans the hook's `sh` child mid-`cmd.Run()` (reparented, holds service log fds); (3) apply.go:210-213 — verify's answered-404 degradation path drops `Registered:true` that the other three paths keep, so stop/Retain/Reconcile leave an orphan route that can collide on a moved port (pre-existing from 7c1ab05; round-1 code lens read it as deliberate parity — executor may reject with a structured error if that is the documented intent). Verification: passing. Change left unopened.

Fix pass (round 5): the three round-4 MEDIUMs the user ordered fixed. (1) runStart joins the cancelled start and every blocking phase now honours the command lifetime (logrun.RunContext reaps the commands.start.hooks.pre_run process group; DiscoverPortContext cuts the poll bout; startsvc unwinds on a cancelled discovery with the port_pending attempt already persisted), so the daemon stays tracked, the route owned and the cancelled reason recorded before exit — commit 6aa1ce0. (2) same commit closes the orphaned hook. (3) the answered-404 degradation keeps the write's ownership handle: `verify` stays a pure probe and `Apply` — which knows the write happened — stamps `Registered` on every post-write verdict, commit 1cd64e7.

Review 5: 2026-10-11 (tip f4cd111, rebased onto latest main) — verdict **fix**: 0 CRITICAL, 0 HIGH, 2 MEDIUM, 10 LOW (behavior 0 + 2 · code 1 MEDIUM + 3 LOW · security 0 + 2 LOW · performance 1 MEDIUM + 2 LOW · docs 1 MEDIUM + 1 LOW). All three round-4 MEDIUMs confirmed genuinely fixed in the code paths, not the commit messages: runStart joins the start via a buffered-channel select before returning the cancel error; RunContext's Setpgid + group-kill runs as exec Cancel with ctx threaded through preRunHook and DiscoverPortContext, and a cancelled discovery unwinds keeping the persisted port_pending attempt; Apply stamps Registered on every post-write verdict with verify a pure probe. All 13 behavior.feature scenarios map to real tests pinning their Then-clauses; FEATURES.md, SKILL.md and ADR-0013 match the code. New MEDIUMs, both on edges the round-5 fix left open: (1) `startsvc.go` postRun still runs the `commands.start.hooks.post_run` hook through `logrun.Run` with no context — after a cancelled verify the unwind executes the post_hook to completion, and the new runStart join can block CLI exit for the hook's full unbounded duration (found independently by the code and performance lenses, same root); (2) `docs/planning/0001-fix-ci-integration-proxy/context.md:80` still states the answered-404 degradation is the only path that drops Registered, contradicting commit 1cd64e7. Verification: passing (Lint, CI fast, Test, Integration green; Mutation skipped — draft-PR gate). Change left unopened.

Fix pass (round 6): the two round-5 MEDIUMs. (1) `commands.start.hooks.post_run` now runs through `logrun.RunContext` with the command lifetime threaded into `postRun` exactly like `preRunHook`, so a cancelled start (Ctrl-C, a TUI quit) cuts the hook's process group at once instead of sitting out its unbounded duration and blocking runStart's join-on-cancel; covered by `TestCancelledStartCutsThePostRunHookAtOnce` (committed with this pass). (2) `context.md:80` corrected: `Apply` stamps `Registered` on every post-write verdict (commit 1cd64e7) and only a pre-write refusal returns `Registered=false` — the stale "only path that drops Registered" note is gone. Trivially-safe batched LOW: `applyRoute` normalises the lifetime via `req.context()`.

Review 6: 2026-10-11 (tip c57132e, delta f4cd111..c57132e) — verdict **pass**: 0 CRITICAL, 0 HIGH, 0 MEDIUM, 4 LOW (behavior 0 + 2 · code 0 + 1 · security 0 · performance 0 · docs 0 + 1). Both round-5 MEDIUMs confirmed genuinely fixed: (1) `postRun` mirrors `preRunHook` exactly (`req.context()` → `logrun.RunContext` with Setpgid + group-reap as exec Cancel), both cancel roads — cancelled discovery and cancelled verify — unwind into the postRun call site with an already-cancelled ctx, so runStart's join is bounded on every cancel path at zero happy-path cost; `TestCancelledStartCutsThePostRunHookAtOnce` pins the Then (err nil, fast return, side-effect file never appears, warning names post_run) with 8 s hook vs 4 s threshold margins. (2) `context.md:80` now states the post-1cd64e7 contract and matches `apply.go` (pre-write refusals and a failed Register write are the only `Registered=false` paths); FEATURES.md and the vroom SKILL.md both say pre_run and post_run hooks are cut on cancel. The batched LOW landed (`applyRoute` uses `req.context()`; no raw `req.Ctx` left in startsvc). LOWs recorded: cancelled post_run cut logged with the same wording as a real hook failure; only the cancelled-discovery road into postRun is test-covered; context.md:80 omits the failed-Register path in its prose; range also carries the round-5 progress commit 2c21d4e. Verification: passing (Lint, CI fast, Test, Integration green; Mutation skipped — draft-PR gate; the local-lint red from the parent checkout's untracked .golangci.yml is not a change defect — CI lints the isolated checkout). Change opened for integration.

| Scenario (behavior.feature) | Status | Commit |
| --- | --- | --- |
| A route the proxy picks up inside the window is published | ✅ | 7c1ab05 |
| A genuinely unserved route still degrades when the window closes | ✅ | 1cd64e7 |
| A cancelled start unwinds before the command exits | ✅ | 6aa1ce0 |
| A cancelled commands.start.hooks.pre_run leaves no orphan | ✅ | 6aa1ce0 |
| A declared proxy port that refuses connections degrades at once | ✅ | f806c8e |
| A routed-but-dead backend still publishes its Url | ✅ | 7c1ab05 |
| A served route never pays the window | ✅ | f806c8e |
| No proxy running degrades without probing | ✅ | 7c1ab05 |
| The window is on by default and can be opted out | ✅ | f806c8e |
| A cancelled command stops the start and is not misreported | ✅ | dccaa0b |
| The ladder e2e Url answers from its own backend | ✅ | 7c1ab05 |
| A route survives a proxy restart | ✅ | 7c1ab05 |
| Live portless routes are not evicted | ✅ | 7c1ab05 |
