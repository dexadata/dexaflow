#!/usr/bin/env bash
# check-values-doc-comments.sh — a chart values.yaml doc comment must not carry a
# second " --" on its delimiter line.
#
# helm-docs treats a space followed by two dashes as the doc-comment delimiter,
# and it takes the LAST one on the line. So a description that mentions a CLI
# flag in prose — `helm --set-string`, the shape that actually shipped — has its
# text and its `(bool)` type hint swallowed: the README row renders with an empty
# description and the type inferred from a nil value, i.e. `string` for what the
# chart documents and validates as a tri-state boolean.
#
# The helm-docs CI gate cannot catch this. It diffs the committed README against
# helm-docs' own output, and helm-docs produces the broken row deterministically,
# so the gate is SATISFIED by it. Nothing else compares the row against the
# comment that was meant to produce it — hence this script.
#
# Scope is the delimiter line only, and that is deliberate rather than lazy:
# continuation lines of a multi-line doc comment carry " --" through to the
# README intact (verified against helm-docs 1.14.2), as do plain `#` block
# comments, which helm-docs does not read at all. Flagging those would be noise.
#
# The fix is never to drop the flag from the prose: hug the dashes with a
# backtick so no space immediately precedes them (helm `--set-string`), which
# reads better in the rendered table anyway.
set -euo pipefail

cd "$(dirname "$0")/.."

fail=0

for values in helm/*/values.yaml; do
	[ -f "$values" ] || continue
	# Doc-comment delimiter lines whose text, after the delimiter, contains
	# another " --". `grep -n` keeps the line number for the report.
	hits=$(grep -nE '^[[:space:]]*#[[:space:]]--([[:space:]].*)?[[:space:]]--' "$values" || true)
	if [ -n "$hits" ]; then
		while IFS= read -r hit; do
			echo "FAIL $values:${hit%%:*}: a second \" --\" on a doc-comment line; helm-docs will swallow the description and the type hint" >&2
			echo "     ${hit#*:}" | cut -c1-160 >&2
		done <<<"$hits"
		fail=1
	fi
done

if [ "$fail" -ne 0 ]; then
	echo >&2
	echo "check-values-doc-comments: wrap the flag so no space precedes the dashes, e.g. helm \`--set-string\`, then re-run \`helm-docs -c helm/dexaflow\`." >&2
	exit 1
fi

echo "check-values-doc-comments: every values.yaml doc comment survives helm-docs"
