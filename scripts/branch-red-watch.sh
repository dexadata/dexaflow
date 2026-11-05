#!/usr/bin/env bash
# Keep a red main (or release-X.Y) visible until somebody turns it green.
#
# On 2026-11-05 main went red three times on infrastructure (Playwright fonts,
# a k3d 504, an empty API answer in the coverage badge job), and each time it
# stayed red until someone happened to look. A red main blocks every release
# (release-ci-gate.sh), so it is everybody's problem the moment it happens.
#
# Two entry points, both run by .github/workflows/branch-red.yaml:
#
#   event <run-id>     A watched workflow run on a branch failed. On its
#                      first attempt, when the failed jobs' log matches
#                      FLAKE_RE (scripts/flake-patterns.sh) or there is no
#                      log to read, the failed jobs are rerun once and nothing
#                      is reported yet. Otherwise the branch is reconciled
#                      at once.
#   reconcile <branch>... For each branch, every active workflow's newest
#                      finished run on it (pushes, schedules and manual runs;
#                      pull requests and the merge queue are not the branch)
#                      is read. Any of them failed: the issue "CI is red on
#                      <branch>" (label ci-red) is opened, or updated and
#                      commented on when a new workflow turns red. None
#                      failed and none of the listed ones is still running:
#                      the issue is closed with the commit that turned it
#                      green.
#
# A cancelled run is not a verdict (a newer push supersedes it), so the run
# before it decides. A run still in flight keeps whatever the issue says.
#
# Usage:
#   scripts/branch-red-watch.sh event <run-id>
#   scripts/branch-red-watch.sh reconcile <branch>...
#   scripts/branch-red-watch.sh --self-test
#
# BRANCH_RED_DRY=1 reads and prints the decision without writing anything.
set -euo pipefail

REPO="${BRANCH_RED_REPO:-dexadata/dexaflow}"
LABEL=ci-red
# The workflow that runs this script; its own runs are never judged.
SELF_WORKFLOW="${BRANCH_RED_SELF:-Branch red watch}"

# shellcheck source=scripts/flake-patterns.sh
. "$(dirname "${BASH_SOURCE[0]}")/flake-patterns.sh"

# ---- pure logic (unit-testable, no network) --------------------------------

# branch_state <runs-json> [since]: one line per workflow,
# "<state>\t<workflow>\t<url>\t<sha>", state red, green or running.
# <runs-json> is an array of workflow runs (name, status, conclusion, event,
# html_url, head_sha, created_at, id) of one branch, any workflows, any order.
# Runs created before <since> (an RFC 3339 time) are ignored: a workflow that
# failed months ago and never ran again (a manual publish job) is not what
# "main is red" means.
branch_state() {
	jq -r --arg self "$SELF_WORKFLOW" --arg since "${2:-}" '
		[.[] | select(.name != $self) | select(.created_at >= $since)
		 | select(.event != "pull_request" and .event != "pull_request_target" and .event != "merge_group")]
		| group_by(.name)[]
		| sort_by([.created_at, .id]) | reverse
		| (first) as $newest
		| ([.[] | select(.status == "completed" and .conclusion != "cancelled" and .conclusion != "skipped")] | first) as $done
		| if $newest.status != "completed" then "running\t\($newest.name)\t\($newest.html_url)\t\($newest.head_sha)"
		  elif $done == null then empty
		  elif ($done.conclusion == "failure" or $done.conclusion == "timed_out" or $done.conclusion == "startup_failure")
		    then "red\t\($done.name)\t\($done.html_url)\t\($done.head_sha)"
		  else "green\t\($done.name)\t\($done.html_url)\t\($done.head_sha)"
		  end' <<<"$1"
}

# decide <states> <listed>: what to do with the issue, given branch_state's
# output and the workflows the open issue lists (one per line, empty when
# there is no open issue). Echoes open, update, close or keep.
decide() {
	local states="$1" listed="$2" red w
	red="$(awk -F'\t' '$1 == "red" {print $2}' <<<"$states" | sort)"
	if [ -n "$red" ]; then
		if [ -z "$listed" ]; then echo open
		elif [ "$red" = "$(sort <<<"$listed")" ]; then echo keep
		else echo update; fi
		return
	fi
	[ -n "$listed" ] || { echo keep; return; }
	# A listed workflow still running has not turned green yet.
	while IFS= read -r w; do
		[ -n "$w" ] || continue
		if awk -F'\t' -v w="$w" '$1 == "running" && $2 == w {found=1} END {exit !found}' <<<"$states"; then
			echo keep; return
		fi
	done <<<"$listed"
	echo close
}

# new_red <states> <listed>: the red workflows the issue does not list yet.
new_red() {
	awk -F'\t' '$1 == "red" {print $2}' <<<"$1" | sort | comm -23 - <(sort <<<"$2" | sed '/^$/d')
}

# issue_body <branch> <states>: the issue text. The marker lines carry the
# listed workflows, so the next run reads them back without parsing prose.
issue_body() {
	local branch="$1" states="$2"
	printf '%s\n' "<!-- branch-red: $branch -->"
	printf 'The newest finished run of these workflows on `%s` failed. Until they are green again, `scripts/cut-release.sh` and the Release workflow refuse to release from `%s` ([RELEASING.md](https://github.com/%s/blob/main/RELEASING.md#red-ci-blocks-a-release)).\n\n' "$branch" "$branch" "$REPO"
	printf '| Workflow | Run | Commit |\n|---|---|---|\n'
	awk -F'\t' '$1 == "red" {printf "| %s | [run](%s) | %s |\n", $2, $3, substr($4, 1, 8)}' <<<"$states"
	printf '\nRead the failed job before rerunning it. A transient failure is rerun with `gh run rerun <id> --failed`; one that matches no pattern in `scripts/flake-patterns.sh` is a bug of the code or of CI, and its fix is a pull request. This issue closes itself when every workflow above has a green run on `%s`.\n\n' "$branch"
	awk -F'\t' '$1 == "red" {print "<!-- red-workflow: " $2 " -->"}' <<<"$states"
}

# listed_in <body>: the workflows an issue body lists.
listed_in() { sed -n 's/^<!-- red-workflow: \(.*\) -->$/\1/p' <<<"$1"; }

# rerun_or_report <attempt> <flake-verdict>: "rerun" only for a first attempt
# whose failure reads as transient or left no log; everything else reports.
rerun_or_report() {
	if [ "$1" = 1 ] && { [ "$2" = flake ] || [ "$2" = unknown ]; }; then echo rerun; else echo report; fi
}

# flake_verdict <log-text>: flake, real or unknown; same rule as cut-release.sh.
flake_verdict() {
	[ -n "${1:-}" ] || { echo unknown; return 0; }
	if printf '%s' "$1" | grep -qE "$FLAKE_RE"; then echo flake; else echo real; fi
}

# ---- network ---------------------------------------------------------------

# branch_runs <branch>: the recent runs of every active workflow on <branch>.
# One query per workflow, so a weekly workflow is judged by its own last run
# however many runs other workflows made since.
branch_runs() {
	local b="$1" id out=""
	# Dependabot's dynamic workflows (path dynamic/...) are not the branch's CI.
	for id in $(gh api "repos/$REPO/actions/workflows?per_page=100" \
		--jq '.workflows[] | select(.state == "active") | select(.path | startswith(".github/workflows/")) | .id'); do
		out="$out$(gh api "repos/$REPO/actions/workflows/$id/runs?branch=$b&per_page=10&exclude_pull_requests=true" \
			--jq '[.workflow_runs[] | {name, status, conclusion, event, html_url, head_sha, created_at, id}]')"
	done
	jq -s 'add // []' <<<"$out"
}

reconcile_branch() {
	local b="$1"
	local runs states issue num body listed action title="CI is red on $b"
	runs="$(branch_runs "$b")"
	# Eight days: a weekly workflow is still judged by its last run.
	states="$(branch_state "$runs" "$(date -u -d "-${BRANCH_RED_MAX_AGE_DAYS:-8} days" +%Y-%m-%dT%H:%M:%SZ)")"
	issue="$(gh api "repos/$REPO/issues?labels=$LABEL&state=open&per_page=100" \
		--jq "[.[] | select(.title == \"$title\")] | first // empty | {number, body}")"
	num=""; listed=""
	if [ -n "$issue" ]; then
		num="$(jq -r .number <<<"$issue")"
		listed="$(listed_in "$(jq -r '.body // ""' <<<"$issue")")"
	fi
	action="$(decide "$states" "$listed")"
	echo "$b: $action ($(awk -F'\t' '$1 == "red"' <<<"$states" | wc -l | tr -d ' ') red, issue ${num:-none})"
	if [ "${BRANCH_RED_DRY:-0}" = 1 ]; then
		printf '%s\n' "$states"
		return 0
	fi
	case "$action" in
	open)
		gh label create "$LABEL" --repo "$REPO" --color B60205 \
			--description "A workflow is red on main or a release branch" >/dev/null 2>&1 || true
		body="$(issue_body "$b" "$states")"
		gh issue create --repo "$REPO" --title "$title" --label "$LABEL" --body "$body" ;;
	update)
		body="$(issue_body "$b" "$states")"
		gh issue edit "$num" --repo "$REPO" --body "$body" >/dev/null
		local fresh; fresh="$(new_red "$states" "$listed")"
		if [ -n "$fresh" ]; then
			gh issue comment "$num" --repo "$REPO" --body "Now also red on \`$b\`: $(paste -sd, - <<<"$fresh" | sed 's/,/, /g')." >/dev/null
		fi ;;
	close)
		local tip; tip="$(gh api "repos/$REPO/commits/$b" --jq .sha)"
		gh issue close "$num" --repo "$REPO" --comment "Green again: every workflow this issue listed has a green run on \`$b\` (tip ${tip:0:8})." >/dev/null ;;
	keep) ;;
	esac
}

on_event() { # <run-id>
	local id="$1" run attempt branch conclusion verdict
	run="$(gh api "repos/$REPO/actions/runs/$id" --jq '{run_attempt, head_branch, conclusion}')"
	attempt="$(jq -r .run_attempt <<<"$run")"
	branch="$(jq -r .head_branch <<<"$run")"
	conclusion="$(jq -r .conclusion <<<"$run")"
	case "$conclusion" in
	failure | timed_out | startup_failure) ;;
	*) echo "run $id concluded $conclusion: nothing to do"; return 0 ;;
	esac
	verdict="$(flake_verdict "$(gh run view "$id" --repo "$REPO" --log-failed 2>/dev/null | tail -c 2000000 || true)")"
	if [ "$(rerun_or_report "$attempt" "$verdict")" = rerun ]; then
		echo "run $id (attempt $attempt) failed on $branch with a $verdict log: rerunning its failed jobs once"
		[ "${BRANCH_RED_DRY:-0}" = 1 ] || gh run rerun "$id" --repo "$REPO" --failed
		return 0
	fi
	echo "run $id (attempt $attempt) failed on $branch with a $verdict log: reporting"
	reconcile_branch "$branch"
}

self_test() {
	local fail=0
	_eq() { [ "$1" = "$2" ] || { echo "FAIL: $3: got '$1', want '$2'"; fail=1; }; }
	_r() { # name status conclusion event created id
		printf '{"name":"%s","status":"%s","conclusion":%s,"event":"%s","html_url":"https://x/%s","head_sha":"abcdef0123456789","created_at":"%s","id":%s}' \
			"$1" "$2" "$3" "$4" "$6" "$5" "$6"
	}
	local s
	s="$(branch_state "[$(_r CI completed '"success"' push 2026-11-05T10:00:00Z 1)]")"
	_eq "$(cut -f1,2 <<<"$s")" "green	CI" "a passing push run is green"
	s="$(branch_state "[$(_r CI completed '"success"' push 2026-11-05T10:00:00Z 1),$(_r CI completed '"failure"' push 2026-11-05T11:00:00Z 2)]")"
	_eq "$(cut -f1 <<<"$s")" red "the newest finished run decides"
	s="$(branch_state "[$(_r CI completed '"failure"' push 2026-11-05T10:00:00Z 1),$(_r CI completed '"cancelled"' push 2026-11-05T11:00:00Z 2)]")"
	_eq "$(cut -f1 <<<"$s")" red "a cancelled run is not a verdict: the run before it decides"
	s="$(branch_state "[$(_r CI completed '"failure"' push 2026-11-05T10:00:00Z 1),$(_r CI in_progress null push 2026-11-05T11:00:00Z 2)]")"
	_eq "$(cut -f1 <<<"$s")" running "a run in flight is running, not red"
	s="$(branch_state "[$(_r CI completed '"failure"' pull_request 2026-11-05T10:00:00Z 1),$(_r CI completed '"failure"' merge_group 2026-11-05T10:00:00Z 2)]")"
	_eq "$s" "" "pull request and merge queue runs are not the branch"
	s="$(branch_state "[$(_r 'Branch red watch' completed '"failure"' schedule 2026-11-05T10:00:00Z 1)]")"
	_eq "$s" "" "the watch never judges its own runs"
	s="$(branch_state "[$(_r Fuzz completed '"timed_out"' schedule 2026-11-05T10:00:00Z 1),$(_r CI completed '"success"' push 2026-11-05T10:00:00Z 2)]")"
	_eq "$(awk -F'\t' '$1=="red"{print $2}' <<<"$s")" Fuzz "a timed out nightly is red next to a green CI"
	s="$(branch_state "[$(_r Publish completed '"failure"' push 2026-08-01T10:00:00Z 1),$(_r CI completed '"success"' push 2026-11-05T10:00:00Z 2)]" 2026-10-28T00:00:00Z)"
	_eq "$(cut -f1,2 <<<"$s")" "green	CI" "a failure older than the window is not judged"

	local red_ci="red	CI	u	s" green_ci="green	CI	u	s" run_ci="running	CI	u	s"
	_eq "$(decide "$red_ci" "")" open "red with no issue opens one"
	_eq "$(decide "$red_ci" "CI")" keep "red already listed keeps the issue"
	_eq "$(decide "$red_ci"$'\n'"red	Fuzz	u	s" "CI")" update "a newly red workflow updates the issue"
	_eq "$(decide "$green_ci" "CI")" close "listed workflow green again closes"
	_eq "$(decide "$run_ci" "CI")" keep "a listed workflow still running keeps the issue open"
	_eq "$(decide "$green_ci" "")" keep "green with no issue does nothing"
	_eq "$(decide "" "")" keep "nothing read does nothing"
	_eq "$(new_red "$red_ci"$'\n'"red	Fuzz	u	s" "CI")" Fuzz "new_red names only the workflow the issue lacks"

	local body; body="$(issue_body main "$red_ci"$'\n'"red	E2E split	u	s")"
	_eq "$(listed_in "$body" | paste -sd, -)" "CI,E2E split" "the body lists what it reports, and reads back"
	_eq "$(decide "$red_ci" "$(listed_in "$body")")" update "one of two listed turned green: update"

	_eq "$(rerun_or_report 1 flake)" rerun "first attempt, transient: rerun"
	_eq "$(rerun_or_report 1 unknown)" rerun "first attempt, no log: rerun"
	_eq "$(rerun_or_report 1 real)" report "first attempt, real failure: report"
	_eq "$(rerun_or_report 2 flake)" report "a second attempt is never rerun again"
	_eq "$(flake_verdict 'Error response from daemon: Gateway Time-out')" flake "a k3d 504 is transient"
	_eq "$(flake_verdict 'FAIL: TestFoo')" real "a test failure is real"
	[ "$fail" = 0 ] && echo "branch-red-watch self-test: OK"
	return "$fail"
}

case "${1:-}" in
--self-test) self_test ;;
event) [ -n "${2:-}" ] || { echo "usage: branch-red-watch.sh event <run-id>" >&2; exit 2; }; on_event "$2" ;;
reconcile)
	shift; [ $# -gt 0 ] || { echo "usage: branch-red-watch.sh reconcile <branch>..." >&2; exit 2; }
	for b in "$@"; do reconcile_branch "$b"; done ;;
*) echo "usage: branch-red-watch.sh event <run-id> | reconcile <branch>... | --self-test" >&2; exit 2 ;;
esac
