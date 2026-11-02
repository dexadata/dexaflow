#!/usr/bin/env bash
# Print the branch a version is cut from (ADR 0102, amending ADR 0062).
#
#   X.Y.0 and its candidates          main
#   X.Y.Z (Z > 0) and its candidates  main, unless .github/patch-branches.txt
#                                     maps the minor X.Y to a release branch
#
# Patches come from main for as long as everything on main may ship in the
# next patch of the current minor. The first change that may not (a feature
# for the next minor that is not ready to ship, a breaking change) opens a
# release branch for the minor: create release-X.Y from the minor's newest
# GA tag and add the line `X.Y release-X.Y` to .github/patch-branches.txt on
# main in the same pull request that brings that change. From then on the
# minor's patches come from the branch, and fixes reach it as cherry-picks.
#
# The mapping is read from origin/main when that ref exists, because the
# decision belongs to main whatever the checkout is (a cut can run from a
# release branch worktree); otherwise from this checkout. PATCH_BRANCHES_FILE
# overrides both (tests).
#
# One copy of this decision: cut-release.sh, docs-gap.sh, release-gap.sh and
# the docs gap workflow call this script rather than repeat it.
#
# Usage:
#   scripts/release-base.sh <version>      # prints main or release-X.Y
#   scripts/release-base.sh --self-test
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAP_PATH=".github/patch-branches.txt"

# mapping: print the mapping file's content (comments included; base_for
# ignores them).
mapping() {
	if [ -n "${PATCH_BRANCHES_FILE:-}" ]; then
		cat "$PATCH_BRANCHES_FILE" 2>/dev/null || true
	elif git -C "$ROOT" rev-parse -q --verify origin/main >/dev/null 2>&1; then
		git -C "$ROOT" show "origin/main:$MAP_PATH" 2>/dev/null || true
	else
		cat "$ROOT/$MAP_PATH" 2>/dev/null || true
	fi
}

# base_for <version>: reads the mapping on stdin, prints the base branch.
base_for() {
	local v="${1#v}" patch minor b
	v="${v%%-*}"; patch="${v##*.}"; minor="${v%.*}"
	if [ "$patch" = 0 ]; then printf 'main'; return; fi
	b="$(sed 's/#.*//' | awk -v m="$minor" '($1 "") == (m "") { print $2; exit }')"
	printf '%s' "${b:-main}"
}

self_test() {
	local fail=0 map
	_eq() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got '$2', want '$3'"; fail=1; fi; }
	map=$'# minor branch\n0.4 release-0.4\n0.5 release-0.5 # opened for 0.6 work\n'
	_eq "a minor GA is cut from main"            "$(printf '%s' "$map" | base_for 0.5.0)"      main
	_eq "a minor rc is cut from main"            "$(printf '%s' "$map" | base_for 0.6.0-rc.2)" main
	_eq "a mapped patch uses its branch"          "$(printf '%s' "$map" | base_for 0.5.3)"      release-0.5
	_eq "a mapped patch rc uses its branch"       "$(printf '%s' "$map" | base_for v0.4.9-rc.1)" release-0.4
	_eq "an unmapped patch is cut from main"      "$(printf '%s' "$map" | base_for 0.6.4)"      main
	_eq "no mapping file means main"              "$(printf '' | base_for 0.5.9)"               main
	_eq "a commented line maps nothing"           "$(printf '# 0.7 release-0.7\n' | base_for 0.7.1)" main
	_eq "minor 0.5 does not match 0.50"           "$(printf '0.50 release-0.50\n' | base_for 0.5.2)" main
	_eq "patch 10 is still a patch"               "$(printf '0.5 release-0.5\n' | base_for 0.5.10)" release-0.5
	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

case "${1:-}" in
	--self-test) self_test; exit $? ;;
	"" | -*) echo "usage: release-base.sh <version> | --self-test" >&2; exit 2 ;;
esac
[[ "${1#v}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] || { echo "release-base.sh: invalid version '$1'" >&2; exit 2; }
mapping | base_for "$1"
echo
