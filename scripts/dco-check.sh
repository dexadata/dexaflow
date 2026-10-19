#!/usr/bin/env bash
# Every commit of a pull request carries a Developer Certificate of Origin
# sign-off from its author (#1575).
#
# Dexaflow takes outside contributions into an open source core that a
# hosted product builds on, so the origin of each change matters. The DCO
# (https://developercertificate.org) is the lightest way to record it: the
# author adds `Signed-off-by: Name <email>` to each commit (`git commit -s`,
# or the prepare-commit-msg hook `make setup` installs), certifying they may
# contribute it under the project's license. Adopting it before outside
# contributions arrive means nobody is asked to rewrite published history.
#
# A commit passes when a Signed-off-by trailer names its author's email
# (case-insensitively). Merge commits are skipped, and so are commits by
# Dependabot, which signs nothing and whose changes are generated.
#
# Usage:
#   scripts/dco-check.sh --range <base> [<head>]
#   scripts/dco-check.sh --self-test
set -euo pipefail

# EXEMPT matches the author emails that need no sign-off.
EXEMPT='^[0-9]+\+dependabot\[bot\]@users\.noreply\.github\.com$'

# commit_finding <sha>: prints why the commit fails, or nothing.
commit_finding() {
	local sha="$1" email signoffs
	email=$(git log -1 --format=%ae "$sha")
	printf '%s' "$email" | grep -qE "$EXEMPT" && return 0
	signoffs=$(git log -1 --format=%B "$sha" | git interpret-trailers --parse | grep -i '^Signed-off-by:' | sed 's/^[^:]*: *//' || true)
	if [ -z "$signoffs" ]; then
		echo "commit $(git rev-parse --short "$sha") by <$email> has no Signed-off-by trailer"
		return 0
	fi
	if ! printf '%s\n' "$signoffs" | grep -qiF "<$email>"; then
		echo "commit $(git rev-parse --short "$sha") is signed off by $(printf '%s' "$signoffs" | paste -sd ',' -), not by its author <$email>"
	fi
}

check_range() { # <base> <head>
	local base="$1" head="$2" sha out=""
	while IFS= read -r sha; do
		out+=$(commit_finding "$sha")$'\n'
	done < <(git rev-list --no-merges "$base..$head")
	out=$(printf '%s' "$out" | sed '/^$/d')
	if [ -z "$out" ]; then
		echo "OK: every commit is signed off by its author (DCO)."
		return 0
	fi
	{
		echo "FAIL: these commits have no Developer Certificate of Origin sign-off from their author:"
		printf '%s\n' "$out" | sed 's/^/  /'
		echo
		echo "Sign off each commit with its author's name and email: 'git commit -s' for a new one,"
		echo "'git rebase --signoff $base' to add it to the commits of this branch, then push again."
		echo "CONTRIBUTING.md explains what the sign-off certifies."
	} >&2
	return 1
}

self_test() {
	local tmp fail=0 rc base out
	tmp=$(mktemp -d)
	trap 'rm -rf "${tmp:-}"' RETURN
	# A git hook exports these, and the fixture's commits would land in the
	# repository being committed to (scripts/AGENTS.md).
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_PREFIX
	export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
	(
		cd "$tmp" &&
			git -c init.defaultBranch=main init -q . &&
			git config user.email ada@example.invalid && git config user.name "Ada L" &&
			git commit -q --allow-empty -m base
	) >/dev/null 2>&1 || {
		echo "  FAIL self-test setup could not build the fixture repo"
		return 1
	}
	base=$(git -C "$tmp" rev-parse HEAD)
	_case() { # <name> <want-rc> <want-substring> <shell that adds commits>
		git -C "$tmp" reset -q --hard "$base"
		(cd "$tmp" && eval "$4") >/dev/null 2>&1
		rc=0
		out=$(cd "$tmp" && check_range "$base" HEAD 2>&1) || rc=$?
		if [ "$rc" = "$2" ] && case "$out" in *"$3"*) true ;; *) false ;; esac; then
			printf '  ok   %s\n' "$1"
		else
			printf '  FAIL %s: exit %s (want %s), output:\n%s\n' "$1" "$rc" "$2" "$out"
			fail=1
		fi
	}
	_case "a signed-off commit passes" 0 "OK:" 'git commit -q --allow-empty -s -m "feat: x"'
	_case "a commit without a sign-off fails" 1 "has no Signed-off-by trailer" 'git commit -q --allow-empty -m "feat: x"'
	_case "a sign-off by someone else fails" 1 "not by its author <ada@example.invalid>" \
		'git commit -q --allow-empty -m "feat: x" -m "Signed-off-by: Bob <bob@example.invalid>"'
	_case "the email is compared case-insensitively" 0 "OK:" \
		'git commit -q --allow-empty -m "feat: x" -m "Signed-off-by: Ada L <Ada@Example.invalid>"'
	_case "one unsigned commit among signed ones fails" 1 "has no Signed-off-by" \
		'git commit -q --allow-empty -s -m a && git commit -q --allow-empty -m b && git commit -q --allow-empty -s -m c'
	_case "a sign-off in the body, not a trailer, fails" 1 "has no Signed-off-by trailer" \
		'git commit -q --allow-empty -m "feat: x" -m "Signed-off-by: Ada L <ada@example.invalid> is what I mean to add later." -m "Refs #1"'
	_case "a Dependabot commit is exempt" 0 "OK:" \
		'git -c user.name="dependabot[bot]" -c user.email="49699333+dependabot[bot]@users.noreply.github.com" commit -q --allow-empty -m "build(deps): bump x"'
	_case "a merge commit is skipped" 0 "OK:" \
		'git checkout -q -b side && git commit -q --allow-empty -s -m side && git checkout -q main && git merge -q --no-ff --no-edit side'
	if [ "$fail" -eq 0 ]; then
		echo "dco-check self-test: ok"
		return 0
	fi
	echo "dco-check self-test: FAIL"
	return 1
}

case "${1:-}" in
--range)
	[ -n "${2:-}" ] || {
		echo "usage: $0 --range <base> [<head>]" >&2
		exit 2
	}
	check_range "$2" "${3:-HEAD}"
	;;
--self-test) self_test ;;
*)
	echo "usage: $0 --range <base> [<head>] | --self-test" >&2
	exit 2
	;;
esac
