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
