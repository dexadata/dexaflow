#!/usr/bin/env bash
# Enforce the text rules of AGENTS.md on what a change adds (#1573).
#
# AGENTS.md section 2 asks for no em dashes in new text and no attribution
# trailers or footers in commit messages: no Co-Authored-By line, no session
# link, no generated-by footer. A maintainer enforced both by hand, so an
# outside contributor only learned them from a review comment. This script
# checks only what the change adds: the existing text keeps its em dashes
# until a cleanup of its own.
#
# Modes:
#   --range <base> [<head>]  added lines of base...head (default head: HEAD),
#                            and the message of every non-merge commit in
#                            base..head. CI runs this on a pull request.
#   --staged                 added lines of the staged changes. The pre-commit
#                            hook runs this.
#   --message <file>         one commit message. The commit-msg hook runs this.
#   --self-test
#
# Every finding names the file and line, or the commit, and the rule.
set -euo pipefail

# EM is U+2014, written as bytes so this file has none of its own.
EM=$(printf '\342\200\224')

# Added lines under these paths are not checked: fixtures may need the
# character on purpose.
SKIP_PATHS='(^|/)testdata/'

# added_findings: reads a unified diff (--unified=0) on stdin and prints
# "<file>:<line>: <rule>" for each added line holding an em dash.
added_findings() {
	awk -v em="$EM" -v skip="$SKIP_PATHS" '
		/^\+\+\+ / { file = substr($0, 5); sub(/^b\//, "", file); next }
		/^@@ / {
			split($3, plus, ","); line = substr(plus[1], 2) + 0; next
		}
		/^\+/ {
			if (file != "/dev/null" && file !~ skip && index(substr($0, 2), em) > 0)
				printf "%s:%d: added text has an em dash (U+2014); use a comma, a colon, parentheses or two sentences\n", file, line
			line++
			next
		}
	'
}

# message_findings <label>: reads a commit message on stdin and prints
# "<label>: <rule>: <line>" for each attribution trailer or footer. Comment
# lines (git's template) are skipped.
message_findings() {
	awk -v label="$1" '
		/^#/ { next }
		tolower($0) ~ /^co-authored-by:/ { report("Co-Authored-By trailer") ; next }
		$0 ~ /^[^A-Za-z0-9]*Generated (with|by) / && ($0 ~ /\]\(/ || $0 ~ /https?:\/\//) { report("generated-by footer"); next }
		$0 ~ /https?:\/\/[^ ]*\/sessions?[\/_-]/ { report("session link"); next }
		function report(rule) { printf "%s: %s (no attribution trailers or footers, AGENTS.md section 2): %s\n", label, rule, $0 }
	'
}

# range_findings <base> <head>
range_findings() {
	local base="$1" head="$2" sha
	git diff --unified=0 --no-color --no-ext-diff -M "$base...$head" | added_findings
	while IFS= read -r sha; do
		git log -1 --format=%B "$sha" | message_findings "commit $(git rev-parse --short "$sha")"
	done < <(git rev-list --no-merges "$base..$head")
}

report() { # <findings>
	if [ -z "$1" ]; then
		echo "OK: no em dash in added text and no attribution trailer in a commit message."
		return 0
	fi
	{
		echo "FAIL: the text rules of AGENTS.md section 2:"
		printf '%s\n' "$1" | sed 's/^/  /'
	} >&2
	return 1
}

main() {
	case "${1:-}" in
	--range)
		[ -n "${2:-}" ] || {
			echo "usage: $0 --range <base> [<head>]" >&2
			return 2
		}
		report "$(range_findings "$2" "${3:-HEAD}")"
		;;
	--staged)
		report "$(git diff --cached --unified=0 --no-color --no-ext-diff -M | added_findings)"
		;;
	--message)
		[ -f "${2:-}" ] || {
			echo "usage: $0 --message <file>" >&2
			return 2
		}
		report "$(message_findings "commit message" <"$2")"
		;;
	--self-test) self_test ;;
	*)
		echo "usage: $0 --range <base> [<head>] | --staged | --message <file> | --self-test" >&2
		return 2
		;;
	esac
}

self_test() {
	local tmp fail=0 rc out base
	tmp=$(mktemp -d)
	trap 'rm -rf "${tmp:-}"' RETURN
	# A git hook exports these, and the fixture's commits would land in the
	# repository being committed to (scripts/AGENTS.md).
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_PREFIX
	export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
	(
		cd "$tmp" &&
			git -c init.defaultBranch=main init -q . &&
			git config user.email t@example.invalid && git config user.name tester &&
			printf 'old line %s kept\nsecond\n' "$EM" >notes.md &&
			git add -A && git commit -qm base
	) >/dev/null 2>&1 || {
		echo "  FAIL self-test setup could not build the fixture repo"
		return 1
	}
	base=$(git -C "$tmp" rev-parse HEAD)

	_case() { # <name> <want-rc> <want-substring> <commit message> <shell to change the tree>
		local name="$1" want_rc="$2" want="$3" msg="$4" change="$5"
		git -C "$tmp" reset -q --hard "$base"
		(cd "$tmp" && eval "$change" && git add -A && git commit -q --allow-empty -m "$msg") >/dev/null 2>&1
		rc=0
		out=$(cd "$tmp" && main --range "$base" HEAD 2>&1) || rc=$?
		if [ "$rc" = "$want_rc" ] && case "$out" in *"$want"*) true ;; *) false ;; esac; then
			printf '  ok   %s\n' "$name"
		else
			printf '  FAIL %s: exit %s (want %s), output:\n%s\n' "$name" "$rc" "$want_rc" "$out"
			fail=1
		fi
	}
	_case "plain text and a plain message pass" 0 "OK:" "docs: add a note" "printf 'a, b\n' >>notes.md"
	_case "an added em dash fails with file and line" 1 "notes.md:3: added text has an em dash" "docs: add a note" "printf 'a %s b\n' \"$EM\" >>notes.md"
	_case "an em dash in a new file names its line" 1 "new.md:2: added text has an em dash" "docs: new" "printf 'one\ntwo %s\n' \"$EM\" >new.md"
	_case "an existing em dash is not a finding" 0 "OK:" "docs: touch" "printf 'more\n' >>notes.md"
	_case "an em dash under testdata is skipped" 0 "OK:" "test: fixture" "mkdir -p x/testdata && printf '%s\n' \"$EM\" >x/testdata/f.txt"
	_case "a Co-Authored-By trailer fails" 1 "Co-Authored-By trailer" $'fix: a thing\n\nCo-Authored-By: Someone <s@example.invalid>' "printf 'x\n' >>notes.md"
	_case "a lowercase co-authored-by fails" 1 "Co-Authored-By trailer" $'fix: a thing\n\nco-authored-by: Someone <s@example.invalid>' "printf 'x\n' >>notes.md"
	_case "a generated-by footer fails" 1 "generated-by footer" $'fix: a thing\n\nGenerated with [Some Tool](https://example.invalid/tool)' "printf 'x\n' >>notes.md"
	_case "prose that says generated by passes" 0 "OK:" $'build: regenerate\n\nGenerated by sqlc from the queries.' "printf 'x\n' >>notes.md"
	_case "a session link fails" 1 "session link" $'fix: a thing\n\nhttps://example.invalid/code/session_01ABC' "printf 'x\n' >>notes.md"
	_case "a message that names co-authors in prose passes" 0 "OK:" $'docs: thank the co-authors of the design' "printf 'x\n' >>notes.md"

	# --staged reads the index; --message reads a file.
	git -C "$tmp" reset -q --hard "$base"
	printf 'staged %s\n' "$EM" >>"$tmp/notes.md"
	git -C "$tmp" add notes.md
	rc=0
	out=$(cd "$tmp" && main --staged 2>&1) || rc=$?
	if [ "$rc" = 1 ] && case "$out" in *"notes.md:3:"*) true ;; *) false ;; esac; then
		echo "  ok   --staged finds an em dash in the index"
	else
		printf '  FAIL --staged: exit %s, output:\n%s\n' "$rc" "$out"
		fail=1
	fi
	printf 'fix: x\n\n# Co-Authored-By: in a comment is git template text\n' >"$tmp/msg"
	rc=0
	main --message "$tmp/msg" >/dev/null 2>&1 || rc=$?
	if [ "$rc" = 0 ]; then echo "  ok   --message skips comment lines"; else
		echo "  FAIL --message flagged a comment line"
		fail=1
	fi
	printf 'fix: x\n\nCo-Authored-By: A <a@example.invalid>\n' >"$tmp/msg"
	rc=0
	main --message "$tmp/msg" >/dev/null 2>&1 || rc=$?
	if [ "$rc" = 1 ]; then echo "  ok   --message finds a trailer"; else
		echo "  FAIL --message missed a trailer"
		fail=1
	fi

	if [ "$fail" -eq 0 ]; then
		echo "lint-new-text self-test: ok"
		return 0
	fi
	echo "lint-new-text self-test: FAIL"
	return 1
}

main "$@"
