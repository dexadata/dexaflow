#!/usr/bin/env bash
# The unit-coverage floor (ADR 0011), in one place for the pre-commit hook and
# the CI "Enforce coverage floor" step. Each used to carry its own copy of the
# exclude list; the hook's fell behind CI's, measured 78.1% where CI measured
# 81.2% on the same commit, and stopped the v0.5.2-rc.1 cut at its prepare
# commit (#1504 realigned them by hand). Both now call this script.
#
# Usage:
#   scripts/coverage-gate.sh <coverprofile> [<base-coverprofile> <base-label>]
#
# With a base profile (CI fetches main's with scripts/coverage-base.sh), the
# job summary also shows the change per package against it. The base only
# informs: the floor is the one gate.
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

# package_stats <coverprofile>: one "<percent>\t<package>\t<covered>\t<statements>"
# line per package, read from the profile itself (statements per block,
# covered when the block ran). A block that appears more than once (one line
# per test binary) counts once, covered if any run covered it. The package
# "*" is the whole profile.
package_stats() {
	awk '
		NR == 1 && /^mode:/ { next }
		{
			split($1, loc, ":"); file = loc[1]
			pkg = file; sub(/\/[^\/]*$/, "", pkg)
			if (!($1 in seen)) { seen[$1] = 1; stmts[pkg] += $2; stmts["*"] += $2 }
			if ($3 > 0 && !($1 in hit)) { hit[$1] = 1; covered[pkg] += $2; covered["*"] += $2 }
		}
		END {
			for (p in stmts) printf "%.1f\t%s\t%d\t%d\n", (stmts[p] ? 100 * covered[p] / stmts[p] : 0), p, covered[p], stmts[p]
		}
	' "$1"
}

# package_table <coverprofile>: a Markdown table of the statement coverage of
# each package in the profile, lowest first.
package_table() {
	package_stats "$1" | awk -F'\t' '$2 != "*"' | sort -n -k1,1 -k2,2 | awk -F'\t' '
		BEGIN { print "| Package | Coverage | Statements |"; print "|---|---:|---:|" }
		{ printf "| `%s` | %s%% | %d / %d |\n", $2, $1, $3, $4 }
	'
}

# delta_table <base-coverprofile> <coverprofile> <base-label>: the packages
# whose coverage moved against the base by 0.1 point or more, largest drop
# first, then the packages only one side has; one line saying so when none
# did. The percentages come from the statement counts, not from the rounded
# figures, so a change is never an artifact of rounding.
delta_table() {
	local rows
	rows=$({
		package_stats "$1" | sed 's/^/B\t/'
		package_stats "$2" | sed 's/^/H\t/'
	} | awk -F'\t' -v label="$3" '
		$3 == "*" { next }
		{ pct = ($5 ? 100 * $4 / $5 : 0); seen[$3] = 1 }
		$1 == "B" { base[$3] = pct }
		$1 == "H" { head[$3] = pct }
		END {
			for (p in seen) {
				if (!(p in base)) printf "2\t0\t| `%s` | | %.1f%% | new |\n", p, head[p]
				else if (!(p in head)) printf "3\t0\t| `%s` | %.1f%% | | removed |\n", p, base[p]
				else {
					d = head[p] - base[p]
					if (d >= 0.05 || d <= -0.05) printf "1\t%.4f\t| `%s` | %.1f%% | %.1f%% | %+.1f |\n", d, p, base[p], head[p], d
				}
			}
		}
	' | sort -t "$(printf '\t')" -k1,1n -k2,2n -k3,3 | cut -f3-)
	if [ -n "$rows" ]; then
		printf '| Package | %s | This run | Change (points) |\n|---|---:|---:|---:|\n' "$3"
		printf '%s\n' "$rows"
	else
		printf 'No package moved by 0.1 point or more against %s.\n' "$3"
	fi
}

# total_delta <base-coverprofile> <coverprofile>: the change of the total, in
# points, from the statement counts of both profiles.
total_delta() {
	local b h
	b=$(package_stats "$1" | awk -F'\t' '$2 == "*" { print ($4 ? 100 * $3 / $4 : 0) }')
	h=$(package_stats "$2" | awk -F'\t' '$2 == "*" { print ($4 ? 100 * $3 / $4 : 0) }')
	awk -v b="${b:-0}" -v h="${h:-0}" 'BEGIN { printf "%+.1f", h - b }'
}

# summary <total> <coverprofile> [<base-coverprofile> <base-label>]: the job
# summary of a run, the total against the floor and the per-package table,
# so the number is read on the pull request's checks rather than only kept as
# an artifact (#1572). With a base, also the change against it, per package;
# a base that could not be fetched (an empty or missing file) is named as
# such instead.
summary() {
	local verdict="above" against=""
	awk -v t="$1" -v f="$FLOOR" 'BEGIN { exit !(t < f) }' && verdict="below"
	if [ $# -ge 4 ] && [ -s "$3" ]; then
		against=", **$(total_delta "$3" "$2")** points against $4"
	fi
	printf '## Unit test coverage\n\n'
	printf '**%s%%** of statements (filtered), %s the %s%% floor (ADR 0011)%s.\n\n' "$1" "$verdict" "$FLOOR" "$against"
	if [ $# -ge 4 ]; then
		if [ -s "$3" ]; then
			printf '<details open><summary>Change per package against %s</summary>\n\n' "$4"
			delta_table "$3" "$2" "$4"
			printf '\n</details>\n\n'
		else
			printf 'No coverage profile of %s was available, so there is no per-package change.\n\n' "$4"
		fi
	fi
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
	printf '%s\n' 'mode: atomic' \
		'github.com/x/y/internal/a/a.go:1.1,2.2 3 1' \
		'github.com/x/y/internal/a/a.go:3.1,4.2 1 1' \
		'github.com/x/y/internal/b/b.go:1.1,2.2 2 0' \
		'github.com/x/y/internal/b/b.go:1.1,2.2 2 5' \
		'github.com/x/y/internal/c/c.go:1.1,2.2 4 0' \
		'github.com/x/y/internal/d/d.go:1.1,2.2 2 0' \
		'github.com/x/y/internal/d/d.go:3.1,4.2 2 0' \
		'github.com/x/y/internal/e/e.go:1.1,2.2 1 1' >"$tmp/base.out"
	out=$(delta_table "$tmp/base.out" "$tmp/cover.out" "main at 1a2b3c4d")
	_expect "the table names the base" '| Package | main at 1a2b3c4d | This run | Change (points) |'
	_expect "a drop is a negative change" '| `github.com/x/y/internal/a` | 100.0% | 75.0% | -25.0 |'
	_expect "a gain is a positive change" '| `github.com/x/y/internal/d` | 0.0% | 50.0% | +50.0 |'
	_expect "a package only the base has is removed" '| `github.com/x/y/internal/e` | 100.0% | | removed |'
	case "$out" in *internal/b*|*internal/c*) echo "  FAIL an unchanged package is listed"; fail=1 ;; *) echo "  ok   an unchanged package is not listed" ;; esac
	if [ "$(printf '%s\n' "$out" | sed -n 3p)" != '| `github.com/x/y/internal/a` | 100.0% | 75.0% | -25.0 |' ]; then
		echo "  FAIL the largest drop is not listed first"
		fail=1
	else echo "  ok   the largest drop is listed first"; fi
	out=$(delta_table "$tmp/cover.out" "$tmp/base.out" "main at 1a2b3c4d")
	_expect "a package only this run has is new" '| `github.com/x/y/internal/e` | | 100.0% | new |'
	out=$(delta_table "$tmp/cover.out" "$tmp/cover.out" "main at 1a2b3c4d")
	_expect "no change says so" 'No package moved by 0.1 point or more against main at 1a2b3c4d.'
	out=$(summary 81.2 "$tmp/cover.out" "$tmp/base.out" "main at 1a2b3c4d")
	# base: 4+2+0+0+1 = 7 of 15 covered (46.7%); this run: 3+2+0+2 = 7 of 14 (50.0%).
	_expect "the summary names the change of the total" 'floor (ADR 0011), **+3.3** points against main at 1a2b3c4d.'
	_expect "the summary has the per-package change" 'Change per package against main at 1a2b3c4d'
	: >"$tmp/empty.out"
	out=$(summary 81.2 "$tmp/cover.out" "$tmp/empty.out" "main")
	_expect "a missing base is named, not failed on" 'No coverage profile of main was available, so there is no per-package change.'
	out=$(summary 81.2 "$tmp/cover.out")
	case "$out" in *"Change per package"*|*"No coverage profile"*) echo "  FAIL a summary without a base mentions one"; fail=1 ;; *) echo "  ok   a summary without a base has no delta" ;; esac
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

filtered="$(mktemp)" base_filtered="$(mktemp)"
trap 'rm -f "$filtered" "$base_filtered"' EXIT
grep -vE "$EXCLUDE" "$profile" >"$filtered"
base_args=()
if [ $# -ge 2 ]; then
	# The same exclude list on both sides, or a change to it would read as a
	# coverage change. A missing or empty base leaves an empty file, which
	# the summary names as no profile available.
	grep -vE "$EXCLUDE" "$2" >"$base_filtered" 2>/dev/null || true
	base_args=("$base_filtered" "${3:-the base branch}")
fi
total="$(go tool cover -func="$filtered" | tail -1 | awk '{print $3}' | tr -d '%')"

echo "Total coverage (filtered): ${total}% (floor: ${FLOOR}%)"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	summary "$total" "$filtered" ${base_args[@]+"${base_args[@]}"} >>"$GITHUB_STEP_SUMMARY"
fi
if [ -n "${GITHUB_OUTPUT:-}" ]; then
	echo "badge=$(badge_json "$total")" >>"$GITHUB_OUTPUT"
fi
if awk -v t="$total" -v f="$FLOOR" 'BEGIN { exit !(t < f) }'; then
	echo "::error::Coverage ${total}% is below floor ${FLOOR}% (see ADR 0011); add tests"
	exit 1
fi
