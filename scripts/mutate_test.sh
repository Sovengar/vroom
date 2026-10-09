#!/usr/bin/env bash
# mutate_test.sh — the suite for scripts/mutate.sh, and the only harness that reaches every red path of the gate.
# It can because the verdict is a pure function of paths: report.json, the run log and the allowlist are fabricated.
# No gremlins, no Go build, no git, no clock, well under a second. Run: bash scripts/mutate_test.sh

set -uo pipefail

# The suite asserts what the gate does when a knob is ABSENT, so it must not inherit one.
# The workflow's job-level env reaches this script: exported, those assertions went vacuous and MUTATE_BASE pointed
# the fixture repos at a base they do not have. Unsetting here makes the suite hermetic, not correct-by-luck.
unset MUTATE_BASE MUTATE_CAP MUTATE_WORKERS MUTATE_STALL MUTATE_CEILING \
	MUTATE_JOB_CEILING MUTATE_SETUP_RESERVE MUTATE_JOB_START MUTATE_SUMMARY \
	MUTATE_RUN_LOG MUTATE_SCOPE_FILE MUTATE_BUDGET_FILE MUTATE_REPORT MUTATE_ENGINE \
	MUTATE_FORBIDDEN MUTATE_EXCLUDE MUTATE_COVERPKG MUTATE_TIMEOUTS

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
MUTATE="$HERE/mutate.sh"

pass=0
fail=0
ok() {
	pass=$((pass + 1))
	printf 'PASS  %s\n' "$1"
}
bad() {
	fail=$((fail + 1))
	printf 'FAIL  %s\n' "$1"
}
check() { # check <label> <expected> <actual>
	if [[ $2 == "$3" ]]; then ok "$1"; else bad "$1 (expected [$2] got [$3])"; fi
}
contains() { # contains <label> <needle> <haystack>
	if [[ $3 == *"$2"* ]]; then ok "$1"; else bad "$1 (missing [$2])"; fi
}
lacks() { # lacks <label> <needle> <haystack>
	if [[ $3 != *"$2"* ]]; then ok "$1"; else bad "$1 (unexpected [$2])"; fi
}

[[ -f $MUTATE ]] || {
	echo "no $MUTATE"
	exit 1
}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/gitdash-mutate-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

# --- fixtures ---------------------------------------------------------------

# Space-separated files the engine walked WITHOUT measuring; empty except for the scope cases.
SKIP_FILES=''

# A report whose only survivors are the given "<TYPE> <file>:<line>" entries.
# REPORT_STATUS overrides the literal in each entry, so a drifted gremlins status can be fabricated.
make_report() { # make_report <path> <total> <lived_entries_csv...>
	local path=$1 total=$2
	shift 2
	local lived_files='' killed=$((total - $#))
	local type file line entry skip status=${REPORT_STATUS:-LIVED}
	# Over "$@", not over $*: each entry carries a space of its own.
	for entry in "$@"; do
		type=${entry%% *}
		file=${entry#* }
		line=${file##*:}
		file=${file%:*}
		lived_files+="{\"file_name\":\"$file\",\"mutations\":[{\"type\":\"$type\",\"line\":$line,\"column\":9,\"status\":\"$status\"}]},"
	done
	# A real --diff run reports every out-of-scope mutant as SKIPPED; they carry no weight in the totals.
	for skip in $SKIP_FILES; do
		lived_files+="{\"file_name\":\"$skip\",\"mutations\":[{\"type\":\"ARITHMETIC_BASE\",\"line\":7,\"column\":3,\"status\":\"SKIPPED\"}]},"
	done
	printf '{"go_module":"gitdash","mutants_total":%d,"mutants_killed":%d,"mutants_lived":%d,' \
		"$total" "$killed" "$#" >"$path"
	local efficacy
	efficacy=$(awk -v k="$killed" -v t="$total" 'BEGIN{printf "%.2f", t?100*k/t:0}')
	printf '"mutants_not_viable":0,"mutants_not_covered":0,"test_efficacy":%s,"mutations_coverage":%s,' \
		"$efficacy" "$efficacy" >>"$path"
	printf '"elapsed_time":30.5,"mutator_statistics":{},"files":[%s]}' "${lived_files%,}" >>"$path"
}

# A run log shaped like a real one: a line per mutant in the real format, plus the aggregate footer.
# Extra arguments are appended verbatim; the supervisor's own lines belong there too (section 17 produces those).
make_log() { # make_log <path> <killed> <timed_out> [extra_line...]
	local path=$1 killed=$2 timed=$3
	shift 3
	{
		printf 'Starting...\n'
		printf 'Gathering coverage...\n'
		printf 'done in 2.1s\n'
		local i
		for ((i = 1; i <= killed; i++)); do
			printf '  %s%s at internal/tui/app.go:%d:9\n' ' ' KILLED "$i"
		done
		[[ $timed -gt 0 ]] && for ((i = 1; i <= timed; i++)); do
			printf '   TIMED OUT CONDITIONALS_BOUNDARY at internal/tui/toast.go:%d:9\n' "$i"
		done
		printf '\n'
		printf 'Mutation testing completed in 30s\n'
		printf 'Killed: %d, Lived: 0, Not covered: 0\n' "$killed"
		printf 'Timed out: %d, Not viable: 0, Skipped: 0\n' "$timed"
		printf 'Test efficacy: 100.00%%\n'
		printf 'Mutator coverage: 100.00%%\n'
		local extra
		for extra in "$@"; do printf '%s\n' "$extra"; done
	} >"$path"
}


# A stub engine emitting the two shapes a real run produces, so the RUN phase is testable without gremlins.
# The only way to reach supervisor start in a unit test, and the only way to pin SKIPPED's split counts.
# Named the way gremlins names it, repo-relative: the scope-integrity check compares repo-relative paths.
make_stub_engine() { # make_stub_engine <path> <in_scope> <skipped> <file>
	cat >"$1" <<'EOS'
#!/usr/bin/env bash
# Knobs come from the environment so this fixture stays readable.
dry=0
out=report.json
prev=
for a in "$@"; do
	[[ $prev == --output ]] && out=$a
	[[ $a == --dry-run ]] && dry=1
	prev=$a
done
emit_skipped() {
	local i
	for ((i = 1; i <= STUB_SKIPPED; i++)); do
		printf '     SKIPPED ARITHMETIC_BASE at internal/tui/app.go:%d:9\n' "$i"
	done
}
emit_inscope() {
	local i
	for ((i = 1; i <= STUB_IN_SCOPE; i++)); do
		if ((i % 2 == 0)); then
			printf '  RUNNABLE CONDITIONALS_BOUNDARY at %s:%d:7\n' "$STUB_FILE" "$i"
		else
			printf '  RUNNABLE CONDITIONALS_NEGATION at %s:%d:7\n' "$STUB_FILE" "$i"
		fi
	done
}
if [[ $dry -eq 1 ]]; then
	printf 'Starting...\nGathering coverage...\ndone in 2.5s\n'
	emit_skipped
	emit_inscope
	printf '\nDry run completed in 2.5s\n'
	printf 'Runnable: %d, Not covered: 0\n' "$STUB_IN_SCOPE"
	printf 'Mutator coverage: 100.00%%\n'
	exit 0
fi
emit_skipped
emit_inscope
printf '\nMutation testing completed in 3s\n'
printf 'Killed: %d, Lived: 0, Not covered: 0\n' "$STUB_IN_SCOPE"
printf 'Timed out: 0, Not viable: 0, Skipped: %d\n' "$STUB_SKIPPED"
printf 'Test efficacy: 100.00%%\nMutator coverage: 100.00%%\n'
printf '{"go_module":"gitdash","mutants_total":%d,"mutants_killed":%d,"mutants_lived":0,' \
	"$STUB_IN_SCOPE" "$STUB_IN_SCOPE" > "$out"
printf '"mutants_not_viable":0,"mutants_not_covered":0,"test_efficacy":100.00,' >> "$out"
printf '"mutations_coverage":100.00,"elapsed_time":3.0,"mutator_statistics":{},' >> "$out"
printf '"files":[{"file_name":"%s","mutations":[{"type":"CONDITIONALS_NEGATION","line":1,"column":7,"status":"KILLED"}]}]}' \
	"$STUB_FILE" >> "$out"
exit 0
EOS
	chmod +x "$1"
	printf 'STUB_IN_SCOPE=%s\nSTUB_SKIPPED=%s\nSTUB_FILE=%s\n' "$2" "$3" "$4" >"$1.env"
}

# A repo with the REAL vendored supervisor, so warm, dry-run, the two counts and the verdict run for real.
# Without it the measured job-budget check is unreachable here: a no-op engine dies in the dry-run.
gr4=$tmp/repo-run-phase
mkdir -p "$gr4/scripts" "$gr4/pkg"
cp "$MUTATE" "$gr4/scripts/mutate.sh"
cp "$HERE/watchdog.sh" "$gr4/scripts/watchdog.sh"
chmod +x "$gr4/scripts/mutate.sh" "$gr4/scripts/watchdog.sh"
printf 'package pkg\n' >"$gr4/pkg/gate.go"
printf '# allowlist\n' >"$gr4/.mutation-allowlist"
git -C "$gr4" init -q -b main
git -C "$gr4" config user.email t@example.com
git -C "$gr4" config user.name t
git -C "$gr4" add -A
git -C "$gr4" commit -qm base

# Runs the script in $gr4 with the stub engine wired in.
run_with_stub() { # run_with_stub <in_scope> <skipped> [--ci] [env assignments...]
	local in_scope=$1 skipped=$2 ci=()
	shift 2
	if [[ ${1:-} == --ci ]]; then ci=(--ci); shift; fi
	make_stub_engine "$gr4/stub-engine" "$in_scope" "$skipped" pkg/gate.go
	rm -f "$gr4/out.txt" "$gr4/report.json"
	# The env file is a list of KEY=VAL the stub reads; splitting it is the point.
	# shellcheck disable=SC2046
	(cd "$gr4" && env $(cat stub-engine.env) MUTATE_ENGINE="$gr4/stub-engine" \
		WATCHDOG_POLL=1 MUTATE_RUN_LOG=run.log MUTATE_SCOPE_FILE=scope.txt \
		MUTATE_BUDGET_FILE=budget.txt GITHUB_OUTPUT="$gr4/out.txt" "$@" \
		bash scripts/mutate.sh --run "${ci[@]+${ci[@]}}" 2>&1)
}

# The verdict as the workflow invokes it: three paths plus the two numbers only the run phase knows.
# The ceilings file is per case: the suite must not inherit the repo's, or every expiry assertion
# below would be judging its fixture against the recorded hangs of the real project.
verdict() { # verdict <case_dir> <expected_total> <engine_rc> [extra flags...]
	local d=$1 total=$2 rc=$3
	shift 3
	local out rc_out
	out=$(MUTATE_TIMEOUTS="${TIMEOUTS_FIXTURE:-$d/timeouts}" "$MUTATE" --verdict-only \
		"$d/report.json" "$d/run.log" "$d/allowlist" \
		--expected-total "$total" --engine-rc "$rc" "$@" 2>&1)
	rc_out=$?
	printf '%s' "$out" >"$d/verdict.out"
	return "$rc_out"
}

new_case() { # new_case <name> -> echoes the case dir
	local d="$tmp/$1"
	mkdir -p "$d"
	printf '# allowlist fixture\n' >"$d/allowlist"
	printf '%s' "$d"
}

# ============================================================================
echo "=== 1. survivors that are not allowlisted block the merge ==="
# ============================================================================

d=$(new_case new-survivors)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292' 'CONDITIONALS_NEGATION internal/tui/app.go:380'
make_log "$d/run.log" 44 0
verdict "$d" 46 0
check "new survivors: red" 1 $?
out=$(cat "$d/verdict.out")
contains "new survivors: lists the first one" "CONDITIONALS_BOUNDARY internal/tui/table.go:292" "$out"
contains "new survivors: lists the second one" "CONDITIONALS_NEGATION internal/tui/app.go:380" "$out"
contains "new survivors: publishes the counts" "allowlisted: 0, new: 2" "$out"
contains "new survivors: publishes the figures" "total: 46" "$out"
contains "new survivors: says how to resolve it" "Add a test that kills them" "$out"

# ============================================================================
echo
echo "=== 2. the allowlist is compared by line, not by substring ==="
# ============================================================================

d=$(new_case allowlist-by-line)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
make_log "$d/run.log" 45 0
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:295\n' >>"$d/allowlist"
verdict "$d" 46 0
check "same file, other line: red" 1 $?
contains "same file, other line: names the survivor" "CONDITIONALS_BOUNDARY internal/tui/table.go:292" "$(cat "$d/verdict.out")"

# The same entry at the right line is the other half of the same scenario.
d=$(new_case allowlist-same-line)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
make_log "$d/run.log" 45 0
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
verdict "$d" 46 0
check "same file, same line: green" 0 $?
contains "same file, same line: counts it as allowlisted" "allowlisted: 1, new: 0" "$(cat "$d/verdict.out")"

# ============================================================================
echo
echo "=== 3. known survivors only: green, with the numbers ==="
# ============================================================================

d=$(new_case all-known)
make_report "$d/report.json" 50 'CONDITIONALS_BOUNDARY internal/tui/table.go:292' 'ARITHMETIC_BASE internal/forge/parse.go:45' 'CONDITIONALS_BOUNDARY internal/tui/toast.go:75'
make_log "$d/run.log" 47 0
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\nARITHMETIC_BASE internal/forge/parse.go:45\n' >>"$d/allowlist"
printf 'CONDITIONALS_BOUNDARY internal/tui/toast.go:75\n' >>"$d/allowlist"
verdict "$d" 50 0
check "all known: green" 0 $?
out=$(cat "$d/verdict.out")
contains "all known: survivor count" "surviving in the measured scope: 3" "$out"
contains "all known: allowlisted count" "allowlisted: 3, new: 0" "$out"
contains "all known: killed count" "killed: 47" "$out"
contains "all known: calls it measured and clean" "measured and clean" "$out"

# ============================================================================
echo
echo "=== 4. nothing to mutate is a verdict of its own ==="
# ============================================================================

# Go files but zero mutable statements: no report, and the engine said it had nothing to report.
d=$(new_case no-mutants)
make_log "$d/run.log" 0 0 'No results to report.'
verdict "$d" 0 0
check "zero mutants: green" 0 $?
out=$(cat "$d/verdict.out")
contains "zero mutants: says nothing to mutate" "nothing to mutate" "$out"
contains "zero mutants: records that nothing was measured" "no mutation was measured" "$out"
lacks "zero mutants: does not claim a failure" "no measurement" "$out"

# The same state with a pre-count that disagrees: "nothing to mutate" may never come from an absent report.
d=$(new_case no-mutants-disagrees)
make_log "$d/run.log" 0 0 'No results to report.'
verdict "$d" 12 0
check "zero mutants but pre-count says 12: red" 1 $?
contains "zero mutants but pre-count says 12: names the count" "expected 12" "$(cat "$d/verdict.out")"

# Mutants against a pre-count of zero: the denominator came from a different run than the numerator.
d=$(new_case precount-zero-with-mutants)
make_report "$d/report.json" 8 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
make_log "$d/run.log" 7 0
verdict "$d" 0 0
check "pre-count 0 with 8 measured: red" 1 $?
contains "pre-count 0 with 8 measured: names the contradiction" "measured 8" "$(cat "$d/verdict.out")"

# The one the smoke caught: the base check resolved origin/<base> while the diff asked for <base>.
# On a runner the bare name does not resolve, so the diff failed and its empty output read as nothing to mutate.
gr3=$tmp/repo-remote-base
mkdir -p "$gr3/scripts"
cp "$MUTATE" "$gr3/scripts/mutate.sh"
chmod +x "$gr3/scripts/mutate.sh"
printf 'package x\n' >"$gr3/x.go"
printf '#!/usr/bin/env bash\nexit 0\n' >"$gr3/scripts/watchdog.sh"
chmod +x "$gr3/scripts/watchdog.sh"
printf '# allowlist\n' >"$gr3/.mutation-allowlist"
git -C "$gr3" init -q -b main
git -C "$gr3" config user.email t@example.com
git -C "$gr3" config user.name t
git -C "$gr3" add -A
git -C "$gr3" commit -qm base
git -C "$gr3" checkout -qb feature
printf 'package x2\n' >"$gr3/x.go"
printf 'package y\n' >"$gr3/y.go"
git -C "$gr3" add -A
git -C "$gr3" commit -qm change
# The runner's shape: the base lives only as refs/remotes/origin/<base> and HEAD is detached.
git -C "$gr3" update-ref refs/remotes/origin/main refs/heads/main
git -C "$gr3" checkout -q --detach
git -C "$gr3" update-ref -d refs/heads/main
if git -C "$gr3" rev-parse --verify --quiet main >/dev/null 2>&1; then
	bad "remote-only base fixture: the bare base still resolves, so the case proves nothing"
else
	out=$(cd "$gr3" && MUTATE_BASE=main MUTATE_ENGINE=true MUTATE_RUN_LOG=run.log \
		MUTATE_SCOPE_FILE=scope.txt MUTATE_BUDGET_FILE=budget.txt \
		bash scripts/mutate.sh --diff 2>&1)
	rc=$?
	check "base that exists only as a remote-tracking ref: red" 1 $rc
	contains "base that exists only as a remote-tracking ref: names the ref" "main" "$out"
	lacks "base that exists only as a remote-tracking ref: never says nothing to mutate" "nothing to mutate" "$out"
	if [[ -f $gr3/report.json ]]; then
		bad "unresolvable base: an engine ran anyway"
	else
		ok "unresolvable base: nothing ran"
	fi
fi

# The same repo against the ref that DOES exist: the precheck announces the files it is about to mutate.
out=$(cd "$gr3" && MUTATE_BASE=origin/main MUTATE_ENGINE=true MUTATE_RUN_LOG=run.log \
	MUTATE_SCOPE_FILE=scope.txt MUTATE_BUDGET_FILE=budget.txt \
	MUTATE_SUMMARY=summary.md bash scripts/mutate.sh --diff 2>&1)
rc=$?
check "base that exists as a remote-tracking ref: gets past the precheck" 2 $rc
contains "base that exists as a remote-tracking ref: measured 2 files in scope" "files in scope: 2" "$(cat "$gr3/budget.txt")"
check "base that exists as a remote-tracking ref: announced both files" "x.go y.go" "$(tr '\n' ' ' <"$gr3/scope.txt" | sed 's/ $//')"

# ============================================================================
echo
echo "=== 5. a PR with no Go files never launches a measurement ==="
# ============================================================================

# The precheck runs before the engine, so this is a real repo with a real committed diff and a marker engine.
gr=$tmp/repo-no-go
mkdir -p "$gr"
git -C "$gr" init -q -b main
git -C "$gr" config user.email t@example.com
git -C "$gr" config user.name t
printf 'name = "x"\n' >"$gr/config.toml"
mkdir -p "$gr/scripts"
printf 'x\n' >"$gr/Makefile"
printf 'y\n' >"$gr/CONTRIBUTING.md"
cp "$MUTATE" "$gr/scripts/mutate.sh"
chmod +x "$gr/scripts/mutate.sh"
printf '# allowlist\n' >"$gr/.mutation-allowlist"
printf '#!/usr/bin/env bash\necho RAN > "$PWD/engine-ran"\n' >"$gr/scripts/watchdog.sh"
chmod +x "$gr/scripts/watchdog.sh"
git -C "$gr" add -A
git -C "$gr" commit -qm base
git -C "$gr" checkout -qb feature
printf 'z\n' >>"$gr/Makefile"
printf 'w\n' >"$gr/scripts/new.sh"
git -C "$gr" add -A
git -C "$gr" commit -qm change

out=$(cd "$gr" && MUTATE_ENGINE='bash -c "echo RAN > \"$PWD/engine-ran\""' \
	MUTATE_RUN_LOG=run.log MUTATE_SCOPE_FILE=scope.txt \
	MUTATE_BUDGET_FILE=budget.txt MUTATE_SUMMARY=summary.md \
	bash scripts/mutate.sh --diff 2>&1)
rc=$?
check "no .go in the diff: green" 0 "$rc"
contains "no .go in the diff: says nothing to mutate" "nothing to mutate" "$out"
contains "no .go in the diff: records that nothing was measured" "no mutation was measured" "$out"
contains "no .go in the diff: says the scope is the source" "scope precheck found 0 files" "$out"
lacks "no .go in the diff: does not claim a failed measurement" "no measurement" "$out"
if [[ -f $gr/engine-ran ]]; then
	bad "no .go in the diff: the engine was launched anyway"
else
	ok "no .go in the diff: no engine ran"
fi
if [[ -f $gr/run.log ]]; then
	bad "no .go in the diff: a run log was written"
else
	ok "no .go in the diff: no run log written"
fi

# Staging a .go file does not put it in scope, which is why a smoke fixture has to be COMMITTED and pushed.
mkdir -p "$gr/internal/newpkg"
printf 'package newpkg\n\nfunc F() int { return 1 }\n' >"$gr/internal/newpkg/new.go"
git -C "$gr" add internal/newpkg/new.go
if git -C "$gr" diff --cached --name-only | grep -q '\.go$'; then
	out=$(cd "$gr" && MUTATE_ENGINE='bash -c "echo RAN > \"$PWD/engine-ran\""' \
		MUTATE_RUN_LOG=run.log MUTATE_SCOPE_FILE=scope.txt \
		MUTATE_BUDGET_FILE=budget.txt bash scripts/mutate.sh --diff 2>&1)
	rc=$?
	check "a .go file that is only staged: still nothing to mutate" 0 "$rc"
	contains "a .go file that is only staged: the scope ignores the index" "nothing to mutate" "$out"
	if [[ -f $gr/engine-ran ]]; then
		bad "a .go file that is only staged: the engine ran anyway"
	else
		ok "a .go file that is only staged: no engine ran, so it would have proved nothing"
	fi
else
	bad "staged-only fixture: the file is not in the index, so the case proves nothing"
fi
git -C "$gr" reset -q internal/newpkg/new.go

# A second repo WITH a Go file in scope, so the flag guard and the budget assertions reach the run phase.
gr2=$tmp/repo-stale
mkdir -p "$gr2/scripts"
cp "$MUTATE" "$gr2/scripts/mutate.sh"
chmod +x "$gr2/scripts/mutate.sh"
printf 'package x\n' >"$gr2/x.go"
printf '#!/usr/bin/env bash\nexit 0\n' >"$gr2/scripts/watchdog.sh"
chmod +x "$gr2/scripts/watchdog.sh"
printf '# allowlist\n' >"$gr2/.mutation-allowlist"
git -C "$gr2" init -q -b main >/dev/null 2>&1
git -C "$gr2" config user.email t@example.com
git -C "$gr2" config user.name t
git -C "$gr2" add -A
git -C "$gr2" commit -qm base

# ============================================================================
echo
echo "=== 6. the three reasons for a red are told apart ==="
# ============================================================================

# (a) The measurement could not be made at all: no report with a non-zero pre-count.
d=$(new_case red-no-report)
make_log "$d/run.log" 0 0 'panic: something went wrong'
verdict "$d" 9 0
check "no report: red" 1 $?
out=$(cat "$d/verdict.out")
contains "no report: says there was no measurement" "no measurement" "$out"
contains "no report: includes the tail of the log" "panic: something went wrong" "$out"
contains "no report: includes the log tail fence" '```' "$out"

# (b) The run was cut in half by the supervisor, for each of its two reasons.
d=$(new_case red-stall)
make_log "$d/run.log" 3 0 'watchdog.sh: no progress for 121s -> cutting process group 4242'
verdict "$d" 20 124
check "stall cut: red" 1 $?
contains "stall cut: names the stall" "no progress for 121s" "$(cat "$d/verdict.out")"

d=$(new_case red-ceiling)
make_log "$d/run.log" 3 0 'watchdog.sh: 240s ceiling -> cutting process group 4242'
verdict "$d" 20 124
check "ceiling cut: red" 1 $?
out=$(cat "$d/verdict.out")
contains "ceiling cut: names the ceiling" "240s ceiling" "$out"
lacks "ceiling cut: not blamed on the stall" "no progress for" "$out"

d=$(new_case red-tail-ceiling)
make_log "$d/run.log" 20 0 'watchdog.sh: reached 20/20 but did not exit within the 240s ceiling'
verdict "$d" 20 124
check "post-total tail cut: red" 1 $?
contains "post-total tail cut: names the tail" "did not exit within the 240s ceiling" "$(cat "$d/verdict.out")"

# (c) The engine came out well but wrote nothing: a silent run.
d=$(new_case red-silent)
: >"$d/run.log"
make_report "$d/report.json" 4 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
verdict "$d" 4 0
check "empty log with exit 0: red" 1 $?
out=$(cat "$d/verdict.out")
contains "empty log with exit 0: says nothing was measured" "no measurement" "$out"
contains "empty log with exit 0: names the silent run" "silent run" "$out"

# (d) The engine's own non-zero exit, which is not a cut.
d=$(new_case red-engine-rc)
make_log "$d/run.log" 10 0 'fatal: coverage failed'
make_report "$d/report.json" 12 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
verdict "$d" 12 3
check "engine exit 3: red" 1 $?
contains "engine exit 3: names the exit" "exited 3" "$(cat "$d/verdict.out")"

# The cancellation codes pass through untranslated: they are not survivors.
for rc_code in 130 143; do
	d=$(new_case "cancel-$rc_code")
	make_log "$d/run.log" 10 0
	make_report "$d/report.json" 12 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
	printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
	verdict "$d" 12 "$rc_code"
	check "cancelled ($rc_code): red" 1 $?
	out=$(cat "$d/verdict.out")
	contains "cancelled ($rc_code): says it was cancelled" "was cancelled" "$out"
	lacks "cancelled ($rc_code): is not called a surviving mutant" "surviving mutant(s)" "$out"
done

# ============================================================================
echo
echo "=== 7. expired mutants block even with a perfect efficacy ==="
# ============================================================================

d=$(new_case timed-out)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
make_log "$d/run.log" 43 3
verdict "$d" 46 0
check "expired mutants: red" 1 $?
out=$(cat "$d/verdict.out")
contains "expired mutants: says how many went unchecked" "3 mutants expired" "$out"
contains "expired mutants: says the report does not count them" "does not count them" "$out"

# A truncated log loses the aggregate footer AND keeps the lines it already printed.
# A number derived from partial text looks right while being wrong, so the two counts must agree.
d=$(new_case timed-out-mismatch)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
make_log "$d/run.log" 43 3
grep -v '^Timed out:' "$d/run.log" >"$d/run.log.tmp" && mv "$d/run.log.tmp" "$d/run.log"
verdict "$d" 46 0
check "expired count mismatch: red" 1 $?
out=$(cat "$d/verdict.out")
contains "expired count mismatch: reports both counts" "claims 0 timed-out" "$out"
contains "expired count mismatch: reports the other count" "carries 3 timed-out lines" "$out"

# ============================================================================
# ============================================================================
echo
echo "=== 7b. an expiry is judged against the recorded ceilings, not against silence ==="
# ============================================================================

# Within the ceiling: the run hangs on a hang already known and recorded, so the verdict is green
# AND says out loud what it did not measure. A green that hides this is the one this gate forbids.
d=$(new_case timeouts-recorded)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
make_log "$d/run.log" 45 2
printf 'internal/tui/toast.go 2\n' >"$d/timeouts"
verdict "$d" 46 0
check "recorded ceilings: green" 0 $?
out=$(cat "$d/verdict.out")
contains "recorded ceilings: says the expiries were within them" "2 expired within the ceilings" "$out"
contains "recorded ceilings: publishes file and count against the ceiling" "internal/tui/toast.go: 2/2" "$out"
contains "recorded ceilings: admits they were never tested" "never tested" "$out"
lacks "recorded ceilings: never claims nothing was measured" "**no measurement**" "$out"

# One MORE than recorded: a new hang in a file whose ceiling is already spent.
d=$(new_case timeouts-above-ceiling)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
make_log "$d/run.log" 45 2
printf 'internal/tui/toast.go 1\n' >"$d/timeouts"
verdict "$d" 46 0
check "expiry above ceiling: red" 1 $?
out=$(cat "$d/verdict.out")
contains "expiry above ceiling: names the file" "internal/tui/toast.go: 2 expired, ceiling 1" "$out"
contains "expiry above ceiling: says it is above the recording" "more expired than" "$out"
contains "expiry above ceiling: still counts them as unmeasured" "2 mutants expired" "$out"

# A hang in a file the ceilings file does not mention: ceiling 0, so red on the first one.
d=$(new_case timeouts-unlisted-file)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
make_log "$d/run.log" 45 2
printf 'internal/tui/app.go 5\n' >"$d/timeouts"
verdict "$d" 46 0
check "expiry in unlisted file: red" 1 $?
contains "expiry in unlisted file: ceiling is zero" "internal/tui/toast.go: 2 expired, ceiling 0" "$(cat "$d/verdict.out")"

# No ceilings file at all: there is nothing to judge against, so there is no verdict to give.
d=$(new_case timeouts-missing-file)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
make_log "$d/run.log" 45 2
TIMEOUTS_FIXTURE="$d/never-exists"
verdict "$d" 46 0
rc=$?
unset TIMEOUTS_FIXTURE
check "missing ceilings file: red" 1 $rc
out=$(cat "$d/verdict.out")
contains "missing ceilings file: names what is missing" "is missing" "$out"
contains "missing ceilings file: says why it matters" "no recorded ceiling" "$out"
contains "missing ceilings file: says how to clear a hang" "test that fails fast" "$out"
# ============================================================================
echo
echo "=== 8. the announced scope and the measured scope are the same one ==="
# ============================================================================

# The measured set comes from the RUN LOG, not report.json: the report lists every file merely looked at.
# With --diff those are the SKIPPED ones, so the integrity check has to look past them or it false-positives.
d=$(new_case scope-integrity)
SKIP_FILES='internal/tui/app.go internal/tui/update.go'
make_report "$d/report.json" 12 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
make_log "$d/run.log" 11 0 '  RUNNABLE CONDITIONALS_NEGATION at internal/tui/table.go:381:9'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
printf 'internal/tui/table.go\n' >"$d/scope.txt"
verdict "$d" 12 0 --announced "$d/scope.txt"
check "measured inside the announced scope, with 1090 skipped elsewhere: green" 0 $?

d=$(new_case scope-outside)
SKIP_FILES=''
make_report "$d/report.json" 12 'CONDITIONALS_BOUNDARY internal/config/forge.go:149'
make_log "$d/run.log" 11 0 '  RUNNABLE CONDITIONALS_NEGATION at internal/config/forge.go:150:9'
printf 'CONDITIONALS_BOUNDARY internal/config/forge.go:149\n' >>"$d/allowlist"
printf 'internal/tui/table.go\n' >"$d/scope.txt"
verdict "$d" 12 0 --announced "$d/scope.txt"
check "measured outside the announced scope: red" 1 $?
contains "measured outside the announced scope: names the file" "internal/config/forge.go" "$(cat "$d/verdict.out")"

# No announced scope means nothing to compare against, which is the whole-module local loop's case.
# The relation is SUBSET, not equality: an announced file with no mutant is legitimate.
d=$(new_case scope-absent)
make_report "$d/report.json" 12 'CONDITIONALS_BOUNDARY internal/config/forge.go:149'
make_log "$d/run.log" 11 0
printf 'CONDITIONALS_BOUNDARY internal/config/forge.go:149\n' >>"$d/allowlist"
verdict "$d" 12 0
check "no announced scope to compare against: not a red" 0 $?

# ============================================================================
echo
echo "=== 9. a missing tool cannot produce a green ==="
# ============================================================================

d=$(new_case no-jq)
make_report "$d/report.json" 12 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
make_log "$d/run.log" 11 0
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >>"$d/allowlist"
# A PATH with everything the verdict shells out to except jq: an empty output reads as "zero new survivors".
mkdir -p "$tmp/nojq-bin"
for b in bash env sed grep tail head comm sort mktemp wc tr cat rm dirname awk; do
	src=$(command -v "$b" 2>/dev/null) && ln -sf "$src" "$tmp/nojq-bin/$b"
done
before=$(cat "$d/report.json")
out=$(PATH="$tmp/nojq-bin" "$MUTATE" --verdict-only "$d/report.json" "$d/run.log" \
	"$d/allowlist" --expected-total 12 --engine-rc 0 2>&1)
rc=$?
check "jq missing: red" 1 $rc
contains "jq missing: names the tool" "jq is not available" "$out"
contains "jq missing: does not read as a clean run" "no measurement" "$out"
check "jq missing: the report is left untouched" "$before" "$(cat "$d/report.json")"

# The other half: jq IS installed and returns garbage, and garbage must not read as zero survivors.
d=$(new_case broken-report)
printf '{"mutants_total": 12, "mutants_lived": this is not json\n' >"$d/report.json"
make_log "$d/run.log" 11 0
verdict "$d" 12 0
check "unparseable report: red" 1 $?
contains "unparseable report: says it cannot be parsed" "cannot be parsed" "$(cat "$d/verdict.out")"

# ============================================================================
echo
echo "=== 10. no allowlist: red with the command to seed it ==="
# ============================================================================

d=$(new_case no-allowlist)
make_report "$d/report.json" 12 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
make_log "$d/run.log" 11 0
out=$("$MUTATE" --verdict-only "$d/report.json" "$d/run.log" "$d/nope" \
	--expected-total 12 --engine-rc 0 2>&1)
rc=$?
check "no allowlist: red" 1 $rc
contains "no allowlist: says what is missing" "allowlist" "$out"
contains "no allowlist: says why it matters" "nothing to compare" "$out"
contains "no allowlist: says the exact command" "make mutate" "$out"

# ============================================================================
echo
echo "=== 11. forbidden flags are refused before anything runs ==="
# ============================================================================

# -s and -Sk are the two that slip through: one silences the log, the other is a grouped shorthand.
for forbidden in '-s' '--silent' '-S' '--output-statuses' '-Sk' '-sS' '-xs'; do
	out=$(cd "$gr2" && MUTATE_EXTRA_FLAGS="$forbidden" MUTATE_RUN_LOG=run.log \
		MUTATE_SCOPE_FILE=scope.txt MUTATE_BUDGET_FILE=budget.txt \
		bash scripts/mutate.sh --run 2>&1)
	rc=$?
	check "forbidden '$forbidden': exit 2" 2 $rc
	contains "forbidden '$forbidden': names the flag" "$forbidden" "$out"
	contains "forbidden '$forbidden': points at the contract" "MUTATE_FORBIDDEN" "$out"
done

# The list IS the guard: add a flag to MUTATE_FORBIDDEN and it becomes refused, grouped spelling included.
out=$(cd "$gr2" && MUTATE_FORBIDDEN='-S --output-statuses -s --silent -x --extra-long' \
	MUTATE_EXTRA_FLAGS='-x' MUTATE_RUN_LOG=run.log MUTATE_SCOPE_FILE=scope.txt \
	MUTATE_BUDGET_FILE=budget.txt bash scripts/mutate.sh --run 2>&1)
rc=$?
check "a flag added to MUTATE_FORBIDDEN is refused" 2 $rc
contains "a flag added to MUTATE_FORBIDDEN is refused: names it" "-x" "$out"

out=$(cd "$gr2" && MUTATE_FORBIDDEN='-S --output-statuses -s --silent -x --extra-long' \
	MUTATE_EXTRA_FLAGS='-xy' MUTATE_RUN_LOG=run.log MUTATE_SCOPE_FILE=scope.txt \
	MUTATE_BUDGET_FILE=budget.txt bash scripts/mutate.sh --run 2>&1)
rc=$?
check "a flag added to MUTATE_FORBIDDEN is refused grouped too" 2 $rc
contains "a flag added to MUTATE_FORBIDDEN grouped: says grouped" "grouped" "$out"

out=$(cd "$gr2" && MUTATE_FORBIDDEN='-S --output-statuses -s --silent --extra-long' \
	MUTATE_EXTRA_FLAGS='-xy' MUTATE_RUN_LOG=run.log MUTATE_SCOPE_FILE=scope.txt \
	MUTATE_BUDGET_FILE=budget.txt bash scripts/mutate.sh --run 2>&1)
rc=$?
check "a shorthand absent from the list is not caught by the guard" 2 $rc
lacks "a shorthand absent from the list does not claim MUTATE_FORBIDDEN" "MUTATE_FORBIDDEN" "$out"

# An entry that is neither a long flag nor a shorthand cannot be guarded, and saying so beats ignoring it.
out=$(cd "$gr2" && MUTATE_FORBIDDEN='-S nonsense' MUTATE_RUN_LOG=run.log \
	MUTATE_SCOPE_FILE=scope.txt MUTATE_BUDGET_FILE=budget.txt \
	bash scripts/mutate.sh --run 2>&1)
rc=$?
check "a malformed MUTATE_FORBIDDEN entry: exit 2" 2 $rc
contains "a malformed MUTATE_FORBIDDEN entry: says which" "nonsense" "$out"

# A legal flag must get past the same door: the guard matches forbidden shorthands, not every dash.
out=$(cd "$gr2" && MUTATE_EXTRA_FLAGS='--verbose' MUTATE_RUN_LOG=run.log \
	MUTATE_SCOPE_FILE=scope.txt MUTATE_BUDGET_FILE=budget.txt \
	MUTATE_ENGINE=true bash scripts/mutate.sh --run 2>&1)
rc=$?
check "an unrelated long flag is not caught by the guard" 2 $rc
lacks "an unrelated long flag does not claim MUTATE_FORBIDDEN" "MUTATE_FORBIDDEN" "$out"

# ============================================================================
echo
echo "=== 12. an absent measurement is a hard error, never a silent zero ==="
# ============================================================================

# A report from an earlier run, which must not be readable as this run's.
printf '{"mutants_total":999,"mutants_lived":999}\n' >"$gr2/report.json"

# The engine is a no-op, so the dry-run yields no measurable time and the script must say so.
out=$(cd "$gr2" && MUTATE_ENGINE=true MUTATE_RUN_LOG=run.log \
	MUTATE_SCOPE_FILE=scope.txt MUTATE_BUDGET_FILE=budget.txt \
	bash scripts/mutate.sh --run 2>&1)
rc=$?
check "no measurable coverage time: exit 2" 2 $rc
contains "no measurable coverage time: says what is missing" "no measurable coverage time" "$out"
lacks "no measurable coverage time: announces no ceiling" "per-mutant ceiling" "$out"
if [[ -f $gr2/report.json ]]; then
	bad "stale report: survived the start of a new run"
else
	ok "stale report: deleted before a new run starts"
fi

# ============================================================================
echo
echo "=== 13. the budget is mandatory in CI and its invariants are asserted ==="
# ============================================================================

# The budget assertions need a run that REACHES supervisor start, hence the stub engine and the real supervisor.

# A complete --ci budget is mandatory knob by knob: inheriting the local row is CI loosening its own budget.
ci_out=$(run_with_stub 2 0 --ci)
rc=$?
check "--ci with no budget at all: exit 2" 2 $rc
contains "--ci with no budget at all: names the first missing knob" "MUTATE_CAP" "$ci_out"

ci_out=$(run_with_stub 2 0 --ci MUTATE_CAP=60s MUTATE_WORKERS=4 MUTATE_STALL=3m \
	MUTATE_CEILING=5m MUTATE_JOB_CEILING=8m)
rc=$?
check "--ci without MUTATE_SETUP_RESERVE: exit 2" 2 $rc
contains "--ci without MUTATE_SETUP_RESERVE: names it" "MUTATE_SETUP_RESERVE" "$ci_out"

ci_out=$(run_with_stub 2 0 --ci MUTATE_CAP=60s MUTATE_WORKERS=4 MUTATE_STALL=3m \
	MUTATE_CEILING=5m MUTATE_JOB_CEILING=8m MUTATE_SETUP_RESERVE=150s)
rc=$?
check "--ci without MUTATE_JOB_START: exit 2" 2 $rc
contains "--ci without MUTATE_JOB_START: names it" "MUTATE_JOB_START" "$ci_out"

# The numbers the workflow ships. This one has to complete, so it also proves the measured check is not a wall.
CI_BUDGET=(MUTATE_CAP=60s MUTATE_WORKERS=4 MUTATE_STALL=3m MUTATE_CEILING=5m
	MUTATE_JOB_CEILING=8m MUTATE_SETUP_RESERVE=150s)
ci_out=$(run_with_stub 2 0 --ci "${CI_BUDGET[@]}" MUTATE_JOB_START="$(($(date +%s) - 5))")
rc=$?
check "the shipped budget: the run completes" 0 $rc
lacks "the shipped budget: no invariant complaint" "must be" "$ci_out"
contains "the shipped budget: the measured check reports the clock" "job budget:" "$ci_out"
contains "the shipped budget: it names the supervisor ceiling" "supervisor ceiling 300s" "$ci_out"

# The static half: the ceiling plus whatever runs before it must fit under the job.
# "A ceiling that fits on paper and not in the clock" leaves nothing for the checkout and the suites.
ci_out=$(run_with_stub 2 0 --ci MUTATE_CAP=60s MUTATE_WORKERS=4 MUTATE_STALL=3m \
	MUTATE_CEILING=5m MUTATE_JOB_CEILING=6m MUTATE_SETUP_RESERVE=150s \
	MUTATE_JOB_START="$(date +%s)")
rc=$?
check "ceiling plus reserve over the job ceiling: exit 2" 2 $rc
contains "ceiling plus reserve over the job ceiling: names the sum" "450s" "$ci_out"
contains "ceiling plus reserve over the job ceiling: names the job ceiling" "6m = 360s" "$ci_out"

# The measured half, at supervisor start: too little job left is the cut the PLATFORM does instead.
# No static check can see it, because the reserve was declared honest.
ci_out=$(run_with_stub 2 0 --ci MUTATE_CAP=60s MUTATE_WORKERS=4 MUTATE_STALL=3m \
	MUTATE_CEILING=5m MUTATE_JOB_CEILING=8m MUTATE_SETUP_RESERVE=1s \
	MUTATE_JOB_START="$(($(date +%s) - 400))")
rc=$?
check "too little job budget left at supervisor start: exit 2" 2 $rc
contains "too little job budget left: names what remains" "s of 8m remain" "$ci_out"
contains "too little job budget left: names the supervisor ceiling" "supervisor alone can take 5m" "$ci_out"
lacks "too little job budget left: the supervisor was never launched" "supervising" "$ci_out"
if [[ -f $gr4/report.json ]]; then
	bad "too little job budget left: a mutation ran anyway"
else
	ok "too little job budget left: no mutation ran"
fi

# Neither half alone is enough: the static one is defeated by a small reserve, the measured one by a stopped clock.
ci_out=$(run_with_stub 2 0 --ci "${CI_BUDGET[@]}" MUTATE_JOB_START=0)
rc=$?
check "no clock to measure with: the run still completes" 0 $rc
lacks "no clock to measure with: no runtime complaint" "remain after" "$ci_out"

# A cap above the whole run trips the chain's first link: a stall is below the ceiling, so is any cap above it.
ci_out=$(run_with_stub 2 0 --ci "${CI_BUDGET[@]}" MUTATE_CAP=9m MUTATE_JOB_START="$(date +%s)")
rc=$?
check "per-mutant cap above the run ceiling: exit 2" 2 $rc
contains "per-mutant cap above the run ceiling: says why" "under half the stall" "$ci_out"

# A stall longer than the run is a stall limit that never fires.
ci_out=$(run_with_stub 2 0 --ci "${CI_BUDGET[@]}" MUTATE_STALL=9m MUTATE_JOB_START="$(date +%s)")
rc=$?
check "stall above the run ceiling: exit 2" 2 $rc
contains "stall above the run ceiling: says why" "below the run ceiling" "$ci_out"

# ============================================================================
echo
echo "=== 13b. SKIPPED counts for the supervisor and not for the verdict ==="
# ============================================================================

# The bug this pins: with --diff the out-of-scope mutants come out SKIPPED and were counted as expected.
# That is what let a diff that measured NOTHING come out as a clean measurement.

out=$(run_with_stub 4 1090)
rc=$?
check "4 in scope of 1090 considered: green" 0 $rc
contains "4 in scope of 1090 considered: stderr names both counts" "4 in scope of 1094 considered" "$out"
check "4 in scope of 1090 considered: the supervisor watches all 1094" \
	"watch_lines=1094" "$(grep '^watch_lines=' "$gr4/out.txt")"
check "4 in scope of 1090 considered: the verdict expects 4" \
	"expected_total=4" "$(grep '^expected_total=' "$gr4/out.txt")"
contains "4 in scope of 1090 considered: called a clean measurement" "measured and clean" "$out"

# A _test.go-only diff has Go files in scope and zero mutable statements, and the report still EXISTS.
# With the counts mixed up it is expected=1090 measured=0: the mirror hole, a green reading as a measurement.
out=$(run_with_stub 0 1090)
rc=$?
check "nothing in scope with 1090 skipped: green" 0 $rc
contains "nothing in scope: says nothing to mutate" "nothing to mutate" "$out"
contains "nothing in scope: says it measured nothing" "no mutation was measured" "$out"
lacks "nothing in scope: not called a clean measurement" "measured and clean" "$out"
contains "nothing in scope: the verdict names the pre-count as the source" \
	"pre-count of in-scope mutants was 0" "$out"
check "nothing in scope: the verdict expected 0" \
	"expected_total=0" "$(grep '^expected_total=' "$gr4/out.txt")"

# The same shape at verdict level: a pre-count of 7 against a report measuring none of them is a red.
d=$(new_case precount-nonzero-measured-zero)
make_report "$d/report.json" 0
make_log "$d/run.log" 0 0
verdict "$d" 7 0
rc=$?
check "expected 7 with a report measuring 0: red" 1 $rc
out=$(cat "$d/verdict.out")
contains "expected 7 with a report measuring 0: names the gap" "measured none of them" "$out"
contains "expected 7 with a report measuring 0: says it is not a verdict" "never measured" "$out"
lacks "expected 7 with a report measuring 0: not called a clean measurement" "measured and clean" "$out"

# The mirror of the check above: a pre-count of 0 against a full report.
d=$(new_case precount-zero-with-mutants)
make_report "$d/report.json" 8 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
make_log "$d/run.log" 7 0
verdict "$d" 0 0
check "expected 0 with 8 measured: red" 1 $?
contains "expected 0 with 8 measured: names the contradiction" "measured 8" "$(cat "$d/verdict.out")"

# Nothing in scope and nothing skipped, and the engine said it had nothing to report: the OTHER honest green.
# Defensive for this repo, since a --diff run always CONSIDERS the whole module, so pinned as a shape.
d=$(new_case no-mutants)
make_log "$d/run.log" 0 0 'No results to report.'
verdict "$d" 0 0
rc=$?
check "nothing considered and nothing in scope: green" 0 $rc
out=$(cat "$d/verdict.out")
contains "nothing considered: says nothing to mutate" "nothing to mutate" "$out"
contains "nothing considered: names the pre-count as the source" "pre-count" "$out"

# ============================================================================
echo
echo "=== 14. the scope is measured against the base ref, or it is not measured ==="
# ============================================================================

out=$(cd "$gr2" && MUTATE_BASE=refs/heads/does-not-exist MUTATE_ENGINE=true \
	MUTATE_RUN_LOG=run.log MUTATE_SCOPE_FILE=scope.txt MUTATE_BUDGET_FILE=budget.txt \
	MUTATE_SUMMARY=summary.md bash scripts/mutate.sh --diff 2>&1)
rc=$?
check "unresolvable base: red" 1 $rc
contains "unresolvable base: says the base could not be resolved" "could not be resolved" "$out"
contains "unresolvable base: explains why that is not an empty diff" "indistinguishable from a PR with no changes" "$out"
if [[ -f $gr2/engine-ran || -f $gr2/run.log ]]; then
	bad "unresolvable base: something was measured anyway"
else
	ok "unresolvable base: nothing was measured"
fi

# ============================================================================
echo
echo "=== 15. the summary the workflow publishes carries the reason ==="
# ============================================================================

d=$(new_case summary-file)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
make_log "$d/run.log" 45 0
printf 'per-mutant cap: 120s\nrun ceiling: 4m\n' >"$d/budget.txt"
"$MUTATE" --verdict-only "$d/report.json" "$d/run.log" "$d/allowlist" \
	--expected-total 46 --engine-rc 0 --budget "$d/budget.txt" \
	--summary "$d/summary.md" >/dev/null 2>&1
summary=$(cat "$d/summary.md" 2>/dev/null)
contains "summary: has the header" "### Mutation testing" "$summary"
contains "summary: names the survivor" "CONDITIONALS_BOUNDARY internal/tui/table.go:292" "$summary"
contains "summary: publishes the budget it ran with" "budget this run was measured with" "$summary"
contains "summary: publishes the per-mutant cap" "per-mutant cap: 120s" "$summary"

echo
echo "=== 16. the survivor count is read from the report, not inferred from a literal ==="
# ============================================================================

# The verdict filters by the literal "LIVED"; a rename makes it match nothing and every survivor invisible.
# An invisible survivor reads as "new: 0" and a green, so mutants_lived (which a rename cannot fudge) must agree.
d=$(new_case drifted-status)
REPORT_STATUS=SURVIVED make_report "$d/report.json" 46 \
	'CONDITIONALS_BOUNDARY internal/tui/table.go:292' \
	'CONDITIONALS_NEGATION internal/tui/app.go:380'
make_log "$d/run.log" 44 0
out=$("$MUTATE" --verdict-only "$d/report.json" "$d/run.log" "$d/allowlist" \
	--expected-total 46 --engine-rc 0 2>&1)
check "drifted status: red" 1 $?
contains "drifted status: names the reported count" "2" "$out"
contains "drifted status: names the entries that carry it" "0" "$out"
contains "drifted status: says a drifted literal hides survivors" "LIVED" "$out"
lacks "drifted status: never says measured and clean" "measured and clean" "$out"

# The mismatch the other way round: the one entry is allowlisted, so only the mismatch can make it red.
d=$(new_case drifted-status-inflated)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
sed -i 's/"mutants_lived":1,/"mutants_lived":2,/' "$d/report.json"
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >"$d/allowlist"
make_log "$d/run.log" 45 0
out=$("$MUTATE" --verdict-only "$d/report.json" "$d/run.log" "$d/allowlist" \
	--expected-total 46 --engine-rc 0 2>&1)
check "inflated survivor count: red" 1 $?
contains "inflated survivor count: names both numbers" "2" "$out"
lacks "inflated survivor count: never says measured and clean" "measured and clean" "$out"

# The honest case must still be green: the assertion cannot be a blanket red.
d=$(new_case status-agrees)
make_report "$d/report.json" 46 'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >"$d/allowlist"
make_log "$d/run.log" 45 0
out=$("$MUTATE" --verdict-only "$d/report.json" "$d/run.log" "$d/allowlist" \
	--expected-total 46 --engine-rc 0 2>&1)
check "status agrees: green" 0 $?
contains "status agrees: says measured and clean" "measured and clean" "$out"

# No survivors at all: guarding against 0 != 0 would red every green run.
d=$(new_case status-zero)
make_report "$d/report.json" 46
make_log "$d/run.log" 46 0
out=$("$MUTATE" --verdict-only "$d/report.json" "$d/run.log" "$d/allowlist" \
	--expected-total 46 --engine-rc 0 2>&1)
check "no survivors: green" 0 $?

# Two mutants of the SAME type on ONE line: one allowlist entry, two survivors. The coherence
# check runs against the records and not against the deduplicated list, so this has to be green —
# otherwise two comparisons on a single line turn every clean run into a false red.
d=$(new_case two-mutants-one-line)
make_report "$d/report.json" 46 \
	'CONDITIONALS_BOUNDARY internal/tui/table.go:292' \
	'CONDITIONALS_BOUNDARY internal/tui/table.go:292'
printf 'CONDITIONALS_BOUNDARY internal/tui/table.go:292\n' >"$d/allowlist"
make_log "$d/run.log" 45 0
out=$("$MUTATE" --verdict-only "$d/report.json" "$d/run.log" "$d/allowlist" \
	--expected-total 46 --engine-rc 0 2>&1)
check "two mutants on one line: green" 0 $?
contains "two mutants on one line: one entry covers both" "measured and clean" "$out"
contains "two mutants on one line: counts the distinct entries" "surviving in the measured scope: 1" "$out"

echo
echo "=== 17. a cut reason survives the trip from the supervisor to the summary ==="
# ============================================================================

# Sections 6 and 12 FABRICATE a log already carrying the supervisor's line, which a real run never produced.
# These cases drive the real supervisor over a wedging engine, so the line has to arrive on its own.

# An engine that announces three mutants and delivers one before hanging, so the STALL fires, not the ceiling.
cat >"$gr4/wedge-engine" <<'EOS'
#!/usr/bin/env bash
dry=0
prev=
for a in "$@"; do
	[[ $prev == --output ]] && out=$a
	[[ $a == --dry-run ]] && dry=1
	prev=$a
done
if [[ $dry -eq 1 ]]; then
	printf 'Starting...\nGathering coverage...\ndone in 2.5s\n'
	printf '  RUNNABLE CONDITIONALS_BOUNDARY at pkg/gate.go:1:7\n'
	printf '  RUNNABLE CONDITIONALS_BOUNDARY at pkg/gate.go:2:7\n'
	printf '  RUNNABLE CONDITIONALS_BOUNDARY at pkg/gate.go:3:7\n'
	printf '\nDry run completed in 2.5s\nRunnable: 3, Not covered: 0\n'
	printf 'Mutator coverage: 100.00%%\n'
	exit 0
fi
# One of the three, then silence: no report is ever written.
printf '  RUNNABLE CONDITIONALS_BOUNDARY at pkg/gate.go:1:7\n'
sleep 3600
EOS
chmod +x "$gr4/wedge-engine"
rm -f "$gr4/run.log" "$gr4/report.json" "$gr4/out.txt"
out=$(cd "$gr4" && env STUB_IN_SCOPE=1 STUB_SKIPPED=0 STUB_FILE=pkg/gate.go \
	WATCHDOG_POLL=1 MUTATE_ENGINE="$gr4/wedge-engine" \
	MUTATE_CAP=2s MUTATE_STALL=6s MUTATE_CEILING=30s MUTATE_RUN_LOG=run.log \
	MUTATE_SCOPE_FILE=scope.txt MUTATE_BUDGET_FILE=budget.txt \
	MUTATE_SUMMARY=summary.md GITHUB_OUTPUT="$gr4/out.txt" \
	bash scripts/mutate.sh --run 2>&1)
rc=$?
check "wedged run: red" 1 "$rc"
contains "wedged run: names the stall" "no progress for" "$out"
lacks "wedged run: never says the reason is missing" "reason not found in the log" "$out"
contains "wedged run: the reason is in the run log" "no progress for" "$(cat "$gr4/run.log")"
contains "wedged run: the reason reaches the summary" "no progress for" "$(cat "$gr4/summary.md")"
# The supervisor's lines must not be mistaken for mutant lines, or the run would be red for the wrong reason.
contains "wedged run: still says a cut is not a test verdict" "Neither is a verdict about the tests" "$out"

# The cross-check must survive the merge: supervisor noise must neither drown the footer nor fake a TIMED OUT.
d=$(new_case timeout-crosscheck-with-supervisor-noise)
make_log "$d/run.log" 3 2 'watchdog.sh: alive +5s 3/5'
verdict "$d" 5 0
check "timed out plus supervisor noise: red" 1 $?
contains "timed out plus supervisor noise: names the expired count" "2 mutants expired" "$(cat "$d/verdict.out")"

# And the counts still disagree when they really do, which the merge must not hide.
d=$(new_case timeout-crosscheck-lies)
make_log "$d/run.log" 3 0 'watchdog.sh: alive +5s 3/5'
sed -i 's/^Timed out: 0,/Timed out: 4,/' "$d/run.log"
verdict "$d" 5 0
check "footer lying about timeouts: red" 1 $?
contains "footer lying about timeouts: names both counts" "4 timed-out mutants but carries 0" "$(cat "$d/verdict.out")"

echo
echo "=== 18. a forbidden flag cannot arrive through the engine string ==="
# ============================================================================

# reject_forbidden only saw the flags the script builds itself, and MUTATE_ENGINE is split the same way at the call.
# So a flag smuggled in there reached the engine: --silent is the one that empties the log this gate reads.
# The stub engine ignores what it does not know, so the difference between the two runs is the guard alone.
make_stub_engine "$gr4/stub-engine" 3 0 pkg/gate.go
engine_guard() { # engine_guard <engine string...>
	rm -f "$gr4/run.log" "$gr4/report.json"
	(cd "$gr4" && env STUB_IN_SCOPE=3 STUB_SKIPPED=0 STUB_FILE=pkg/gate.go \
		MUTATE_ENGINE="$1" MUTATE_RUN_LOG=run.log MUTATE_SCOPE_FILE=scope.txt \
		MUTATE_BUDGET_FILE=budget.txt bash scripts/mutate.sh --run --dry 2>&1)
}

out=$(engine_guard "$gr4/stub-engine --silent")
check "engine with --silent: refused" 2 $?
contains "engine with --silent: names the flag" "silent" "$out"
contains "engine with --silent: says it breaks the contract" "one-line-per-mutant" "$out"

# The grouped spelling has to be caught through the engine string too.
out=$(engine_guard "$gr4/stub-engine -Sk")
check "engine with a grouped shorthand: refused" 2 $?
contains "engine with a grouped shorthand: names the shorthand" "shorthand" "$out"

# The shorthand on its own.
out=$(engine_guard "$gr4/stub-engine -s")
check "engine with a bare shorthand: refused" 2 $?

# A clean engine string must still run, or the guard is a blanket refusal worse than none.
out=$(engine_guard "$gr4/stub-engine")
check "clean engine string: not refused" 0 $?
contains "clean engine string: enumerated" "in scope of" "$out"

echo
echo "=== 19. the dry-run is a measurement or it is a hard error ==="
# ============================================================================

# DRY_RC was captured and only quoted in the next error's message, so a dry-run that failed was read past.
make_stub_engine "$gr4/dry-fail" 3 0 pkg/gate.go
cat >>"$gr4/dry-fail" <<'EOS'
EOS
# A dry-run that enumerates normally and THEN fails: the exit code is the only visible difference.
cat >"$gr4/dry-fail" <<'EOS'
#!/usr/bin/env bash
dry=0
prev=
for a in "$@"; do
	[[ $a == --dry-run ]] && dry=1
	prev=$a
done
if [[ $dry -eq 1 ]]; then
	printf 'Starting...\nGathering coverage...\ndone in 2.5s\n'
	printf '  RUNNABLE CONDITIONALS_BOUNDARY at pkg/gate.go:1:7\n'
	printf '\nDry run completed in 2.5s\nRunnable: 1, Not covered: 0\n'
	printf 'Mutator coverage: 100.00%%\n'
	printf 'fatal: the mutator set is empty\n' >&2
	exit 3
fi
exit 0
EOS
chmod +x "$gr4/dry-fail"
out=$(cd "$gr4" && env STUB_IN_SCOPE=1 STUB_SKIPPED=0 STUB_FILE=pkg/gate.go \
	MUTATE_ENGINE="$gr4/dry-fail" MUTATE_RUN_LOG=run.log MUTATE_SCOPE_FILE=scope.txt \
	MUTATE_BUDGET_FILE=budget.txt bash scripts/mutate.sh --run --dry 2>&1)
check "a failing dry-run: hard error" 2 $?
contains "a failing dry-run: names the exit code" "3" "$out"
contains "a failing dry-run: says it cannot be trusted" "dry-run" "$out"

# A healthy dry-run with the same output must still pass: the assertion is on the code, not the shape.
out=$(cd "$gr4" && env STUB_IN_SCOPE=1 STUB_SKIPPED=0 STUB_FILE=pkg/gate.go \
	MUTATE_ENGINE="$gr4/stub-engine" MUTATE_RUN_LOG=run.log MUTATE_SCOPE_FILE=scope.txt \
	MUTATE_BUDGET_FILE=budget.txt bash scripts/mutate.sh --run --dry 2>&1)
check "a healthy dry-run: accepted" 0 $?

echo
echo "=== 20. the coverage duration is parsed whatever unit the engine prints ==="
# ============================================================================

# Go prints a duration with a unit once it passes a minute, and the parser only accepted bare digits.
# So a repo whose coverage pass crossed 60s died with exit 2 on EVERY PR, for a reason unrelated to the change.
duration_case() { # duration_case <dry-run footer duration> -> echoes the run output
	local engine="$gr4/dur-engine"
	cat >"$engine" <<EOS
#!/usr/bin/env bash
dry=0
prev=
for a in "\$@"; do
	[[ \$a == --dry-run ]] && dry=1
	prev=\$a
done
if [[ \$dry -eq 1 ]]; then
	printf 'Starting...\nGathering coverage...\ndone in $1\n'
	printf '  RUNNABLE CONDITIONALS_BOUNDARY at pkg/gate.go:1:7\n'
	printf 'Runnable: 1, Not covered: 0\nMutator coverage: 100.00%%\n'
	exit 0
fi
exit 0
EOS
	chmod +x "$engine"
	(cd "$gr4" && env MUTATE_ENGINE="$engine" MUTATE_CAP=120s MUTATE_RUN_LOG=run.log \
		MUTATE_SCOPE_FILE=scope.txt MUTATE_BUDGET_FILE=budget.txt \
		bash scripts/mutate.sh --run --dry 2>&1)
}

# Seconds are normalised to milliseconds, and printed locale-independently to keep the decimal comma out.
out=$(duration_case '8.858216525s')
contains "seconds only: parsed" "coverage 8.858s" "$out"

out=$(duration_case '1m2.5s')
contains "one minute and change: parsed" "coverage 62.5s" "$out"

out=$(duration_case '1h2m3.5s')
contains "hours included: parsed" "coverage 3723.5s" "$out"

out=$(duration_case '2m')
contains "whole minutes: parsed" "coverage 120s" "$out"

# A Spanish locale prints the decimal as a comma, which every unit has to survive.
out=$(duration_case '1m2,5s')
contains "decimal comma: parsed" "coverage 62.5s" "$out"

# A duration in a unit it does not understand is a hard error, not a partial parse.
out=$(duration_case '5x3s')
check "unknown unit: hard error" 2 $?
out=$(duration_case 'nonsense')
check "no duration at all: hard error" 2 $?

# An absent or zero duration is still a hard error, not a silent zero.
out=$(duration_case '0s')
check "zero duration: hard error" 2 $?

echo
echo "=== 21. a timed-out mutant is observable before the stall cuts the run ==="
# ============================================================================

# The cap and the stall were both 120s, and a mutant emits no progress while it runs, so the two raced.
# The supervisor won: a 124 with the log truncated and the expired mutant never reported.

# 2 * CAP < STALL   a mutant is REPORTED before the stall gives up; the second cap is the contention margin.
# STALL < CEILING   a stalled run is cut by the supervisor, not by the platform.
# CEILING + RESERVE < JOB_CEILING   everything before the supervisor fits too.

# CAP == STALL is the bug itself.
ci_out=$(run_with_stub 2 0 --ci MUTATE_CAP=120s MUTATE_WORKERS=4 MUTATE_STALL=2m \
	MUTATE_CEILING=4m MUTATE_JOB_CEILING=8m MUTATE_SETUP_RESERVE=150s \
	MUTATE_JOB_START="$(date +%s)")
rc=$?
check "cap equal to stall: exit 2" 2 $rc
contains "cap equal to stall: names both" "120s" "$ci_out"
contains "cap equal to stall: says a cap needs room under the stall" "cut as a stall instead of reported as TIMED OUT" "$ci_out"

# CAP above STALL: worse, the cut always wins.
ci_out=$(run_with_stub 2 0 --ci MUTATE_CAP=200s MUTATE_WORKERS=4 MUTATE_STALL=2m \
	MUTATE_CEILING=4m MUTATE_JOB_CEILING=8m MUTATE_SETUP_RESERVE=150s \
	MUTATE_JOB_START="$(date +%s)")
check "cap above stall: exit 2" 2 $?

# Cap under the stall but without the margin: one cap of slack, and the engine's own +2s grace still trips it.
ci_out=$(run_with_stub 2 0 --ci MUTATE_CAP=95s MUTATE_WORKERS=4 MUTATE_STALL=3m \
	MUTATE_CEILING=4m MUTATE_JOB_CEILING=8m MUTATE_SETUP_RESERVE=150s \
	MUTATE_JOB_START="$(date +%s)")
check "cap just under stall, no margin: exit 2" 2 $?

# The chain satisfied: the run gets to the supervisor.
ci_out=$(run_with_stub 2 0 --ci MUTATE_CAP=60s MUTATE_WORKERS=4 MUTATE_STALL=3m \
	MUTATE_CEILING=5m MUTATE_JOB_CEILING=8m MUTATE_SETUP_RESERVE=150s \
	MUTATE_JOB_START="$(($(date +%s) - 5))")
rc=$?
check "cap with a full cap of margin under the stall: the run completes" 0 $rc
lacks "cap with a full cap of margin: no invariant complaint" "must be" "$ci_out"

# The local whole-module row must satisfy the same chain, read from the script's own table so it cannot drift.
local_row=$(sed -n 's/^\tCAP_DEFAULT=\([0-9]*\)s WORKERS_DEFAULT=\([0-9]*\) STALL_DEFAULT=\([0-9]*\)m CEILING_DEFAULT=\([0-9]*\)m$/\1 \2 \3 \4/p' "$MUTATE")
read -r l_cap l_workers l_stall l_ceiling <<<"$local_row"
if [[ -z ${l_cap:-} ]]; then
	bad "the local whole-module row is declared in the budget table"
else
	ok "the local whole-module row is declared in the budget table"
	check "the local row's workers" 0 $((l_workers > 0 ? 0 : 1))
	check "the local row's cap under its stall" 0 $((2 * l_cap < l_stall * 60 ? 0 : 1))
	check "the local row's stall under its ceiling" 0 $((l_stall * 60 < l_ceiling * 60 ? 0 : 1))
fi

echo
echo "=== 22. the comment convention is enforced, not just claimed ==="
# ============================================================================

# AGENTS.md says our code comments are one line each, and for a while that was false here: 45 blocks.
# A convention nobody checks is a comment, and this branch exists to stop exactly that.
# The two vendored files are exempt BY DESIGN: a frozen copy from chezmoi, and reformatting is what diverges.

# Our files: no comment line over this width, and no CONTIGUOUS run longer than the
# header, the budget table and the section banners actually need.
OUR_MAX_WIDTH=170
OUR_MAX_RUN=6
VENDORED=(scripts/watchdog.sh scripts/watchdog_test.sh)

for f in scripts/mutate.sh scripts/mutate_test.sh; do
	over=$(awk -v w="$OUR_MAX_WIDTH" 'BEGIN{c=0} /^[[:space:]]*#/ && length($0)>w {c++} END{print c+0}' "$f")
	check "$(basename "$f"): no comment line over $OUR_MAX_WIDTH chars" 0 "$over"

	longest=$(awk '
		/^[[:space:]]*#/ { run++; if (run > max) max = run; next }
		{ run = 0 }
		END { print max + 0 }' "$f")
	check "$(basename "$f"): no comment run longer than $OUR_MAX_RUN lines" 0 \
		"$((longest > OUR_MAX_RUN ? 1 : 0))"
done

# The exemption is asserted only as far as it can be: these files exist, so the
# convention skips them. What they ARE is not checked here — the frozen copy is
# covered by scripts/watchdog_test.sh (behaviour) and by diffing against chezmoi.
# Deliberately not a golden hash: the origin lives in another repo and is meant to
# move, so pinning its bytes would make a legitimate upstream sync a constant to edit.
for f in "${VENDORED[@]}"; do
	if [[ -f $f ]]; then
		ok "vendored and untouched by the convention: $f"
	else
		bad "vendored file missing: $f"
	fi
done
# ============================================================================
echo
echo "=== 23. the exclusion the Makefile declares is the one the gate runs ==="
# ============================================================================

# MUTATE_EXCLUDE lives in the Makefile and mutate.sh reads it from there, so this is the pin on
# that single source: files the gate must keep and files it must keep out. Widening the regexp is
# a deliberate act, and this is where it has to be said out loud.
exclude=$(sed -n 's/^MUTATE_EXCLUDE ?= //p' "$HERE/../Makefile" | head -n 1)
if [ -n "$exclude" ]; then
	ok "MUTATE_EXCLUDE is declared in the Makefile"
else
	bad "MUTATE_EXCLUDE is declared in the Makefile (missing)"
	exclude='(^(^))'
fi

gated=$(printf '%s\n' "internal/config/config.go" "internal/tui/app.go" "cmd/vroom/main.go" |
	grep -cE "$exclude" || true)
check "gated files are not excluded (0 matches)" 0 "$gated"

excluded=$(printf '%s\n' ".worktrees/wt-x/internal/tui/app.go" | grep -cEvE "$exclude" || true)
check "excluded files stay out of the gate (0 left in)" 0 "$excluded"

echo
printf '%d/%d passed\n' "$pass" "$((pass + fail))"
[[ $fail -eq 0 ]]
