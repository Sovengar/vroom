#!/usr/bin/env bash
# mutate.sh — measure the mutation of a scope AND decide its verdict, in one process.
# The rule that shapes the whole design: "no measurement" is a RED verdict, never a green one.
# The old gate printed "no report.json -> no result, gate passes" and exited 0.
# Usage and exit codes: --help. The design is in AGENTS.md, section "Mutation".

set -uo pipefail

# The single definition of "one unit of progress", counted by both the numerator and the denominator.
# The trailing [^[:space:]] tolerates the CR a terminal line discipline adds.
PROGRESS_RE='at [^[:space:]]+:[[:digit:]]+:[[:digit:]]+[^[:space:]]*$'

# Flags that break the "exactly one line per mutant" contract, and with it the denominator.
# The guard below is DERIVED from this list, so no message can name a flag nothing enforces.
# Overridable, and the suite relies on that to prove the derivation is real.
MUTATE_FORBIDDEN=${MUTATE_FORBIDDEN:-'-S --output-statuses -s --silent'}

# The budget, one row per scope, so both loops read the same formula instead of each keeping its own copy.
# Under --ci these are not inherited but mandatory: "the environment wins" would let CI loosen its own budget.
# The diff row is the CI row, and its chain is asserted below: 2*cap < stall < ceiling, ceiling + reserve < job.
# scope  cap   workers stall  ceiling
# run    180s  8       20m    60m
# diff   180s  4       8m     13m

# --- output: every reason line goes to stdout AND, when --summary was given, to that file
OUT_LINES=()

_out() {
	printf '%s\n' "$*"
	OUT_LINES+=("$*")
}

_flush_summary() { # _flush_summary <path> <title>
	[[ -n ${1:-} ]] || return 0
	mkdir -p "$(dirname "$1")"
	{
		printf '### %s\n\n' "$2"
		printf '%s\n' "${OUT_LINES[@]}"
	} >"$1"
}

die() { # die <code> <message...>
	local code=$1
	shift
	printf 'mutate: %s\n' "$*" >&2
	exit "$code"
}

# Seconds, or a number with a 30s/5m/2h suffix.
_to_secs() {
	case $1 in
	*s) echo $((${1%s})) ;;
	*m) echo $((${1%m} * 60)) ;;
	*h) echo $((${1%h} * 3600)) ;;
	*) echo "$1" ;;
	esac
}

# Go prints time.Duration with a unit once it passes a minute (1h2m3.5s, 2m, 1m2.5s).
# Anything but h/m/s, or a total of zero, is a hard error: never a silent 0.
# Normalised to milliseconds because it is only ever divided into a cap.
_go_duration_secs() { # _go_duration_secs <duration> -> seconds on stdout, or fail
	local secs
	secs=$(awk -v d="$1" 'BEGIN{
		gsub(/,/, ".", d)
		total = 0
		while (match(d, /^[0-9]+(\.[0-9]+)?[hms]/)) {
			n = substr(d, 1, RLENGTH)
			u = substr(n, length(n), 1)
			v = substr(n, 1, length(n) - 1) + 0
			if (u == "h") total += v * 3600
			else if (u == "m") total += v * 60
			else total += v
			d = substr(d, RLENGTH + 1)
		}
		if (d != "") exit 1
		if (total <= 0) exit 1
		sec = int(total)
		printf "%d.%03d", sec, int((total - sec) * 1000 + 0.5)
	}') || return 1
	# Trims only the padding the fixed three decimals added: it stops at the first significant digit.
	while [[ $secs == *.*0 ]]; do secs=${secs%0}; done
	[[ $secs == *. ]] && secs=${secs%.}
	printf '%s\n' "$secs"
}

# --- VERDICT: a pure function of paths, no gremlins, no Go, no git, no clock
# Branch order is "least ambiguous first": a missing tool cannot judge anything, a cancelled run has no verdict.
# Whether the run happened, as opposed to what it measured: "no result" vs "a result", which one cascade stops saying.
_verdict_run_state() { # <run_log> <engine_rc>
	local run_log=$1 engine_rc=$2 reason

	# The shape --silent leaves behind: exit 0, a report present, no line to count.
	if [[ ! -s $run_log ]]; then
		_out "- **no measurement**: the run log is empty, so the engine produced no progress lines at all."
		_out "- An empty log is exactly what a silent run leaves behind while exiting 0; nothing was measured."
		return 1
	fi

	# 124 is the supervisor's own "I cut it"; the reason is its last stderr line, inside the run log.
	# Stall and ceiling demand opposite responses, so they are told apart rather than lumped together.
	if [[ $engine_rc -eq 124 ]]; then
		reason=$(grep -oE 'no progress for [0-9]+s|reached .* did not exit within the [0-9]+s ceiling|[0-9]+s ceiling' "$run_log" | tail -1)
		_out "- **no measurement**: the supervisor cut the run — ${reason:-reason not found in the log}."
		_out "- A stall means the engine stopped making progress; the ceiling means it outlived its budget. Neither is a verdict about the tests."
		_out ''
		_log_tail "$run_log"
		return 1
	fi

	# 130/143 mean the SUPERVISOR was signalled; no layer may translate them into survivors.
	if [[ $engine_rc -eq 130 || $engine_rc -eq 143 ]]; then
		_out "- **no measurement**: the run was cancelled (the supervisor exited $engine_rc), so there is no result to judge."
		_out "- 130 and 143 pass through untranslated on purpose: they mean the runner cancelled the job, not that mutation produced a verdict."
		return 1
	fi

	if [[ $engine_rc -ne 0 ]]; then
		_out "- **no measurement**: the mutation engine exited $engine_rc, so the run failed."
		_out "- An exit that is not 124/130/143 is the engine's own error, not a cut by the supervisor."
		_out ''
		_log_tail "$run_log"
		return 1
	fi
	return 0
}

# An expired mutant was NEVER measured: not a kill, not a survivor, and report.json leaves it out of
# the total. So it can never become green by silence -- it is red unless $MUTATE_TIMEOUTS records the
# hang, with a per-file CEILING. Same contract as the allowlist: the gate compares against something
# committed, never against nothing, and the green still has to say what it did not measure.
_verdict_timeouts() { # <run_log> <timed_out>
	local run_log=$1 total=$2
	local timeouts=${TIMEOUTS:-.mutation-timeouts}
	local counts over='' file seen ceiling

	_out "- $total mutants expired on the per-mutant timeout and were never tested."
	_out "- The report does not count them, so its efficacy covers fewer mutants than the run generated."

	# Missing baseline first: without it every expiry is a coin toss between a regression and a hang
	# already known, and neither answer could be justified in the summary where a red is read.
	if [[ ! -f $timeouts ]]; then
		_out "- **no measurement**: $timeouts is missing, so these expiries have no recorded ceiling to be judged against."
		_out "- An expiry is either contention (raise the per-mutant cap or lower the worker count, then rerun) or an infinite-loop mutant."
		_out "- For the second: a test that fails fast kills it, or record \`<file> <ceiling>\` with the reason in $timeouts."
		return 1
	fi

	# A CEILING PER FILE, not an exact set of lines: expiry is partly contention, so the same suite
	# reported 9 hangs one run and 7 the next, and pinning lines would fail at random. The count per
	# file is the stable half -- a new hang, or one more than recorded, is still a red. Both inputs
	# are lines the log already had to agree on: the ceilings file, and the per-mutant records whose
	# total the footer cross-check above has verified.
	counts=$(awk -v ceilings="$timeouts" '
		NR == FNR {
			if ($0 ~ /^[[:space:]]*(#|$)/ || NF < 2) next
			ceil[$1] = $2
			next
		}
		{
			if (!match($0, /^[[:space:]]*TIMED OUT [^ ]+ at [^[:space:]]+:[0-9]+:[0-9]+/)) next
			s = substr($0, RSTART, RLENGTH)
			sub(/^[[:space:]]*TIMED OUT [^ ]+ at /, "", s)
			sub(/:[0-9]+:[0-9]+$/, "", s)
			seen[s]++
		}
		END {
			for (f in seen) printf "%s %d %d\n", f, seen[f], (f in ceil) ? ceil[f] + 0 : 0
		}
	' "$timeouts" "$run_log" | sort)

	while read -r file seen ceiling; do
		[[ -n ${file:-} ]] || continue
		if ((seen > ceiling)); then over+="$file $seen $ceiling"$'\n'; fi
	done <<<"$counts"

	if [[ -n $over ]]; then
		_out "- **no measurement**: more expired than $timeouts records:"
		while read -r file seen ceiling; do
			[[ -n ${file:-} ]] || continue
			_out "  - $file: $seen expired, ceiling $ceiling"
		done <<<"$over"
		_out "- Above the recorded ceiling is either contention (raise the per-mutant cap or lower the worker count, then rerun) or a NEW infinite-loop mutant."
		_out "- Kill it with a test that fails fast, or — only if it is the same known hang — raise the ceiling in $timeouts and say why."
		return 1
	fi

	_out "- $total expired within the ceilings in $timeouts (recorded hangs, never tested):"
	while read -r file seen ceiling; do
		[[ -n ${file:-} ]] || continue
		_out "  - $file: $seen/$ceiling"
	done <<<"$counts"
	return 0
}
_verdict() { # <report> <run_log> <allowlist> <expected_total> <engine_rc> [announced] [budget]
	local report=$1 run_log=$2 allowlist=$3 expected=$4 engine_rc=$5
	local announced=${6:-} budget=${7:-}
	local unannounced

	if ! command -v jq >/dev/null 2>&1; then
		_out "- **no measurement**: jq is not available, so the report cannot be read and no verdict can be reached."
		_out "- Install jq: an extractor that fails leaves an empty output, and an empty output read as 'zero new survivors' is the green-without-measurement this check forbids."
		return 1
	fi

	# Without an allowlist every survivor would look new, so the gate compares against nothing.
	# Red with instructions, never a skip: a skipped required check blocks every PR forever.
	if [[ ! -f $allowlist ]]; then
		_out "- **no measurement**: $allowlist is missing, so the gate has nothing to compare survivors against."
		_out "- Seed it with \`make mutate-all\`, then commit $allowlist."
		return 1
	fi

	# Was there a run at all? Before anything about WHAT it measured: a verdict about a run that never happened is meaningless.
	_verdict_run_state "$run_log" "$engine_rc" || return 1

	# TIMED OUT mutants enter neither the totals nor the efficacy: 15 expired report a perfect score for 15 untested mutants.
	# Counted from the aggregate footer, not per line: a per-line count dies with a truncated log.
	local timed_out timed_out_lines
	timed_out=$(sed -n 's/^Timed out: \([0-9][0-9]*\),.*/\1/p' "$run_log" | tail -1)
	timed_out=${timed_out:-0}
	timed_out_lines=$(grep -cE '^[[:space:]]*TIMED OUT [^ ]+ at [^[:space:]]+:[[:digit:]]+:[[:digit:]]+' "$run_log" || true)
	if [[ $timed_out -ne $timed_out_lines ]]; then
		_out "- **no measurement**: the log claims $timed_out timed-out mutants but carries $timed_out_lines timed-out lines."
		_out "- One of the two counts is truncated or partial, so neither can be trusted and the run cannot be judged."
		return 1
	fi
	# Not green by silence, and not red by reflex either: _verdict_timeouts judges the expiries
	# against the committed ceilings, and only a hang nobody recorded can be a red.
	if [[ $timed_out -gt 0 ]]; then
		_verdict_timeouts "$run_log" "$timed_out" || return 1
	fi

	if [[ ! -f $report ]]; then
		# "Nothing to mutate" ONLY when the run said so AND the pre-count agrees.
		if [[ $expected -eq 0 ]] && grep -qF 'No results to report.' "$run_log"; then
			_out "- **nothing to mutate**: the diff has Go files but no mutable statements, so zero mutants were generated."
			_out "- **no mutation was measured, and that is a result, not a failure.**"
			_out "- Source of this verdict: the pre-count of expected mutants was 0, not the missing report."
			_budget_lines "$budget"
			return 0
		fi
		_out "- **no measurement**: no report was produced, so there is nothing to judge."
		_out "- The pre-count expected $expected mutant(s), so the scope was not empty of mutable code."
		_out ''
		_log_tail "$run_log"
		return 1
	fi

	# Parsed before it is read: a failing extractor leaves empty output, and empty read as "zero new survivors" is the green-without-measurement this check forbids.
	jq -e . "$report" >/dev/null 2>&1 || {
		_out "- **no measurement**: $report exists but cannot be parsed, so it cannot be judged."
		_out "- A failing extractor leaves empty output, and empty output read as 'zero new survivors' is the green this check exists to refuse."
		_out ''
		_log_tail "$run_log"
		return 1
	}

	_out '- measured:'
	_out "  - total: $(jq -r '.mutants_total' "$report")"
	_out "  - killed: $(jq -r '.mutants_killed' "$report")"
	_out "  - survived: $(jq -r '.mutants_lived' "$report")"
	_out "  - not viable: $(jq -r '.mutants_not_viable' "$report")"
	_out "  - not covered: $(jq -r '.mutants_not_covered' "$report")"
	_out "  - test efficacy: $(jq -r '.test_efficacy' "$report")%"
	_out "  - mutator coverage: $(jq -r '.mutations_coverage' "$report")%"

	# A verdict about a set other than the announced one is a verdict about something else.
	# SKIPPED is excluded: with --diff both files[] and the progress lines carry the whole module.
	if [[ -n $announced && -f $announced ]]; then
		unannounced=$(jq -r '.files[] | .file_name as $f | .mutations[] | select(.status != "SKIPPED") | $f' "$report" |
			sort -u | comm -23 - <(grep -vE '^[[:space:]]*$' "$announced" | sort -u))
		if [[ -n $unannounced ]]; then
			_out '- **no measurement**: the run reported mutants for files outside the scope that was announced:'
			local f
			while IFS= read -r f; do [[ -n $f ]] && _out "  - $f"; done <<<"$unannounced"
			return 1
		fi
	fi

	# Both directions: disagreement means the numerator and the denominator came out of different runs.
	local measured_total
	measured_total=$(jq -r '.mutants_total' "$report")
	if [[ $expected -eq 0 && $measured_total != 0 ]]; then
		_out "- **no measurement**: the pre-count expected 0 mutants but the run measured $measured_total."
		_out "- The denominator and the numerator came out of different runs, so the verdict would be about a scope that was never the one announced."
		return 1
	fi
	if [[ $expected -gt 0 && $measured_total == 0 ]]; then
		_out "- **no measurement**: the pre-count expected $expected mutant(s) and the run measured none of them."
		_out "- A green here would be a verdict about mutants that were never measured."
		return 1
	fi

	_verdict_survivors "$report" "$allowlist" "$expected" "$budget"
}

# The comparison against the allowlist, and the only green reachable with a report in hand.
# Split out so the "was there a measurement" half stops sharing a scope and a scratch directory.
_verdict_survivors() { # <report> <allowlist> <expected_total> <budget>
	local report=$1 allowlist=$2 expected=$3 budget=${4:-}
	local tmp total new survivor reported

	tmp=$(mktemp -d "${TMPDIR:-/tmp}/gitdash-mutate-verdict.XXXXXX") || return 1
	jq -r '.files[] | .file_name as $f | .mutations[] | select(.status=="LIVED") | "\(.type) \($f):\(.line)"' "$report" |
		sort -u >"$tmp/lived.txt"
	total=$(wc -l <"$tmp/lived.txt" | tr -d ' ')

	# The filter matches the literal "LIVED"; this count comes from the report and a rename cannot fudge it.
	# Disagreement is red: a drifted literal hides survivors and "new: 0" reads as a green.
	# Counted on the RECORDS, never on the deduplicated file above: two mutants of the same type on
	# one line are ONE allowlist entry but TWO survivors, so comparing against the file's line count
	# made every such run a false red (measured: 223 survivors, 193 distinct entries).
	reported=$(jq -r '.mutants_lived' "$report")
	measured=$(jq '[.files[].mutations[] | select(.status=="LIVED")] | length' "$report")
	if [[ ! $reported =~ ^[0-9]+$ || ! $measured =~ ^[0-9]+$ || $reported -ne $measured ]]; then
		_out "- **no measurement**: the report counts $reported survivor(s) but $measured record(s) carry the LIVED status."
		_out "- A status literal that drifted turns every real survivor into an invisible one, and an invisible survivor reads as 'new: 0' and a green."
		rm -rf "$tmp"
		return 1
	fi

	# Compared BY LINE, not by substring: same file at another line is a new one.
	grep -vE '^[[:space:]]*(#|$)' "$allowlist" | sed 's/[[:space:]]*$//' | sort -u >"$tmp/allow.txt"
	comm -23 "$tmp/lived.txt" "$tmp/allow.txt" >"$tmp/new.txt"
	new=$(wc -l <"$tmp/new.txt" | tr -d ' ')

	_out "- surviving in the measured scope: $total (allowlisted: $((total - new)), new: $new)"
	_budget_lines "$budget"

	if [[ $new -gt 0 ]]; then
		_out ''
		_out "- **$new surviving mutant(s) are not in $allowlist**:"
		while IFS= read -r survivor; do _out "  - $survivor"; done <"$tmp/new.txt"
		_out ''
		_out "Add a test that kills them, or — only if provably equivalent — add the line to $allowlist with a comment saying why."
		rm -rf "$tmp"
		return 1
	fi
	rm -rf "$tmp"

	if [[ $expected -eq 0 ]]; then
		# A _test.go-only diff arrives HERE, not through "No results to report": SKIPPED mutants are results too.
		_out '- **nothing to mutate**: the diff has Go files but no mutable statements, so the engine generated no mutants for it.'
		_out "- **no mutation was measured, and that is a result, not a failure.**"
		_out "- Source of this verdict: the pre-count of in-scope mutants was 0, not an absent report."
	else
		_out '- **measured and clean**: every surviving mutant is allowlisted, so this change introduced no new gap.'
	fi
	return 0
}

_log_tail() { # _log_tail <run_log>
	_out 'Tail of the run log:'
	_out '```'
	local line
	while IFS= read -r line; do _out "  $line"; done < <(tail -n 25 "$1")
	_out '```'
}

# On every verdict: these numbers only mean something relative to the job ceiling, which the workflow hides.
_budget_lines() { # _budget_lines <budget_file>
	local line
	[[ -n ${1:-} && -f $1 ]] || return 0
	_out '- budget this run was measured with:'
	while IFS= read -r line; do _out "  - $line"; done <"$1"
}

# --- RUN: warm, enumerate, supervise

REPORT=${MUTATE_REPORT:-report.json}
RUN_LOG=${MUTATE_RUN_LOG:-.mutation-run.log}
SCOPE_FILE=${MUTATE_SCOPE_FILE:-.mutation-scope.txt}
BUDGET_FILE=${MUTATE_BUDGET_FILE:-.mutation-budget.txt}
ALLOWLIST=${MUTATE_ALLOWLIST:-.mutation-allowlist}
# The recorded hangs, read exactly like the allowlist: an env override, else the committed file.
TIMEOUTS=${MUTATE_TIMEOUTS:-.mutation-timeouts}
ENGINE=${MUTATE_ENGINE:-go tool gremlins unleash}
WATCHDOG=${MUTATE_WATCHDOG:-scripts/watchdog.sh}
# MUTATE_EXCLUDE has exactly one home, the Makefile, and this reads it from there: a second
# hardcoded copy is one more thing that can drift from what `make mutate` actually runs. The suite
# pins the value (see the MUTATE_EXCLUDE case in mutate_test.sh), so widening the regexp is a red
# and not a silent hole. An env override still wins for the local loop, and with no Makefile in
# sight (the suite's fixture repos) an exclusion matching nothing is the honest default: gate
# everything rather than gate nothing.
if [[ -n ${MUTATE_EXCLUDE:-} ]]; then
	EXCLUDE=$MUTATE_EXCLUDE
else
	EXCLUDE=$(sed -n 's/^MUTATE_EXCLUDE ?= //p' Makefile 2>/dev/null | head -n 1)
	: "${EXCLUDE:=(^$)}"
fi
COVERPKG=${MUTATE_COVERPKG:-./...}
BASE=${MUTATE_BASE:-main}

MODE=run
CI=0
DRY=0
VERDICT_ONLY=0
POSITIONAL=()
EXPECTED_TOTAL=${MUTATE_EXPECTED_TOTAL:-}
ENGINE_RC=${MUTATE_ENGINE_RC:-}
ANNOUNCED=${MUTATE_ANNOUNCED:-}
BUDGET=${MUTATE_BUDGET:-}
SUMMARY=${MUTATE_SUMMARY:-}

while [[ $# -gt 0 ]]; do
	case $1 in
	--diff)
		MODE='diff'
		;;
	--run)
		MODE=run
		;;
	--ci)
		CI=1
		;;
	--dry)
		DRY=1
		;;
	--verdict-only)
		VERDICT_ONLY=1
		;;
	--expected-total)
		EXPECTED_TOTAL=${2:-}
		shift
		;;
	--engine-rc)
		ENGINE_RC=${2:-}
		shift
		;;
	--announced)
		ANNOUNCED=${2:-}
		shift
		;;
	--budget)
		BUDGET=${2:-}
		shift
		;;
	--summary)
		SUMMARY=${2:-}
		shift
		;;
	-h | --help)
		sed -n '3,24p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	-*)
		die 2 "unknown flag '$1' (try --help)"
		;;
	*)
		POSITIONAL+=("$1")
		;;
	esac
	shift
done

# --- verdict-only: the decision half, with no measurement machinery at all ----
if [[ $VERDICT_ONLY -eq 1 ]]; then
	[[ ${#POSITIONAL[@]} -eq 3 ]] ||
		die 2 "--verdict-only needs exactly three paths: <report> <run-log> <allowlist>"
	[[ -n $EXPECTED_TOTAL ]] || die 2 "--verdict-only needs --expected-total"
	[[ -n $ENGINE_RC ]] || die 2 "--verdict-only needs --engine-rc"
	[[ $EXPECTED_TOTAL =~ ^[0-9]+$ ]] || die 2 "--expected-total must be a non-negative integer"
	[[ $ENGINE_RC =~ ^[0-9]+$ ]] || die 2 "--engine-rc must be a non-negative integer"
	# Positional, never by convention: a path that resolved to nothing reads as "no log, so no problems".
	[[ -n ${POSITIONAL[1]} ]] || die 2 "--verdict-only was given an empty run-log path"

	_verdict "${POSITIONAL[0]}" "${POSITIONAL[1]}" "${POSITIONAL[2]}" \
		"$EXPECTED_TOTAL" "$ENGINE_RC" "$ANNOUNCED" "$BUDGET"
	rc=$?
	_flush_summary "$SUMMARY" 'Mutation testing'
	exit "$rc"
fi

# --- budget table -----------------------------------------------------------
case $MODE in
run)
	CAP_DEFAULT=180s WORKERS_DEFAULT=8 STALL_DEFAULT=20m CEILING_DEFAULT=60m
	;;
diff)
	# The per-mutant deadline is ceil(cap/coverage)*coverage, and vroom's coverage pass measures
	# ~73s, so cap 180s is what gives a mutant a deadline of about 3.5 minutes (coefficient 3).
	# 2*cap (6m) sits under the 8m stall, or a slow mutant would be cut as a stall instead of
	# reported as TIMED OUT.
	CAP_DEFAULT=180s WORKERS_DEFAULT=4 STALL_DEFAULT=8m CEILING_DEFAULT=13m
	;;
esac

for knob in CAP WORKERS STALL CEILING JOB_CEILING SETUP_RESERVE JOB_START; do
	var=MUTATE_$knob
	if [[ $CI -eq 1 && -z ${!var:-} ]]; then
		die 2 "--ci requires $var: the budget is mandatory here so that a missing one cannot fall back to the local row"
	fi
done

CAP=${MUTATE_CAP:-$CAP_DEFAULT}
WORKERS=${MUTATE_WORKERS:-$WORKERS_DEFAULT}
STALL=${MUTATE_STALL:-$STALL_DEFAULT}
CEILING=${MUTATE_CEILING:-$CEILING_DEFAULT}
JOB_CEILING=${MUTATE_JOB_CEILING:-}
SETUP_RESERVE=${MUTATE_SETUP_RESERVE:-0}
JOB_START=${MUTATE_JOB_START:-0}

CAP_SECS=$(_to_secs "$CAP")
CEILING_SECS=$(_to_secs "$CEILING")
JOB_CEILING_SECS=$([[ -n $JOB_CEILING ]] && _to_secs "$JOB_CEILING" || echo 0)
SETUP_RESERVE_SECS=$(_to_secs "$SETUP_RESERVE")
STALL_SECS=$(_to_secs "$STALL")

# Invariants, not magic numbers: the supervisor must die before the platform does.
# A typo in any limit is caught here rather than by a job cancelled with no reason, no report and no log.
[[ $STALL_SECS -gt 0 ]] || die 2 "the stall limit must be positive, got '$STALL'"
[[ $STALL_SECS -lt $CEILING_SECS ]] || die 2 "the stall limit ($STALL) must be below the run ceiling ($CEILING)"
# A mutant emits no progress while it runs, so the stall detector and the engine's own timeout race.
# If the cap reaches the stall the supervisor wins: a 124 with the log cut short, and the expired mutant never REPORTED.
# That makes the verdict's TIMED OUT branch unreachable, so a whole cap of slack under the stall is required.
[[ $((CAP_SECS * 2)) -lt $STALL_SECS ]] ||
	die 2 "the per-mutant cap ($CAP) leaves no room under the stall limit ($STALL): a slow mutant would be cut as a stall instead of reported as TIMED OUT. The cap must be under half the stall."
# NOT "ceiling < job timeout" but "everything before the supervisor plus its ceiling fits in the job".
# A ceiling that fits on paper and not in the clock is how the platform cancels instead of the supervisor.
if [[ $JOB_CEILING_SECS -gt 0 ]]; then
	[[ $SETUP_RESERVE_SECS -gt 0 ]] ||
		die 2 "MUTATE_SETUP_RESERVE must state how long the steps before the supervisor can take, otherwise the job budget cannot be checked"
	[[ $((CEILING_SECS + SETUP_RESERVE_SECS)) -lt $JOB_CEILING_SECS ]] ||
		die 2 "the run ceiling ($CEILING) plus the setup reserve ($SETUP_RESERVE) is $((CEILING_SECS + SETUP_RESERVE_SECS))s, which does not fit under the job ceiling ($JOB_CEILING = ${JOB_CEILING_SECS}s): the platform would cancel the job before the supervisor can say why"
fi

# --- forbidden flags: derived from MUTATE_FORBIDDEN, never spelled out twice
# A guard that can drift from the list it claims to enforce is a comment pretending to be code.
FORBIDDEN_EXACT=()
FORBIDDEN_SHORTS=()
for _f in $MUTATE_FORBIDDEN; do
	case $_f in
	--?*) FORBIDDEN_EXACT+=("$_f") ;;
	-?) FORBIDDEN_EXACT+=("$_f") FORBIDDEN_SHORTS+=(f="${_f#-}") ;;
	*) die 2 "MUTATE_FORBIDDEN contains '$_f', which is neither a long flag nor a shorthand" ;;
	esac
done
# bash cannot build a character class from a list at match time, so -S and -s become [s,S] here.
FORBIDDEN_SHORT_CLASS=$(IFS=,; echo "${FORBIDDEN_SHORTS[*]-}")

# Rejected before anything runs, so a forbidden flag is a refusal and not a measurement.
reject_forbidden() { # reject_forbidden <flag>...
	local f e
	for f in "$@"; do
		for e in ${FORBIDDEN_EXACT[@]+"${FORBIDDEN_EXACT[@]}"}; do
			case $f in
			"$e" | "$e"=*) die 2 "'$f' breaks the one-line-per-mutant contract and would make the denominator and this gate meaningless (see MUTATE_FORBIDDEN)" ;;
			esac
		done
		if [[ -n $FORBIDDEN_SHORT_CLASS && $f == -[!-]?* ]]; then
			case ${f#-} in
			*[$FORBIDDEN_SHORT_CLASS]*) die 2 "'$f' carries a forbidden shorthand grouped with other flags (see MUTATE_FORBIDDEN)" ;;
			esac
		fi
	done
}

ENGINE_FLAGS=(--exclude-files "$EXCLUDE" --coverpkg "$COVERPKG")
EXTRA_FLAGS=()
if [[ -n ${MUTATE_EXTRA_FLAGS:-} ]]; then
	# shellcheck disable=SC2206
	EXTRA_FLAGS=($MUTATE_EXTRA_FLAGS)
fi
# Split the way the call site splits it: $ENGINE is invoked unquoted, so that is where a flag slips through.
# shellcheck disable=SC2206
ENGINE_WORDS=($ENGINE)
reject_forbidden "${ENGINE_FLAGS[@]}" ${EXTRA_FLAGS[@]+"${EXTRA_FLAGS[@]}"} ${ENGINE_WORDS[@]+"${ENGINE_WORDS[@]}"}

# --- scope precheck: gremlins silently falls back to the whole module on an empty diff
# Without this a PR with no Go code launches a full-module run inside a short job.
# The scope is the COMMITTED diff, not the index.
SCOPE_ARGS=()
if [[ $MODE == diff ]]; then
	# ONE string, verified and then diffed: they used to differ, and on a runner the bare name fails to resolve.
	if ! git rev-parse --verify --quiet "$BASE" >/dev/null 2>&1; then
		_out "- **no measurement**: the base ref \`$BASE\` could not be resolved, so the set of files to mutate is unknown."
		_out "- An empty diff from an unresolved base is indistinguishable from a PR with no changes, and treating them alike is the hole this check closes."
		_out "- Pass the SAME ref you fetched, e.g. \`MUTATE_BASE=origin/main\` on a runner where the base is only a remote-tracking ref."
		_flush_summary "$SUMMARY" 'Mutation testing'
		die 1 "base ref $BASE could not be resolved"
	fi
	SCOPE_ARGS=(--diff "$BASE")
	# A diff that cannot be computed is an ERROR: `git diff` printing nothing on failure is a diff with no Go files.
	if ! git diff --name-only "$BASE...HEAD" >"$SCOPE_FILE.all"; then
		rm -f "$SCOPE_FILE.all"
		_out "- **no measurement**: the diff against \`$BASE\` could not be computed, so the scope is unknown."
		_out "- A failed \`git diff\` prints nothing, and nothing is indistinguishable from 'this PR touches no Go files'."
		_flush_summary "$SUMMARY" 'Mutation testing'
		die 1 "git diff $BASE...HEAD failed"
	fi
	grep '\.go$' "$SCOPE_FILE.all" >"$SCOPE_FILE" || true
	rm -f "$SCOPE_FILE.all"
else
	git ls-files '*.go' | grep -vE "$EXCLUDE" >"$SCOPE_FILE" || true
fi
ANNOUNCED_COUNT=$(grep -cvE '^[[:space:]]*$' "$SCOPE_FILE" || true)

# Published before any early exit, so the upload attaches the log even when the run stops at the precheck.
if [[ -n ${GITHUB_OUTPUT:-} ]]; then
	{
		printf 'report=%s\n' "$REPORT"
		printf 'run_log=%s\n' "$RUN_LOG"
		printf 'scope=%s\n' "$MODE"
	} >>"$GITHUB_OUTPUT"
fi

write_budget_file() {
	[[ -n $BUDGET_FILE ]] || return 0
	{
		printf 'scope: %s\n' "$MODE"
		printf 'per-mutant cap: %s\n' "$CAP"
		printf 'workers: %s\n' "$WORKERS"
		printf 'stall limit: %s\n' "$STALL"
		printf 'run ceiling: %s\n' "$CEILING"
		printf 'job ceiling: %s\n' "${JOB_CEILING:-unset}"
		printf 'files in scope: %s\n' "$ANNOUNCED_COUNT"
	} >"$BUDGET_FILE"
}
write_budget_file

# Decided by the PRE-CHECK, never rederived from a missing report: re-deriving moves the lie, it does not fix it.
if [[ $ANNOUNCED_COUNT -eq 0 ]]; then
	_out "- **nothing to mutate**: the scope against \`$BASE\` contains no .go files, so no mutation run was launched."
	_out "- **no mutation was measured.** This is a result, not a failure."
	_out "- Source of this verdict: the scope precheck found 0 files, not an absent report."
	_budget_lines "$BUDGET_FILE"
	_flush_summary "$SUMMARY" 'Mutation testing'
	exit 0
fi

# --- prerequisites ----------------------------------------------------------
command -v jq >/dev/null 2>&1 ||
	die 2 "jq is required to read the report and compare it against $ALLOWLIST"
[[ -x $WATCHDOG ]] ||
	die 2 "the supervisor is missing at $WATCHDOG (it is vendored in this repo; chezmoi is not involved)"
[[ -f $ALLOWLIST ]] ||
	die 2 "no $ALLOWLIST, so the gate has nothing to compare survivors against; seed it with: make mutate"

# The working tree survives between local runs, so a stale report must never be read as this one's.
rm -f "$REPORT" "$RUN_LOG"

# gremlins sets each mutant's timeout from ITS OWN coverage run, so that run has to measure the
# same thing the coefficient was computed from: a warm test cache reports ~0.2s, the ceiling
# collapses to a couple of seconds and every mutant of a slow package expires untested.
export GOFLAGS="-count=1${GOFLAGS:+ $GOFLAGS}"

# `-exec=setsid` is vroom's own and load-bearing: internal/process does `kill(-pgid, ...)`, and
# the CONDITIONALS_BOUNDARY mutant of `pgid > 0` turns the suite's own `Stop(StopSpec{Pgid: 0})`
# into `kill(-0)`, which is SIGTERM to every process in the group. Under gremlins that group is
# gremlins itself: it traps the first signal, closes its channel and panics on the second, and
# the run dies with exit 2 ("no measurement"). 3/3 runs died in internal/process without this;
# the whole suite re-measured with it: same results, same exit codes, 3173/3173, no extra time.
export GOFLAGS="$GOFLAGS -exec=setsid"

# --- warm: a cache primer only, never the source of the denominator
# -run '^$' builds the instrumented packages and runs zero suites, so its duration is build time.
echo "mutate: warming the build cache…" >&2
go test -coverpkg "$COVERPKG" -run '^$' ./... >/dev/null 2>&1 || true

# --- enumerate and measure elapsed, in ONE dry-run ---------------------------
DRY_OUT=$(mktemp "${TMPDIR:-/tmp}/gitdash-mutate-dry.XXXXXX") || die 2 "cannot create the dry-run scratch file"
# shellcheck disable=SC2064
trap "rm -f '$DRY_OUT'" EXIT

$ENGINE "${SCOPE_ARGS[@]}" --dry-run "${ENGINE_FLAGS[@]}" ${EXTRA_FLAGS[@]+"${EXTRA_FLAGS[@]}"} >"$DRY_OUT" 2>&1
DRY_RC=$?

# The dry-run is the denominator's source, so a failed one is not a warning.
# Its exit code used to be quoted only in the next error's message and never checked.
if [[ $DRY_RC -ne 0 ]]; then
	tail -n 25 "$DRY_OUT" >&2
	die 2 "the dry-run exited $DRY_RC, so neither the denominator nor the coverage time it reports can be trusted"
fi

cov_secs=$(_go_duration_secs "$(sed -n 's/.*done in \([^[:space:]]*\).*/\1/p' "$DRY_OUT" | tail -1)")
if [[ -z $cov_secs ]]; then
	tail -n 25 "$DRY_OUT" >&2
	die 2 "the dry-run reported no measurable coverage time, so the per-mutant cap cannot be derived (dry-run exit $DRY_RC)"
fi

# Two counts: WATCH_LINES is every mutant CONSIDERED (the supervisor's denominator, SKIPPED included).
# EXPECTED_MEASURED is the in-scope subset the verdict compares against. See AGENTS.md.
WATCH_LINES=$(grep -cE "$PROGRESS_RE" "$DRY_OUT" || true)
EXPECTED_MEASURED=$(grep -E "$PROGRESS_RE" "$DRY_OUT" | grep -cvE '^[[:space:]]*SKIPPED ' || true)

# ceil(cap / elapsed) as an integer: it is the only per-mutant lever gremlins has.
COEF=$(awk -v cap="$CAP_SECS" -v el="$cov_secs" 'BEGIN{printf "%d", (cap/el==int(cap/el)) ? cap/el : int(cap/el)+1}')
PER_MUTANT=$(awk -v el="$cov_secs" -v k="$COEF" 'BEGIN{printf "%d", el*k+2}')

echo "mutate: coverage ${cov_secs}s -> coefficient $COEF (per-mutant ceiling ~${PER_MUTANT}s, cap $CAP)" >&2
echo "mutate: $WORKERS workers, $EXPECTED_MEASURED in scope of $WATCH_LINES considered, $ANNOUNCED_COUNT files in the announced scope" >&2

if [[ $DRY -eq 1 ]]; then
	echo "mutate: --dry: warmed and enumerated, nothing mutated" >&2
	exit 0
fi

# The measured half of the budget invariant, checked where it can still save the run.
if [[ $JOB_CEILING_SECS -gt 0 && $JOB_START =~ ^[0-9]+$ && $JOB_START -gt 0 ]]; then
	elapsed=$(( $(date +%s) - JOB_START ))
	remaining=$(( JOB_CEILING_SECS - elapsed ))
	[[ $remaining -gt $CEILING_SECS ]] ||
		die 2 "not enough job budget left to supervise: ${remaining}s of $JOB_CEILING remain after ${elapsed}s already spent, and the supervisor alone can take $CEILING ($CEILING_SECS). Raise timeout-minutes or lower MUTATE_CEILING."
	echo "mutate: job budget: ${elapsed}s spent, ${remaining}s left, supervisor ceiling ${CEILING_SECS}s" >&2
fi

# --- supervise: no exec, because it would take away the ability to tell 124 from a failed mutation
echo "mutate: supervising (stall $STALL, ceiling $CEILING, $WATCH_LINES progress lines to watch)" >&2
# stderr is folded into the SAME log: the supervisor's reasons go to its stderr, and a stdout-only
# tee dropped the one line that explains a 124, so the verdict printed "reason not found in the log".
# It cannot disturb the TIMED OUT cross-check: the supervisor prefixes every line with its own name.
"$WATCHDOG" "$STALL" "$CEILING" "$WATCH_LINES" "$PROGRESS_RE" -- \
	$ENGINE "${SCOPE_ARGS[@]}" "${ENGINE_FLAGS[@]}" \
	--workers "$WORKERS" --timeout-coefficient "$COEF" --output "$REPORT" \
	${EXTRA_FLAGS[@]+"${EXTRA_FLAGS[@]}"} 2>&1 |
	tee "$RUN_LOG"

# Taken immediately: under pipefail a pipeline's status is "the last non-zero", so a failed tee would read as a failed run.
ENGINE_RC=${PIPESTATUS[0]}

echo "mutate: engine exit $ENGINE_RC, log $RUN_LOG, report $REPORT" >&2

# One path for producer, verdict and upload, so they cannot disagree if told the same one.
if [[ -n ${GITHUB_OUTPUT:-} ]]; then
	printf 'expected_total=%s\n' "$EXPECTED_MEASURED" >>"$GITHUB_OUTPUT"
	printf 'watch_lines=%s\n' "$WATCH_LINES" >>"$GITHUB_OUTPUT"
fi

_verdict "$REPORT" "$RUN_LOG" "$ALLOWLIST" "$EXPECTED_MEASURED" "$ENGINE_RC" "$SCOPE_FILE" "$BUDGET_FILE"
rc=$?
_flush_summary "$SUMMARY" 'Mutation testing'
exit "$rc"