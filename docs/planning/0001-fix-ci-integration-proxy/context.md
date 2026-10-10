---
feature: 0001-fix-ci-integration-proxy
freshness: 36ecaf27dd8dbd4623e6e3738a7156f92ef7ae8d
codegraph: ready
generated_by: codebase-researcher
---

# Context: fix/ci-integration-proxy — review the bounded verify wait, close docs, land

## Scope
- In: review committed fix `7c1ab05` as the implementation (verify it, do not rewrite it); amend `docs/adr/adr-0013-vroom-registers-portless-routes.md` (new measured-facts row M19 + §6 correction); add one clause to `~/.agents/skills/vroom/SKILL.md`; run all gates; open PR, land on `main`, confirm the push run; `make install`.
- Out: no fix rewrite, no portless pin/version change, no mutation-gate file changes (`scripts/mutate.sh`, `.mutation-allowlist`, `.mutation-timeouts`, `scripts/coverage-floor`), ADR-0014 untouched, no new ADR file (fallback `adr-0015-portless-route-propagation-window` only if amending ADR-0013 proves impossible, with a pointer from §6).

## Files to Touch
| Symbol / Area | File | Lines | Why |
|---------------|------|-------|-----|
| Measured-facts table (M18 is the last row) | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/docs/adr/adr-0013-vroom-registers-portless-routes.md | 49-70 | Add row **M19**: a freshly written alias reaches the proxy asynchronously — `fs.watch` debounce (~100 ms) when the watcher works, 3 s polling fallback when it does not (the inotify-starved CI runner). Next free id is M19 |
| Decision 6 "one request" claim (now false) | same ADR | 176-193 (claim at 182) | Correct §6: verify now retries the probe for a bounded window (default 3.5 s, 250 ms poll) before declaring `route_not_served`; keep the true part — any HTTP response incl. 502 proves routing |
| `url_generation` degradation clause | /home/buble/.agents/skills/vroom/SKILL.md | 301-316 (insert after 316) | One clause: a route the proxy is about to serve now publishes its URL (bounded probe window); a genuinely unserved route degrades after the window with the same reason. User-visible ⇒ same-change update per repo AGENTS.md |
| FEATURES.md Layer 3 entry (already edited by the fix) | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/docs/FEATURES.md | 441-454 | Verify the committed edit reads correctly; close doc edges only, no rewrite |
| plan.md progress mirror | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/docs/planning/0001-fix-ci-integration-proxy/plan.md | 35-50 | Flip each scenario row to verified + its commit as gates pass |
| apply_paths_test.go (only if mutation demands it) | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/internal/portless/apply_paths_test.go | 220-243 | Possible timing-tolerant dead-port test (see Risks). Never rewrite existing tests |

Read-only references (the fix under test — review, do not modify):
| Symbol / Area | File | Lines | Why |
|---------------|------|-------|-----|
| `verify()` bounded window | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/internal/portless/apply.go | 145-204 | deadline 156; `answered` flag 158/169/192; early break on dead port 183-185; deadline break 186-188; degradations 151-153, 192-203 |
| `Apply()` | apply.go | 96-134 | `Registered` set at 119 (write happened); verify call at 133 |
| `Result` tri-state / `Degraded` / `Warn` | apply.go | 63-81, 251-283 | Contract + warnings, unchanged |
| `IsTestBinary()` / `ClientFor` | apply.go | 34, 37-49 | Test-binary gateway to a bin-less client (hermeticity) |
| `DefaultVerifyWait` / `verifyPollInterval` | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/internal/portless/portless.go | 29, 33 | 3.5 s / 250 ms |
| `verifyWait` field / `WithVerifyWait` / `New` | portless.go | 77-78, 93-94, 97-109 | Option seam; `0` restores the single immediate probe |
| Statuses / reasons | portless.go | 36-39, 42-52 | `registered`/`degraded`; `route_not_served`, `proxy_unreachable`, `proxy_not_running` (unchanged) |
| `PORTLESS_STATE_DIR` / `PORTLESS_BIN` seams | portless.go | 129-131, 142-144 | Env seams CI and the repro script rely on |
| `TestVerifyWaitsForTheProxyToPickUpAFreshlyWrittenRoute` | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/internal/portless/apply_paths_test.go | 137-170 | Window success path: fake probe counts calls, `WithVerifyWait(2s)` |
| `TestVerifyDegradesAfterThePropagationWindowCloses` | apply_paths_test.go | 173-203 | Window expiry → `route_not_served` (`WithVerifyWait(120ms)`) |
| `TestVerifyDegradesWhenDeclaredPortDoesNotAcceptConnections` | apply_paths_test.go | 220-243 | Dead port → `proxy_unreachable` at once — **but pins `WithVerifyWait(0)`** via `newClientWithProxy` (34-50) |
| `TestNewInstallsTheDefaultPropagationWindow` | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/internal/portless/portless_test.go | 130-137 | Default 3.5 s; `WithVerifyWait(0)` override |
| `fakePortless.client` (pins `WithVerifyWait(0)`) | portless_test.go | 114-127 | Deterministic fake pattern |
| `TestLadderAgainstARealPortlessProxy` + `assertRouted` | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/internal/portless/ladder_e2e_test.go | 19-110, 113-131 | 5 s bounded body wait, 100 ms poll — e2e counterpart of the same race |
| `TestRouteSurvivesAProxyRestart` | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/internal/portless/integration_test.go | 164-200 | CI-failing test #1 (M3 against real portless) |
| `TestIntegrationDoesNotEvictLivePortlessRoutes` | integration_test.go | 203-253 | CI-failing test #2 (M4) |
| Gate helpers: `skipOrFail` / `integrationStateDir` / `integrationBin` / `requireIntegration` / `startIsolatedProxy` | integration_test.go | 122-161, 255-273 | STRICT skip→fail; `PORTLESS_STATE_DIR` isolation; own proxy per test |
| Ladder rung-advance loop | /home/buble/dev/projects/vroom/.worktrees/vroom.fix-ci-integration-proxy/internal/startsvc/startsvc.go | 275-326 (loop 292-293) | Only a pre-write `route_conflict` advances a rung |

## Contracts
- `Result` tri-state: `Url` published only when seen working; `Registered` (the write happened) ≠ `Succeeded()` (verified) — `apply.go:63-77`, `apply.go:118-119`; ADR-0013 §12 (`adr-0013:283-288`) and §15 (`adr-0013:343-358`).
- Degradation reasons unchanged: `route_not_served`, `proxy_unreachable`, `proxy_not_running` — `portless.go:42-52`; verify paths `apply.go:151-153, 192-203`.
- `WithVerifyWait(0)` = the old single immediate probe; every deterministic fake pins it (`apply_paths_test.go:48`, `portless_test.go:125`) — keep it that way.
- ADR-0013 measured-facts numbering: M1–M18 taken (M18 at `adr-0013:70`); **next free id is M19**. Corrections are `> **CORRECTION (…)**` blockquotes under the affected decision (patterns at `adr-0013:140-143`, `adr-0013:260-281`).
- JSON/TUI route contract unchanged: `route{name,status,url,reason}` — ADR-0013 §12 (`adr-0013:283-288`), FEATURES.md `514-538`.
- Ladder: only a pre-write `route_conflict` advances a rung; every other degradation is systemic — `internal/startsvc/startsvc.go:292-293`.
- Repo boundary: `internal/portless` is the only package allowed to talk to the portless binary (`portless.go:1-2`).

## Pattern to Follow
- Unit tests for `verify` → `apply_paths_test.go`: real-listener tests via `newClientWithProxy` (34-50); window tests via a fake probe closure that counts calls + `WithVerifyWait(>0)` (137-203); seam tests via `fakePortless.client` with `WithVerifyWait(0)` (`portless_test.go:114-127`). Each test carries a MEASURED/regression rationale comment above it.
- ADR measured facts → table rows `| M# | **Fact** | Why it matters |` at `adr-0013:51-70`; keep the framing "measured against portless 0.15.6 / Node 24.15.0" from `adr-0013:42-47` and mark environment-dependent facts as such.
- FEATURES.md → "Layer 3" subsection (`FEATURES.md:441+`): bold-led paragraphs (`**Solution:**`), then `#### Values / Semantics / JSON contract / Use cases / Requirements`; the fix's edit sits in the Solution paragraph (447-454).
- SKILL.md → behavior clauses live under `## Manifest Format (.vroom.toml)` → `url_generation` (`SKILL.md:301-316`); degradation phrasing pattern at `SKILL.md:310-312`; the Maintenance Contract (`SKILL.md:422-428`) mandates the same-change update.

## Tests
- CI-equivalent Integration run: `PORTLESS_BIN=<bin> VROOM_PORTLESS_INTEGRATION=1 VROOM_PORTLESS_INTEGRATION_STRICT=1 go test -race -count=1 ./internal/portless/` (mirrors `.github/workflows/ci.yml:196-200`). Without the env vars the gated tests skip (`requireIntegration`, `integration_test.go:156-161`).
- Repro: `bash docs/planning/0001-fix-ci-integration-proxy/repro-ci-integration-proxy.sh` (needs `node`/`npm` on PATH; fetches `portless@0.15.6` via `npm pack` into `/tmp/opencode/vroom-ci-repro`, forces the polling fallback by throwing before `fs10.watch` in `dist/cli.js` — script lines 41-49 — without touching the global install or `~/.portless`). Narrow with `VROOM_REPRO_TEST_RE`.
- Gates: `make check` (build+lint+test — `Makefile:54`); `make coverage-check` (diff vs `MUTATE_BASE` at 100 % + total ≥ `scripts/coverage-floor` = 100.00 — `Makefile:68-69`, `scripts/diff-coverage.sh`); `make mutate-diff` (`Makefile:59-60`; `MUTATE_BASE ?= main`, `Makefile:7`).
- Integration infra: the CI job installs Node 24 + `portless@0.15.6` via npm with a post-install version assert and exports `PORTLESS_BIN` (`ci.yml:176-194`); every test isolates `PORTLESS_STATE_DIR` in a temp dir and starts its own proxy on a free port (`integration_test.go:132-140, 255-273`); nothing touches `~/.portless`.
- Scenario → test map: window-published → `apply_paths_test.go:137`; window-closes-degrades → `:173`; dead-port-at-once → `:220`; 502-publishes → `:205` + `portless_test.go:404`; served-never-pays → `:71`; no-proxy-no-probe → `:89` + `portless_test.go:243`; default+opt-out → `portless_test.go:130`; ladder-body → `ladder_e2e_test.go:19,113`; restart-survives → `integration_test.go:164`; no-evict → `integration_test.go:203`.

## Conventions & Boundaries
- Deploy: run `make install` after changes — it re-verifies the binary's `vcs.revision` stamp against HEAD and aborts otherwise (`Makefile:31-43`; repo AGENTS.md).
- Same-change docs: any user-visible change lands in `docs/FEATURES.md` **and** `~/.agents/skills/vroom/SKILL.md` in the same commit (repo AGENTS.md; `SKILL.md:422-428`).
- Commits: conventional (`fix(portless): …`, `chore(prototype): …`, `docs: …`, `refactor!(…)` per git log); no AI attribution.
- Planning mirror: keep `plan.md` `## Progress` table (35-50) current — flip each scenario row with its verifying commit.
- Mutation files are frozen (issue non-goal): `scripts/mutate.sh`, `.mutation-allowlist` (215 lines; only 2 pre-existing portless entries, at :79 and :166), `.mutation-timeouts`, `scripts/coverage-floor`. `MUTATE_EXCLUDE` lives in `Makefile:12`; `mutate.sh` exports `GOFLAGS=-exec=setsid` (`mutate.sh:678`) — do not drop.

## Integration Points (non-obvious)
- `IsTestBinary()` (`apply.go:34`) + `ClientFor` (`apply.go:37-49`): every repo caller (TUI, CLI, stacks engine) resolves the real binary here; test binaries get a bin-less `New()`, so no unit test ever touches `~/.portless`.
- Portless route propagation is asynchronous: `fs.watch` debounce (~100 ms) when the watcher works, 3 s polling fallback when it does not (the CI runner). The repro script reproduces the runner by patching the npm tarball, not the global install.
- `assertRouted` (`ladder_e2e_test.go:113-131`) is the e2e counterpart of the same race: 5 s deadline, 100 ms poll, body equality — the status alone is 200 for either backend.
- `VROOM_PORTLESS_INTEGRATION_STRICT=1` (`integration_test.go:123-129`) turns environment skips into failures: a green Integration job that silently skipped every integration test is a red, not a green.
- The `answered` branch (`apply.go:192-195`) is the only verify degradation that does **not** set `Registered` — deliberate, "exactly as before the wait" (pre-fix, a single 404 also returned `Registered=false`). Out of scope per the issue non-goals: a reviewer may note it, the executor must not "fix" it in this change.

## Risks / Assumptions
- Thin margin: `DefaultVerifyWait` 3.5 s vs portless's 3 s polling fallback (~500 ms headroom); Node timer slip under a loaded runner is the residual failure mode. If slip appears on the PR run, the only knob is `DefaultVerifyWait` — do not raise it preemptively (plan decision: every extra second is paid by every genuinely unserved start).
- Mutation timing survivors: the only dead-port test pins `WithVerifyWait(0)` (`apply_paths_test.go:220-243` via `newClientWithProxy`), where the early break (`apply.go:183-185`) and the deadline break (`apply.go:186-188`) are indistinguishable — a mutant deleting the early break survives it. Killer (per plan): a timing-tolerant test — dead port + `WithVerifyWait(>0)` → `proxy_unreachable` returned well before the window (elapsed ≪ wait). Add it only if `make mutate-diff` shows a survivor; never a silent allowlist.
- The 3 s polling interval is measured on the CI runner, not a documented portless constant (issue open question 2) — record it in M19 as an environment fact with that framing.
- Forced-polling suite pays the window on first registration (~31 s vs ~13 s healthy locally) — far under the Integration job's 600 s budget (`ci.yml:166`).
- Environment note: this worktree's filesystem intermittently fails lookups/readdir; if a file read fails, `git -C <worktree> show HEAD:<path>` is the reliable read (content is identical — the tree is clean at `36ecaf2`).
