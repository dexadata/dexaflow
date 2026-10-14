#!/usr/bin/env bash
# Decide whether a pull request needs the heavy k3d end-to-end jobs.
#
# Four jobs ask this question: operator-e2e, split-e2e, e2e-dbt and deploy-e2e.
# The answer used to be a regex pasted into each of them, four copies of one
# decision with nothing reconciling them and no way to ask "would this file list
# run or skip?" short of opening a pull request and watching.
#
# That cost a real cycle. When changelog fragments landed (#1200), every pull
# request started carrying a `.changes/unreleased/*.yaml` file, and `.changes/`
# was not in the allowlist, so the docs-only pull requests this gate exists to
# spare became the ones it stopped sparing. It was found by reading, not by a
# test, because there was no test to fail.
#
# The gate deliberately lives INSIDE each job rather than as a `paths:` filter
# or a job-level `if:`. A skipped job reports nothing, so a failure already
# recorded on that commit stays red forever: rerunning a `pull_request` run
# replays the original event payload, so the condition evaluates the same way
# every time and no remedy can clear the check. changelog-guard.yaml carries
# that lesson, learned from the v0.4.7 cut (#1176).
#
# A second scope, `warm-pools`, answers a narrower question for the warm pools
# leg of deploy-e2e (#1707): does this change touch warm placement or the deploy
# path? That leg repeats the deploy scenario with warm pools on, and the runner
# queue is small, so it runs only when a file on that path changed (WARM_RE),
# on every event: pull requests diff against their base branch, merge queue
# runs against the queue's base commit and pushes against the commit before
# the push. When no diff can be computed it runs, like the default scope.
#
# Usage:
#   scripts/ci-heavy-gate.sh <event-name> <base-ref>   # writes run=… to $GITHUB_OUTPUT
#   scripts/ci-heavy-gate.sh --scope warm-pools <event-name> <base-ref-or-sha>
#   scripts/ci-heavy-gate.sh --decide                  # reads a file list on stdin, prints run|skip
#   scripts/ci-heavy-gate.sh --decide-warm             # the same for the warm-pools scope
#   scripts/ci-heavy-gate.sh --self-test
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# SKIP_RE: paths that cannot change what the heavy jobs test. A pull request
# touching ONLY these skips them.
#
# The list is documented, with a row per entry explaining what it is and why it
# cannot affect a cluster run, in test/README.md. A self-test below reads that
# table and fails when the two disagree, so the prose cannot rot into a third
# copy of this decision.
SKIP_RE='^(docs/|spec/|test/load/|website/|\.changes/|.*\.md$)'

# decide: read a newline-separated file list on stdin, print `run` or `skip`.
#
# An EMPTY list prints `run`. A pull request whose diff we could not compute is
# not a docs-only pull request; it is a pull request we know nothing about, and
# guessing `skip` there would silently drop every heavy job.
decide() {
	local files; files="$(cat)"
	if [ -z "${files//[[:space:]]/}" ]; then
		echo run; return
	fi
	# Filtered into a variable, never `| grep -q`: under pipefail an early exit
	# of grep -q sends SIGPIPE to the writer, the pipeline returns 141 and a
	# large diff read as skip.
	local code; code="$(printf '%s\n' "$files" | grep -vE "$SKIP_RE" || true)"
	if [ -n "$code" ]; then
		echo run
	else
		echo skip
	fi
}

# WARM_RE: the paths the warm pools leg runs on: warm placement and the warm
# worker (dispatch, executor, agent, agentrpc, the token exchange, the agent
# binary and protocol, the agent token), the scheduler that picks the version
# a task is dispatched for, the storage that resolves that version, its warm
# targets and the warm binding (BindWarmAttempt in runs.sql and its generated
# runs.sql.go), the warm eligibility rules (resource unit, default limits), the
# deploy command and the image build, and the leg itself. Deliberately left
# out: cmd/dexaflow-server/, internal/config/, go.mod and ci.yaml, which would
# run the leg on most pull requests (57 of 100 merges with them; 37 of the last
# 100 with this list). Documented in test/README.md (`warm-paths`) and checked
# against it by the self-test.
WARM_RE='^(internal/dispatch/|internal/executor/|internal/agentrpc/|internal/agent/|internal/kubeexchange/|internal/scheduler/|internal/storage/scheduler_store\.go|internal/storage/agent_store\.go|internal/storage/queries/runs\.sql|internal/domain/resource_unit\.go|internal/domain/default_limits\.go|internal/auth/agent_token\.go|internal/cli/deploy|internal/build/|cmd/dexaflow-agent/|proto/|test/e2e/deploy-e2e\.sh|test/e2e/lib\.sh|scripts/ci-heavy-gate\.sh)'

# decide_warm: like decide, for the warm-pools scope. A file the default scope
# skips (prose, docs) never runs the leg, even under a warm path; otherwise one
# file under WARM_RE runs it. An empty list runs, for the reason decide gives.
decide_warm() {
	local files; files="$(cat)"
	if [ -z "${files//[[:space:]]/}" ]; then
		echo run; return
	fi
	# Into a variable first, for the SIGPIPE reason decide gives.
	local warm; warm="$(printf '%s\n' "$files" | grep -vE "$SKIP_RE" | grep -E "$WARM_RE" || true)"
	if [ -n "$warm" ]; then
		echo run
	else
		echo skip
	fi
}

# documented_block NAME: the lines of the fenced block marked NAME in
# test/README.md.
documented_block() {
	awk -v tag="$1" '$0 == "```" tag {inb=1; next} inb && /^```$/ {exit} inb {print}' "$ROOT/test/README.md" 2>/dev/null
}

# documented_skip_paths: the skip list as test/README.md states it, read from
# the fenced block marked `skip-paths`.
documented_skip_paths() {
	documented_block skip-paths
}

self_test() {
	local fail=0
	_eq() { if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"
		else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi; }

	_eq "$(printf '%s\n' 'README.md' 'website/content/x.md' | decide)" "skip" "docs only skips"
	_eq "$(printf '%s\n' 'internal/executor/reconcile.go' | decide)" "run" "a Go change runs"
	_eq "$(printf '%s\n' 'helm/dexaflow/values.yaml' | decide)" "run" "a chart change runs"

	# The #1200 regression: every pull request carries a fragment now, so a
	# docs-only one that does NOT skip means the gate spares nobody.
	_eq "$(printf '%s\n' 'README.md' '.changes/unreleased/x.yaml' | decide)" "skip" "docs plus a changelog fragment still skips"
	_eq "$(printf '%s\n' '.changes/unreleased/x.yaml' | decide)" "skip" "a fragment alone skips"

	# One file outside the allowlist is enough, wherever it sits in the list.
	_eq "$(printf '%s\n' 'docs/a.md' 'Makefile' 'website/b.md' | decide)" "run" "one non-doc file anywhere forces a run"
	_eq "$(printf '%s\n' 'scripts/ci-heavy-gate.sh' | decide)" "run" "a change to this gate itself runs"

	# Load tests are excluded, the suites the heavy jobs run are not.
	_eq "$(printf '%s\n' 'test/load/plan.json' | decide)" "skip" "test/load is excluded"
	_eq "$(printf '%s\n' 'test/e2e/deploy-e2e.sh' | decide)" "run" "test/e2e is NOT excluded"

	# An empty diff must never be read as "docs only".
	_eq "$(printf '' | decide)" "run" "an empty file list runs, it does not skip"
	_eq "$(printf '\n\n' | decide)" "run" "a blank file list runs too"

	# The documented table and the allowlist are one decision in two places.
	local documented; documented="$(documented_skip_paths | awk 'NF' | sort)"
	local actual; actual="$(printf '%s\n' "$SKIP_RE" | sed -E 's/^\^\(//; s/\)$//' | tr '|' '\n' | awk 'NF' | sort)"
	_eq "$documented" "$actual" "test/README.md documents exactly the paths this gate skips"

	# The warm pools scope (#1707): the second deploy-e2e leg runs only when a
	# file on the warm placement or deploy path changed.
	_eq "$(printf '%s\n' 'internal/dispatch/dispatch.go' | decide_warm)" "run" "warm: a dispatch change runs"
	_eq "$(printf '%s\n' 'internal/executor/warmpool.go' | decide_warm)" "run" "warm: an executor change runs"
	_eq "$(printf '%s\n' 'internal/agentrpc/worker_registry.go' | decide_warm)" "run" "warm: the worker registry runs"
	_eq "$(printf '%s\n' 'internal/storage/scheduler_store.go' | decide_warm)" "run" "warm: the warm target source runs"
	_eq "$(printf '%s\n' 'internal/cli/deploy.go' | decide_warm)" "run" "warm: the deploy command runs"
	_eq "$(printf '%s\n' 'test/e2e/deploy-e2e.sh' | decide_warm)" "run" "warm: the deploy e2e itself runs"
	_eq "$(printf '%s\n' 'internal/config/server.go' 'cmd/dexaflow-server/main.go' | decide_warm)" "skip" "warm: config and server wiring alone skip"
	_eq "$(printf '%s\n' 'internal/api/dags.go' 'website/x.md' | decide_warm)" "skip" "warm: an API-only change skips"
	_eq "$(printf '%s\n' 'internal/storage/repository.go' | decide_warm)" "skip" "warm: other storage files skip"
	_eq "$(printf '%s\n' 'internal/storage/queries/runs.sql.go' | decide_warm)" "run" "warm: the warm binding query runs"
	_eq "$(printf '%s\n' 'internal/domain/resource_unit.go' | decide_warm)" "run" "warm: the warm eligibility rules run"
	_eq "$(printf '%s\n' 'internal/auth/agent_token.go' | decide_warm)" "run" "warm: the agent token runs"
	_eq "$(printf '%s\n' 'internal/domain/dag.go' | decide_warm)" "skip" "warm: other domain files skip"
	_eq "$(printf '%s\n' 'internal/dispatch/README.md' | decide_warm)" "skip" "warm: prose under a warm path still skips"
	_eq "$(printf '%s\n' 'internal/api/dags.go' 'internal/agent/warm.go' | decide_warm)" "run" "warm: one warm file anywhere runs"
	_eq "$(printf '' | decide_warm)" "run" "warm: an empty file list runs, it does not skip"

	# A large diff: `printf | grep -q` under pipefail returned 141 once grep -q
	# exited early and the writer took SIGPIPE, and the gate read that as skip.
	local big; big="$(seq 1 5000 | sed 's#^#internal/api/some/longer/path/file#; s#$#.go#')"
	_eq "$(printf '%s\n' "$big" | decide)" "run" "a 5000-file code diff runs"
	_eq "$(printf '%s\n' 'internal/dispatch/dispatch.go' "$big" | decide_warm)" "run" "warm: a 5000-file diff with a warm file first runs"
	_eq "$(printf '%s\n' "$big" | decide_warm)" "skip" "warm: a 5000-file diff with no warm file skips"
	local documented_warm; documented_warm="$(documented_block warm-paths | awk 'NF' | sort)"
	local actual_warm; actual_warm="$(printf '%s\n' "$WARM_RE" | sed -E 's/^\^\(//; s/\)$//' | tr '|' '\n' | awk 'NF' | sort)"
	_eq "$documented_warm" "$actual_warm" "test/README.md documents exactly the paths the warm scope runs on"

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

case "${1:-}" in
	--self-test)   self_test; exit $? ;;
	--decide)      decide; exit 0 ;;
	--decide-warm) decide_warm; exit 0 ;;
esac

scope=heavy
if [ "${1:-}" = "--scope" ]; then
	scope="${2:-}"; shift 2 || true
fi
case "$scope" in
	heavy|warm-pools) ;;
	*) echo "unknown scope '$scope' (want heavy or warm-pools)" >&2; exit 2 ;;
esac

event="${1:-}"; base="${2:-}"
[ -n "$event" ] || { echo "usage: $(basename "$0") [--scope warm-pools] <event-name> <base-ref> | --decide | --decide-warm | --self-test" >&2; exit 2; }

emit() { # <run|skip>
	local run=true; [ "$1" = skip ] && run=false
	[ -n "${GITHUB_OUTPUT:-}" ] && echo "run=$run" >> "$GITHUB_OUTPUT"
	if [ "$scope" = warm-pools ]; then
		if [ "$run" = true ]; then echo "A warm placement or deploy path changed, running the warm pools leg."
		else
			echo "Nothing on the warm placement or deploy path changed, skipping the warm pools leg."
			# A skipped leg still reports green, so say on the run page that it
			# did not run rather than leave a green check that tested nothing.
			if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
				echo "Warm pools leg skipped: no file on the warm placement or deploy path changed (\`warm-paths\` in test/README.md)." >> "$GITHUB_STEP_SUMMARY"
			fi
		fi
		return
	fi
	if [ "$run" = true ]; then echo "Code change detected, running the heavy k3d E2E."
	else echo "Docs or tooling only change, skipping the heavy k3d E2E."; fi
}

# The warm-pools scope decides by path on every event. Outside a pull request
# the base is a commit (the merge queue's base, or the commit before a push);
# a missing or all-zero one (a new branch) runs the leg.
if [ "$scope" = warm-pools ] && [ "$event" != "pull_request" ]; then
	if ! [[ "$base" =~ ^[0-9a-f]{40}$ ]] || [[ "$base" =~ ^0+$ ]]; then
		echo "No base commit for event '$event', running the warm pools leg."
		[ -n "${GITHUB_OUTPUT:-}" ] && echo "run=true" >> "$GITHUB_OUTPUT"
		exit 0
	fi
	git cat-file -e "${base}^{commit}" 2>/dev/null || git fetch origin "$base" >/dev/null 2>&1 ||
		{ echo "FAIL: could not fetch base commit '${base}'" >&2; exit 1; }
	files="$(git diff --name-only "${base}...HEAD")"
	echo "Changed files:"; printf '%s\n' "$files" | sed 's/^/  /'
	emit "$(printf '%s\n' "$files" | decide_warm)"
	exit 0
fi

if [ "$event" != "pull_request" ]; then
	echo "Not a pull_request event, running the heavy k3d E2E."
	[ -n "${GITHUB_OUTPUT:-}" ] && echo "run=true" >> "$GITHUB_OUTPUT"
	exit 0
fi
[ -n "$base" ] || { echo "FAIL: a pull_request needs a base ref" >&2; exit 2; }

# A full fetch of the base, not --depth=1. The checkout is fetch-depth 0, and a
# depth-1 fetch marks the base's CURRENT tip as a shallow boundary. The merge ref
# was cut against the base as it was when the run was queued, so once the base
# advances during the run, the shallow tip has no path back to the merge ref's
# parent and `...` fails with "no merge base". A rerun reuses the same merge ref,
# so it fails again.
git fetch origin "${base}:refs/remotes/origin/${base}" >/dev/null 2>&1 ||
	{ echo "FAIL: could not fetch base ref '${base}'" >&2; exit 1; }

files="$(git diff --name-only "origin/${base}...HEAD")"
echo "Changed files:"; printf '%s\n' "$files" | sed 's/^/  /'
if [ "$scope" = warm-pools ]; then
	emit "$(printf '%s\n' "$files" | decide_warm)"
else
	emit "$(printf '%s\n' "$files" | decide)"
fi
