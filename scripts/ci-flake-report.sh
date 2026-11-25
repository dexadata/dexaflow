#!/usr/bin/env bash
# Weekly CI rerun report (#1576): how often each workflow needed a rerun, and
# which jobs failed and then passed on a rerun of the same run (a flake).
#
# A rerun hides a flake: the run ends green and nobody counts it. This script
# reads the workflow runs of the last N days (every event: pull requests, the
# merge queue, main, schedules), takes the runs that needed more than one
# attempt, and reads every attempt of their jobs. A job is counted as a flake
# in a run when one attempt of it failed (or timed out) and a later attempt of
# the same job succeeded.
#
# The report is markdown on stdout. .github/workflows/ci-flake-report.yaml
# writes it to the step summary and to the body of the one open issue titled
# "CI flake report" (label ci), and each flaky job row names the open issue
# labeled `flaky` whose title carries the job's name, or says it has none:
# every listed flake should have one.
#
# Usage:
#   scripts/ci-flake-report.sh [days]        (default 7; needs gh)
#   scripts/ci-flake-report.sh --self-test
set -euo pipefail

REPO="${CI_FLAKE_REPO:-dexadata/dexaflow}"

# ---- pure logic (no network) ----------------------------------------------

# workflow_table <runs-json>: one markdown row per workflow, most reruns first.
# <runs-json> is an array of runs (name, run_attempt).
workflow_table() {
	jq -r '
		group_by(.name)
		| map({name: .[0].name, runs: length, rerun: (map(select(.run_attempt > 1)) | length)})
		| sort_by([-.rerun, .name])[]
		| "| \(.name) | \(.runs) | \(.rerun) | \(if .runs > 0 then ((.rerun * 1000 / .runs | floor) / 10) else 0 end)% |"
	' <<<"$1"
}

# flaky_jobs <jobs-json>: one line per flake, "<workflow>\t<job>\t<run url>".
# <jobs-json> is an array of {workflow, run_url, jobs: [{name, run_attempt,
# conclusion}]}, one entry per rerun run, every attempt of every job.
flaky_jobs() {
	jq -r '
		.[] | .workflow as $w | .run_url as $u
		| .jobs | group_by(.name)[]
		| sort_by(.run_attempt)
		| (map(select(.conclusion == "failure" or .conclusion == "timed_out")) | map(.run_attempt) | min) as $firstbad
		| select($firstbad != null)
		| select(any(.[]; .conclusion == "success" and .run_attempt > $firstbad))
		| "\($w)\t\(.[0].name)\t\($u)"
	' <<<"$1"
}

# flake_table <flaky-lines> <issues-json>: one markdown row per job, most
# flakes first. <issues-json> is an array of open issues (number, title)
# labeled flaky; a job is tracked by the first whose title contains its name.
flake_table() {
	local lines="$1" issues="$2"
	[ -n "$lines" ] || return 0
	jq -nRr --argjson issues "$issues" '
		[inputs | select(length > 0) | split("\t") | {workflow: .[0], job: .[1], url: .[2]}]
		| group_by([.workflow, .job])
		| map({workflow: .[0].workflow, job: .[0].job, n: length, last: .[-1].url})
		| sort_by([-.n, .workflow, .job])[]
		| . as $r
		| ([$issues[] | select(.title | contains($r.job))][0]) as $i
		| "| \($r.job) | \($r.workflow) | \($r.n) | [run](\($r.last)) | \(if $i then "#\($i.number)" else "none" end) |"
	' <<<"$lines"$'\n'
}

# ---- network --------------------------------------------------------------

# runs_since <iso-date>: every workflow run created since then, as one array.
# Pages by number: --paginate follows next links some proxies rewrite.
runs_since() {
	local since="$1" page=1 out='[]' batch
	while :; do
		batch="$(gh api "repos/$REPO/actions/runs?created=>=$since&per_page=100&page=$page" --jq '.workflow_runs')"
		[ "$(jq 'length' <<<"$batch")" -gt 0 ] || break
		out="$(jq -s 'add' <(printf '%s' "$out") <(printf '%s' "$batch"))"
		[ "$(jq 'length' <<<"$batch")" -eq 100 ] || break
		page=$((page + 1))
		[ "$page" -le 30 ] || break
	done
	# Dependabot's own update jobs (event "dynamic") are not this repository's CI.
	jq '[.[] | select(.status == "completed" and .event != "dynamic") | {id, name, run_attempt, html_url}]' <<<"$out"
}

report() {
	local days="${1:-7}" since runs reruns jobs='[]' r id
	since="$(date -u -d "-${days} days" +%Y-%m-%d 2>/dev/null || date -u -v-"${days}"d +%Y-%m-%d)"
	runs="$(runs_since "$since")"
	reruns="$(jq -c '.[] | select(.run_attempt > 1)' <<<"$runs")"
	while IFS= read -r r; do
		[ -n "$r" ] || continue
		id="$(jq -r '.id' <<<"$r")"
		jobs="$(jq -c --argjson r "$r" --argjson j "$(gh api "repos/$REPO/actions/runs/$id/jobs?filter=all&per_page=100" --jq '[.jobs[] | {name, run_attempt, conclusion}]')" \
			'. + [{workflow: $r.name, run_url: $r.html_url, jobs: $j}]' <<<"$jobs")"
	done <<<"$reruns"
	local issues; issues="$(gh api "repos/$REPO/issues?state=open&labels=flaky&per_page=100" --jq '[.[] | {number, title}]')"
	local total nrerun
	total="$(jq 'length' <<<"$runs")"; nrerun="$(jq '[.[] | select(.run_attempt > 1)] | length' <<<"$runs")"
	printf '## CI reruns since %s\n\n' "$since"
	printf '%s of %s finished runs needed a rerun.\n\n' "$nrerun" "$total"
	printf '| Workflow | Runs | Rerun | Rate |\n|---|---|---|---|\n'
	workflow_table "$runs"
	printf '\n## Jobs that failed and passed on a rerun\n\n'
	local table; table="$(flake_table "$(flaky_jobs "$jobs")" "$issues")"
	if [ -z "$table" ]; then
		printf 'None.\n'
	else
		printf '| Job | Workflow | Flakes | Last | Tracked by |\n|---|---|---|---|---|\n%s\n' "$table"
		printf '\nA job with no tracking issue needs one labeled "flaky" whose title names the job, or a fix.\n'
	fi
}

# ---- self-test --------------------------------------------------------------

self_test() {
	local rc=0 got
	check() { # <name> <want> <got>
		if [ "$2" != "$3" ]; then
			printf 'self-test FAIL: %s\n  want: %s\n  got:  %s\n' "$1" "$2" "$3" >&2
			rc=1
		fi
	}
	got="$(workflow_table '[{"name":"CI","run_attempt":1},{"name":"CI","run_attempt":2},{"name":"CI","run_attempt":1},{"name":"Fuzz","run_attempt":1}]')"
	check "rerun rate per workflow, most reruns first" "| CI | 3 | 1 | 33.3% |
| Fuzz | 1 | 0 | 0% |" "$got"

	local jobs='[{"workflow":"CI","run_url":"u1","jobs":[
		{"name":"E2E k3d","run_attempt":1,"conclusion":"failure"},
		{"name":"E2E k3d","run_attempt":2,"conclusion":"success"},
		{"name":"Lint","run_attempt":1,"conclusion":"success"},
		{"name":"Unit","run_attempt":1,"conclusion":"failure"},
		{"name":"Unit","run_attempt":2,"conclusion":"failure"},
		{"name":"Smoke","run_attempt":1,"conclusion":"timed_out"},
		{"name":"Smoke","run_attempt":2,"conclusion":"success"},
		{"name":"Late","run_attempt":1,"conclusion":"success"},
		{"name":"Late","run_attempt":2,"conclusion":"failure"}]}]'
	got="$(flaky_jobs "$jobs")"
	check "fail then pass is a flake, fail then fail and pass then fail are not" "CI	E2E k3d	u1
CI	Smoke	u1" "$got"

	got="$(flake_table $'CI\tE2E k3d\tu1\nCI\tE2E k3d\tu2\nCI\tSmoke\tu1' '[{"number":7,"title":"E2E k3d flakes on image import"}]')"
	check "flakes grouped per job, tracked by the issue naming it" "| E2E k3d | CI | 2 | [run](u2) | #7 |
| Smoke | CI | 1 | [run](u1) | none |" "$got"

	got="$(flake_table "" '[]')"
	check "no flake, no rows" "" "$got"

	[ "$rc" -eq 0 ] && echo "self-test OK"
	return "$rc"
}

case "${1:-}" in
--self-test) self_test ;;
*) report "${1:-7}" ;;
esac
