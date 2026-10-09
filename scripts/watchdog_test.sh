#!/usr/bin/env bash
# watchdog.sh — the supervisor's suite.
#
# The interesting cases here are not the happy path, they are the ones where
# supervising wrongly is worse than not supervising: a healthy pool cut for
# going quiet, a wedged pool never cut because its siblings keep writing, a run
# killed with its descendants still holding the worktree. Each of those has a
# case below, and each case asserts on the exit code AND on the reason line,
# because a supervisor that returns the right code for the wrong reason is
# still lying.
#
# Timings are deliberately in seconds and the poll is 1s (WATCHDOG_POLL), so the
# whole suite runs in well under a minute. The production poll is 5s and every
# number here is the same shape, just smaller.
#
# VENDORED COPY. Origin: ~/.local/share/chezmoi/home/dot_local/lib/swe/tests/
# watchdog.sh, copied here frozen together with the supervisor it covers. Only
# the paths were adapted; the cases assert the behaviour of scripts/watchdog.sh
# as committed. Diverging from chezmoi is a separate PR in the swe/chezmoi repo.
#
# Run with: bash scripts/watchdog_test.sh   (exits non-zero if any case fails)

set -uo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
# One name, no fallback: inside this repo the supervisor is always
# scripts/watchdog.sh. The chezmoi source tree needed the deployed|executable_
# dance because the file is installed under a different name than it is authored;
# that problem does not exist here and the fallback would only hide a rename.
WD="$HERE/watchdog.sh"

export WATCHDOG_POLL=1

pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'PASS  %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL  %s\n' "$1"; }
check() { # check <label> <expected> <actual>
  if [[ $2 == "$3" ]]; then ok "$1"; else bad "$1 (expected [$2] got [$3])"; fi
}
contains() { # contains <label> <needle> <haystack>
  if [[ $3 == *"$2"* ]]; then ok "$1"; else bad "$1 (missing [$2] in [$3])"; fi
}
lacks() { # lacks <label> <needle> <haystack>
  if [[ $3 != *"$2"* ]]; then ok "$1"; else bad "$1 (unexpected [$2])"; fi
}

[[ -f $WD ]] || { echo "no watchdog at $WD"; exit 1; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/gitdash-watchdog.XXXXXX")
# The supervisor's own group is killed by the PGID assertion case on purpose, so
# it has to be a group this suite can survive.
trap 'rm -rf "$tmp"' EXIT

# Run the supervisor, capturing stdout and stderr separately.
run_wd() { # run_wd <out> <err> [args...]
  local o=$1 e=$2
  shift 2
  bash "$WD" "$@" >"$o" 2>"$e"
}

# --- A. the child finishes on its own ----------------------------------------

echo "=== A. a healthy child is not disturbed ==="

# Three progress lines then a clean exit 0, spread over three polls so the
# supervisor actually has something to watch. The child's code must survive
# verbatim: a supervisor that swallows or rewrites it breaks every caller that
# branches on it.
cat > "$tmp/three.sh" <<'EOF'
for i in 1 2 3; do echo "  RUNNABLE X at f.go:$i:1"; sleep 1; done
exit 0
EOF
run_wd "$tmp/a.out" "$tmp/a.err" 30 60 3 ' at .*:[0-9]+:[0-9]+$' -- bash "$tmp/three.sh"
check "healthy run: exit" 0 $?
check "healthy run: lines survive" 3 "$(grep -cE ' at .*:[0-9]+:[0-9]+$' "$tmp/a.out")"
# The needle is the whole cut line, not the word "cut": the supervisor's own name
# contains it (exe-cut-able), so a bare "cut" matches every line it ever prints.
lacks "healthy run: never cuts" "cutting process group" "$(cat "$tmp/a.err")"

# A nonzero child code is the caller's answer about the tool, not about the
# supervisor, so it must pass through untouched.
echo 'echo "LIVED Y at f.go:9:1"; exit 3' > "$tmp/three_rc.sh"
run_wd "$tmp/b.out" "$tmp/b.err" 30 60 1 ' at .*:[0-9]+:[0-9]+$' -- bash "$tmp/three_rc.sh"
check "child exit 3 passes through" 3 $?

# The PROGRESS_RE is the caller's definition of "one unit of work", and the
# supervisor must count exactly what the caller counts — no more, no less. A
# footer line must not inflate the numerator into a false "total reached", and
# the header must not hide the fact that the numerator is not moving.
cat > "$tmp/footer.sh" <<'EOF'
echo "Starting..."
echo "  RUNNABLE X at f.go:1:1"
echo "  KILLED Y at f.go:2:1"
echo ""
echo "Killed: 2, Lived: 0"
sleep 2
EOF
run_wd "$tmp/c.out" "$tmp/c.err" 30 60 2 ' at .*:[0-9]+:[0-9]+$' -- bash "$tmp/footer.sh"
check "footer lines ignored by PROGRESS_RE: exit" 0 $?
contains "footer lines ignored: reports the ratio" "2/2" "$(cat "$tmp/c.err")"

# --- B. the child stops making progress --------------------------------------

echo
echo "=== B. a wedge is cut ==="

# The simplest wedge: one line, then nothing, forever.
cat > "$tmp/wedge.sh" <<'EOF'
echo "  RUNNABLE X at f.go:1:1"
while :; do sleep 1; done
EOF
run_wd "$tmp/d.out" "$tmp/d.err" 3 600 2 ' at .*:[0-9]+:[0-9]+$' -- bash "$tmp/wedge.sh"
check "wedge: exit 124" 124 $?
contains "wedge: names the stall" "no progress for" "$(cat "$tmp/d.err")"
contains "wedge: keeps what the child wrote" "at f.go:1:1" "$(cat "$tmp/d.out")"

# THE case the whole denominator exists for. Six workers, five of them carry on
# emitting, one wedges halfway through its quota and never comes back. Any signal
# that watches "did the output change" — bytes or lines alike — sees a healthy
# run and never fires, and the whole point of a supervisor is the run that looks
# healthy and is not.
#
# The wedged worker deliberately does NOT finish its quota. That is the faithful
# shape: the total is the enumeration, and a worker that is stuck never reports
# its remaining mutants, so the numerator legitimately stops short of the
# denominator. A fixture where the wedged worker emits everything first would
# reach the total, and reaching the total is a mode switch — the supervisor
# would be right to stop watching progress.
cat > "$tmp/pool.sh" <<'EOF'
# $1 = this worker's quota, $2 = its name.
limit=$1
[[ $2 == wedged ]] && limit=5   # its turn never comes for the rest
for i in $(seq 1 "$limit"); do
  echo "  KILLED $2 at pkg/$2.go:$i:1"
  sleep 1
done
[[ $2 == wedged ]] && while :; do sleep 1; done
EOF
cat > "$tmp/pool_main.sh" <<'EOF'
for w in w1 w2 w3 w4 w5; do bash "$1" 15 "$w" & done
bash "$1" 15 wedged &
wait
EOF
run_wd "$tmp/e.out" "$tmp/e.err" 5 600 90 ' at pkg/.*\.go:[0-9]+:[0-9]+$' -- bash "$tmp/pool_main.sh" "$tmp/pool.sh"
check "pool wedge: exit 124" 124 $?
contains "pool wedge: cut for stalled progress" "no progress for" "$(cat "$tmp/e.err")"
# Proof the siblings were still writing when it fired: without a denominator this
# is a run that looks perfectly healthy.
if [[ $(grep -cE ' at pkg/.*\.go:[0-9]+:[0-9]+$' "$tmp/e.out") -gt 5 ]]; then
  ok "pool wedge: siblings were still emitting when it fired"
else
  bad "pool wedge: siblings were not emitting, so the case proves nothing"
fi
if [[ $(grep -cE ' at pkg/.*\.go:[0-9]+:[0-9]+$' "$tmp/e.out") -lt 90 ]]; then
  ok "pool wedge: it did NOT wait for the total (that is the bug being fixed)"
else
  bad "pool wedge: ran to the total, so it cannot distinguish a wedge"
fi
# And the five healthy workers really had emptied their quotas while the sixth
# was stuck — otherwise "did not reach the total" would just mean "it gave up".
if [[ $(grep -cE ' at pkg/w[1-5]\.go:[0-9]+:[0-9]+$' "$tmp/e.out") -ge 50 ]]; then
  ok "pool wedge: the healthy workers finished their quotas first"
else
  bad "pool wedge: healthy workers did not finish, so the wedge is not isolated"
fi

# A numerator that legitimately stops SHORT of the total is not progress and not
# a wedge: it is a broken child (a mutation the engine could not even write). It
# must still be cut, and the reason must not claim a wedge.
cat > "$tmp/short.sh" <<'EOF'
echo "  RUNNABLE X at f.go:1:1"
while :; do sleep 1; done
EOF
run_wd "$tmp/f.out" "$tmp/f.err" 3 600 9 ' at .*:[0-9]+:[0-9]+$' -- bash "$tmp/short.sh"
check "short numerator: exit 124" 124 $?
contains "short numerator: names the wedge" "no progress for" "$(cat "$tmp/f.err")"
contains "short numerator: reports how far it got" "1/9" "$(cat "$tmp/f.err")"

# --- C. the ceiling ----------------------------------------------------------

echo
echo "=== C. the ceiling still applies ==="

# Keeps emitting forever, so the stall clock never fires. Only MAX can end this,
# which is the proof that the ceiling was not lost when progress detection was
# added.
cat > "$tmp/chatty.sh" <<'EOF'
for i in $(seq 1 600); do echo "  KILLED X at f.go:$i:1"; sleep 1; done
EOF
run_wd "$tmp/g.out" "$tmp/g.err" 600 4 100000 ' at .*:[0-9]+:[0-9]+$' -- bash "$tmp/chatty.sh"
check "ceiling: exit 124" 124 $?
contains "ceiling: named as the ceiling" "ceiling" "$(cat "$tmp/g.err")"
lacks "ceiling: not blamed on the stall" "no progress for" "$(cat "$tmp/g.err")"

# --- D. reaching the total is a mode switch, not a verdict -------------------

echo
echo "=== D. reaching the total does not kill a run that is about to finish ==="

# Emits everything, then spends longer than STALL writing a report in silence —
# exactly what the tail of a real run looks like. Cutting here would kill a run
# that was seconds from a clean exit, so the exit code must be the child's.
cat > "$tmp/tail.sh" <<'EOF'
for i in 1 2 3; do echo "  KILLED X at f.go:$i:1"; done
sleep 6
echo "Killed: 3, Lived: 0"
exit 0
EOF
run_wd "$tmp/h.out" "$tmp/h.err" 2 600 3 ' at .*:[0-9]+:[0-9]+$' -- bash "$tmp/tail.sh"
check "silent report tail: child exit survives" 0 $?
contains "silent report tail: announced the switch" "only the" "$(cat "$tmp/h.err")"
lacks "silent report tail: never cuts" "cutting process group" "$(cat "$tmp/h.err")"

# Same shape, but the tail never ends. Only the ceiling may end it — not the
# stall clock, which is exactly the point.
cat > "$tmp/tail_hang.sh" <<'EOF'
for i in 1 2 3; do echo "  KILLED X at f.go:$i:1"; done
while :; do sleep 1; done
EOF
run_wd "$tmp/i.out" "$tmp/i.err" 2 6 3 ' at .*:[0-9]+:[0-9]+$' -- bash "$tmp/tail_hang.sh"
check "endless tail: exit 124" 124 $?
contains "endless tail: blames the ceiling, not a wedge" "did not exit" "$(cat "$tmp/i.err")"
lacks "endless tail: not blamed on the stall" "no progress for" "$(cat "$tmp/i.err")"

# --- E. the process group ---------------------------------------------------

echo
echo "=== E. the whole group goes, including grandchildren ==="

# The direct child is a shell whose own child holds the worktree. Killing only
# the direct child is the failure mode that leaves a mutated tree behind: it is
# silent, and it is worse than losing the run. So the grandchild's survival is
# asserted directly, by pid.
cat > "$tmp/grandchild.sh" <<'EOF'
echo "  RUNNABLE X at f.go:1:1"
sleep 300 &
echo "$!" > "$1"
wait
EOF
run_wd "$tmp/j.out" "$tmp/j.err" 2 600 5 ' at .*:[0-9]+:[0-9]+$' -- bash "$tmp/grandchild.sh" "$tmp/gc.pid"
check "group kill: exit 124" 124 $?
gc=$(cat "$tmp/gc.pid" 2>/dev/null || echo 0)
if [[ $gc -gt 0 ]] && kill -0 "$gc" 2>/dev/null; then
  bad "group kill: grandchild $gc SURVIVED (the worktree would stay mutated)"
  kill -KILL "$gc" 2>/dev/null || true
else
  ok "group kill: grandchild died with the group"
fi

# Ctrl-C on the supervisor itself. Without an INT/TERM trap the whole engine
# tree keeps running against the repo while the terminal looks like it returned.
"$WD" 600 600 1 '' -- bash "$tmp/grandchild.sh" "$tmp/gc2.pid" \
  >"$tmp/k.out" 2>"$tmp/k.err" &
sup=$!
sleep 2
kill -TERM "$sup"
wait "$sup" 2>/dev/null
sup_rc=$?
check "supervisor signalled: exit 143" 143 "$sup_rc"
gc2=$(cat "$tmp/gc2.pid" 2>/dev/null || echo 0)
sleep 1
if [[ $gc2 -gt 0 ]] && kill -0 "$gc2" 2>/dev/null; then
  bad "orphan group: grandchild $gc2 SURVIVED the supervisor"
  kill -KILL "$gc2" 2>/dev/null || true
else
  ok "orphan group: grandchild died with the supervisor"
fi

# --- F. it refuses rather than pretends --------------------------------------

echo
echo "=== F. it fails loudly instead of supervising badly ==="

# A child that could not be given its own process group would be cut by a signal
# that does not reach its descendants, leaving the worktree mutated and the run
# looking handled. Degrading quietly here is what makes the whole file
# untrustworthy, so it must be a distinct failure — and distinct from 124, which
# means "cut a wedge": a caller branching on 124 must not read this as one.
# The pgid probe is replaced to force the condition, because a guard that cannot
# be provoked is a guard nobody ever finds out is broken.
# Probes that stand in for a broken `ps`. They must echo ONLY a pgid, because
# that is what the supervisor compares: a probe that echoes the pid it was handed
# produces two different "pgids" and the comparison silently means nothing.
printf '#!/usr/bin/env bash\nprintf 1\n' > "$tmp/probe_same.sh"   # never varies
printf '#!/usr/bin/env bash\n:\n' > "$tmp/probe_none.sh"          # answers nothing
# A probe slow enough that a child which exits immediately is already reaped by
# the time it answers — which is the only way to provoke the finished-before-
# supervised path deterministically.
printf '#!/usr/bin/env bash\nsleep 1\n' > "$tmp/probe_slow.sh"
chmod +x "$tmp"/probe_*.sh

out=$(WATCHDOG_PGID_PROBE="bash $tmp/probe_same.sh" bash "$WD" 2 60 1 '' -- sleep 30 2>&1)
rc=$?
check "shared pgid: not 124" "!=124" "$([[ $rc -eq 124 ]] && echo '=124' || echo '!=124')"
check "shared pgid: nonzero" "!=0" "$([[ $rc -eq 0 ]] && echo '=0' || echo '!=0')"
contains "shared pgid: says why" "own process group" "$out"

# An unreadable pgid on a LIVE child is the same class of problem: it cannot be
# supervised, so it must not pretend to have been.
out=$(WATCHDOG_PGID_PROBE="bash $tmp/probe_none.sh" bash "$WD" 2 60 1 '' -- sleep 30 2>&1)
rc=$?
check "unreadable pgid: nonzero" "!=0" "$([[ $rc -eq 0 ]] && echo '=0' || echo '!=0')"
contains "unreadable pgid: refuses" "refusing to run unsupervised" "$out"

# A child that finishes before it can be probed is NOT that case: there is no
# tree left to leak and no group left to signal, so the run is passed straight
# through. Without this, a command that dies on a bad flag would be reported as a
# supervision failure instead of its own error.
out=$(WATCHDOG_PGID_PROBE="bash $tmp/probe_slow.sh" bash "$WD" 2 60 1 '' -- bash -c 'exit 7' 2>&1)
rc=$?
check "already-exited child: code passes through" 7 "$rc"
lacks "already-exited child: no cut claimed" "cutting process group" "$out"

# A refusal must not orphan what it refused to supervise. Running is the thing
# being asserted, not existence: the supervisor signals the group but never reaps
# it on the refusal path, so what remains is a zombie, which is inert.
WATCHDOG_PGID_PROBE="bash $tmp/probe_same.sh" bash "$WD" 2 60 1 '' -- sleep 30 >/dev/null 2>&1 &
leak=$!
sleep 1
still_running=0
for c in $(pgrep -P "$leak" 2>/dev/null); do
  [[ $(ps -o stat= -p "$c" 2>/dev/null) == R* ]] && still_running=1
done
wait "$leak" 2>/dev/null
check "shared pgid: child left not running" 0 "$still_running"

# Usage errors are 2, and they are checked before anything is launched, so a
# typo in the limits cannot leave a child running unsupervised.
bash "$WD" >/dev/null 2>&1
check "no args: exit 2" 2 $?
check "no args: says what is missing" "missing STALL" \
  "$(bash "$WD" 2>&1 >/dev/null | head -1 | sed 's/.*: //')"
run_wd "$tmp/l.out" "$tmp/l.err" 30 60 0 '' --
check "no command: exit 2" 2 $?
contains "no command: prints usage" "usage:" "$(cat "$tmp/l.err")"
run_wd "$tmp/m.out" "$tmp/m.err" 30 60 'abc' '' -- true
check "bad TOTAL: exit 2" 2 $?

# --- G. no denominator is still useful ---------------------------------------

echo
echo "=== G. TOTAL=0 degrades instead of breaking ==="

# For a tool that cannot enumerate itself there is no denominator, and the
# honest degradation is the lines-only signal — strictly better than bytes, and
# it must still cut and still pass codes through.
run_wd "$tmp/n.out" "$tmp/n.err" 3 600 0 '' -- bash "$tmp/wedge.sh"
check "no total: still cuts" 124 $?
contains "no total: still names the stall" "no progress for" "$(cat "$tmp/n.err")"
contains "no total: says it has no denominator" "lines-only" "$(cat "$tmp/n.err")"
run_wd "$tmp/o.out" "$tmp/o.err" 30 60 0 '' -- bash "$tmp/three.sh"
check "no total: healthy run passes" 0 $?

echo
printf '%d/%d passed\n' "$pass" "$((pass + fail))"
[[ $fail -eq 0 ]]
