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
# Usage:
#   scripts/ci-heavy-gate.sh <event-name> <base-ref>   # writes run=… to $GITHUB_OUTPUT
#   scripts/ci-heavy-gate.sh --decide                  # reads a file list on stdin, prints run|skip
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
	if printf '%s\n' "$files" | grep -qvE "$SKIP_RE"; then
		echo run
	else
		echo skip
	fi
}

# documented_skip_paths: the skip list as test/README.md states it, read from
# the fenced block marked `skip-paths`.
documented_skip_paths() {
	awk '/^```skip-paths$/ {inb=1; next} inb && /^```$/ {exit} inb {print}' "$ROOT/test/README.md" 2>/dev/null
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

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

case "${1:-}" in
	--self-test) self_test; exit $? ;;
	--decide)    decide; exit 0 ;;
esac

event="${1:-}"; base="${2:-}"
[ -n "$event" ] || { echo "usage: $(basename "$0") <event-name> <base-ref> | --decide | --self-test" >&2; exit 2; }

emit() { # <run|skip>
	local run=true; [ "$1" = skip ] && run=false
	[ -n "${GITHUB_OUTPUT:-}" ] && echo "run=$run" >> "$GITHUB_OUTPUT"
	if [ "$run" = true ]; then echo "Code change detected, running the heavy k3d E2E."
	else echo "Docs or tooling only change, skipping the heavy k3d E2E."; fi
}

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
emit "$(printf '%s\n' "$files" | decide)"
