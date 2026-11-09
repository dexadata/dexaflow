#!/usr/bin/env bash
# Refuse a GA cut whose release candidate has no filled validation record
# (#1026).
#
# test/release/rc-cluster-validation.md is the checklist for what k3d cannot
# show (NetworkPolicy, cloud IAM, real StorageClasses, eviction). It stays
# manual, run by whoever has a cluster, but nothing used to say whether it ran:
# a GA could be promoted from an rc nobody installed, and when it did run its
# results lived in a chat transcript. This gates on the evidence, not on the
# run: a GA vX.Y.Z needs test/release/results/vX.Y.Z-rc.N.md for one of its
# rcs (the newest one present is read), a copy of the run header and the §6
# table, with
#
#   - the run header's "RC under test", "Image under test" and "Commit under
#     test" filled in (no <placeholder> left), the rc naming this version;
#   - every §6 row marked PASS, FAIL or NOT VERIFIED (a blank row reads as
#     "not run", which is the thing this gate exists to make impossible);
#   - every FAIL row naming the issue opened for it (#NNN in its notes), so a
#     GA over a failed row is a decision someone wrote down.
#
# NOT VERIFIED is a valid answer: a row this cluster cannot settle (§5) is
# recorded as such, never as PASS. A release candidate needs no record, since
# it is the thing being validated. GAs before ENFORCED_SINCE are not gated: the
# gate starts with the first minor patch cut after it merged, so a cut already
# in flight is not refused for a record nobody was asked to write.
#
# Usage: scripts/check-rc-validation.sh <tag>      (vX.Y.Z or vX.Y.Z-rc.N)
#        scripts/check-rc-validation.sh            (gate skipped: no tag)
#        scripts/check-rc-validation.sh --self-test
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
ENFORCED_SINCE="${ENFORCED_SINCE:-0.5.11}"

# version_lt <a> <b>: whether release version a sorts before b.
version_lt() {
	[ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" = "$1" ]
}

# check_record <file> <version>: prints every problem, returns 1 if any.
check_record() {
	local f="$1" version="$2" bad=0 field val rows=0 row state notes
	for field in "RC under test" "Image under test" "Commit under test"; do
		val="$(grep -E "^\| *${field} *\|" "$f" | head -1 | awk -F'|' '{print $3}' | sed 's/^ *//; s/ *$//')"
		if [ -z "$val" ] || [[ "$val" == *"<"*">"* ]]; then
			echo "  run header: \"$field\" is not filled in"
			bad=1
		fi
		if [ "$field" = "RC under test" ] && [ -n "$val" ] && [[ "$val" != *"v$version-rc."* ]]; then
			echo "  run header: \"RC under test\" is $val, not an rc of v$version"
			bad=1
		fi
	done
	# §6 rows: the table after the "## §6" heading, header and separator skipped.
	while IFS= read -r row; do
		rows=$((rows + 1))
		state="$(printf '%s' "$row" | awk -F'|' '{print $4}' | sed 's/^ *//; s/ *$//')"
		notes="$(printf '%s' "$row" | awk -F'|' '{print $5}')"
		case "$state" in
		PASS | "NOT VERIFIED"*) ;;
		FAIL)
			if ! printf '%s' "$notes" | grep -qE '#[0-9]+'; then
				echo "  §6 FAIL without the issue opened for it: $(printf '%s' "$row" | awk -F'|' '{print $2 "|" $3}')"
				bad=1
			fi
			;;
		*)
			echo "  §6 row not recorded (PASS, FAIL or NOT VERIFIED): $(printf '%s' "$row" | awk -F'|' '{print $2 "|" $3}')"
			bad=1
			;;
		esac
	done < <(awk '/^## §6/{s=1; next} s && /^## /{s=0} s && /^\|/' "$f" | sed '1,2d')
	if [ "$rows" -eq 0 ]; then
		echo "  no §6 results table"
		bad=1
	fi
	return "$bad"
}

# newest_record <version>: the record of the highest rc of version, or "".
newest_record() {
	local version="$1"
	[ -d "$ROOT/test/release/results" ] || return 0
	find "$ROOT/test/release/results" -maxdepth 1 -name "v$version-rc.*.md" |
		sed -E 's/.*-rc\.([0-9]+)\.md$/\1 &/' | sort -n | tail -1 | cut -d' ' -f2-
}

main() {
	local tag="${1:-}" version f problems
	if [ -z "$tag" ]; then
		echo "check-rc-validation: gate skipped (no tag; the cut passes the one it cuts)"
		return 0
	fi
	version="${tag#v}"
	if [[ "$version" == *-rc.* ]]; then
		echo "check-rc-validation: gate skipped ($tag is a release candidate, the thing being validated)"
		return 0
	fi
	if version_lt "$version" "$ENFORCED_SINCE"; then
		echo "check-rc-validation: gate skipped (v$version predates the gate, enforced since v$ENFORCED_SINCE)"
		return 0
	fi
	f="$(newest_record "$version")"
	if [ -z "$f" ]; then
		echo "FAIL: no validation record for any rc of v$version." >&2
		echo "Run test/release/rc-cluster-validation.md on the rc and commit its run header and §6 table as test/release/results/v$version-rc.N.md (template: test/release/results/TEMPLATE.md)." >&2
		return 1
	fi
	if ! problems="$(check_record "$f" "$version")"; then
		echo "FAIL: ${f#"$ROOT"/} is not a complete record:" >&2
		printf '%s\n' "$problems" >&2
		return 1
	fi
	echo "check-rc-validation: ${f#"$ROOT"/} is complete"
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' RETURN
	_record() { # <file> <rc> <image> <commit> <state of row 2> <notes of row 2>
		cat >"$1" <<REC
# Validation record

| Field | Value |
|---|---|
| RC under test | \`$2\` |
| Image under test | $3 |
| Commit under test | $4 |

## §6 Results + reporting

| # | Check | PASS/FAIL | Notes / evidence |
|---|---|---|---|
| §2 | smoke: task→success | PASS | run log |
| #726 | api has no tls.key (§4.1) | $5 | $6 |
| #725 | QoS Guaranteed + ClientIP (§4.3) | NOT VERIFIED (cloud) | k3d |
REC
	}
	_case() { # <name> <want exit> <tag>
		local got=0
		ROOT="$tmp" main "$3" >/dev/null 2>&1 || got=$?
		[ "$got" = "$2" ] || { echo "FAIL: $1: exit $got, want $2"; fail=1; }
	}
	_case "no tag is skipped" 0 ""
	_case "an rc is skipped" 0 v9.9.9-rc.1
	_case "a GA before the gate is skipped" 0 v0.5.10
	_case "v0.5.11 is the first GA gated" 1 v0.5.11
	_case "a GA with no record is refused" 1 v9.9.9
	mkdir -p "$tmp/test/release/results"
	_record "$tmp/test/release/results/v9.9.9-rc.1.md" v9.9.9-rc.1 "ghcr.io/x@sha256:abc" 0123abcd PASS ""
	_case "a complete record passes" 0 v9.9.9
	_record "$tmp/test/release/results/v9.9.9-rc.2.md" v9.9.9-rc.2 "ghcr.io/x@sha256:abc" 0123abcd "" ""
	_case "the newest rc's record is read, and a blank row is refused" 1 v9.9.9
	_record "$tmp/test/release/results/v9.9.9-rc.10.md" v9.9.9-rc.10 "ghcr.io/x@sha256:abc" 0123abcd FAIL "cert missing"
	_case "rc.10 sorts after rc.2, and a FAIL with no issue is refused" 1 v9.9.9
	_record "$tmp/test/release/results/v9.9.9-rc.10.md" v9.9.9-rc.10 "ghcr.io/x@sha256:abc" 0123abcd FAIL "opened #4242"
	_case "a FAIL naming its issue passes" 0 v9.9.9
	_record "$tmp/test/release/results/v9.9.9-rc.10.md" v9.9.9-rc.10 "<image>" 0123abcd PASS ""
	_case "a placeholder left in the header is refused" 1 v9.9.9
	_record "$tmp/test/release/results/v9.9.9-rc.10.md" v9.9.9-rc.3 "ghcr.io/x@sha256:abc" 0123abcd PASS ""
	_case "the header must name this rc's version" 0 v9.9.9
	_record "$tmp/test/release/results/v9.9.9-rc.10.md" v9.9.8-rc.3 "ghcr.io/x@sha256:abc" 0123abcd PASS ""
	_case "a header naming another version is refused" 1 v9.9.9
	if [ "$fail" = 0 ]; then echo "check-rc-validation self-test: PASS"; else echo "check-rc-validation self-test: FAIL"; return 1; fi
}

case "${1:-}" in
--self-test) self_test ;;
*) main "$@" ;;
esac
