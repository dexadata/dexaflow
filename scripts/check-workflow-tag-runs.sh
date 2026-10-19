#!/usr/bin/env bash
# Keep path-filtered workflows off release tag pushes.
#
# A `push` trigger with `paths` (or `paths-ignore`) reads like "run when these
# files change", but GitHub ignores path filters for a tag push: such a
# workflow runs on every tag. secrets-e2e.yaml did exactly that, about 53
# minutes of k3d and LocalStack on v0.5.3, competing with the release jobs for
# runners while the rc had already exercised the same code (#1688).
#
# The rule: a `push` trigger that filters by path also filters by ref
# (`branches`, `branches-ignore`, `tags` or `tags-ignore`), so whether it runs
# on tags is written down rather than implied. A workflow that should run on a
# tag says `tags:`; a manual run on a tag goes through workflow_dispatch.
#
# Offline and argument-free, so it also runs as one of the release cut's
# gates.
#
# Usage:
#   scripts/check-workflow-tag-runs.sh [workflows-dir]
#   scripts/check-workflow-tag-runs.sh --self-test
set -euo pipefail

# push_filters <file>: "<line>:<keys>" for each `push:` trigger under the
# top-level `on:`, where <keys> are its filter keys, space separated. Comment
# and blank lines are skipped; an inline `on: [push]` has no filters to read.
push_filters() {
	awk '
		function indent(s) { match(s, /^ */); return RLENGTH }
		{ line = $0; sub(/(^|[[:space:]])#.*$/, "", line) }
		line !~ /[^[:space:]]/ { next }
		{ n = indent(line); key = line; sub(/^ */, "", key); sub(/:.*$/, "", key) }
		n == 0 { flush(); in_on = (line ~ /^("?on"?|true):[[:space:]]*$/); on_child = -1; next }
		!in_on { next }
		on_child < 0 { on_child = n }
		n == on_child { flush(); if (key == "push") { push_at = NR; push_child = -1 } ; next }
		push_at && n > on_child {
			if (push_child < 0) push_child = n
			if (n == push_child) keys = keys " " key
			next
		}
		function flush() { if (push_at) print push_at ":" keys; push_at = 0; keys = "" }
		END { flush() }
	' "$1"
}

# findings <dir>: one line per violation, "<file>:<line>: <what>".
findings() {
	local dir="$1" f hit keys
	for f in "$dir"/*.yml "$dir"/*.yaml; do
		[ -f "$f" ] || continue
		while IFS= read -r hit; do
			keys=" ${hit#*:} "
			case "$keys" in *" paths "* | *" paths-ignore "*) ;; *) continue ;; esac
			case "$keys" in *" branches "* | *" branches-ignore "* | *" tags "* | *" tags-ignore "*) continue ;; esac
			echo "$f:${hit%%:*}: push filters by path but not by ref, so it runs on every tag push"
		done < <(push_filters "$f")
	done
}

check() { # [dir]
	local dir="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.github/workflows}" out
	[ -d "$dir" ] || { echo "FAIL: no workflows directory at $dir" >&2; return 1; }
	out=$(findings "$dir")
	if [ -z "$out" ]; then
		echo "OK: every path-filtered push trigger also says which refs it runs on."
		return 0
	fi
	{
		echo "FAIL: GitHub ignores path filters on tag pushes, so these workflows run on every release tag:"
		printf '%s\n' "$out" | sed 's/^/  /'
		echo
		echo "Add 'branches: [main, \"release-*\"]' (or tags-ignore: [\"v*\"]) to the push trigger;"
		echo "keep workflow_dispatch for a manual run on a tag."
	} >&2
	return 1
}

self_test() {
	local tmp fail=0 rc
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN
	_case() { # <name> <want-rc> <file content>
		rm -f "$tmp"/w/*
		mkdir -p "$tmp/w"
		printf '%s\n' "$3" >"$tmp/w/x.yaml"
		rc=0
		check "$tmp/w" >/dev/null 2>&1 || rc=$?
		if [ "$rc" = "$2" ]; then printf '  ok   %s\n' "$1"; else
			printf '  FAIL %s: exit %s, want %s\n' "$1" "$rc" "$2"
			fail=1
		fi
	}
	_case "push with paths only fails" 1 $'on:\n  push:\n    paths:\n      - "internal/**"\njobs:\n  a: {}'
	_case "push with paths-ignore only fails" 1 $'on:\n  push:\n    paths-ignore: ["docs/**"]\njobs:\n  a: {}'
	_case "push with paths and branches passes" 0 $'on:\n  push:\n    branches: [main, "release-*"]\n    paths:\n      - "internal/**"'
	_case "push with paths and tags-ignore passes" 0 $'on:\n  push:\n    tags-ignore: ["v*"]\n    paths: ["internal/**"]'
	_case "push with paths and tags passes" 0 $'on:\n  push:\n    tags: ["v*"]\n    paths: ["internal/**"]'
	_case "push with branches only passes" 0 $'on:\n  push:\n    branches: [main]'
	_case "a bare push passes" 0 $'on:\n  push:\n  pull_request:'
	_case "pull_request with paths only passes" 0 $'on:\n  pull_request:\n    paths: ["internal/**"]\n  push:\n    branches: [main]'
	_case "an inline on list passes" 0 $'on: [push, pull_request]'
	_case "a commented branches does not count" 1 $'on:\n  push:\n    # branches: [main]\n    paths: ["internal/**"]'
	_case "paths under a job are not a trigger" 0 $'on:\n  workflow_dispatch:\njobs:\n  push:\n    paths: x'
	_case "four-space indentation fails too" 1 $'on:\n    push:\n        paths:\n            - "x/**"\n    workflow_dispatch:'
	_case "a quoted on key is read" 1 $'"on":\n  push:\n    paths: ["x/**"]'
	rc=0
	check "$tmp/nope" >/dev/null 2>&1 || rc=$?
	if [ "$rc" = 1 ]; then echo "  ok   a missing directory fails"; else
		echo "  FAIL a missing directory exited $rc"
		fail=1
	fi
	if [ "$fail" -eq 0 ]; then
		echo "check-workflow-tag-runs self-test: ok"
		return 0
	fi
	echo "check-workflow-tag-runs self-test: FAIL"
	return 1
}

case "${1:-}" in
--self-test)
	self_test
	exit $?
	;;
*)
	check "$@"
	exit $?
	;;
esac
