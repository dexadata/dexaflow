#!/usr/bin/env bash
# manifest_diff compares a discovered set with a committed manifest and names
# what differs. Sourced by cut-release.sh (the gate set) and
# check-script-selftests.sh (the self-test set).
#
# Both used to assert a floor pinned to the current count (MIN_GATES,
# MIN_SELFTESTS). A floor only notices shrinkage, it cannot see a rename and a
# replacement that keep the count, it reports a number instead of a name, and
# every addition had to remember to bump it (#982). Set equality against a
# committed list closes all four: adding or removing an entry is a diff that
# must touch the manifest, and the failure names the entry.
#
# Manifest format: one entry per line; blank lines and `#` comments ignored.
#
# Usage: . scripts/manifest-diff.sh; manifest_diff <manifest> <<<"$discovered"
#        scripts/manifest-diff.sh --self-test

manifest_diff() { # <manifest>, discovered entries on stdin
	local manifest="$1" want got missing unexpected msg=""
	if [ ! -f "$manifest" ]; then
		echo "manifest $manifest does not exist"
		return 1
	fi
	want="$(sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$manifest" | sed '/^$/d' | LC_ALL=C sort -u)"
	got="$(sed '/^$/d' | LC_ALL=C sort -u)"
	missing="$(LC_ALL=C comm -23 <(printf '%s\n' "$want") <(printf '%s\n' "$got") | sed '/^$/d' | paste -sd ' ' -)"
	unexpected="$(LC_ALL=C comm -13 <(printf '%s\n' "$want") <(printf '%s\n' "$got") | sed '/^$/d' | paste -sd ' ' -)"
	[ -z "$missing" ] && [ -z "$unexpected" ] && return 0
	[ -n "$missing" ] && msg="missing $missing"
	[ -n "$unexpected" ] && msg="${msg:+$msg; }unexpected $unexpected"
	echo "$msg (update $manifest if the change is intended)"
	return 1
}

self_test() {
	local fail=0 tmp out rc
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN
	printf '# a comment\n\nb.sh\na.sh  # trailing\n' >"$tmp/m.txt"
	_case() { # <name> <want-rc> <want-out> <discovered>
		rc=0
		out="$(manifest_diff "$tmp/m.txt" <<<"$4")" || rc=$?
		if [ "$rc" = "$2" ] && [[ "$out" == *"$3"* ]]; then
			printf '  ok   %s\n' "$1"
		else
			printf '  FAIL %s\n    rc=%s out=%q\n' "$1" "$rc" "$out"; fail=1
		fi
	}
	_case "equal sets pass in any order" 0 "" $'a.sh\nb.sh'
	_case "a missing entry is named" 1 "missing b.sh (update" $'a.sh'
	_case "an unexpected entry is named" 1 "unexpected c.sh" $'a.sh\nb.sh\nc.sh'
	_case "a rename keeps the count and still fails" 1 "missing b.sh; unexpected c.sh" $'a.sh\nc.sh'
	_case "nothing discovered fails" 1 "missing a.sh b.sh" ""
	rc=0; out="$(manifest_diff "$tmp/nope.txt" <<<"a.sh")" || rc=$?
	if [ "$rc" = 1 ] && [[ "$out" == *"does not exist"* ]]; then echo "  ok   a missing manifest fails"; else echo "  FAIL a missing manifest fails"; fail=1; fi
	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

if [ "${BASH_SOURCE[0]}" = "$0" ] && [ "${1:-}" = "--self-test" ]; then
	set -euo pipefail
	self_test
	exit $?
fi
