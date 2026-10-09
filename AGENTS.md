# AGENTS.md — vroom

## Crucial step after any code change

**Deploy the binary** (tests/smoke with `go run` do not update the
installed one; the user runs the binary from `~/.local/bin`, not the repo):

```bash
make install
```

`make install` **verifies** that the deployed binary carries the revision
stamp that was just compiled, and **aborts if not**. Do not use
`go build -o ~/.local/bin/vroom` by hand: without that check nothing distinguishes
the old binary from the new one, and a wrong revision has already been deployed
without anyone noticing. Reason in the `Makefile` (`install`), along with the
measurement that supports it.

Without this step, any verification the user does on the TUI uses the
old version. Run it ALWAYS at the end of a code task, after
verification (`make check`).

## New feature → docs/FEATURES.md

Any **new feature** — and any user-visible change to an existing one — must be
documented in `docs/FEATURES.md` **in the same change**: add or update its entry with
what it does and how it is triggered (key, flag or command). A feature that is
not in `docs/FEATURES.md` does not exist for the next reader. Keep it a concise
inventory, not a tutorial: the details live in `README.md` and `docs/adr/`.

## CI and `main` protection

Two workflows, three required checks: `Lint`, `Test` and `Mutation`. Neither
workflow uses `paths` filters (a skipped workflow leaves its required checks
stuck pending and blocks every PR that skips it).

### `CI` (`.github/workflows/ci.yml`) — every PR, and pushes to `main`

The gate. `Mutation` is a job of this file: `Lint` ∥ `Test`, the `Integration`
job for the real-portless E2E tests, plus the mutation job on PRs.

- **`Lint`**: `make lint` → golangci-lint **v2.13.2** (pinned in the `Makefile`;
  no `.golangci.yml`, so the default set runs).
- **`Test`**:
  1. **fd** first: the scanner prefers `fd --hidden` and the scanner tests
     assume it exists (local `make test` takes it for granted). The runner has
     none (Debian ships `fdfind`), so a pinned static musl build is cached with
     `actions/cache` and verified against a self-pinned sha256 — never an apt
     install, never a silent WalkDir fallback.
  2. `go test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...`
     (the whole suite, no `-short`).
  3. **`Coverage gate`** → `scripts/diff-coverage.sh`: the PR's **diff at 100%**
     and the **total against `scripts/coverage-floor`** (100.00, can only go
     up). The base is the explicit merge-base, hence `fetch-depth: 0`.

### `CI fast` (`.github/workflows/ci-fast.yml`) — every commit on a branch

Build + unit tests (fd included), no lint, no mutation. **Not required**: a red
never blocks a merge and a green never authorises one. Its 10m ceiling is for a
cold runner, not for the ~1m15s the suite takes warm.

### `Mutation` — a job of `ci.yml`, non-draft PRs only

Runs on non-draft PRs. The job always reports — no `needs:`, no
`continue-on-error`, the only `if:` is the event gate — which is what makes it a
required check.

- **Where the decision lives**: `scripts/mutate.sh` measures AND decides in one
  step (`scripts/mutate.sh --diff --ci --summary …`); the workflow only brings
  paths, refs and budget. *"Could not measure"* is a red, never a green, and a
  red says how to fix it in the step summary.
- **Budget** (mandatory under `--ci`, asserted at startup):
  `2*CAP < STALL < CEILING`, `CEILING + SETUP_RESERVE < JOB_CEILING` →
  `180s · 4 workers · 8m · 13m · +600s · 25m`.
- **Local loop**: `make mutate` (whole module) and `make mutate-diff` (the diff
  against `MUTATE_BASE`), same wiring as CI. `make coverage-check` is the local
  equivalent of the coverage gate.
- **Scope and the one vroom-specific flag**: `MUTATE_EXCLUDE` lives in the
  `Makefile` (`\.worktrees/` and `internal/testutil/`) and `scripts/mutate.sh`
  reads it from there, so local and CI gate the same set. `mutate.sh` ALSO exports
  `GOFLAGS=-exec=setsid`, because `internal/process` stops services with
  `kill(-pgid, …)` and the `CONDITIONALS_BOUNDARY` mutant of `pgid > 0` turns the
  suite's own `Stop(StopSpec{Pgid: 0})` into `kill(-0)`: SIGTERM to the whole
  process group, which under gremlins IS gremlins — it traps the first signal,
  closes its channel and panics on the second (`exit 2`, "no measurement"). Each
  test binary in its own session keeps that blast radius inside the session.
  **Do not drop the flag**: 3/3 whole-module runs died without it; the whole suite
  measured with it gives the same results (3173/3173, same exit codes, no cost).
- **Files that make the gate possible** (all committed):
  `.mutation-allowlist` (survivors accepted **by line**; missing = red with the
  command to seed it), `.mutation-timeouts` (`<file> <ceiling>` for mutants that
  expired and were never tested), `scripts/coverage-floor` (the total, ratchet),
  `scripts/watchdog.sh` + `scripts/watchdog_test.sh` (vendored frozen copy) and
  `scripts/mutate_test.sh` (the gate's red paths, ~1s, run as the `Shell suites`
  CI step).

Rules for the `main` branch (ruleset **`protect-main`**, reproducible with
`scripts/setup-repo-protection.sh`):

- Merge **only via PR**, with `Lint`, `Test` and `Mutation` green; force-push and
  deletion of `main` blocked. `Build` is not a check anymore: it moved to the
  advisory `CI fast`, so a required `Build` context would deadlock every PR.
- **Admin bypass** exists and is **deliberate** (approved by the user): an
  admin *could* push directly, but the working intention is always the
  PR path. No non-admin actor can do it.
- `delete_branch_on_merge=true`: GitHub deletes the remote branch on merge.

Before a merge: verify that the `push` workflow on `main` stayed green and that the
README badge reports `passing` (the badge caches for a few seconds).

Local gate in a single command: **`make check`** (build + lint + test), the same
trio that CI requires. Must exist in all repos of the family.

### Waiting for CI

To follow a PR's checks, wait with `gh run watch <run-id> --exit-status` (or
`gh pr checks <n> --watch`). Never `sleep` + `gh pr checks`: runs go stale after
a force-push and the id has to be asked for again.
