#!/usr/bin/env bash
# Checks that scripts/pre-commit keeps the hook's repository variables away
# from the tests it runs (#1959).
#
# `git commit` exports GIT_INDEX_FILE to a hook, and GIT_DIR too when the
# commit is made in a linked worktree, where it points into the main
# repository. The hook runs `go test ./...`, and a test that ran `git init` in
# a temp dir with those variables re-initialized the main repository instead:
# a contributor's checkout ended up with core.bare = true, and git then failed
# there and in every worktree with "this operation must be run in a work tree".
#
# The check installs scripts/pre-commit in a scratch repository, with stubs
# for go, gofmt, golangci-lint, ruff and the scripts the hook calls, and
# commits from a linked worktree of it. The stubbed `go test` does what that
# test did. The scratch repository must keep core.bare unset or false and
# keep its commits and branches, and `go test` must not see the variables.
#
# Usage: check-pre-commit-git-env.sh [--hook <path>] | --self-test
set -euo pipefail
# This script builds fixture repositories itself, and a hook may run it.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_PREFIX
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# check_hook <hook>: 0 when a commit from a worktree with <hook> installed
# leaves the main repository alone, 1 with the reasons on stderr otherwise.
check_hook() {
	local hook="$1" tmp main bin rc=0 out
	tmp="$(mktemp -d)"
	main="$tmp/main" bin="$tmp/bin"
	mkdir -p "$main/scripts" "$bin"

	# The stubs. `go test` runs git in a temp dir, as the test of the incident
	# did, and records the repository variables it was given.
	cat >"$bin/go" <<-'EOF'
		#!/usr/bin/env bash
		[ "${1:-}" = test ] || exit 0
		env | grep -E '^GIT_(DIR|WORK_TREE|INDEX_FILE|OBJECT_DIRECTORY|ALTERNATE_OBJECT_DIRECTORIES|COMMON_DIR|PREFIX)=' >>"$HOOK_ENV_LOG" || true
		d="$(mktemp -d)"
		git -C "$d" init -q
		rm -rf "$d"
	EOF
	printf '#!/bin/sh\nexit 0\n' >"$bin/gofmt"
	cp "$bin/gofmt" "$bin/golangci-lint"
	cp "$bin/gofmt" "$bin/ruff"
	cp "$bin/gofmt" "$main/scripts/lint-new-text.sh"
	cp "$bin/gofmt" "$main/scripts/coverage-gate.sh"
	chmod +x "$bin"/* "$main/scripts"/*.sh

	local -a ident=(-c user.name=check -c user.email=check@example.invalid)
	git -c init.defaultBranch=main init -q "$main"
	git -C "$main" "${ident[@]}" add -A
	git -C "$main" "${ident[@]}" commit -q -m base
	git -C "$main" worktree add -q -b wt "$tmp/wt" 2>/dev/null
	install -m 0755 "$hook" "$main/.git/hooks/pre-commit"
	local before
	before="$(git -C "$main" for-each-ref --format='%(refname) %(objectname)')"

	echo change >"$tmp/wt/file"
	git -C "$tmp/wt" add file
	if ! out="$(cd "$tmp/wt" && PATH="$bin:$PATH" HOOK_ENV_LOG="$tmp/env.log" \
		git "${ident[@]}" commit -q -m change 2>&1)"; then
		printf 'the commit from the worktree failed:\n%s\n' "$out" >&2
		rc=1
	fi

	local bare
	bare="$(git config --file "$main/.git/config" core.bare 2>/dev/null || true)"
	if [ "$bare" = true ]; then
		echo "the main repository now has core.bare = true" >&2
		rc=1
	fi
	if [ -s "$tmp/env.log" ]; then
		printf 'go test inherited the repository variables:\n%s\n' "$(sort -u "$tmp/env.log")" >&2
		rc=1
	fi
	# The worktree's own branch moves by one commit; nothing else may change.
	local after
	after="$(git --git-dir="$main/.git" for-each-ref --format='%(refname) %(objectname)' | grep -v '^refs/heads/wt ')"
	if [ "$after" != "$(grep -v '^refs/heads/wt ' <<<"$before")" ]; then
		printf 'the main repository refs changed:\nbefore: %s\nafter:  %s\n' "$before" "$after" >&2
		rc=1
	fi
	rm -rf "$tmp"
	return "$rc"
}

self_test() {
	local tmp fail=0
	tmp="$(mktemp -d)"
	# The hook of this checkout passes.
	if check_hook "$ROOT/scripts/pre-commit" 2>"$tmp/err"; then
		echo "ok: scripts/pre-commit leaves the main repository alone"
	else
		echo "FAIL: scripts/pre-commit: $(cat "$tmp/err")" >&2
		fail=1
	fi
	# The same hook without its unset line is the one of the incident: the
	# check must catch it, or it proves nothing.
	grep -v '^unset GIT_DIR' "$ROOT/scripts/pre-commit" >"$tmp/leaky"
	if check_hook "$tmp/leaky" 2>"$tmp/err"; then
		echo "FAIL: a hook that keeps GIT_DIR passed the check" >&2
		fail=1
	elif grep -q 'core.bare = true' "$tmp/err"; then
		echo "ok: a hook that keeps GIT_DIR is caught (core.bare = true)"
	else
		echo "FAIL: a hook that keeps GIT_DIR failed for another reason: $(cat "$tmp/err")" >&2
		fail=1
	fi
	rm -rf "$tmp"
	[ "$fail" -eq 0 ]
}

case "${1:-}" in
--self-test) self_test ;;
--hook) check_hook "${2:?--hook needs a path}" && echo "check-pre-commit-git-env: ok" ;;
"") check_hook "$ROOT/scripts/pre-commit" && echo "check-pre-commit-git-env: ok" ;;
*)
	echo "usage: $0 [--hook <path>] | --self-test" >&2
	exit 2
	;;
esac
