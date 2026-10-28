#!/usr/bin/env bash
# Runs every scripts/*.sh self-test.
#
# Release and gate scripts carry logic that nothing else exercises: the tag
# normalizers and FLAKE_RE in cut-release.sh (FLAKE_RE decides whether a red run
# is rerun or stops a release), and the ref comparisons in the check-* gates.
# Each keeps a `self_test()` behind `--self-test` — needing no network and
# running in milliseconds — and for a while nothing ran them, so the coverage
# rotted exactly where it was needed. Two real defects shipped that way: three
# FLAKE_RE alternatives that could never match, and a gate that failed every rc
# cut it was globbed into.
#
# Discovery is by the `self_test()` definition rather than a hand-kept list, so
# a new script's self-test joins this gate by existing (anywhere under scripts/
# or test/), the same property that makes cut-release.sh's run_gates() glob
# scripts/check-*.sh.
#
# run_gates() globs scripts/check-*.sh, so this is also part of the cut's own
# pre-flight.
set -euo pipefail
# A git hook exports GIT_DIR, GIT_INDEX_FILE and the like. Discovery below and
# every self-test that builds a fixture repository would follow them to the
# repository being committed to: the fixtures' commits and tags landed there.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_PREFIX
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# scripts/ AND test/, because a self-test nothing runs is the defect this gate
# exists to prevent, and it does not stop being that defect because the script
# lives one directory over. test/gcp/provision.sh and test/gcp/teardown.sh both
# shipped with a passing self-test that this loop could not see.
found=0 failed=0
# Discovery asks git what belongs to the repository, rather than walking the
# filesystem. It used to be a hand-kept pair of directories, scripts/ and test/,
# and the comment above already tells what that costs; it happened again when a
# self-tested helper landed in website/scripts/ci/ and this gate could not see
# it, so the self-test ran nowhere (#1242).
#
# Walking the filesystem instead was worse. It picked up a whole second copy of
# the repository under .claude/worktrees/ and a 28,000-file virtualenv under
# tmp/, taking the gate from 21 files to 28,696 and tripling its CPU time, and
# the fix for that would have been another exclusion list.
#
# `git ls-files` has neither problem: a new directory joins by existing, and
# nothing ignored, untracked or vendored is ever walked.
_selftest_candidates() { # <extension>
	git -C "$ROOT" ls-files -z "*.$1" 2>/dev/null | tr '\0' '\n' | sed "s|^|$ROOT/|" | sort
}

for f in $(_selftest_candidates sh); do
	grep -qE '^self_test\(\)' "$f" || continue
	found=$((found + 1))
	if out="$(bash "$f" --self-test 2>&1)"; then
		echo "OK: $(basename "$f") --self-test"
	else
		printf 'FAIL: %s --self-test\n%s\n' "$(basename "$f")" "$out" >&2
		failed=$((failed + 1))
	fi
done

# Python helpers get the same treatment, by the same discovery-by-existence
# rule. Not every script here is shell, and a report generator whose filter
# silently returns nothing fails exactly as quietly as a gate that always
# passes. Scripts without a self_test are skipped, as above.
#
# test/ is walked here for the same reason the shell loop above walks it, and
# because it was NOT walked here first: the shell loop grew a `find "$ROOT/test"`
# and the Python loop kept its scripts/ glob, so a self-tested Python helper
# under test/ was discovered by nobody and its gate existed only in its author's
# head. That asymmetry is the whole failure mode this file exists to prevent.
for f in $(_selftest_candidates py); do
	[ -f "$f" ] || continue
	grep -qE '^def self_test\(' "$f" || continue
	found=$((found + 1))
	if out="$(python3 "$f" --self-test 2>&1)"; then
		echo "OK: $(basename "$f") --self-test"
	else
		printf 'FAIL: %s --self-test\n%s\n' "$(basename "$f")" "$out" >&2
		failed=$((failed + 1))
	fi
done

# A rename or a lost self_test() would otherwise shrink this gate to nothing
# while still exiting 0 — the same silent-shrink hole run_gates guards against.
if [ "$found" -lt "${MIN_SELFTESTS:-20}" ]; then
	echo "FAIL: found only $found self-test(s), expected at least ${MIN_SELFTESTS:-20}. Did a script lose its self_test()?" >&2
	exit 1
fi
[ "$failed" -eq 0 ] || exit 1
echo "check-script-selftests: $found self-test(s) pass"
