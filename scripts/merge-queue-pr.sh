#!/usr/bin/env bash
# Print the pull request number a merge queue run is testing.
#
# A `merge_group` event carries no pull_request payload: the guards that judge
# a pull request (the milestone guard, #1569) have to recover its number from
# the temporary branch the queue builds, which GitHub names
#   refs/heads/gh-readonly-queue/<base-branch>/pr-<number>-<head-sha>
# When several pull requests are grouped, the branch names the last one; the
# others had their own queue entries checked first.
#
# Usage:
#   scripts/merge-queue-pr.sh <merge_group.head_ref>   # prints <number>, exit 1 if none
#   scripts/merge-queue-pr.sh --self-test
set -euo pipefail

pr_of() { # <head_ref>
	local ref="${1#refs/heads/}"
	if [[ "$ref" =~ ^gh-readonly-queue/.+/pr-([0-9]+)-[0-9a-f]{7,64}$ ]]; then
		echo "${BASH_REMATCH[1]}"
		return 0
	fi
	return 1
}

self_test() {
	local fail=0 got
	_eq() { if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi; }
	_eq "$(pr_of refs/heads/gh-readonly-queue/main/pr-1569-4c78b7c3a1b2c3d4e5f60718293a4b5c6d7e8f90)" "1569" "a queue branch on main"
	_eq "$(pr_of gh-readonly-queue/release-0.5/pr-7-abcdef1)" "7" "a queue branch on a release branch, without refs/heads/"
	got=$(pr_of refs/heads/main) || got="none"
	_eq "$got" "none" "a branch that is not a queue branch has no number"
	got=$(pr_of 'refs/heads/gh-readonly-queue/main/pr-12-nothex') || got="none"
	_eq "$got" "none" "a malformed queue branch has no number"
	got=$(pr_of 'refs/heads/gh-readonly-queue/main/pr-12;rm-abcdef1') || got="none"
	_eq "$got" "none" "only digits come out, whatever the branch holds"
	if [ "$fail" -eq 0 ]; then echo "merge-queue-pr self-test: ok"; return 0; fi
	echo "merge-queue-pr self-test: FAIL"; return 1
}

case "${1:-}" in
	--self-test) self_test; exit $? ;;
	"") echo "usage: $0 <merge_group.head_ref> | --self-test" >&2; exit 2 ;;
	*) pr_of "$1" || { echo "not a merge queue branch: $1" >&2; exit 1; } ;;
esac
