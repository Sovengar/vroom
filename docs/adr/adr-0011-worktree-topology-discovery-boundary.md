# ADR-0011 — Worktree topology discovery boundary

- Status: accepted
- Date: 2026-09-23
- Feature: `0011-feature-worktree-nesting`

## Context

Until now `internal/gitinfo` read a project's branch **from disk only**
(parsing `HEAD`), without spawning the git binary. That principle gave
instantaneous, testable reads, and covered normal repos and worktrees.

To display nested worktrees and bare repos it is necessary to know **which
worktrees a repo registers** and **which is its main checkout**. That
information is not on the project's disk: only `git worktree list` knows it.
Furthermore, a bare repo has no `.vroom.toml`, so scanning by manifests
would never find it.

This breaks the `gitinfo` principle and introduces an environment dependency
(the git binary) and a process cost in the scan path.

## Decision

1. **Dedicated `internal/worktree` layer.** It is the only point in the
   project authorized to spawn git. It exposes plain data: `List(dir)` (pure
   parser of `git worktree list --porcelain`) and `IsBareRepo(dir)`
   (heuristic). `gitinfo` **is not modified** and remains disk-only.

2. **Flat, annotated scanner contract.** The scanner builds no nested
   structure: it annotates the existing `[]scanner.Project` with additive
   fields (`RepoRoot`, `IsWorktree`, `IsBareContainer`, `WorktreeErr`).
   Consumers (`group`, `tui`, `cli`, `orchestrate`) continue to receive a
   flat slice. Visual nesting is a presentation concern in `tui.buildTree`.

3. **Per-repo degradation, not all-or-nothing.** `List` returns a sentinel
   (`ErrGitUnavailable`) if git is missing, and an error if git fails or the
   output is invalid. The invocation is bounded with a timeout
   (`exec.CommandContext`) and is gated by the presence of a real git repo.
   The scanner records the reason in `Project.WorktreeErr` and continues: no
   project is hidden and the TUI does not crash.

4. **Reinforced bare repo heuristic.** The conjunction of `HEAD` + `objects/`
   + `refs/`, the absence of `.git`, and the authoritative `core.bare = true`
   marker written by `git init --bare` / `git clone --bare` is required.

5. **Bare-container and worktrees outside the root.** The scanner synthesizes
   container rows without a manifest for bare repos and for in-root
   worktrees without `.vroom.toml`. A worktree whose main checkout is outside
   the scan root is still annotated (`RepoRoot` outside); the TUI synthesizes
   the container row when building the tree.

## Consequences

- Positives: a single boundary for the git dependency, testable in
  isolation (pure parser and heuristic); flat contract preserved for all
  consumers; controlled degradation.
- Negatives / tradeoffs: the scan now spawns git per repo (cost and
  environment dependency). Mitigated by timeout, gating, and degradation.
- Documented limitations: a prunable/absent worktree or one added during the
  session is not refreshed until the next scan. Topology is discovered from
  in-root repos; a worktree without a manifest whose repo has no in-root
  project is not detected.

## Alternatives considered

- **Reading `.git` by hand to derive the main checkout.** A worktree's `.git`
  file points to the gitdir, not the main checkout; reconstructing the
  relationship would require parsing `commondir` and would duplicate fragile
  git logic.
- **Nested type in the scanner (`Repo{Worktrees []Project}`).** It would break
  all consumers and the path maps; discarded due to cost and coupling.
- **Nesting inside `group.Arrange`.** The repo→worktree relationship is
  orthogonal to `primary_group`/`secondary_group`; mixing them would have
  merged two concepts and broken existing grouping (option B).
