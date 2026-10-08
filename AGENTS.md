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

CI lives in `.github/workflows/ci.yml` and runs on **every PR** and on **every push
to `main`** (no `paths` filters: a skipped workflow leaves the required checks
in pending forever and blocks all PRs). Three jobs:

- **`Build`**: `go build ./...` and `go vet ./...`.
- **`Lint`**: `make lint` → golangci-lint **v2.13.2** (version pinned in the
  `Makefile`; there is no `.golangci.yml`, the default set runs).
- **`Test`**: `go test -race -covermode=atomic -coverprofile=… ./...` (full
  suite, without `-short`), with **fd** installed on the runner beforehand: the
  scanner prefers `fd --hidden` and the scanner tests assume it exists
  (local `make test` also takes it for granted).

Rules for the `main` branch (ruleset **`protect-main`**, reproducible with
`scripts/setup-repo-protection.sh`):

- Merge **only via PR**, with the three checks green; force-push and deletion of
  `main` blocked.
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
