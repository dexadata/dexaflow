#!/usr/bin/env bash
# The unit-coverage floor (ADR 0011), in one place for the pre-commit hook and
# the CI "Enforce coverage floor" step. Each used to carry its own copy of the
# exclude list; the hook's fell behind CI's, measured 78.1% where CI measured
# 81.2% on the same commit, and stopped the v0.5.2-rc.1 cut at its prepare
# commit (#1504 realigned them by hand). Both now call this script.
#
# When GITHUB_STEP_SUMMARY is set (a GitHub Actions step), the total and the
# per-package coverage are also written to the job summary, so the number is on
# the run page of every PR instead of only inside the uploaded artifact (#1572).
#
# Usage:
#   scripts/coverage-gate.sh <coverprofile>
#   scripts/coverage-gate.sh --self-test
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

# per_package <filtered-profile>: one Markdown table row per package, with the
# share of its statements covered, sorted by package path. A profile line is
# `file:start.col,end.col <statements> <hit-count>`; the package is the file's
# directory.
per_package() {
	awk '
		NR == 1 && /^mode:/ { next }
		{
			split($1, loc, ":"); file = loc[1]
			pkg = file; sub(/\/[^\/]*$/, "", pkg)
			stmts[pkg] += $2
			if ($3 > 0) { hit[pkg] += $2 }
		}
		END {
			for (p in stmts) {
				pct = stmts[p] ? 100 * hit[p] / stmts[p] : 0
				printf "| `%s` | %.1f%% |\n", p, pct
			}
		}
	' "$1" | LC_ALL=C sort
}

# summary <total> <filtered-profile>: the Markdown written to the job summary.
summary() {
	printf '### Go unit coverage\n\n'
	printf 'Total (filtered, see scripts/coverage-gate.sh): **%s%%**, floor %s%%.\n\n' "$1" "$FLOOR"
	printf '<details><summary>Per package</summary>\n\n| Package | Coverage |\n|---|---|\n'
	per_package "$2"
	printf '\n</details>\n'
}

self_test() {
	local tmp out
	tmp="$(mktemp)"
	trap 'rm -f "$tmp"' RETURN
	printf '%s\n' 'mode: atomic' \
		'example.com/m/a/x.go:1.1,2.2 3 1' \
		'example.com/m/a/x.go:3.1,4.2 1 0' \
		'example.com/m/b/y.go:1.1,2.2 2 0' >"$tmp"
	out="$(per_package "$tmp")"
	if [ "$out" != "$(printf '%s\n' '| `example.com/m/a` | 75.0% |' '| `example.com/m/b` | 0.0% |')" ]; then
		printf 'self-test FAIL: per_package gave\n%s\n' "$out" >&2
		return 1
	fi
	out="$(summary 81.2 "$tmp")"
	case "$out" in
	*'**81.2%**, floor 80%'*) ;;
	*) printf 'self-test FAIL: summary has no total line\n%s\n' "$out" >&2; return 1 ;;
	esac
	echo "self-test OK"
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit
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
if awk -v t="$total" -v f="$FLOOR" 'BEGIN { exit !(t < f) }'; then
	echo "::error::Coverage ${total}% is below floor ${FLOOR}% (see ADR 0011); add tests"
	exit 1
fi
