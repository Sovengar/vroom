#!/usr/bin/env bash
# diff-coverage.sh — coverage of the lines this PR TOUCHES.
# Why the diff and not the total: a threshold on the project total is a number no PR can move locally (three tested lines take you to 99.9%, a hundred untested lines drop you to 97%), the first is almost always noise and the second almost always a bug, and a gate that cannot tell them apart serves neither.
# The diff inverts it: it looks ONLY at the changed lines and demands 100% there, so whoever touches the code has to test it and old untouched code cannot block a PR; it is the metric Google calls changelist coverage (Ivankovic et al., "Code Coverage at Google", FSE 2019: 14M changelist measurements over a trillion lines) and the one shown in code review to author and reviewers.
#
# Usage:  diff-coverage.sh <profile.out> [base] [--min N] [-a extra.profile]
#   profile.out    output of go test -coverprofile
#   base           ref to compare against (default: MUTATE_BASE, then main)
#   --min N         minimum diff percentage (default 100)
#   -a profile     extra profile to merge (a subprocess's counters; also settable
#                  with DIFF_COVERAGE_EXTRA, space separated)
#
# Output: per-file table + summary; exit 1 if the diff falls below the minimum,
# exit 2 if the calculation cannot be done.
set -euo pipefail

PROFILE="${1:?missing coverage profile}"
shift || true

BASE="${MUTATE_BASE:-main}"
MIN=100
EXTRA_ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --min) MIN="${2:?--min needs a number}"; shift 2 ;;
    --min=*) MIN="${1#--min=}"; shift ;;
    -a) EXTRA_ARGS+=("${2:?-a needs a profile}"); shift 2 ;;
    -a*) EXTRA_ARGS+=("${1#-a}"); shift ;;
    *) BASE="$1"; shift ;;
  esac
done

if [ ! -f "$PROFILE" ]; then
  echo "diff-coverage: the profile '$PROFILE' does not exist" >&2
  exit 2
fi

# The profile has one block per test binary: the same range appears N times, one with count>0 (the binary owning the package) and the rest at 0 (the others instrument but do not exercise); deduplicating by range keeping the MAX count is what turns the profile into a measurement, since without it this very repo would read 10% coverage at 98%.
python3 - "$PROFILE" "$BASE" "$MIN" "${EXTRA_ARGS[@]}" <<'PY'
import re, subprocess, sys, os
from collections import defaultdict

profile, base, min_pct = sys.argv[1], sys.argv[2], float(sys.argv[3])
# Extra profiles arrive unfiltered by existence: one that does not exist has to be an error and not silently ignored (which would let the gate pass without it, the exact thing the extra profile exists to prevent).
argv_extra = [a for a in sys.argv[4:] if not a.startswith("-")]

# `go test -coverprofile` does NOT collect a SUBPROCESS's counters, and vroom's main() does run in one: cmd/vroom/main_process_test.go forwards the whole environment so GOCOVERDIR reaches the child and its counters land in the parent's profile. Measured: 3173/3173 statements, 100.00%. The `-a` flag remains for a profile produced outside the suite, and it is merged by MAX count over the same range, the same rule as the main profile's duplicates: two measurements of the same block, the bigger one wins; adding them would give a meaningless number.
extra_profiles = argv_extra + os.environ.get("DIFF_COVERAGE_EXTRA", "").split()
all_profiles = [profile] + extra_profiles

# The prefix comes from go.mod and not from a constant: if the module were renamed, a hardcoded prefix would leave every path unmatched and the script would report "no lines touched", which is a silent pass for the whole PR.
prefix = ""
try:
    with open("go.mod") as fh:
        for l in fh:
            if l.startswith("module "):
                prefix = l.split(None, 1)[1].strip()
                break
except OSError:
    pass

blocks = defaultdict(dict)   # file -> {(sl,sc,el,ec): count}
for prof in all_profiles:
    if not os.path.exists(prof):
        print(f"diff-coverage: the extra profile does not exist: {prof}", file=sys.stderr)
        sys.exit(2)
    with open(prof) as fh:
        for line in fh:
            if line.startswith("mode:"):
                continue
            rng, n, c = line.rsplit(" ", 2)
            m = re.match(r"^(.*):(\d+)\.(\d+),(\d+)\.(\d+)$", rng)
            if not m:
                continue
            f = m.group(1)
            if prefix and f.startswith(prefix + "/"):
                f = f[len(prefix) + 1:]
            key = (int(m.group(2)), int(m.group(3)), int(m.group(4)), int(m.group(5)))
            prev = blocks[f].get(key, -1)
            if int(c) > prev:
                blocks[f][key] = int(c)

total_stmt = sum(len(v) for v in blocks.values())
covered = sum(1 for v in blocks.values() for c in v.values() if c > 0)
pct = 100.0 * covered / total_stmt if total_stmt else 0.0

# `git diff -U0` gives the hunks without context, so a modified line counts and the ones around it do not.
def changed_lines(path):
    try:
        out = subprocess.run(
            ["git", "diff", "-U0", "--diff-filter=ACMRT", f"{base}...HEAD", "--", path],
            capture_output=True, text=True, check=False).stdout
    except FileNotFoundError:
        return None
    if not out.strip():
        return None
    added = set()
    for hunk in re.finditer(r"^@@ -\S+ \+(\d+)(?:,(\d+))? @@", out, re.M):
        start, count = int(hunk.group(1)), int(hunk.group(2) or 1)
        if count == 0:
            continue          # deletions only: they do not exist, they are not coverable
        added.update(range(start, start + count))
    return added or None

# A touched line is covered when it belongs to a block with count>0, and a multi-line block marks all of them: in Go a block is a group of statements and if one of the group ran, the group ran.
# And a touched line only counts if it is ALSO in some block (count>0 or not): `git diff` cannot tell a statement from a comment, a closing `}` or an import line, and counting those as "uncovered" would make a file with 60 comment lines and 3 statements read 5% and the gate unreachable no matter how many tests are written.
diff_total = diff_cov = 0
per_file = defaultdict(lambda: [0, 0])   # file -> [total, covered]
uncovered = []

for f, bs in sorted(blocks.items()):
    touched = changed_lines(f)
    if not touched:
        continue
    # Statement lines per the profile itself: the only source of truth on what is executable.
    ejecutable = {ln for (sl, sc, el, ec) in bs for ln in range(sl, el + 1)}
    touched = touched & ejecutable
    if not touched:
        continue
    hit = set()
    for (sl, sc, el, ec), c in bs.items():
        if c <= 0:
            continue
        for ln in touched:
            if sl <= ln <= el:
                hit.add(ln)
    # Each touched line counts ONCE however many blocks touch it: without this a line on the border of three blocks counts triple and the diff percentage stops being a percentage of lines.
    for ln in touched:
        per_file[f][0] += 1
    for ln in hit:
        per_file[f][1] += 1
    for ln in sorted(touched - hit):
        uncovered.append(f"{f}:{ln}")

diff_total = sum(v[0] for v in per_file.values())
diff_cov = sum(v[1] for v in per_file.values())

print(f"## Coverage\n")
print(f"**Project total: {pct:.2f}%** ({covered}/{total_stmt} statements)")
print(f"**Diff vs {base}: "
      + (f"{(100.0 * diff_cov / diff_total if diff_total else 100.0):.2f}%** ({diff_cov}/{diff_total} lines)"
         if diff_total else "no .go lines touched**") + "")
print()

# The floor's red has to be actionable: name the blocks that are still uncovered. A single
# number is a red nobody can act on, and this is the list that says which test to write.
project_uncovered = sorted(
    f"{f}:{sl}-{el}"
    for f, bs in blocks.items()
    for (sl, sc, el, ec), c in bs.items() if c <= 0
)
if project_uncovered:
    shown = project_uncovered[:25]
    print(f"<details><summary>Uncovered blocks ({len(project_uncovered)})</summary>\n")
    for s in shown:
        print(f"- `{s}`")
    if len(project_uncovered) > len(shown):
        print(f"- … and {len(project_uncovered) - len(shown)} more")
    print("\n</details>\n")

if per_file:
    print("| File | Diff |")
    print("| --- | --- |")
    for f, (t, c) in sorted(per_file.items()):
        print(f"| `{f}` | {100.0 * c / t:.1f}% ({c}/{t}) |")
    print()

if uncovered:
    print("<details><summary>Diff lines not covered "
          f"({len(uncovered)})</summary>")
    print()
    for s in uncovered:
        print(f"- `{s}`")
    print()
    print("</details>")
    print()

# Two different gates, hence two different numbers: the DIFF at 100% (whoever touches the code has to test it, old uncovered lines cannot block a PR) and the TOTAL with a floor (the diff only looks at what is new, so a PR can add untested code to an already covered file without the diff noticing, and the floor is what keeps the whole from degrading, being able to only go up).
# The floor lives in a repo file and not in the workflow: if it is an exception it gets a name, is committed and shows up in the diff; here the floor is only BOUGHT (the total never drops below it) and a higher total WARNS, so raising it stays a decision.
diff_pct = 100.0 * diff_cov / diff_total if diff_total else 100.0
failures = []

if diff_pct < min_pct:
    failures.append(f"the diff stops at {diff_pct:.2f}%, below the requested {min_pct:.0f}%")

baseline_file = "scripts/coverage-floor"
floor = None
if os.path.exists(baseline_file):
    # The floor file carries an explanatory comment on top, so the number is the LAST non-empty non-comment line; reading the first word would give "#".
    with open(baseline_file) as fh:
        for l in fh:
            l = l.strip()
            if l and not l.startswith("#"):
                floor = float(l)
                break
    if floor is None:
        print(f"diff-coverage: {baseline_file} has no number", file=sys.stderr)
        sys.exit(2)
    # The comparison uses the SAME precision the floor is printed with and the total is printed with: comparing the raw float against a two-decimal floor fails the gate over 0.001 (with 1929/1960 the total is 98.11800610376399, prints as "98.12" against a "98.12" floor, and 98.118 < 98.12); the floor is a number you read, not a float.
    if round(pct, 2) < round(floor, 2):
        failures.append(f"the total drops to {pct:.2f}%, below the floor of {floor:.2f}% "
                      f"(raise the tests, do not lower the floor; if the floor is wrong, "
                      f"fix scripts/coverage-floor in the SAME commit)")
    elif round(pct, 2) > round(floor, 2):
        print(f"warning: the total is {pct:.2f}% and the floor is {floor:.2f}%. "
              f"Raise scripts/coverage-floor to {pct:.2f} in this commit so "
              f"the ratchet does not go stale.", file=sys.stderr)
else:
    print(f"warning: {baseline_file} is missing; the total floor is not checked",
          file=sys.stderr)

for f in failures:
    print(f"diff-coverage: {f}", file=sys.stderr)

if failures:
    print(f"diff: {diff_pct:.2f}% (minimum {min_pct:.0f}%) · "
          f"total: {pct:.2f}% (floor {floor if floor is not None else 'none'})")
    sys.exit(1)
print(f"diff-coverage: ok (diff {diff_pct:.2f}%, total {pct:.2f}%)")
PY