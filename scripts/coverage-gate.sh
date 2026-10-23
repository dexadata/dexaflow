#!/usr/bin/env bash
# The unit-coverage floor (ADR 0011), in one place for the pre-commit hook and
# the CI "Enforce coverage floor" step. Each used to carry its own copy of the
# exclude list; the hook's fell behind CI's, measured 78.1% where CI measured
# 81.2% on the same commit, and stopped the v0.5.2-rc.1 cut at its prepare
# commit (#1504 realigned them by hand). Both now call this script.
#
# Usage:
#   scripts/coverage-gate.sh <coverprofile>
set -euo pipefail

FLOOR=80

# Excluded from the floor: generated code, main packages, version, and the
# process-orchestration of external binaries (Docker/initdb/pg_ctl) and the
# DB/lock bindings that only integration/e2e can exercise honestly.
#
# /test/ covers the load and soak harnesses (test/load/*, test/soak/*):
# `package main` programs that drive a real Postgres and a real control plane,
# the same category as /cmd/. Their pure logic IS tested
# (test/soak/monitor/*_test.go); what cannot be covered honestly here is their
# database and HTTP orchestration.
EXCLUDE='(/internal/storage/queries/|/internal/storage/repository\.go:|/internal/storage/postgres\.go:|/internal/storage/redis\.go:|/internal/storage/scheduler_store\.go:|/internal/storage/agent_store\.go:|/internal/storage/xcom_index\.go:|/internal/xcom/redis_backend\.go:|/internal/logs/tail\.go:|/internal/scheduler/leader\.go:|/internal/cli/managed_postgres\.go:|/internal/cli/backup_cmd\.go:|/internal/cli/restore_cmd\.go:|/internal/cli/forget\.go:|/internal/cli/dbt_manifest\.go:|\.pb\.go:|\.gen\.go:|/internal/version/|/cmd/|/test/)'

# package_table <coverprofile>: a Markdown table of the statement coverage of
# each package in the profile, lowest first, read from the profile itself
# (statements per block, covered when the block ran). A block that appears
# more than once (one line per test binary) counts once, covered if any run
# covered it.
package_table() {
	awk '
		NR == 1 && /^mode:/ { next }
		{
			split($1, loc, ":"); file = loc[1]
			pkg = file; sub(/\/[^\/]*$/, "", pkg)
			if (!($1 in seen)) { seen[$1] = 1; stmts[pkg] += $2 }
			if ($3 > 0 && !($1 in hit)) { hit[$1] = 1; covered[pkg] += $2 }
		}
		END {
			for (p in stmts) printf "%.1f\t%s\t%d\t%d\n", (stmts[p] ? 100 * covered[p] / stmts[p] : 0), p, covered[p], stmts[p]
		}
	' "$1" | sort -n -k1,1 -k2,2 | awk -F'\t' '
		BEGIN { print "| Package | Coverage | Statements |"; print "|---|---:|---:|" }
		{ printf "| `%s` | %s%% | %d / %d |\n", $2, $1, $3, $4 }
	'
}

# summary <total> <coverprofile>: the job summary of a run, the total against
# the floor and the per-package table, so the number is read on the pull
# request's checks rather than only kept as an artifact (#1572).
summary() {
	local verdict="above"
	awk -v t="$1" -v f="$FLOOR" 'BEGIN { exit !(t < f) }' && verdict="below"
	printf '## Unit test coverage\n\n'
	printf '**%s%%** of statements (filtered), %s the %s%% floor (ADR 0011).\n\n' "$1" "$verdict" "$FLOOR"
	printf '<details><summary>Coverage per package, lowest first</summary>\n\n'
	package_table "$2"
	printf '\n</details>\n'
}

# badge_json <total>: a shields.io endpoint document for the README badge
# (#1572), red below the floor, green from it and bright green from 90%. CI
# publishes it on the badges branch for each push to main.
badge_json() {
	local color
	color=$(awk -v t="$1" -v f="$FLOOR" 'BEGIN { print (t < f ? "red" : (t < 90 ? "green" : "brightgreen")) }')
	printf '{"schemaVersion":1,"label":"coverage","message":"%s%%","color":"%s"}\n' "$1" "$color"
}

self_test() {
	local tmp fail=0 out
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN
	printf '%s\n' 'mode: atomic' \
		'github.com/x/y/internal/a/a.go:1.1,2.2 3 1' \
		'github.com/x/y/internal/a/a.go:3.1,4.2 1 0' \
		'github.com/x/y/internal/b/b.go:1.1,2.2 2 0' \
		'github.com/x/y/internal/b/b.go:1.1,2.2 2 5' \
		'github.com/x/y/internal/c/c.go:1.1,2.2 4 0' \
		'github.com/x/y/internal/d/d.go:1.1,2.2 2 1' \
		'github.com/x/y/internal/d/d.go:1.1,2.2 2 3' \
		'github.com/x/y/internal/d/d.go:3.1,4.2 2 0' >"$tmp/cover.out"
	out=$(package_table "$tmp/cover.out")
	_expect() { # <name> <want-substring>
		case "$out" in *"$2"*) printf '  ok   %s\n' "$1" ;; *)
			printf '  FAIL %s: want %q in:\n%s\n' "$1" "$2" "$out"
			fail=1
			;;
		esac
	}
	_expect "a partly covered package" '| `github.com/x/y/internal/a` | 75.0% | 3 / 4 |'
	_expect "a block covered by any test binary counts once, covered" '| `github.com/x/y/internal/b` | 100.0% | 2 / 2 |'
	_expect "a block two test binaries covered counts once" '| `github.com/x/y/internal/d` | 50.0% | 2 / 4 |'
	_expect "an uncovered package" '| `github.com/x/y/internal/c` | 0.0% | 0 / 4 |'
	if [ "$(printf '%s\n' "$out" | sed -n 3p)" != '| `github.com/x/y/internal/c` | 0.0% | 0 / 4 |' ]; then
		echo "  FAIL the lowest package is not listed first"
		fail=1
	else echo "  ok   the lowest package is listed first"; fi
	out=$(summary 79.5 "$tmp/cover.out")
	_expect "the summary names a total below the floor" "**79.5%** of statements (filtered), below the ${FLOOR}% floor"
	out=$(summary 81.2 "$tmp/cover.out")
	_expect "the summary names a total above the floor" "**81.2%** of statements (filtered), above the ${FLOOR}% floor"
	out=$(badge_json 79.5)
	_expect "a badge below the floor is red" '{"schemaVersion":1,"label":"coverage","message":"79.5%","color":"red"}'
	out=$(badge_json "$FLOOR.0")
	_expect "a badge at the floor is green" '"color":"green"'
	out=$(badge_json 90.0)
	_expect "a badge from 90% is bright green" '"color":"brightgreen"'
	if [ "$fail" -eq 0 ]; then
		echo "coverage-gate self-test: ok"
		return 0
	fi
	echo "coverage-gate self-test: FAIL"
	return 1
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit $?
fi

profile="${1:?usage: coverage-gate.sh <coverprofile>}"
[ -s "$profile" ] || { echo "::error::coverage profile '$profile' is missing or empty"; exit 1; }

filtered="$(mktemp)"
trap 'rm -f "$filtered"' EXIT
grep -vE "$EXCLUDE" "$profile" >"$filtered"
total="$(go tool cover -func="$filtered" | tail -1 | awk '{print $3}' | tr -d '%')"

echo "Total coverage (filtered): ${total}% (floor: ${FLOOR}%)"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	summary "$total" "$filtered" >>"$GITHUB_STEP_SUMMARY"
fi
if [ -n "${GITHUB_OUTPUT:-}" ]; then
	echo "badge=$(badge_json "$total")" >>"$GITHUB_OUTPUT"
fi
if awk -v t="$total" -v f="$FLOOR" 'BEGIN { exit !(t < f) }'; then
	echo "::error::Coverage ${total}% is below floor ${FLOOR}% (see ADR 0011); add tests"
	exit 1
fi
