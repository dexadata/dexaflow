#!/usr/bin/env bash
# Fetches the coverage profile of the branch a change goes into, for the
# per-package delta in the "Test + Coverage" job summary (#1572). The test job
# of every push uploads its profile as the artifact coverage-<sha>; this reads
# the one of BASE_SHA (the pull request's base commit, the merge queue's base,
# or the commit a push replaced) and, when that run has not uploaded one yet
# or its artifact expired, the newest green push run on BASE_REF.
#
# Usage (GH_TOKEN and GITHUB_REPOSITORY set, as in a workflow step):
#   BASE_SHA=<sha> BASE_REF=main scripts/coverage-base.sh <out-profile>
#
# Prints the label of the profile it wrote ("main at 1a2b3c4d") on stdout. It
# never fails the step: without a profile it prints nothing, removes
# <out-profile> and says why on stderr, and the summary then has no delta.
set -uo pipefail

out="${1:?usage: coverage-base.sh <out-profile>}"
ref="${BASE_REF:-main}"
repo="repos/${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"
rm -f "$out"

# artifact_id <sha>: the id of the unexpired coverage artifact of that commit.
artifact_id() {
	gh api "$repo/actions/artifacts?name=coverage-$1&per_page=5" \
		--jq '[.artifacts[] | select(.expired | not)][0].id // empty' 2>/dev/null
}

sha="" id=""
if [ -n "${BASE_SHA:-}" ] && [ "${BASE_SHA}" != "0000000000000000000000000000000000000000" ]; then
	sha="$BASE_SHA"
	id=$(artifact_id "$sha")
fi
if [ -z "$id" ]; then
	for sha in $(gh api "$repo/actions/workflows/ci.yaml/runs?branch=$ref&event=push&status=success&per_page=5" \
		--jq '.workflow_runs[].head_sha' 2>/dev/null); do
		id=$(artifact_id "$sha")
		[ -n "$id" ] && break
	done
fi
if [ -z "$id" ]; then
	echo "coverage-base: no coverage artifact of $ref was found" >&2
	exit 0
fi

zip="$(mktemp)"
trap 'rm -f "$zip"' EXIT
if gh api "$repo/actions/artifacts/$id/zip" >"$zip" 2>/dev/null &&
	unzip -p "$zip" coverage.out >"$out" 2>/dev/null && [ -s "$out" ]; then
	echo "$ref at ${sha:0:8}"
else
	echo "coverage-base: the coverage artifact of $ref at ${sha:0:8} could not be read" >&2
	rm -f "$out"
fi
exit 0
