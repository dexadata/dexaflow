#!/usr/bin/env bash
# Refuse a release from a commit whose required checks are not green.
#
# The rule (RELEASING.md, "Red CI blocks a release"): no release candidate or
# GA is tagged on a commit of main or of a release-X.Y branch while one of
# that branch's required status checks failed, never ran, or is still
# running. Before this, the cut judged only the prepare PR's merge commit,
# about an hour into the cut, and a tag pushed by hand was judged by nothing:
# main was red three times on 2026-11-05 and nothing stopped a cut from
# starting on it.
#
# Required means what the branch's ruleset requires (the
# required_status_checks rule), read from the API on every call, so the gate
# and the merge queue never disagree about which checks matter. A check that
# is not required and is red is reported, not judged here: the cut's merge
# commit gate (wait_sha_green) still requires every run green after the
# transient reruns, and only --allow-red=<reason> passes it.
#
# How one required check is judged on a commit. A commit carries several runs
# of the same check: the merge queue's run (merge_group), the push run of the
# branch, and reruns. On main the push run skips the heavy jobs because the
# queue already ran them, so a skipped run is not a verdict. The newest run
# that was not skipped decides:
#
#   success, neutral                 pass
#   failure, cancelled, timed_out,   red: the check did not pass on this
#   action_required, startup_failure,     commit, whatever the reason
#   stale
#   queued, in_progress              pending: wait
#   no run, or only skipped runs     missing: wait (the push run may not have
#                                    registered yet), red at the deadline
#
# Usage:
#   scripts/release-ci-gate.sh <sha> <branch> [--wait <seconds>]
#   scripts/release-ci-gate.sh --self-test
#
# Exit status: 0 every required check passed, 1 a required check is red,
# 2 not decided (pending or missing at the deadline, or the API unreadable).
# RELEASE_CI_REQUIRED (one check name per line) replaces the ruleset read,
# for tests and for a branch without a ruleset.
set -euo pipefail

REPO="${RELEASE_CI_REPO:-dexadata/dexaflow}"

# ---- pure logic (unit-testable, no network) --------------------------------

# check_states <required-json> <check-runs-json>: one line per required check,
# "<state>\t<name>\t<detail>", state one of pass, red, pending, missing.
# <check-runs-json> is an array of check runs as the commits/<sha>/check-runs
# API returns them (name, status, conclusion, started_at, id, html_url).
check_states() {
	jq -rn --argjson req "$1" --argjson runs "$2" '
		$req[] as $name
		| [$runs[] | select(.name == $name)
		   | select((.status == "completed" and .conclusion == "skipped") | not)]
		| sort_by([(.started_at // ""), (.id // 0)])
		| if length == 0 then "missing\t\($name)\tno run that was not skipped"
		  else last as $r
		  | if $r.status != "completed" then "pending\t\($name)\t\($r.status)"
		    elif ($r.conclusion == "success" or $r.conclusion == "neutral") then "pass\t\($name)\t\($r.conclusion)"
		    elif ($r.conclusion // "") == "" then "pending\t\($name)\tno conclusion yet"
		    else "red\t\($name)\t\($r.conclusion) \($r.html_url // "")"
		    end
		  end'
}

# verdict <states>: GREEN, RED, PENDING or MISSING for the output of
# check_states. RED outranks the rest: a failed required check does not
# become green by waiting. An empty input is MISSING, never GREEN.
verdict() {
	local s="$1"
	[ -n "$s" ] || { echo MISSING; return; }
	if grep -q '^red' <<<"$s"; then echo RED
	elif grep -q '^pending' <<<"$s"; then echo PENDING
	elif grep -q '^missing' <<<"$s"; then echo MISSING
	else echo GREEN; fi
}

# other_red <required-json> <check-runs-json>: the checks that are not
# required and whose newest run that was not skipped did not pass, one name
# per line. Reported as a warning: the merge commit gate of the cut judges
# them.
other_red() {
	jq -rn --argjson req "$1" --argjson runs "$2" '
		[$runs[] | select(.name as $n | $req | index($n) | not)
		 | select((.status == "completed" and .conclusion == "skipped") | not)]
		| group_by(.name)[]
		| sort_by([(.started_at // ""), (.id // 0)]) | last
		| select(.status == "completed")
		| select(.conclusion != "success" and .conclusion != "neutral")
		| "\(.name) (\(.conclusion))"'
}

# ---- network ---------------------------------------------------------------

required_checks() { # <branch>
	if [ -n "${RELEASE_CI_REQUIRED:-}" ]; then
		jq -Rn '[inputs | select(length > 0)]' <<<"$RELEASE_CI_REQUIRED"
		return
	fi
	gh api "repos/$REPO/rules/branches/$1" \
		--jq '[.[] | select(.type == "required_status_checks") | .parameters.required_status_checks[].context] | unique'
}

# check_runs <sha>: every check run of the commit, as one JSON array. Pages
# by number rather than with --paginate, whose next links name the repository
# by numeric id, a form some API proxies refuse. A release commit carries
# about 140 runs, so the loop stops well before its cap of 10 pages.
check_runs() { # <sha>
	local page=1 out="" n
	while [ "$page" -le 10 ]; do
		local chunk
		chunk="$(gh api "repos/$REPO/commits/$1/check-runs?per_page=100&page=$page" \
			--jq '[.check_runs[] | {name, status, conclusion, started_at, id, html_url}]')" || return 1
		out="$out$chunk"
		n="$(jq 'length' <<<"$chunk")"
		[ "$n" -lt 100 ] && break
		page=$((page + 1))
	done
	jq -s 'add // []' <<<"$out"
}

main() {
	local sha="" branch="" wait=0
	while [ $# -gt 0 ]; do
		case "$1" in
		--self-test) self_test; exit $? ;;
		--wait) wait="${2:-}"; shift ;;
		-*) echo "unknown flag: $1" >&2; exit 2 ;;
		*) if [ -z "$sha" ]; then sha="$1"; else branch="$1"; fi ;;
		esac
		shift
	done
	[ -n "$sha" ] && [ -n "$branch" ] && [[ "$wait" =~ ^[0-9]+$ ]] || {
		echo "usage: release-ci-gate.sh <sha> <branch> [--wait <seconds>]" >&2
		exit 2
	}

	local req
	req="$(required_checks "$branch" 2>/dev/null)" || req=""
	if [ -z "$req" ] || [ "$(jq 'length' <<<"$req" 2>/dev/null || echo 0)" = 0 ]; then
		echo "release-ci-gate: could not read the required checks of $branch (no ruleset, or the API failed): nothing was judged" >&2
		exit 2
	fi

	local start=$SECONDS runs states v
	while :; do
		runs="$(check_runs "$sha" 2>/dev/null)" || runs=""
		if [ -n "$runs" ] && states="$(check_states "$req" "$runs" 2>/dev/null)"; then
			v="$(verdict "$states")"
		else
			v=UNREADABLE
		fi
		case "$v" in
		GREEN | RED) break ;;
		esac
		[ $((SECONDS - start)) -lt "$wait" ] || break
		sleep 30
	done

	local name
	if [ -n "$runs" ]; then
		while IFS= read -r name; do
			[ -n "$name" ] && echo "release-ci-gate: warning: ${sha:0:8} has a red check that $branch does not require: $name" >&2
		done < <(other_red "$req" "$runs" 2>/dev/null || true)
	fi
	case "$v" in
	GREEN)
		echo "release-ci-gate: every required check of $branch passed on ${sha:0:8} ($(jq 'length' <<<"$req") checks)"
		exit 0 ;;
	RED)
		echo "release-ci-gate: ${sha:0:8} is red on $branch, a release from it is refused. Required checks that did not pass:" >&2
		grep '^red' <<<"$states" | cut -f2- | sed 's/^/  /' >&2
		echo "Fix the branch (a new commit that turns it green), or rerun a transient failure with gh run rerun <id> --failed, then cut again." >&2
		exit 1 ;;
	PENDING | MISSING)
		echo "release-ci-gate: ${sha:0:8} is not finished on $branch after ${wait}s; required checks not yet passed:" >&2
		grep -E '^(pending|missing)' <<<"$states" | cut -f1,2 | sed 's/^/  /' >&2
		exit 2 ;;
	*)
		echo "release-ci-gate: could not read the check runs of ${sha:0:8}: nothing was judged" >&2
		exit 2 ;;
	esac
}

self_test() {
	local fail=0
	_eq() { [ "$1" = "$2" ] || { echo "FAIL: $3: got '$1', want '$2'"; fail=1; }; }
	local req='["Lint","Test"]'
	_run() { # name status conclusion started id
		printf '{"name":"%s","status":"%s","conclusion":%s,"started_at":"%s","id":%s,"html_url":"u%s"}' \
			"$1" "$2" "$3" "$4" "$5" "$5"
	}
	local ok_l ok_t
	ok_l="$(_run Lint completed '"success"' 2026-11-05T10:00:00Z 1)"
	ok_t="$(_run Test completed '"success"' 2026-11-05T10:00:00Z 2)"

	_eq "$(verdict "$(check_states "$req" "[$ok_l,$ok_t]")")" GREEN "both required checks passed"
	_eq "$(verdict "$(check_states "$req" "[$ok_l]")")" MISSING "a required check with no run is not green"
	_eq "$(verdict "")" MISSING "an empty state list is never green"
	_eq "$(verdict "$(check_states "$req" "[$ok_l,$(_run Test completed '"failure"' 2026-11-05T10:00:00Z 2)]")")" RED "a failed required check is red"
	_eq "$(verdict "$(check_states "$req" "[$ok_l,$(_run Test completed '"cancelled"' 2026-11-05T10:00:00Z 2)]")")" RED "a cancelled required check is red, not green"
	_eq "$(verdict "$(check_states "$req" "[$ok_l,$(_run Test in_progress null 2026-11-05T10:00:00Z 2)]")")" PENDING "a running required check is pending"
	_eq "$(verdict "$(check_states "$req" "[$ok_l,$(_run Test completed '""' 2026-11-05T10:00:00Z 2)]")")" PENDING "a completed run with no conclusion is pending"
	# main: the merge queue ran the job, the push run skipped it.
	_eq "$(verdict "$(check_states "$req" "[$ok_l,$ok_t,$(_run Test completed '"skipped"' 2026-11-05T11:00:00Z 3)]")")" GREEN "a newer skipped run does not hide the queue's pass"
	_eq "$(verdict "$(check_states "$req" "[$ok_l,$(_run Test completed '"skipped"' 2026-11-05T11:00:00Z 3)]")")" MISSING "only skipped runs is missing"
	# A rerun that passed after a failure, and the other way round.
	_eq "$(verdict "$(check_states "$req" "[$ok_l,$(_run Test completed '"failure"' 2026-11-05T10:00:00Z 2),$(_run Test completed '"success"' 2026-11-05T10:30:00Z 4)]")")" GREEN "the newest run decides: a passing rerun"
	_eq "$(verdict "$(check_states "$req" "[$ok_l,$ok_t,$(_run Test completed '"failure"' 2026-11-05T10:30:00Z 4)]")")" RED "the newest run decides: a failing push run after the queue's pass"
	# Same start second: the higher id is the newer run.
	_eq "$(verdict "$(check_states "$req" "[$ok_l,$(_run Test completed '"success"' 2026-11-05T10:00:00Z 9),$(_run Test completed '"failure"' 2026-11-05T10:00:00Z 8)]")")" GREEN "ties break on id"
	# Red outranks pending.
	_eq "$(verdict "$(check_states "$req" "[$(_run Lint in_progress null 2026-11-05T10:00:00Z 1),$(_run Test completed '"timed_out"' 2026-11-05T10:00:00Z 2)]")")" RED "red outranks pending"
	# A red check outside the required set does not change the verdict, and is reported.
	local extra; extra="$(_run Coverage completed '"failure"' 2026-11-05T10:00:00Z 5)"
	_eq "$(verdict "$(check_states "$req" "[$ok_l,$ok_t,$extra]")")" GREEN "a red check that is not required does not block here"
	_eq "$(other_red "$req" "[$ok_l,$ok_t,$extra]")" "Coverage (failure)" "the red check that is not required is reported"
	_eq "$(other_red "$req" "[$ok_l,$ok_t,$(_run Coverage completed '"skipped"' 2026-11-05T10:00:00Z 5)]")" "" "a skipped check is not reported"
	_eq "$(check_states "$req" "[$ok_l,$(_run Test completed '"failure"' 2026-11-05T10:00:00Z 7)]" | grep '^red' | cut -f3)" "failure u7" "the red line carries the conclusion and the link"
	[ "$fail" = 0 ] && echo "release-ci-gate self-test: OK"
	return "$fail"
}

main "$@"
