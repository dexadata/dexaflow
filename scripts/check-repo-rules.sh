#!/usr/bin/env bash
# Checks the text rules of AGENTS.md section 2 that a machine can check, so a
# contributor learns them from CI instead of from a review comment (#1573):
#
#   - no em dash (U+2014) in the lines a change ADDS: existing text keeps its em
#     dashes until a separate cleanup, so only added lines of the diff are read;
#   - no Co-Authored-By trailer, session link or generated-by footer in the
#     commit messages of the range;
#   - the same for the pull request description, when PR_BODY is set (the
#     guard workflow passes it).
#
# The other rules are checked elsewhere or not at all: the release milestone by
# milestone-guard.yaml, contiguous migration numbers by
# migrations/sequence_test.go, and "English everywhere" is left to review, since
# no check tells English from another language reliably.
#
# Every failure names the file and line (or the commit) it found.
#
# Usage:
#   scripts/check-repo-rules.sh <base-ref>   # the range <base-ref>...HEAD
#   scripts/check-repo-rules.sh --staged     # the staged diff (pre-commit hook)
#   scripts/check-repo-rules.sh --self-test
#
# Run bare (as cut-release.sh's run_gates() runs every scripts/check-*.sh), it
# has no range to judge and exits 0: a release branch carries text merged before
# this rule existed, and that text is not what this gate is about.
set -euo pipefail

EM_DASH=$'\xe2\x80\x94'

# Paths whose added lines are not text the contributor wrote: vendored or
# generated copies of upstream files, which must stay byte for byte as upstream
# ships them. Each entry is an extended regex matched against the repo path.
EXEMPT_PATHS_RE='^(internal/ui/assets/|test/om-contract/om_models\.py$|go\.sum$|package-lock\.json$|website/package-lock\.json$)'

# Lines that make a commit message or a PR description an attribution: a
# Co-Authored-By trailer, a *-Session trailer, a link to a session, or a
# "Generated with/by [tool](link)" footer. Matched case-insensitively.
ATTRIBUTION_RE='^[[:space:]]*co-authored-by:|^[[:space:]]*[a-z0-9-]*-session:|https?://[^[:space:]]*/session[_/-]|generated (with|by) [^[:space:]]*[[:space:]]*\[|^[^[:alnum:]]*generated (with|by) .*https?://'

# added_em_dashes reads a unified diff (git diff -U0 style) on stdin and prints
# "path:line: text" for each ADDED line that holds an em dash, skipping exempt
# paths. Kept a pure filter so --self-test can drive it without git.
added_em_dashes() {
	DASH="$EM_DASH" EXEMPT="$EXEMPT_PATHS_RE" awk '
		/^\+\+\+ / {
			path = substr($0, 5); sub(/^b\//, "", path)
			skip = (path == "/dev/null" || path ~ ENVIRON["EXEMPT"])
			next
		}
		/^@@ / {
			# @@ -a,b +c,d @@: the next added line is line c of the new file.
			split($3, plus, ","); line = substr(plus[1], 2) + 0
			next
		}
		/^\+/ {
			if (!skip && index($0, ENVIRON["DASH"]) > 0) { printf "%s:%d: %s\n", path, line, substr($0, 2) }
			line++
			next
		}
	'
}

# attribution_lines reads a text on stdin and prints "line N: text" for each
# line that is an attribution trailer, session link or generated-by footer.
attribution_lines() {
	grep -inE "$ATTRIBUTION_RE" | sed -E 's/^([0-9]+):/line \1: /' || true
}

fail=0
report() { # <heading> <findings>
	[ -n "$2" ] || return 0
	fail=1
	printf 'FAIL: %s\n' "$1"
	printf '%s\n' "$2" | sed 's/^/  /'
	echo
}

check_diff() { # <git diff args...>
	local found
	found="$(git diff --no-color --no-ext-diff -U0 "$@" | added_em_dashes)"
	report "em dash in added lines (AGENTS.md: use a comma, a colon, parentheses or two sentences)" "$found"
}

check_commits() { # <base-ref>
	local sha found all=""
	while IFS= read -r sha; do
		[ -n "$sha" ] || continue
		found="$(git log -1 --format=%B "$sha" | attribution_lines)"
		[ -n "$found" ] && all+="commit ${sha:0:12} $(git log -1 --format=%s "$sha")"$'\n'"$(printf '%s\n' "$found" | sed 's/^/  /')"$'\n'
	done < <(git rev-list --no-merges "$1..HEAD")
	report "attribution in commit messages (AGENTS.md: no Co-Authored-By, session links or generated-by footers)" "${all%$'\n'}"
}

check_body() {
	[ -n "${PR_BODY:-}" ] || return 0
	local found
	found="$(printf '%s\n' "$PR_BODY" | attribution_lines)"
	report "attribution in the pull request description" "$found"
	found="$(printf '%s\n' "$PR_BODY" | grep -nF "$EM_DASH" | sed -E 's/^([0-9]+):/line \1: /' || true)"
	report "em dash in the pull request description" "$found"
}

self_test() {
	local bad=0 got
	_expect() { # <name> <got> <want>
		if [ "$2" = "$3" ]; then printf '  ok   %s\n' "$1"; else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$1" "$2" "$3"; bad=1; fi
	}
	local diff
	diff="diff --git a/a.md b/a.md
--- a/a.md
+++ b/a.md
@@ -3,0 +4,2 @@
+plain line
+a ${EM_DASH} b
diff --git a/old.md b/old.md
--- a/old.md
+++ b/old.md
@@ -7 +7 @@
-x ${EM_DASH} removed
+x, kept
@@ -20,0 +21 @@
+ctx ${EM_DASH} new
diff --git a/internal/ui/assets/x.js b/internal/ui/assets/x.js
--- a/internal/ui/assets/x.js
+++ b/internal/ui/assets/x.js
@@ -0,0 +1 @@
+vendored ${EM_DASH} copy
diff --git a/gone.md b/gone.md
--- a/gone.md
+++ /dev/null
@@ -1 +0,0 @@
-deleted ${EM_DASH} text"
	got="$(printf '%s\n' "$diff" | added_em_dashes)"
	_expect "reports added em dashes with file and line, ignores removed, vendored and deleted" "$got" \
		"a.md:5: a ${EM_DASH} b
old.md:21: ctx ${EM_DASH} new"

	got="$(printf '%s\n' 'fix(x): thing' '' 'Body.' '' 'Co-authored-by: A <a@example.com>' | attribution_lines)"
	_expect "flags a Co-Authored-By trailer in any case" "$got" "line 5: Co-authored-by: A <a@example.com>"
	got="$(printf '%s\n' 'fix: y' '' 'Tool-Session: https://example.com/code/session_0123' | attribution_lines | wc -l | tr -d ' ')"
	_expect "flags a session trailer and link" "$got" "1"
	got="$(printf '%s\n' 'body' 'https://example.com/code/session_abc' | attribution_lines)"
	_expect "flags a bare session link" "$got" "line 2: https://example.com/code/session_abc"
	got="$(printf '%s\n' 'body' '' '_Generated by [Some Tool](https://example.com)_' | attribution_lines)"
	_expect "flags a generated-by footer" "$got" "line 3: _Generated by [Some Tool](https://example.com)_"
	got="$(printf '%s\n' 'fix(proto): regenerate' '' 'The stubs are generated by buf from proto/agent.proto.' 'See the sessions page.' | attribution_lines)"
	_expect "passes an ordinary message that says generated and session" "$got" ""

	# The script end to end, in a throwaway repository: added lines only, the
	# commit range, and the PR description.
	local tmp script out rc
	script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN
	_run() { # <commands...>: runs in $tmp with neutral git config, prints output and rc
		(
			exec 2>&1
			export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
			cd "$tmp" || exit 1
			"$@"
		)
	}
	_run sh -c "git init -q -b main . && git config user.email t@example.invalid && git config user.name t &&
		mkdir -p scripts && cp '$script' scripts/ &&
		printf 'old ${EM_DASH} text\n' > old.md && git add -A && git commit -qm base &&
		git update-ref refs/remotes/origin/main HEAD && git checkout -qb pr &&
		printf 'old ${EM_DASH} text\nnew, clean line\n' > old.md && git commit -qam 'docs: add a line'" >/dev/null
	rc=0; out="$(_run bash scripts/check-repo-rules.sh origin/main)" || rc=$?
	_expect "passes a range whose added lines are clean, despite an existing em dash" "$rc" "0"
	_run sh -c "printf 'added ${EM_DASH} here\n' >> old.md && git commit -qam 'docs: more' -m 'Co-Authored-By: X <x@example.com>'" >/dev/null
	rc=0; out="$(_run bash scripts/check-repo-rules.sh origin/main)" || rc=$?
	_expect "fails a range with an added em dash and a trailer" "$rc" "1"
	case "$out" in *"old.md:3: added"*) echo "  ok   names the file and line" ;; *) printf '  FAIL names the file and line\n    got: %q\n' "$out"; bad=1 ;; esac
	case "$out" in *"Co-Authored-By: X"*) echo "  ok   names the trailer" ;; *) printf '  FAIL names the trailer\n    got: %q\n' "$out"; bad=1 ;; esac
	rc=0; out="$(_run sh -c "git reset -q --hard HEAD~1 && PR_BODY='Fixes #1 ${EM_DASH} thing' bash scripts/check-repo-rules.sh origin/main")" || rc=$?
	_expect "fails an em dash in the PR description" "$rc" "1"
	rc=0; out="$(_run sh -c "printf 'staged ${EM_DASH} line\n' >> old.md && git add old.md && bash scripts/check-repo-rules.sh --staged")" || rc=$?
	_expect "fails an em dash in the staged diff" "$rc" "1"
	rc=0; out="$(_run sh -c "git reset -q --hard && unset GITHUB_BASE_REF; bash scripts/check-repo-rules.sh")" || rc=$?
	_expect "skips with no range (release cut)" "$rc" "0"

	if [ "$bad" -eq 0 ]; then echo "self-test: PASS"; else echo "self-test: FAIL"; return 1; fi
}

case "${1:-}" in
--self-test)
	self_test
	exit
	;;
--staged)
	cd "$(dirname "$0")/.."
	check_diff --cached
	;;
"")
	echo "OK: no range given (pass a base ref, or --staged): nothing to check."
	exit 0
	;;
*)
	cd "$(dirname "$0")/.."
	base="$1"
	# A base that does not resolve must fail, never pass: an empty range would
	# approve everything.
	git rev-parse --verify --quiet "$base^{commit}" >/dev/null ||
		{ echo "FAIL: base ref '$base' is not present; fetch it first (shallow clone?)"; exit 1; }
	check_diff "$base...HEAD"
	check_commits "$base"
	check_body
	;;
esac

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "OK: no em dash in added lines and no attribution in commits or the description."
