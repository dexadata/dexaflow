#!/usr/bin/env bash
# Keep the workflows safe and green for pull requests from forks.
#
# Two rules, both about the first outside pull request (#1560):
#
# 1. No workflow triggers on `pull_request_target`. That event runs with the
#    base repository's secrets and a write token, and the moment a job in it
#    checks out the pull request's code, any fork can run code with them. The
#    guards that need to work for forks (milestone, changelog, docs) run on
#    `pull_request` with a read-only token and take their opt-outs from the
#    description instead of from privileged actions.
#
# 2. A workflow that runs on `pull_request` references no repository secret
#    other than GITHUB_TOKEN. A fork's pull request gets no secrets at all, so
#    such a job fails, or worse, silently tests nothing, for a reason its
#    author cannot fix. A job that really needs one guards itself with
#    `github.event.pull_request.head.repo.full_name == github.repository` and
#    reports a "skipped on fork" summary; its line then carries the marker
#    `fork-safe:` with the reason, and this check lets it through.
#
# Offline and argument-free, so it also runs as one of the release cut's
# gates.
#
# Usage:
#   scripts/check-fork-safe-workflows.sh [workflows-dir]
#   scripts/check-fork-safe-workflows.sh --self-test
set -euo pipefail

MARKER="fork-safe:"

# uncommented <file>: the file without comment-only lines and trailing
# comments, with line numbers ("<n>:<text>"). A comment that names an event is
# not a trigger.
uncommented() {
	awk '{ line = $0; sub(/(^|[[:space:]])#.*$/, "", line); if (line ~ /[^[:space:]]/) print NR ":" line }' "$1"
}

# triggers_on_pull_request <file>: true when `pull_request` is among its events.
triggers_on_pull_request() {
	uncommented "$1" | grep -qE '^[0-9]+:([[:space:]]*pull_request:|[[:space:]]*-[[:space:]]*pull_request[[:space:]]*$|on:.*[^_a-z]pull_request([^_a-z]|$))'
}

# findings <dir>: one line per violation, "<file>:<line>: <what>".
findings() {
	local dir="$1" f hit
	for f in "$dir"/*.yml "$dir"/*.yaml; do
		[ -f "$f" ] || continue
		while IFS= read -r hit; do
			echo "$f:${hit%%:*}: triggers on pull_request_target"
		done < <(uncommented "$f" | grep -E 'pull_request_target' || true)
		triggers_on_pull_request "$f" || continue
		while IFS= read -r hit; do
			sed -n "${hit%%:*}p" "$f" | grep -qF "$MARKER" && continue
			echo "$f:${hit%%:*}: uses a repository secret in a workflow that runs on pull_request ($(printf '%s' "$hit" | grep -oE 'secrets\.[A-Za-z0-9_]+' | grep -v 'secrets\.GITHUB_TOKEN' | head -1))"
		done < <(grep -nE 'secrets\.[A-Za-z0-9_]+' "$f" | grep -vE 'secrets\.GITHUB_TOKEN([^A-Za-z0-9_]|$)' | grep -vE '^[0-9]+:[[:space:]]*#' || true)
	done
}

check() { # [dir]
	local dir="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.github/workflows}" out
	[ -d "$dir" ] || { echo "FAIL: no workflows directory at $dir" >&2; return 1; }
	out=$(findings "$dir")
	if [ -z "$out" ]; then
		echo "OK: no pull_request_target, and no repository secret in a pull_request workflow."
		return 0
	fi
	{
		echo "FAIL: these workflow lines break pull requests from forks or expose secrets to them:"
		printf '%s\n' "$out" | sed 's/^/  /'
		echo
		echo "Use pull_request, never pull_request_target. A job that needs a secret skips itself on forks"
		echo "(head.repo.full_name == github.repository) and its line carries '${MARKER} <reason>'."
	} >&2
	return 1
}

self_test() {
	local tmp fail=0 rc
	tmp=$(mktemp -d); trap 'rm -rf "$tmp"' RETURN
	_case() { # <name> <want-rc> <file content>
		rm -f "$tmp"/w/*; mkdir -p "$tmp/w"
		printf '%s\n' "$3" >"$tmp/w/x.yaml"
		rc=0; check "$tmp/w" >/dev/null 2>&1 || rc=$?
		if [ "$rc" = "$2" ]; then printf '  ok   %s\n' "$1"; else printf '  FAIL %s: exit %s, want %s\n' "$1" "$rc" "$2"; fail=1; fi
	}
	_case "a plain pull_request workflow passes" 0 $'on:\n  pull_request:\njobs:\n  a:\n    steps:\n      - env:\n          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}'
	_case "pull_request_target fails" 1 $'on:\n  pull_request_target:\n    branches: [main]'
	_case "pull_request_target in a list fails" 1 $'on: [push, pull_request_target]'
	_case "pull_request_target named in a comment passes" 0 $'# never use pull_request_target here\non:\n  pull_request:'
	_case "a secret in a pull_request workflow fails" 1 $'on:\n  pull_request:\njobs:\n  a:\n    steps:\n      - env:\n          KEY: ${{ secrets.DEVTO_TOKEN }}'
	_case "a secret in an on-list pull_request workflow fails" 1 $'on: [push, pull_request]\njobs:\n  a:\n    steps:\n      - env:\n          KEY: ${{ secrets.X }}'
	_case "a secret in a dash-list pull_request workflow fails" 1 $'on:\n  - push\n  - pull_request\njobs:\n  a:\n    steps:\n      - env:\n          KEY: ${{ secrets.X }}'
	_case "a marked, fork-guarded secret passes" 0 $'on:\n  pull_request:\njobs:\n  a:\n    steps:\n      - env:\n          KEY: ${{ secrets.X }} # fork-safe: job skips on forks'
	_case "a secret in a push-only workflow passes" 0 $'on:\n  push:\n    branches: [main]\njobs:\n  a:\n    steps:\n      - env:\n          KEY: ${{ secrets.DEVTO_TOKEN }}'
	_case "GITHUB_TOKEN_X is not GITHUB_TOKEN" 1 $'on:\n  pull_request:\njobs:\n  a:\n    steps:\n      - env:\n          KEY: ${{ secrets.GITHUB_TOKEN_X }}'
	rc=0; check "$tmp/nope" >/dev/null 2>&1 || rc=$?
	if [ "$rc" = 1 ]; then echo "  ok   a missing directory fails"; else echo "  FAIL a missing directory exited $rc"; fail=1; fi
	if [ "$fail" -eq 0 ]; then echo "check-fork-safe-workflows self-test: ok"; return 0; fi
	echo "check-fork-safe-workflows self-test: FAIL"; return 1
}

case "${1:-}" in
	--self-test) self_test; exit $? ;;
	*) check "$@"; exit $? ;;
esac
