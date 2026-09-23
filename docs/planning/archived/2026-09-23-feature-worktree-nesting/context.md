---
feature: 0011-feature-worktree-nesting
freshness: 0c0965338e6f4f04d136665850c61b36787c2bc3
codegraph: ready
generated_by: codebase-researcher
---

# Context: Worktrees de git anidados (0011)

Go module `vroom`, Go 1.26.3. Flat scan contract `[]scanner.Project` is the
invariant: the topology layer **annotates** projects, it never builds a nested
type. Nesting is a presentation concern resolved in the TUI tree builder.
`codegraph` is initialized (`.codegraph/`, synced at the freshness commit).

## Scope
- In: topology discovery (`git worktree list --porcelain` + bare-repo heuristic)
  in a new `internal/worktree` package; `scanner.Project` relation annotations;
  TUI repo row (expand/collapse, indent, repo-scoped collapse key); CLI
  path-first disambiguation + additive JSON fields; deterministic stack
  resolution (explicit error on duplicate `Manifest.Name`).
- Out: changing grouping semantics (`group.Arrange` untouched), auto-choosing a
  canonical worktree, renaming manifests, UI to create/delete worktrees.

## Files to Touch
| Symbol / Area | File | Lines | Why |
|---------------|------|-------|-----|
| **NEW** `internal/worktree` pkg | internal/worktree/worktree.go (new) | — | Sole place allowed to spawn `git`; porcelain parser + bare heuristic + degradation |
| `Project` struct | internal/scanner/scanner.go | 19-26 | Add `RepoRoot`, `IsWorktree`, `IsBareContainer`, `WorktreeErr` fields |
| `Scan` / `ScanResult` | internal/scanner/scanner.go | 29-55 | Entry point; after collecting projects, call worktree layer and annotate (no nested type) |
| `scanWithFD` | internal/scanner/scanner.go | 71-103 | Collection path #1; annotations applied post-scan |
| `scanWithWalk` | internal/scanner/scanner.go | 114-159 | Collection path #2; same annotation step |
| `skipDirs` | internal/scanner/scanner.go | 106-112 | Worktrees outside root aren't found by scan; do not extend here (see Risks) |
| `inspectDir` | internal/scanner/scanner.go | 161-180 | Per-dir assembly; bare-container rows need a distinct synthetic project (no manifest) |
| `fdPath` / exec pattern | internal/scanner/scanner.go | 57-68 | **Pattern to follow** for spawning an external binary + degradation |
| `gitinfo.readHEAD` | internal/gitinfo/gitinfo.go | 25-53 | **Do NOT modify**; disk-only, worktree-aware via `.git` file — reference for what the new layer must not duplicate |
| `treeItemKind` consts | internal/tui/projectlist.go | 12-19 | Add `itemRepo` kind (repo container row) |
| `treeItem` struct | internal/tui/projectlist.go | 24-30 | Add repo/container reference + child marker |
| `buildTree` | internal/tui/projectlist.go | 37-121 | Core nesting logic: repo row + indented worktrees; keep `group.Arrange` output order |
| `treeLines` | internal/tui/projectlist.go | 144-163 | Indent worktree rows one level deeper |
| `primaryRow`/`secondaryRow`/`groupHeaderRow` | internal/tui/projectlist.go | 168-190 | Model repo-row rendering after these; collapse glyph default inverted |
| `treeRow`/`treeDot` | internal/tui/projectlist.go | 195-220 | Worktree rows reuse this; bare container must render non-operable |
| `stackStats` | internal/tui/projectlist.go | 252-272 | **First-match-by-name bug**: `break` at line 266 picks arbitrary project; must share engine criterion |
| `Model` struct | internal/tui/app.go | 97-174 | `collapsed`, `entries`, `tree`, `services`, `branches` maps; add repo-collapse handling |
| `findComposeFile` | internal/tui/app.go | 190-213 | Walks parents of each project path; worktrees outside root affect discovery |
| `New` | internal/tui/app.go | 216-313 | Scan → services/branches by path → `group.Arrange` → restore collapsed (302-306) → `buildTree` |
| `refreshCmd` | internal/tui/app.go | 418-447 | Branches keyed by path; worktree rows need branch refresh |
| `navigate` | internal/tui/app.go | 1038-1057 | Cursor over new repo rows |
| `enterSelection` | internal/tui/app.go | 1064-1091 | Toggle repo collapse; **repo key namespace must differ from group keys** |
| `toggleStack` | internal/tui/app.go | 1415-1463 | Duplicate-name resolution duplicated here (1435-1443); must align with engine |
| `nodeMembers`/`nodeStats` | internal/tui/app.go | 1468-1493 | Repo row running/total count over nested worktrees |
| `selectedItem`/`selected`/`onHeader`/`selectedNode` | internal/tui/app.go | 1917-1975 | Repo rows must not be treated as projects |
| `secondaryKey` | internal/tui/app.go | 1980-1982 | Collapse key format `primary/secondary`; repo keys need a disjoint namespace |
| `findProject` | internal/cli/cli.go | 138-165 | Path-first disambiguation; actionable duplicate error |
| `ProjectInfo` | internal/cli/cli.go | 44-63 | Add additive `repo_root`, `is_worktree`, `bare_container` JSON fields |
| `buildProjectInfo` | internal/cli/cli.go | 183-228 | Populate new fields |
| `cmdList` | internal/cli/cli.go | 280-304 | Keep flat array (back-compat) |
| `Run` arg dispatch | internal/cli/cli.go | 234-277 | Accept positional path + `--path` flag |
| `cmdStart`/`cmdStop`/`cmdOneShot`/`cmdLogs` | internal/cli/cli.go | 306-579 | Route through updated `findProject` |
| `cmdHelp` | internal/cli/cli.go | 582-598 | Document `--path` |
| `ResolveServices` | internal/orchestrate/engine.go | 67-84 | **`index[name]=p` last-wins** at 71; replace with duplicate detection |
| `validateServices` | internal/orchestrate/engine.go | 246-258 | Dedups names then calls ResolveServices |
| `Launch` | internal/orchestrate/engine.go | 103-175 | Consumes resolved services; no change to loop expected |
| `StopStack` | internal/orchestrate/engine.go | 194-205 | Iterates **all** name matches (no break) — must use same explicit resolution |
| `StackStatus` | internal/orchestrate/engine.go | 213-243 | Same first-match loop as TUI `stackStats`; share criterion |
| `PathKey` | internal/state/hash.go | 11-14 | Service state keyed by absolute path — **unaffected**, confirms nesting needs no migration |

## Contracts
- `scanner.Project` — `internal/scanner/scanner.go:19-26`. Extend with additive
  fields only; keep the flat slice contract so `group`, `tui`, `cli`,
  `orchestrate` keep receiving `[]Project`.
- `group.Entry` / `group.Arrange` — `internal/group/group.go:13-57`. **Must stay
  unchanged.** Repo nesting is orthogonal to `primary_group`/`secondary_group`.
- `manifest.Manifest` — `internal/manifest/manifest.go:36-46`. `Name` is **not
  unique** across worktrees; never use it as an identity key.
- `state.Store.LoadCollapsed`/`SaveCollapsed` — `internal/state/state.go:180-207`.
  Persisted `map[string]bool` keyed by collapse key. New repo keys must be
  disjoint from `primary` and `primary/secondary` keys (see Pattern).
- CLI JSON back-compat — `ListResult.Projects` stays a **flat array**;
  `ProjectInfo` gains fields only (`repo_root`, `is_worktree`, `bare_container`).
- New `worktree.Worktree` struct is internal to the layer; the scanner maps it
  into flat `Project` annotations, never exposing it downstream.

## Pattern to Follow
- **External-binary spawning + degradation** → `internal/scanner/scanner.go:57-103`
  (`fdPath` `exec.LookPath` + known paths; `scanWithFD` `exec.Command` +
  `CombinedOutput`, wraps errors). Mirror this for `git`, but add a **per-repo
  timeout** (`exec.CommandContext`) and a **sentinel error** (e.g.
  `ErrGitUnavailable`) so the scanner degrades per repo instead of failing the
  whole scan. `List` returning `([]Worktree, error)` with the sentinel lets the
  scanner set `Project.WorktreeErr` and continue.
- **Porcelain parser** → pure function over `strings.Split` of the
  `git worktree list --porcelain` output; blank-line separated blocks with
  `worktree <path>`, `HEAD <sha>`, `branch <ref>`, `detached`, `bare`,
  `prunable`. No exec inside the parser (table-driven testable).
- **Bare-repo heuristic** → `IsBareRepo(dir) bool`: conjunction `HEAD` +
  `objects/` + `refs/` present AND no `.git`; plan.md decision 7 recommends also
  requiring the authoritative `git init --bare` marker to cut false positives.
- **Table-driven unit tests** → `internal/scanner/scanner_test.go` (`tree`
  helper `mkdir`/`file`, lines 10-47) is the model for filesystem fixtures.
- **TUI model tests** → `internal/tui/app_test.go` (`newTestModel` 43-51,
  `writeTestTree` 104+, `isolateConfig` 67, `newNestedTestModel` 1342,
  `TestNestedTreeRender` 1381). Tests are **model-level** (`New(...)` then
  assert on `m.tree`/`treeLines`); note: **no `teatest` usage exists** despite
  the index note — do not introduce it.
- **CLI JSON error shape** → `outputError` (`internal/cli/cli.go:96-100`) emits
  `{"error":"..."}` to stderr and exits 1. Duplicate-name error must go through
  it and list candidate paths.

## Tests
- Existing affected:
  - `internal/scanner/scanner_test.go` — add annotation/bare/degradation cases.
  - `internal/group/group_test.go` — **must remain green unchanged** (regression
    guard that option B is untouched).
  - `internal/orchestrate/engine_test.go` — `TestResolveServices` (42-61) and
    `TestResolveServicesNotFound` (63-75) are the direct targets for duplicate
    detection; `mockManager` at 14-40.
  - `internal/tui/app_test.go` — tree build/render + collapse persistence;
    `newNestedTestModel`/`TestNestedTreeRender` are the closest precedents.
  - `internal/gitinfo/gitinfo_test.go` — guard that `gitinfo` is unchanged.
- No test file exists for `internal/cli` (only `cli.go`). Add
  `internal/cli/cli_test.go` for `findProject` (path/name/duplicate). Because
  `findProject` is unexported, the test must live in package `cli`.
- Framework / runner: standard `go test ./...`; TUI tests are plain unit tests.
- Integration infra: none (no DB/broker); filesystem fixtures via `t.TempDir()`
  and `state.NewStoreAt(t.TempDir())`. Real `git` subprocess tests must guard on
  `exec.LookPath("git")` and `t.Skip` when absent.
- Suggested mapping: scenarios 1-2 → scanner/tui tree tests; scenario 3
  (bare) → `worktree.IsBareRepo` unit + scanner; scenario 4 (detached/
  unconfigured/prunable/submodule) → worktree parser + tui; scenario 6
  (degradation) → worktree sentinel + scanner; scenario 7 (CLI) → new
  `cli_test.go`; scenario 8 (stacks) → `orchestrate/engine_test.go` + tui
  `stackStats`.
- **Runner + repo rule** (AGENTS.md): after verification run
  `go build ./... && go vet ./... && go test ./...`, then rebuild the deployed
  binary: `go build -o ~/.local/bin/vroom ./cmd/vroom`.

## Conventions & Boundaries
- Docs/prose in **Spanish**; code identifiers and comments in **English**.
- **Do NOT spawn `git` anywhere except the new topology layer.** `internal/gitinfo`
  stays disk-only (`internal/gitinfo/gitinfo.go:1-4` documents this contract).
- Cross-platform via build tags (unix/windows) in `process` and `tui/term`; the
  new layer should be pure Go + `os/exec` (portable), no build tags needed.
- Keep the scanner output flat and path-keyed; all consumer maps (`services`,
  `branches`, `state.PathKey`) are keyed by absolute path.

## Integration Points (non-obvious)
- **Collapse-key namespace collision.** Existing keys: primary `"X"` and
  secondary `"X/Y"` (`secondaryKey`, `internal/tui/app.go:1980-1982`; built in
  `buildProjectInfo` at `internal/cli/cli.go:210-217`). Repo collapse keys
  **must** be disjoint (e.g. a `repo:` / `worktree:` prefix), otherwise a repo
  named like a group toggles the wrong node and persisted state corrupts.
- **Default inverted.** Groups default **expanded**; repos default **collapsed**
  (issue AC). `buildTree` currently only skips when `m.collapsed[key]` is true —
  repo rows need the inverse default.
- **`stackStats` first-match bug** (`internal/tui/projectlist.go:252-272`,
  `break` at 266) and `toggleStack`'s inline duplicate loop
  (`internal/tui/app.go:1435-1443`) and `engine.StackStatus`
  (`internal/orchestrate/engine.go:224-239`) each independently resolve a
  service name by first match. All three must route through **one shared
  criterion** so TUI and CLI agree (behavior scenario 8).
- **`StopStack`** (`internal/orchestrate/engine.go:194-205`) currently stops
  **every** project matching a name (no break) — inconsistent with the new
  explicit-error policy; align it.
- **Worktrees outside the scan root.** `git worktree list` may return paths
  outside `root`; the scanner only sees in-root projects, so a repo whose main
  checkout is out-of-root but whose worktree is in-root needs a **synthesized
  container row** (behavior scenario: "Worktrees fuera del scan root").
- **`findComposeFile`** (`internal/tui/app.go:190-213`) ascends parents of each
  project path; adding worktree projects changes which dirs are probed — verify
  no spurious compose discovery.
- **Submodules** have a `.git` **file** pointing at `modules/...`, not
  `gitdir:` to a worktree. The worktree layer must not treat a submodule as a
  worktree of its parent (behavior scenario 4).
- **Service state unaffected:** `state.PathKey` (`internal/state/hash.go:11-14`)
  hashes the absolute project path, and service dirs/logs/meta are all
  path-derived (`internal/state/state.go`). Nesting changes presentation and
  lookup only; no state migration and no key change is needed.
- **`Manifest.Name` collisions** already surface today as CLI
  `ambiguous project name "X"` (`internal/cli/cli.go:158-164`) and as silent
  last-wins in `ResolveServices` (`internal/orchestrate/engine.go:71`).

## Risks / Assumptions
- **Feasible.** No structural blocker found; the flat-contract approach is
  consistent with all consumers. The only ADR-worthy tradeoff is spawning `git`
  in the scan path (already flagged in `plan.md`, `adr_required: true`).
- Scanner depth (`config.ScannerConfig.Depth`, `internal/config/config.go:62`)
  limits in-root discovery; worktrees outside root won't be discovered by
  `fd`/`WalkDir` and rely on `git worktree list` from an in-root repo. Do not
  extend `skipDirs` for this.
- Prunable/absent worktrees reported by git must be skipped (not rendered as
  operable rows); a worktree added mid-session is not refreshed until next scan
  (documented limitation, plan.md).
- Bare-repo false positives: require the full conjunction + bare marker + no
  `.git` (plan.md decision 7); `IsBareRepo` must not match a normal repo that
  has a `.git` dir or file.
- Real-git integration tests are environment-dependent; keep the parser and
  heuristic pure so coverage does not depend on the `git` binary being present.
