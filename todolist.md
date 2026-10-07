# todolist.md — test coverage from 72.9% → ≥98%

Baseline measured on 2026-10-02 with exact CI parity:

```bash
go test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
```

**4409 statements, 1199 uncovered → 72.9%.** Identical with and without `-race`.
544 tests in 60 files. 18 packages.

---

## 0. The math of the goal (read before accepting 98%)

| | statements | uncovered |
|---|---|---|
| `internal/tui` | 1978 | 558 |
| rest (17 packages) | 2431 | 641 |

To reach 98% total you need **≤88 uncovered statements**. Two facts
condition the entire plan:

1. **The total 98% depends entirely on the TUI.** If the other 17 packages
   reach 98% (≈49 uncovered) and the TUI stays at 71.8%, the total lands at
   **86.2%**. With the TUI at 90% → 94.4%. With the TUI at 95% → 96.6%. The TUI has
   to reach ~98% for the total to reach 98%. There is no shortcut: it is not "raising the
   coverage of the easy packages".

2. **Even so it is achievable, and for a structural reason**: 90% of
   `internal/tui` are pure render functions with value receiver that
   return `[]string` (`metricsLines`, `gitLines`, `treeLines`,
   `detailsLines`, `stackDetailsLines`, `rightColumnLines`…). They are not a Bubbletea
   `Program` that has to be driven. They are testable functions with table-driven and
   golden tests, no new seam needed. The part that *is* a `Program` (`Init`, `Update`) is
   a fraction of the weight.

That is why the plan is executable. Phase 4 is the expensive one, Phases 1–3 are the
cheap ones.

---

## 1. Repo doctrine — mandatory, non-negotiable

Extracted from the existing comments in the code. A PR that contradicts it
should be rejected.

- **Test the real thing, not a stub.** `removeabsent_test.go`: *"These cases do NOT
  go through Release: they exercise the REAL RemoveAbsent, with the exec
  injected, because Release delegates to a stub and a test that only looks at the stub would not
  test anything of the code under test."*
- **Do not introduce swappable global variables as a seam.** `apply.go`
  documents `IsTestBinary()` as *"Does not depend on a variable that a test can
  unset."* That is why Phase 3 uses a **real subprocess**, not `var exit = os.Exit`.
- **Table-driven** for multiple cases, `t.Run(tt.name, ...)`, named by
  scenario and not by input mechanics.
- **`t.TempDir()`** for everything that touches disk. Never the real home.
- **Integration with `testing.Short()`** when running external commands.
- **Deterministic golden**, updatable only via a repo `-update` path, and
  re-verified without `-update` afterwards.
- **Comment in Spanish, explain the why** (it is the dominant convention of the
  repo; existing tests document the bug they prevent).

---

## Phase 0 — Test infrastructure (blocks Phases 3 and 4)

Without this, not even a CLI or TUI test can be written. **Zero coverage
delta**, it is enabling work.

- [ ] **0.1 Golden harness in `internal/tui`.**
  Helper `assertGolden(t, name, lines []string)` that compares against
  `internal/tui/testdata/<name>.golden`. `-update` flags to regenerate.
  Determinism: **no line may contain a clock, real PID or absolute
  path** — the `*Lines` receive `time.Time`/pid from the Model as data, so
  tests fix the time with `t.Setenv`/Model fields, not with `time.Now()`
  inside the render. If a golden turns out non-deterministic, the culprit is a
  `time.Now()` in the render and it must be injected, not sanitizing the golden.
  Acceptance: nonexistent golden fails the test with a message saying how
  to generate it; `-update` regenerates and the rerun without `-update` passes.

- [ ] **0.2 Subprocess helper with child coverage.**
  `internal/testsub` (new package, test-only): Given a command, it executes it and
  collects its coverage via `GOCOVERDIR`. Requires compiling the vroom
  binary with `go build -cover -o <tmp>/vroom ./cmd/vroom` (supported since Go 1.20; the
  repo is on go 1.26.3) and running with `GOCOVERDIR` pointing to a temp
  dir. Acceptance: the test sees statements from `main()` and from `outputError`,
  which are currently unreachable in-process.

- [ ] **0.3 Profile merging in the report.**
  Merging (`go tool covdata`) of child process profiles into the
  parent's profile, so that `coverage.out` gives the real total and not just the
  test process's. Without this, the Phase 6 gate measures wrong.

- [ ] **0.4 Documented suite time budget.** Today the profile is
  60s (`internal/startsvc`). Phases 3 and 4 raise that. Measure and write down the
  budget; if it blows up, the answer is `testing.Short()`, not raising the
  CI timeout blindly.

---

## Phase 1 — Pure functions at 0%, no infrastructure (cheap)

All of this is table-driven over pure functions. **~197 statements.**
Expected: **72.9% → ~77.3%**

- [ ] **1.1 `internal/tui/bordered` (82 uncovered, worst ratio: 42.7%).**
  `parseAnsiSegments`, `wrapLine`, `isResetStyle` — all three at 0%. Pure
  deterministic text functions over the renderer: zero excuses. Cases:
  valid SGR sequences, malformed sequence mid-line, reset
  `ESC[0m` vs `ESC[m`, text without escapes, line wider than the width (wrap at
  word boundary and hard break), width 0 and width 1, tab and double-width
  characters (CJK) in the wrap. **Careful: it is the renderer, a bug here is
  visual corruption in the TUI, and today nobody catches it.**
  Acceptance: ≥98% of the package.

- [ ] **1.2 `internal/tui/outputtabs` — the `apply*` at 0%.**
  `applyMetrics`, `applyGit`, `applyEnv`, `firstLine`, `probeHealth`,
  `recordStackEventByName`. The existing tests (`outputtabs_test.go`) exercise
  `metricsLines`/`gitLines` **pre-populating the Model cache**: they test the
  pure half of the system and ignore the transition half. These tests pass
  even if the pipeline that fills the cache is broken. Tests: each `cmd` (42.9%)
  injecting the exec seam and firing the resulting `tea.Msg`; each `apply*`
  with success msg, error msg, and msg for a service that no longer exists.
  Acceptance: the cache is filled by testing the `cmd`, not by hand.

- [ ] **1.3 `internal/state` (68.9%).**
  `NewStore` and `Base` at 0% because tests build the Store directly and
  skip the constructor. Cover `NewStore` with `t.Setenv("HOME", tmp)` +
  `t.TempDir()`: base dir creation, permissions, error if the base dir is a
  file. `DefaultBaseDir` (33.3%) with and without `HOME`. `EnsureServiceDir`,
  `SaveMeta`, `RegisterPid`, `ClearPid`, `SaveCollapsed` are at 62–75%: cover
  their error branches (non-writable dir, corrupt meta, already-registered pid).
  Acceptance: ≥98%.

- [ ] **1.4 `internal/portless` — the real gaps, not the stubs.**
  `Warn` is at **18.2%** and is the real hole. The 0% of `RemoveAbsent`
  (`ReleaserFunc` adapter), `InertReleaser`, `IsTestBinary` and `ClientFor` are
  deliberate: they are safety guards, not debt. Cover `Binary`, `Default`,
  `StateDir` with `t.Setenv("PATH")` and `HOME` redirected (and **verify with
  `IsTestBinary` that the test cannot touch the developer's real portless**),
  and `withReason`.
  Acceptance: ≥98%, or the remaining 0% documented in a comment as
  intentional.

- [ ] **1.5 Stubs at 0% of a single statement in already-high packages.**
  `manifest.Exists`, `orchestrate.LaunchAsync`, `scanner.IsNestedRow`,
  `agents` (2), `group` (1), `worktree` (5), `gitinfo` (3), `tail` (5),
  `config` (7), `launcher` (10), `startsvc` (10). All table-driven, low
  risk, raises the total almost for free.

- [ ] **1.6 `internal/process` — the reachable part.**
  `warnf` (2), `lineageDesc` (2). `ReadEnviron`/`ReadMetrics` (82.9%) read
  `/proc`: either cover them against the real `/proc` of the test process itself (which
  is always there and is deterministic on Linux), or mark as integration with
  `testing.Short()`.

---

## Phase 2 — TUI render sweep with golden (the biggest of the cheap ones)

Closes `*Lines`, `*ContentLines`, `fitLines`, `clipLines`, `padLines`,
`frameBoxLines`, `treeLines`, `rightColumnLines`, `detailsContentLines`,
`consoleContentLines`, `groupDetailsLines`, `allDetailsLines`, `threadsLines`,
`timelineLines`, `dashboard.go`, `projectlist.go`, `serviceview.go`
(`stackDetailsLines` at 0%), `tui/route.go` (63.6%), `tui/term.go` (87.7%).

**~300 statements.** Expected cumulative: **~84.1%**

- [ ] **2.1 One golden per Model state, not per function.** The error to avoid
  is a golden for each of the ~20 `*Lines` functions: that proves nothing,
  because the functions call each other and the same state would repeat 20
  times. The right thing is one golden per **screen scenario**: service
  stopped / running / starting / stopping / with error, with and without group, with and
  without stack, selected and unselected service, empty log and log with
  lines, viewport with scroll and without scroll, minimum width and wide width. That
  exercises the ~20 functions at once and truly covers.
- [ ] **2.2 Layout edge cases**, which is where the uncovered
  branches concentrate: width 0/1/2, height 0/1, lines longer than the panel, truncation with
  ellipsis, multibyte text, hidden panel (`full` toggles), resize.
- [ ] **2.3 `tui/route.go` (63.6%)** — `tuiRouteReleaser` at 40%: it is the
  TUI↔portless integration, i.e. where route ownership is decided.
  Same rigor as in `internal/portless`: stub is not enough, the
  real releaser must be exercised.

---

## Phase 3 — `internal/cli`: from 30.1% to ≥98% (288 statements)

The package has **a single `os.Exit`**: `outputError` (cli.go:152). Everything
else are functions that write to `os.Stdout` via `outputJSON`. The 4 current test
files (`cli_test.go`, `portjson_test.go`, `port_test.go`,
`route_test.go`, `route_release_test.go`) only cover project and port lookup
helpers.

**Expected cumulative: ~90.2%**

- [ ] **3.1 `outputJSON` / `outputError`.** The JSON format **is the public
  interface for agents** (the repo itself is described as "JSON interface for
  AI agents"). Today it is at 0% and nobody verifies it: a field name change
  breaks every consumer and CI stays green. Test the exact JSON
  shape (2-space indentation, `omitempty` on `Stdout`/`Stderr`, `error` key)
  by decoding into `map[string]any` and comparing against the struct, not against
  literal strings.
- [ ] **3.2 `resolveRoot` (0%).** `~` branch with `UserHomeDir`,
  relative→absolute branch with `filepath.Join`, and empty config. The `~`
  expansion is path logic and a bug there sends the scanner to the wrong place
  silently.
- [ ] **3.3 `Run` — dispatch table by subcommand.**
  `list`/`status`, `start`, `stop`, `build`, `install`, `oneshot`, `logs`,
  `help`, `launch`, and the `len(args)==0 → false` case (which means "launch the
  TUI"). Verify two different and both important things: **which
  subcommand it routes to**, and **what it returns** (true = handled, false = TUI). Without
  this test, a typo in the `switch` routes `stop` to `start` and nothing fails.
- [ ] **3.4 The nine `cmd*` (all at 0%).** Cases per subcommand: happy path,
  nonexistent project, **ambiguous project** (`findProject` already returns
  ambiguity and the message is actionable — it must be tested), `--path`
  disambiguating, unknown flags, and missing arguments (each must give
  the correct `usage:`, not a panic).
- [ ] **3.5 `runLogged`.** It is the path that captures stdout/stderr from a
  one-shot command and feeds it into `appendLine`. The log is what the user will read
  when something fails: it needs success case, failing command,
  output interleaving, and log that cannot be opened.
- [ ] **3.6 `cmd/vroom/main.go` (0%, 14 statements).** Unreachable in-process
  because of the three `os.Exit`. Only covered with the 0.2 subprocess
  harness: TUI case (no args) is not automatable — the exclusion is documented —, but
  the three error paths are (`NewStore` failing, `Getwd` failing,
  `tea.Run` failing) with manipulated `HOME`/cwd.
- [ ] **3.7 `loadConfig`.** Trivial but today it is a 0% inside a package at
  30%; included for free.

---

## Phase 4 — `internal/tui/app.go`: transitions (the expensive phase)

558 uncovered statements in the TUI, most of them here. **This phase is the one
that decides whether the total reaches 98%.**

**Expected cumulative: ~95.1%**

- [ ] **4.1 `Model.Init` (0%)** and `tickCmd`/`consoleTickCmd`/`threadsCmd` (0%):
  the `tea.Cmd`s. Test the `cmd` with the exec seam injected and assert the
  `tea.Msg` it produces; `Init` is dispatched and the initial batch is asserted.
- [ ] **4.2 `Update` is at 36.5%** — it is a single enormous `switch` and is the
  largest uncovered block in the entire repo. Table-driven: **one case per `tea.Msg`
  and per key**, asserting the state transition, not the render. Missing
  messages: ticks, results of `metricsCmd`/`gitCmd`/`envCmd`/`healthCmd`,
  `consoleMsg`, `threadsMsg`, `editLogs`. Keys: navigation, `q`/esc,
  view shortcuts, resize, every toggle.
- [ ] **4.3 The toggles at 0%:** `toggleStack`, `toggleComposers`,
  `markStackStopping`, `scrollDetails`. Toggles are exactly where a
  state gets out of sync: the symptom is "the TUI shows something that is no
  longer true", which no other test catches.
- [ ] **4.4 `refreshBatch` (0%)** — it is the name of the initial dashboard
  load. A 0% here means that **the first screen after opening
  vroom has no test**. Maximum priority within the phase.
- [ ] **4.5 `stateOfMeta` (0%)** — translates `Meta` to UI state. If it is
  wrong, the UI lies about a real service.
- [ ] **4.6 `scrollDetails` + viewport.** Scroll with logs longer than the
  panel, saturation at the extremes, resize that invalidates the offset.

---

## Phase 5 — Leftovers of already-high packages (144 statements)

`process` (69), `scanner` (27), `orchestrate` (28), `launcher` (10),
`startsvc` (10).

**Expected cumulative: ~97.6%**

- [ ] **5.1 `internal/process`** is the largest: `daemon_unix.go` (83.1%),
  `dynamic_unix.go` (91.5%), `lineage_unix.go` (87.0%), `threads_unix.go`
  (84.0%), `metrics_unix.go`. Uses real processes and real `/proc`. What is not
  deterministically reachable (reading `/proc` of an already-dead
  process, race between `kill` and `wait`) goes to `testing.Short()` or is declared
  excluded, but **every real error branch is tested**.
- [ ] **5.2 `internal/scanner`** (86.7%): `WalkDir` fallback when `fd` is
  missing — it is a path that CI exercises on a runner without `fd` installed and locally
  never, so today it is green without actually running. Test both paths.
- [ ] **5.3 `internal/orchestrate`** (89.7%): `engine.go` (87.7%),
  `compose.go`, `health.go`. These tests take 38s: they are the worst cost/benefit
  ratio in the entire plan, which is why they go last.

---

## Phase 6 — Gate: so the 98% does not come undone

- [ ] **6.1 Threshold in CI.** Today `.github/workflows/ci.yml` **only reports**
  coverage in the step summary; it blocks nothing. With the existing mutation
  gate (`mutation.yml`, blocks new survivors in the diff) there is an
  asymmetry: mutation prevents coverage from **dropping**, but does not raise the
  floor. Add a statements threshold and, preferably, **do not go below the value
  recorded in the repo itself** (`coverage.floor` in a versioned file), not a
  fixed number in the YAML that goes stale without anyone noticing.
- [ ] **6.2 Reproducibility of the number.** Document in the `Makefile` the exact
  command (`make cover`) that produces the profile, and include subprocess
  profiles (Phase 0.3). A gate that measures differently from how the baseline
  was measured is a gate that lies.
- [ ] **6.3 Integrate with the mutation gate.** Statement coverage **and**
  mutant survival are two distinct signals: the first measures how much
  code is executed, the second measures whether any test truly verifies it. A
  suite can have 98% coverage and zero detection power — all code
  executed, nothing asserted. Both gates together, not one.

---

## 7. The final stretch: the last ~2%

Reaching 98% requires ≤88 uncovered statements out of 4409. The last ones are, by
nature, system branches: `/proc` of a racing process, a `syscall`
that only fails under specific permissions, an `os.Exit` that is only reached with a
corrupt `HOME`.

**Rule, so this does not become a number fight:**

- Each exclusion is documented at the exact point in the code, with a comment
  that says **why it is not reachable**, as is already done in `apply.go` with
  `IsTestBinary`. Without a written reason, it is not an exclusion: it is debt.
- If at the end of Phases 0–5 the total lands between 96% and 98%, **the number is
  published as-is and the goal is revisited with data**, not
  cosmetically lowered by lowering the threshold or inflating the number with inflated exclusions. An honest 96%
  with justified exclusions is worth more than a 98% with a gap.

---

## Execution order and why in this order

1. **Phase 0** — produces no coverage, but without 0.1 there is no golden and without 0.2 there is no
   CLI nor `main`. Everything else depends on this.
2. **Phase 1** — the cheapest: pure functions already at 0%, no new seam, low
   risk. Lowers the number right away and validates that the 0.1 golden harness works
   (1.1 is the test bench for 0.1).
3. **Phase 2** — the largest volume and the lowest risk, thanks to the renders
   being pure. Raises the TUI from 71.8% to ~85%.
4. **Phase 3** — the CLI, which is public surface for agents and is at 30%.
   Depends on 0.2.
5. **Phase 4** — the expensive one. Done with the confidence that 1–3 are already green and
   the golden harness is proven.
6. **Phase 5** — leftovers of packages that are already high: it is where the least
   remains to be gained, so it goes last.
7. **Phase 6** — the gate, when the number is already defensible. Putting the threshold
   earlier only causes exclusions to be lowered to make it pass.

Each phase is a PR. The `AGENTS.md` rule applies **at the end of each one**:
`make check` and then `make install`, which verifies the revision stamp of the
deployed binary.
