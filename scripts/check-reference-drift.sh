#!/usr/bin/env bash
# Fail when the committed reference pages differ from what their generators
# write from the current source (#940).
#
# The website build regenerates the CLI, Go, OpenAPI and Python reference
# trees before Hugo runs, so the published site was always current, but the
# committed trees were not: nothing compared them, and the Go tree fell
# thousands of lines behind the code, so a reviewer reading it in the
# repository read signatures that no longer existed. This runs the three
# generators that need only Go and Python's standard library and fails with
# the command to run when the tree they write differs from the commit.
#
# The Python API subsite (website/static/python-api, gen-python.sh) is left
# out: rendering it installs pdoc and the Airflow Task SDK into a venv, too
# heavy for a gate on every pull request. The website build still regenerates
# it before publishing.
#
# Run by the proto-drift job of ci.yaml and by cut-release.sh's run_gates().
#
# Usage: scripts/check-reference-drift.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

TREES=(website/content/reference/cli website/content/reference/go website/static/openapi.yaml)

for tree in "${TREES[@]}"; do
	[ -e "$tree" ] || { echo "FAIL: $tree does not exist; update TREES in $0" >&2; exit 1; }
done
# changed_trees lists every path under TREES that differs from HEAD, staged,
# unstaged or untracked, so the generators never overwrite uncommitted edits
# and the verdict compares with the commit.
changed_trees() {
	git diff --name-only HEAD -- "${TREES[@]}"
	git ls-files --others --exclude-standard -- "${TREES[@]}"
}
if [ -n "$(changed_trees)" ]; then
	echo "FAIL: the reference trees differ from HEAD before generating; commit or stash them first:" >&2
	changed_trees | sed 's/^/  /' >&2
	exit 1
fi

./website/scripts/gen-cli.sh >/dev/null
./website/scripts/gen-openapi.sh >/dev/null
./website/scripts/gen-go.sh >/dev/null

drift="$(changed_trees)"
if [ -n "$drift" ]; then
	echo "FAIL: the committed reference pages are stale:" >&2
	printf '%s\n' "$drift" | sed 's/^/  /' >&2
	echo "Run ./website/scripts/gen-cli.sh, gen-openapi.sh and gen-go.sh from the repository root and commit the result." >&2
	exit 1
fi
echo "check-reference-drift: the CLI, OpenAPI and Go reference pages match their generators"
