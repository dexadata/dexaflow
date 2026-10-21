#!/usr/bin/env bash
# Keep Go production files under 1,500 lines, and the files already over it
# from growing.
#
# #1580 set a 1,500-line target for Go files, and nothing enforced it while the
# splits waited: between 2026-10-09 and 2026-10-16 cmd/dexaflow-server/main.go
# grew from 2,893 to 3,335 lines and internal/storage/repository.go from 2,303
# to 2,748 (#1942). A target nobody checks drifts the wrong way.
#
# The rule, over every tracked .go file that is not a test (_test.go, testdata/)
# and not generated (a "// Code generated ... DO NOT EDIT." line before the
# package clause, which sqlc, protoc-gen-go and oapi-codegen all write):
#
#   - a file over LIMIT lines fails, unless scripts/go-file-length-allowlist.txt
#     names it;
#   - an allowlisted file fails when it grows past the length recorded there;
#   - an allowlisted file that shrank fails until its entry is lowered to the
#     new length, and one at or under LIMIT until its entry is removed, so the
#     recorded numbers only go down and the list only shrinks;
#   - an entry for a file that is gone, renamed, a test or generated fails, so
#     a rename cannot carry an allowance to a path nothing checks.
#
# Usage:
#   scripts/check-go-file-length.sh
#   scripts/check-go-file-length.sh --self-test
set -euo pipefail

LIMIT=1500
ALLOWLIST="scripts/go-file-length-allowlist.txt"

# ── Pure helpers, so --self-test can drive them without the real tree ───────

# is_candidate exits 0 when the repository-relative path $1 is a Go production
# file: a .go file that is not a test and not under a testdata directory.
is_candidate() { # <path>
	case "$1" in
	*_test.go | testdata/* | */testdata/*) return 1 ;;
	*.go) return 0 ;;
	esac
	return 1
}

# is_generated reads Go source on stdin and exits 0 when it carries the
# standard "// Code generated ... DO NOT EDIT." marker before the package
# clause (https://go.dev/s/generatedcode).
is_generated() {
	awk '
		/^\/\/ Code generated .* DO NOT EDIT\.$/ { found = 1; exit }
		/^package[[:space:]]/ { exit }
		END { exit !found }
	'
}

# measure reads repository-relative paths on stdin and prints
# "<path><TAB><lines>" for each one that is a Go production file, exists and is
# not generated.
measure() {
	local path
	while IFS= read -r path; do
		is_candidate "$path" || continue
		[ -f "$path" ] || continue
		is_generated <"$path" && continue
		printf '%s\t%s\n' "$path" "$(awk 'END { print NR }' "$path")"
	done
}

# judge reads measure's output on stdin and the allowlist from $2, prints one
# "FAIL: ..." line per violation of the rule above for limit $1, and exits
# non-zero when there is any.
#
# Entries are "<path> <lines>", one per line; "#" starts a comment.
judge() { # <limit> <allowlist>
	awk -v limit="$1" -v list="$2" -v q="'" '
		function fail(msg) { print "FAIL: " msg; bad = 1 }
		BEGIN {
			FS = "\t"
			n = 0
			while ((getline line < list) > 0) {
				n++
				sub(/#.*/, "", line)
				sub(/[[:space:]]+$/, "", line)
				sub(/^[[:space:]]+/, "", line)
				if (line == "") continue
				if (line !~ /^[^[:space:]]+[[:space:]]+[0-9]+$/) {
					fail(list ":" n ": malformed entry \"" line "\"; want \"<path> <lines>\"")
					continue
				}
				split(line, f, /[[:space:]]+/)
				p = f[1]; c = f[2] + 0
				if (p in allowed) { fail(list ":" n ": " p " is listed more than once"); continue }
				allowed[p] = c
				if (c <= limit) fail(list ":" n ": " p " is recorded at " c " lines, at or under the limit of " limit "; an entry is only for a file over it, so remove it")
			}
			close(list)
		}
		{
			p = $1; c = $2 + 0
			seen[p] = 1
			if (!(p in allowed)) {
				if (c > limit) fail(p " has " c " lines, over the limit of " limit ". Keep it at or under " limit ": split it by concern into files of the same package (#1580).")
				next
			}
			if (allowed[p] <= limit) next
			if (c > allowed[p]) {
				fail(p " grew to " c " lines; " list " pins it at " allowed[p] ". Move the new code to another file of the package instead of growing this one.")
			} else if (c <= limit) {
				fail(p " is down to " c " lines, at or under the limit of " limit ": remove its entry from " list ".")
			} else if (c < allowed[p]) {
				fail(p " shrank to " c " lines from " allowed[p] ": lower its entry to " q p " " c q " in " list ", so it cannot grow back.")
			}
		}
		END {
			for (p in allowed) if (!(p in seen)) fail(list ": " p " is listed but is not a Go production file any more (deleted, renamed, a test or generated): remove its entry.")
			exit bad
		}
	'
}

self_test() {
	local fail=0 tmp
	tmp=$(mktemp -d)
	# shellcheck disable=SC2064 # expand $tmp now: the script exits after this.
	trap "rm -rf '$tmp'" EXIT

	_yes() { # <name> <cmd...>
		local name=$1; shift
		if "$@"; then printf '  ok   %s\n' "$name"; else printf '  FAIL %s\n' "$name"; fail=1; fi
	}
	_no() { # <name> <cmd...>
		local name=$1; shift
		if "$@"; then printf '  FAIL %s\n' "$name"; fail=1; else printf '  ok   %s\n' "$name"; fi
	}
	# shellcheck disable=SC2329 # invoked through _yes and _no.
	_gen() { printf '%s' "$1" | is_generated; }
	# _judge <name> <want: pass|fail> <want-substring|""> <measurements> <allowlist>
	_judge() {
		local name=$1 want=$2 sub=$3 out rc=0
		printf '%s' "$5" >"$tmp/allow.txt"
		out=$(printf '%s' "$4" | judge 10 "$tmp/allow.txt") || rc=$?
		if [ "$want" = pass ] && [ "$rc" -ne 0 ]; then
			printf '  FAIL %s (wanted a pass)\n%s\n' "$name" "$out"; fail=1; return
		fi
		if [ "$want" = fail ] && [ "$rc" -eq 0 ]; then
			printf '  FAIL %s (wanted a failure)\n' "$name"; fail=1; return
		fi
		if [ -n "$sub" ] && [[ "$out" != *"$sub"* ]]; then
			printf '  FAIL %s\n    output: %s\n    wanted: %s\n' "$name" "$out" "$sub"; fail=1; return
		fi
		printf '  ok   %s\n' "$name"
	}

	# Which paths count.
	_yes "a production .go file is a candidate" is_candidate internal/api/server.go
	_yes "a root-level .go file is a candidate" is_candidate embed.go
	_no "a _test.go file is not" is_candidate internal/api/server_test.go
	_no "a file under testdata/ is not" is_candidate internal/parser/testdata/big.go
	_no "a top-level testdata/ file is not" is_candidate testdata/x.go
	_no "a non-Go file is not" is_candidate scripts/check-go-file-length.sh
	_no "a .go.tmpl file is not" is_candidate internal/gen/x.go.tmpl

	# Generated code, as each generator in this repository writes it.
	_yes "sqlc output is generated" _gen $'// Code generated by sqlc. DO NOT EDIT.\n// versions:\n//   sqlc v1.29.0\n\npackage queries\n'
	_yes "protoc-gen-go output is generated" _gen $'// Code generated by protoc-gen-go. DO NOT EDIT.\n// versions:\n\npackage agentv1\n'
	_yes "oapi-codegen output is generated" _gen $'// Package client provides primitives.\n//\n// Code generated by github.com/oapi-codegen/oapi-codegen/v2 version v2.4.1 DO NOT EDIT.\npackage client\n'
	_yes "a marker after a build constraint is generated" _gen $'//go:build linux\n\n// Code generated by stringer; DO NOT EDIT.\n\npackage x\n'
	_no "hand-written code is not generated" _gen $'// Package api serves HTTP.\npackage api\n\nfunc f() {}\n'
	_no "a marker after the package clause does not count" _gen $'package api\n\n// Code generated by sqlc. DO NOT EDIT.\n'
	_no "prose that mentions the marker does not count" _gen $'// This file is not Code generated by anything, edit freely.\npackage api\n'

	# measure, over a fixture tree.
	mkdir -p "$tmp/tree/a/testdata"
	printf 'package a\n\nfunc f() {}\n' >"$tmp/tree/a/a.go"
	printf 'package a\n\nfunc g() {}\nfunc h() {}\n' >"$tmp/tree/a/b.go"
	printf '// Code generated by sqlc. DO NOT EDIT.\n\npackage a\n' >"$tmp/tree/a/q.sql.go"
	printf 'package a\n' >"$tmp/tree/a/a_test.go"
	printf 'package a\n' >"$tmp/tree/a/testdata/t.go"
	local got want
	got=$(cd "$tmp/tree" && printf '%s\n' a/a.go a/a_test.go a/b.go a/gone.go a/q.sql.go a/testdata/t.go README.md | measure) || true
	want=$(printf 'a/a.go\t3\na/b.go\t4')
	if [ "$got" = "$want" ]; then printf '  ok   %s\n' "measure counts production files only"; else
		printf '  FAIL measure counts production files only\n    got:  %q\n    want: %q\n' "$got" "$want"; fail=1
	fi

	# judge, with a limit of 10.
	_judge "files at or under the limit pass" pass "" $'a.go\t3\nb.go\t10\n' ""
	_judge "a file over the limit fails" fail "a.go has 11 lines" $'a.go\t11\n' ""
	_judge "the failure says how to fix it" fail "split it" $'a.go\t11\n' ""
	_judge "an allowlisted file at its length passes" pass "" $'a.go\t20\nb.go\t3\n' $'# comment\n\na.go 20\n'
	_judge "an allowlisted file that grew fails" fail "a.go grew to 21 lines" $'a.go\t21\n' $'a.go 20\n'
	_judge "an allowlisted file that shrank must lower its entry" fail "lower its entry to 'a.go 15'" $'a.go\t15\n' $'a.go 20\n'
	_judge "an allowlisted file under the limit must leave the list" fail "remove its entry" $'a.go\t10\n' $'a.go 20\n'
	_judge "an entry for a file nothing measures fails" fail "gone.go" $'a.go\t3\n' $'gone.go 20\n'
	_judge "a duplicate entry fails" fail "more than once" $'a.go\t20\n' $'a.go 20\na.go 20\n'
	_judge "an entry at or under the limit fails" fail "at or under the limit" $'a.go\t9\n' $'a.go 9\n'
	_judge "a malformed entry fails" fail "malformed" $'a.go\t20\n' $'a.go 20\nb.go twenty\n'
	_judge "an entry with trailing spaces and tabs parses" pass "" $'a.go\t20\n' $'a.go\t20  \n'
	_judge "every violation is reported, not only the first" fail "b.go has 12 lines" $'a.go\t11\nb.go\t12\n' ""

	# The real allowlist must parse: a typo there would otherwise only show as a
	# confusing failure on the next unrelated PR.
	local root
	root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
	if [ -f "$root/$ALLOWLIST" ]; then
		local bad
		bad=$(sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$root/$ALLOWLIST" | grep -vE '^$|^[^[:space:]]+\.go[[:space:]]+[0-9]+$' || true)
		if [ -z "$bad" ]; then printf '  ok   %s\n' "the real allowlist parses"; else
			printf '  FAIL the real allowlist has malformed lines:\n%s\n' "$bad"; fail=1
		fi
	else
		printf '  FAIL %s is missing\n' "$ALLOWLIST"; fail=1
	fi

	[ "$fail" -eq 0 ] && echo "self-test: all passed"
	return "$fail"
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit $?
fi

cd "$(dirname "$0")/.."

[ -f "$ALLOWLIST" ] || { echo "FAIL: $ALLOWLIST not found" >&2; exit 1; }

if ! out=$(git ls-files -- '*.go' | measure | judge "$LIMIT" "$ALLOWLIST"); then
	printf '%s\n' "$out" >&2
	exit 1
fi

echo "OK: no Go production file is over $LIMIT lines outside $ALLOWLIST, and no allowlisted file grew"
