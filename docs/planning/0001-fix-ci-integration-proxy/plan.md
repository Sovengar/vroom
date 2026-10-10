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

Phase: review

Review: 2026-08-24 — verdict **fix**: 0 CRITICAL, 0 HIGH, 2 MEDIUM, 8 LOW across five parallel reviews (behavior 2 LOW · code 4 LOW · security 0 · performance 1 MEDIUM + 2 LOW · docs 1 MEDIUM). Verification: passing (Lint, CI fast, Test, Integration green; Mutation skipped).

| Scenario (behavior.feature) | Status | Commit |
| --- | --- | --- |
| A route the proxy picks up inside the window is published | ✅ | 7c1ab05 |
| A genuinely unserved route still degrades when the window closes | ✅ | 7c1ab05 |
| A declared proxy port that refuses connections degrades at once | ✅ | f806c8e |
| A routed-but-dead backend still publishes its Url | ✅ | 7c1ab05 |
| A served route never pays the window | ✅ | f806c8e |
| No proxy running degrades without probing | ✅ | 7c1ab05 |
| The window is on by default and can be opted out | ✅ | f806c8e |
| The ladder e2e Url answers from its own backend | ✅ | 7c1ab05 |
| A route survives a proxy restart | ✅ | 7c1ab05 |
| Live portless routes are not evicted | ✅ | 7c1ab05 |
