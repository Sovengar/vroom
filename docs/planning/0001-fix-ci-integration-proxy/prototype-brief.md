# Prototype brief — `fix/ci-integration-proxy`

## Goal
Get a quick, local handle on the CI-only failure of the `Integration` job: a **reproduction** the user can run, the **root cause** (runner env vs code), and a **candidate fix** committed on this branch.

## Problem & evidence
- `.github/workflows/ci.yml`, job `Integration` ("Integration tests against the real portless") is red on **every push to main since Oct 9** — deterministic (original run and rerun fail identically), CI-only, and unrelated to the recent url_generation refactor.
- Failing run: https://github.com/Sovengar/vroom/actions/runs/38059158256 (tip `a261fb9`) — only `Integration` red; Lint/Test green; Mutation skipped (push to main).
- Exactly 3 tests fail, all in `internal/portless/`:
  - `TestRouteSurvivesAProxyRestart` (integration_test.go:177)
  - `TestIntegrationDoesNotEvictLivePortlessRoutes` (integration_test.go:231)
  - `TestLadderAgainstARealPortlessProxy` (ladder_e2e_test.go:44)
- Symptom in all three: after vroom registers the portless alias, a probe through the isolated proxy gets 404; the route ends `Status: degraded`, `Reason: route_not_served`, empty `Url`, `Port: 0`, `Registered: false`.
- On this machine the same suite passes (5/5).

## What the CI job does
- ubuntu-24.04; Go from `go.mod`; **Node 24** via setup-node; **portless pinned to 0.15.6** (`npm install -g portless@0.15.6`, version asserted after install).
- Runs `go test -race -count=1 ./internal/portless/` with `VROOM_PORTLESS_INTEGRATION=1` and `VROOM_PORTLESS_INTEGRATION_STRICT=1` (STRICT turns environment skips into failures).
- Per the workflow comments: every test isolates `PORTLESS_STATE_DIR` in a temp dir and starts **its own proxy on a free port**; nothing touches `~/.portless`. CI also exports `PORTLESS_BIN` — check whether the tests read it.

## Prototype result (on this branch)
- **Root cause**: portless exposes a freshly written alias to its proxy asynchronously — `fs.watch` debounce normally, a **3s polling fallback** when the watcher is unavailable (the CI runner) — while vroom probed the route exactly once and degraded `{degraded, route_not_served}` on the stale cache.
- **Reproduction**: `docs/planning/0001-fix-ci-integration-proxy/repro-ci-integration-proxy.sh` (commit `36ecaf2`) forces portless into its polling fallback and shows the exact CI signature pre-fix, green post-fix.
- **Candidate fix** (commit `7c1ab05`): `verify()` retries the probe for a bounded window (`DefaultVerifyWait` 3.5s, 250ms interval, `WithVerifyWait`; a proxy refusing connections still degrades immediately).
- Verified locally: forced-polling repro green, CI-equivalent suite green (healthy 13s / forced 31s), `make check` green, nothing pushed.

## Leads (verify, don't trust)
- Local vs runner delta: exact `node --version` and `portless --version` on both; how the tests locate the portless binary.
- Anything the fix's bounded window should also cover (other verification paths), and whether the ladder/restart tests need their own waits.

## Definition of done
1. The CI failure is fixed on the branch, with the existing gates green.
2. Behavior captured so a reviewer can see expected vs actual.
3. Landed on `main` (user instructed: finish everything and merge).

## Constraints
- Branch `fix/ci-integration-proxy` only; publish through the pipeline's normal flow.
- Minimal change; this is a field fix, not a refactor.
