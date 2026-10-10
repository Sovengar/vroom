Status: approved

# Fix: `Integration` job red on every push to main (portless route propagation race)

Branch: `fix/ci-integration-proxy`

## User Story

As a vroom maintainer,
I want the `Integration` CI job to be green on the PR and on `main`,
so that the required-check gate stays trustworthy and pushes to `main` stop failing CI for a race that has nothing to do with the code under change.

## Problem & evidence

The `.github/workflows/ci.yml` job `Integration` ("Integration tests against the real portless") has been red on **every push to `main` since Oct 9**. It is:

- **Deterministic** — the original run and its rerun fail identically.
- **CI-only** — the same suite passes 5/5 on the developer machine.
- **Unrelated to the recent url_generation refactor** — it fails on the same 3 tests across tips.

Failing run: <https://github.com/Sovengar/vroom/actions/runs/38059158256> (tip `a261fb9`) — only `Integration` red; `Lint`/`Test` green; `Mutation` skipped (push to `main`, non-PR).

Exactly **3 tests** fail, all in `internal/portless/`:

| Test | Location |
|------|----------|
| `TestRouteSurvivesAProxyRestart` | `integration_test.go` (reported at :177) |
| `TestIntegrationDoesNotEvictLivePortlessRoutes` | `integration_test.go` (reported at :231) |
| `TestLadderAgainstARealPortlessProxy` | `ladder_e2e_test.go` (reported at :44) |

Symptom in all three: after vroom registers the portless alias, a probe through the isolated proxy gets **404**; the route ends `Status: degraded`, `Reason: route_not_served`, empty `Url`, `Port: 0`, `Registered: false`.

### CI job facts

- `ubuntu-24.04`; Go from `go.mod`; **Node 24** via `setup-node`; **portless pinned to 0.15.6** (`npm install -g portless@0.15.6`, version asserted after install).
- Runs `go test -race -count=1 ./internal/portless/` with `VROOM_PORTLESS_INTEGRATION=1` and `VROOM_PORTLESS_INTEGRATION_STRICT=1` (STRICT turns environment skips into failures).
- Every test isolates `PORTLESS_STATE_DIR` in a temp dir and starts its own proxy on a free port; nothing touches `~/.portless`.
- `ci-fast.yml` (feature-branch pushes) runs only build + unit tests: the real-portless suite exists **only** in the `ci.yml` `Integration` job — on PRs and on pushes to `main` — so a failure the dev machine cannot see stayed invisible on the branch until it reached `main`.

## Root cause

portless exposes a freshly written alias to its proxy **asynchronously**:

- normally via the `fs.watch` debounce (~100 ms), so the route is served almost immediately;
- via a **3 s polling fallback** when the watcher is unavailable — which is the case on the inotify-starved CI runner.

vroom's `verify()` probed **exactly once**, read the stale proxy cache, and degraded `{degraded, route_not_served}` a route the proxy was about to serve. On a developer machine the watcher works, the route is served within ~100 ms, and the single probe passes — hence CI-only.

## What the prototype already delivered (starting point, not a proposal to redo)

These commits are **already on the branch** and are the change under test. They are bounded and local:

- `7c1ab05` — **`fix(portless): wait out the proxy route propagation window`**
  - `verify()` now retries the https/http probe for a bounded window: `DefaultVerifyWait` 3.5 s, `verifyPollInterval` 250 ms, new `WithVerifyWait` option. `WithVerifyWait(0)` restores the previous single immediate probe so the deterministic fakes stay fast; a proxy whose declared port refuses connections still degrades at once.
  - Ladder e2e (`ladder_e2e_test.go`) now waits for the expected backend body instead of a single request.
  - Tests added: `TestVerifyWaitsForTheProxyToPickUpAFreshlyWrittenRoute`, `TestVerifyDegradesAfterThePropagationWindowCloses`, `TestNewInstallsTheDefaultPropagationWindow`.
  - `docs/FEATURES.md` updated to describe the bounded wait.
- `36ecaf2` — **`chore(prototype): reproduce the CI integration proxy failure locally`**: `docs/planning/0001-fix-ci-integration-proxy/repro-ci-integration-proxy.sh` forces a scratch portless@0.15.6 into its polling fallback (without touching the global portless, `~/.portless` or the main checkout) so the exact CI failure is runnable locally.

**Local verification already done (nothing pushed):** forced-polling repro green, CI-equivalent suite green (healthy ~13 s / forced ~31 s), `make check` green.

## Remaining scope

1. **Verify the fix against the CI-equivalent path.** Open the PR and confirm the `Integration` job is green; after merge confirm the `push` run on `main` is green.
2. **Review the prototype diff as the change under test** — treat `7c1ab05` (and the repro `36ecaf2`) as the implementation, not as something to rewrite.
3. **Keep every existing gate green:** `Lint`, `Test`, `Mutation`; diff coverage at 100 % against the merge-base and the total at/above `scripts/coverage-floor`.
4. **Capture behavior** in `behavior.feature` (expected vs actual) for a reviewer.
5. **Docs:** `docs/FEATURES.md` is already updated by the fix; update the global skill `~/.agents/skills/vroom/SKILL.md` in the same change if the verify behavior is user-visible (it currently does not mention the propagation window).
6. **Land on `main`** (user instructed: finish everything and merge).

## Non-goals

- **No portless version bump or pin change** — CI intentionally tests **0.15.6 as installed**; the whole point is that vroom tolerates the runner's environment.
- **No refactor** — a field fix; the diff stays small.
- **No unrelated CI jobs** touched.
- **Nothing that changes the mutation gate's decision** (`scripts/mutate.sh`, `.mutation-allowlist`, `.mutation-timeouts`, `scripts/coverage-floor`) beyond what this change strictly requires.
- No change to the degradation semantics: a genuinely unserved route still ends `degraded` / `route_not_served`, just after the bounded window instead of on the first probe.

## Acceptance criteria

- [ ] `Integration` job green on the non-draft PR (`go test -race -count=1 ./internal/portless/` with `VROOM_PORTLESS_INTEGRATION=1` and `VROOM_PORTLESS_INTEGRATION_STRICT=1`).
- [ ] `Integration` job green on the `push` run to `main` after merge.
- [ ] `make check` green locally (build + lint + test).
- [ ] Forced-polling repro green: `bash docs/planning/0001-fix-ci-integration-proxy/repro-ci-integration-proxy.sh`.
- [ ] `behavior.feature` scenarios written and verified (expected vs actual for the propagation window and the degradation contract).
- [ ] `Lint`, `Test` and `Mutation` green on the PR; diff coverage 100 % and total ≥ `scripts/coverage-floor`.
- [ ] Docs updated in the same change: `docs/FEATURES.md` (already touched) and `~/.agents/skills/vroom/SKILL.md` if the behavior is user-visible.
- [ ] Change merged to `main`; deployable binary (`make install`) still builds and revision-verifies.

## Risks

- **Bounded window slows `Apply` only for not-yet-served routes.** A served route returns on the first probe, so the common case is unchanged; only a 404 keeps probing until the window closes or the route is served. The deterministic tests pin `WithVerifyWait(0)` so they do not pay the wait.
- **Margin is thin.** `DefaultVerifyWait` is 3.5 s against a 3 s polling interval (~500 ms headroom). If portless's fallback interval ever grows, this regresses; the 250 ms poll interval is a deliberate trade to catch the 100 ms debounce quickly without spinning the proxy.
- **A genuinely unserved route now takes up to ~3.5 s to degrade** instead of failing on the first probe — an `Apply` latency cost, accepted to stop misreporting a route the proxy is about to serve.
- **CI flakiness sources beyond this race** (runner inotify state, Node/portless version assertions, free-port races) are not addressed here; the fix targets the reproduced root cause only.
- **Mutation:** the new loops/branches in `apply.go` and `portless.go` must not add survivors; if any appear they must be killed by tests, not allowlisted.

## Open questions

- Should the propagation-window fact be recorded in `docs/adr/adr-0013-vroom-registers-portless-routes.md` (MEASURED facts) or left as an implementation note? The prototype recorded it only in `docs/FEATURES.md`.
- Is the 3 s polling interval a stable, documented portless 0.15.6 detail, or an environment-dependent one? The brief measured it; a durable margin may want a confirming note.
- Does the added wait threaten the `Integration` job's 10-minute timeout on a cold runner? Local CI-equivalent runs measured ~31 s forced, so no — to be confirmed on the PR.

## References

- Prototype brief: `docs/planning/0001-fix-ci-integration-proxy/prototype-brief.md`
- Repro script: `docs/planning/0001-fix-ci-integration-proxy/repro-ci-integration-proxy.sh`
- Failing run: <https://github.com/Sovengar/vroom/actions/runs/38059158256>
- CI workflow: `.github/workflows/ci.yml` (job `Integration`)
- Fix commit: `7c1ab05`; repro commit: `36ecaf2`
