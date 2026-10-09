#!/usr/bin/env bash
# watchdog.sh — supervise a command and cut it when it STOPS MAKING PROGRESS,
# not only when it runs out of wall clock.
#
# `timeout N` is a ceiling, and a ceiling does not shorten a wedge: a run that
# hangs at minute two still occupies the machine for the full N. What this
# supervises is PROGRESS. The signal is the number of progress lines measured
# against a KNOWN TOTAL, because a wedged worker inside a pool is invisible to
# any "did the file grow" test — its siblings keep writing, so the file grows,
# the clock resets, and the supervisor never fires. That is the failure this
# script exists to prevent, and it is the most likely one.
#
# It lives in the repo because the mutation gate runs on a GitHub runner with no
# dotfiles tree: scripts/mutate.sh invokes this copy from the `Mutation` job in
# .github/workflows/ci.yml, and scripts/mutate_test.sh asserts its behaviour.
#
# Two contract notes, both load-bearing:
#
#   * Progress is counted in LINES, never in bytes. Bytes answer "is it
#     writing"; lines answer "is it advancing"; only lines against a
#     denominator can tell a healthy pool from a wedged one. A progress bar
#     that repaints with \r inflates bytes without ever emitting a line.
#   * The wrapped command must write UNBUFFERED. This script cannot enforce it:
#     stdbuf acts on C stdio through LD_PRELOAD and a Go program ignores it
#     entirely, so forcing it here would be theatre. A block-buffered child
#     (Python without PYTHONUNBUFFERED=1) looks wedged for reasons that have
#     nothing to do with progress, and does so non-reproducibly. The caller
#     owns this.
#
# Reaching TOTAL is not finishing. After the last progress line the command
# still has to tear down and write its report, and none of that prints. So once
# the total is reached the stall clock stops and only the absolute ceiling keeps
# running: a metric's endpoint is not the process's endpoint, and the only
# finished signal is process exit.
#
# Coupling: this script is only harmless because the command it was written for
# does NOT mutate the tree in place — it copies the source to a temp dir per
# worker (workdir.CachedDealer) and mutates the copy. If that ever changes,
# killing a run leaves a mutated working environment behind and this script
# stops being harmless.
#
# Usage: watchdog.sh STALL MAX TOTAL PROGRESS_RE -- cmd args...
#   STALL        seconds without a new progress line before the child counts as
#                wedged. Bare number, or a 30s/5m/2h suffix.
#   MAX          absolute ceiling, same formats.
#   TOTAL        expected number of progress lines. 0 means "unknown", which
#                degrades to the lines-only signal (correct for a tool that
#                cannot enumerate itself).
#   PROGRESS_RE  an ERE matching ONE progress line; empty matches every line.
#                Engine-specific, and owned by the caller: the SAME expression
#                must be used to produce TOTAL, or the denominator counts
#                something the numerator does not.
#
# Exit: the child's own code, or 124 when the child was cut — stall, ceiling, or
# post-total tail. Codes are for machines; the reason is the last line on
# stderr, before the captured output, for people. 130/143 mean the supervisor
# itself was signalled, which is the user's Ctrl-C, not a decision of this
# script. Callers MUST pass those through untranslated: they mean the run was
# cancelled, not that mutation produced a verdict.
#
# Note: the EXIT trap does not run on SIGKILL, so the temp file's removal is not
# guaranteed. Nothing depends on it surviving.

set -u

# Seconds, or a number with a 30s/5m/2h suffix: a bare integer is how a ceiling
# reads, a suffixed one is how a margin is thought about.
_wd_to_secs() {
	case $1 in
	*s) echo $((${1%s})) ;;
	*m) echo $((${1%m} * 60)) ;;
	*h) echo $((${1%h} * 3600)) ;;
	*) echo "$1" ;;
	esac
}

# One poll interval. It is the resolution of "how long has it been wedged", so it
# bounds how wrong the reported stall age can be, and nothing else depends on it.
# Overridable because a caller working in seconds rather than minutes needs a
# finer poll, and because a test suite that waits 5s per case waits 5s per case.
: "${WATCHDOG_POLL:=5}"

# stdout of the child is redirected to this file, which is why an ERE is a
# usable definition of a progress line: with stdout not a terminal, the engine
# under it emits neither ANSI colour (fatih/color probes isatty) nor CRLF (that
# is the terminal line discipline, not the writer). The expression is still
# written to tolerate trailing whitespace so it does not silently depend on
# that remaining true.
_wd_progress_count() { # <file> <ere>
	# grep -c prints 0 AND exits 1 when nothing matched, so the status is
	# discarded explicitly rather than relied upon.
	grep -cE -- "$2" "$1" 2>/dev/null || true
}

# The process group of a pid, as a bare number. `ps` is asked for the pgid rather
# than guessed from the pid: the whole design assumes the child got its OWN group,
# and the one thing worth asserting is that it did.
#
# WATCHDOG_PGID_PROBE replaces the probe and must echo ONLY the pgid (the pid
# arrives as an argument, and whatever the probe prints is compared verbatim —
# padding or an echoed pid makes the comparison meaningless). It exists because
# `ps` output is not portable, BSD and busybox disagree, and because a guard that
# cannot be provoked is a guard nobody ever finds out is broken.
_wd_pgid_of() { # <pid>
	if [[ -n ${WATCHDOG_PGID_PROBE:-} ]]; then
		# Deliberately unquoted: the probe is a command, possibly with arguments.
		# shellcheck disable=SC2086
		$WATCHDOG_PGID_PROBE "$1" 2>/dev/null | tr -d '[:space:]'
	else
		ps -o pgid= -p "$1" 2>/dev/null | tr -d '[:space:]'
	fi
}

# Cut the child's whole process group. Set -m gave the child its own group, so
# the pgid is the child's pid and the signal reaches the grandchildren (`go test`
# under a mutation engine) that hold the working environment. There is
# deliberately NO
# fallback to killing the bare pid: if the group cannot be killed, killing only
# the direct child leaves the grandchildren running against a mutated tree, and
# that failure is silent in the worst direction — a lost run is worth more than a
# failed one. A run that cannot be cut cleanly must be a loud run.
_wd_kill_group() { # <pid> <signal>
	# wd_child_pgid is empty exactly when the child was never supervised (it had
	# already exited), i.e. there is no group left to signal. Succeed, so callers
	# do not each have to branch on it.
	[[ -z ${1:-} ]] && return 0
	kill "-$2" -- "-$1" 2>/dev/null
}

swe_watchdog() { # <stall> <max> <total> <progress_re> -- cmd...
	local stall max total re
	stall=$(_wd_to_secs "${1:?missing STALL}") || return 2
	max=$(_wd_to_secs "${2:?missing MAX}") || return 2
	total=${3:-0}
	re=${4:-}
	shift 4 2>/dev/null || shift $#
	[[ ${1:-} == "--" ]] && shift
	[[ $# -gt 0 ]] || {
		echo "usage: ${0##*/} STALL MAX TOTAL PROGRESS_RE -- cmd..." >&2
		return 2
	}
	[[ $total =~ ^[0-9]+$ ]] || {
		echo "${0##*/}: TOTAL must be a non-negative integer, got '$total'" >&2
		return 2
	}
	[[ $stall =~ ^[0-9]+$ && $max =~ ^[0-9]+$ ]] || {
		echo "${0##*/}: STALL and MAX must resolve to non-negative integers" >&2
		return 2
	}

	# Everything the EXIT trap needs is a global on purpose. A `local` would be
	# unbound by the time the trap runs — the trap fires when the shell exits,
	# which is after this function has returned — and under `set -u` that turns
	# cleanup into a hard error, i.e. the exact opposite of cleaning up.
	wd_out=$(mktemp) || return 2

	# The supervisor's own group. Read BEFORE the child exists: after `set -m`
	# the script's own $$ must never become the thing we signal.
	local self_pgid
	self_pgid=$(_wd_pgid_of $$)

	wd_pid=''
	wd_child_done=0
	wd_child_pgid=''
	_wd_cleanup() {
		# Ctrl-C on the supervisor must not leave the whole engine tree running
		# against the repo. wd_child_done keeps this from firing after a clean
		# exit; an empty wd_child_pgid means the child was already gone before it
		# was supervised.
		if [[ $wd_child_done -eq 0 && -n $wd_pid && -n $wd_child_pgid ]]; then
			_wd_kill_group "$wd_pid" TERM
		fi
		rm -f "$wd_out" 2>/dev/null || true
	}
	trap _wd_cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM

	# `set -m` is a builtin, so the child gets its own process group on macOS
	# too, where `setsid` does not exist. Verified: child pgid == child pid,
	# shell pgid different, and a grandchild inherits the child's pgid — which is
	# what makes the group signal reach it.
	set -m
	"$@" >"$wd_out" 2>&1 &
	wd_pid=$!
	set +m

	local child_pgid
	child_pgid=$(_wd_pgid_of "$wd_pid")
	if [[ -z $child_pgid ]]; then
		# Job control reaps a finished child, so a command that exits before the
		# probe runs is legitimately absent from the process table. There is no
		# tree left to leak and nothing to signal, so this is not a supervision
		# failure — it is a run that finished before it was supervised. Reap it
		# and hand back its code.
		if ! kill -0 "$wd_pid" 2>/dev/null; then
			local rc=0
			wait "$wd_pid" || rc=$?
			wd_child_done=1
			cat "$wd_out"
			return "$rc"
		fi
		# Still alive and still no readable pgid: it cannot be supervised, so it
		# must not pretend to have been.
		echo "${0##*/}: cannot read the child's pgid (pid $wd_pid); refusing to run unsupervised" >&2
		return 2
	fi
	wd_child_pgid=$child_pgid
	if [[ $child_pgid == "$self_pgid" ]]; then
		# Signalling this group would signal the supervisor and every sibling it
		# shares, so the cut below would be a no-op at best. Degrade loudly.
		echo "${0##*/}: child pgid $child_pgid == supervisor pgid; the child could not be" \
			"given its own process group, so killing it would leave its descendants" \
			"running. Refusing to pretend the run is supervised." >&2
		return 2
	fi

	printf '%s: supervising pid %d (pgid %s) — stall %ds, ceiling %ds' \
		"${0##*/}" "$wd_pid" "$child_pgid" "$stall" "$max" >&2
	if [[ $total -gt 0 ]]; then
		printf ', expecting %d progress lines' "$total" >&2
	else
		printf ', no total (lines-only)' >&2
	fi
	printf '\n' >&2

	local start now last_advance processed prev=0 verdict='' phase=progress first=1
	start=$(date +%s)
	last_advance=$start

	while kill -0 "$wd_pid" 2>/dev/null; do
		sleep "$WATCHDOG_POLL"
		now=$(date +%s)
		processed=$(_wd_progress_count "$wd_out" "$re")

		if [[ $processed -ne $prev ]]; then
			prev=$processed
			last_advance=$now
		fi

		# The one line per poll that answers the question you actually have at
		# minute 40: is it working, and how far along.
		if [[ $total -gt 0 ]]; then
			printf '%s: alive +%ds  %d/%d\n' "${0##*/}" "$((now - start))" "$processed" "$total" >&2
		else
			printf '%s: alive +%ds  %d lines\n' "${0##*/}" "$((now - start))" "$processed" >&2
		fi

		if [[ $first -eq 1 ]]; then
			first=0
			# "Never wrote" and "stopped writing" are different failures with
			# different causes — buffering, a prompt, a missing flag — and
			# collapsing them makes the report useless. Name this one. Note that
			# an engine in its coverage phase is legitimately at zero lines
			# (a partial line with no newline yet), so this says what to look at,
			# not what is wrong.
			if [[ $processed -eq 0 ]]; then
				printf '%s:   nothing yet after %ds — buffered output, or waiting for input\n' \
					"${0##*/}" "$((now - start))" >&2
			fi
		fi

		# >= and not ==: a command emitting more matching lines than the caller
		# predicted must not wedge the supervisor on an unreachable equality.
		#
		# This is a MODE SWITCH, not a verdict. Cutting here would kill a run
		# that is seconds from a clean exit and is only doing its report. Past
		# the total there is no progress left to watch, so the stall clock stops
		# and the absolute ceiling takes over alone.
		if [[ $total -gt 0 && $processed -ge $total && $phase == progress ]]; then
			phase=tail
			printf '%s:   %d/%d reached — only the %ds ceiling applies from here\n' \
				"${0##*/}" "$processed" "$total" "$max" >&2
		fi

		if [[ $phase == progress ]]; then
			if [[ $((now - last_advance)) -ge $stall ]]; then
				verdict="no progress for $((now - last_advance))s"
				break
			fi
			printf '%s:   stall in %ds\n' "${0##*/}" "$((stall - (now - last_advance)))" >&2
		fi

		if [[ $((now - start)) -ge $max ]]; then
			if [[ $phase == tail ]]; then
				verdict="reached $processed/$total but did not exit within the ${max}s ceiling"
			else
				verdict="${max}s ceiling"
			fi
			break
		fi
	done

	if [[ -n $verdict ]]; then
		printf '%s: %s -> cutting process group %s\n' \
			"${0##*/}" "$verdict" "$child_pgid" >&2
		_wd_kill_group "$wd_pid" TERM || true
		sleep 2
		_wd_kill_group "$wd_pid" KILL || true
		# Reap, so the group is really gone before the temp file is read and the
		# caller is told 124.
		wait "$wd_pid" 2>/dev/null || true
		wd_child_done=1
		cat "$wd_out"
		printf '%s: cut — %s\n' "${0##*/}" "$verdict" >&2
		return 124
	fi

	local rc=0
	wait "$wd_pid" || rc=$?
	wd_child_done=1
	cat "$wd_out"
	return "$rc"
}

# Command-only here: inside the repo there is no second consumer for the function,
# so the dual-mode guard that chezmoi needs (swe_source_once) is dead weight.
swe_watchdog "$@"
exit $?