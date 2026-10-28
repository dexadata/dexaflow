#!/usr/bin/env bash
# Fail if a file that configures one editor or coding assistant is committed.
#
# ADR 0069 splits contributor context in two: what a contributor needs to change
# Dexaflow correctly is public and tool-agnostic (AGENTS.md, the nested
# AGENTS.md files, recipes, specs), and files written for one tool stay local.
# .gitignore keeps them out of a normal `git add`, but `git add -f`, a rename or
# a tool that writes outside the ignored names would still slip one in; until
# #1550 the repository carried such a file. This gate checks the tracked tree.
#
# Usage:
#   scripts/check-no-tool-context.sh              # check the tracked tree
#   scripts/check-no-tool-context.sh --self-test
set -euo pipefail

# Extended regular expression over repository-relative paths.
PATTERN='(^|/)(CLAUDE(\.local)?\.md|CLAUDE\.md\.template|GEMINI\.md|COPILOT\.md|\.cursorrules|\.aider[^/]*)$|(^|/)\.(claude|cursor|windsurf|continue)/|^\.github/copilot-instructions\.md$|^\.local/'

# check reads paths on stdin and prints the offending ones; non-zero when any.
check() {
	local hits
	hits=$(grep -E "$PATTERN" || true)
	if [ -n "$hits" ]; then
		echo "FAIL: tool-specific context files are tracked (ADR 0069 keeps them local):"
		printf '  %s\n' $hits
		echo
		echo "Move what other contributors need into AGENTS.md or a nested AGENTS.md,"
		echo "then: git rm --cached <path>"
		return 1
	fi
}

self_test() {
	local bad good p
	bad=("CLAUDE.md" "sub/CLAUDE.local.md" ".github/CLAUDE.md.template" "GEMINI.md"
		".claude/settings.json" "x/.cursor/rules/a.mdc" ".cursorrules" ".aider.conf.yml"
		".github/copilot-instructions.md" ".local/notes.md" ".windsurf/rules.md")
	good=("AGENTS.md" "migrations/AGENTS.md" "docs/claude-notes.txt" "website/content/mcp/_index.md"
		"internal/claudeish/x.go" "local/readme.md")
	for p in "${bad[@]}"; do
		printf '%s\n' "$p" | check >/dev/null 2>&1 && { echo "self-test FAIL: accepted $p" >&2; return 1; }
	done
	for p in "${good[@]}"; do
		printf '%s\n' "$p" | check >/dev/null 2>&1 || { echo "self-test FAIL: rejected $p" >&2; return 1; }
	done
	echo "self-test OK"
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit
fi

cd "$(dirname "$0")/.."
git ls-files | check
echo "OK: no tool-specific context files are tracked"
