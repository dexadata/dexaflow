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

profile="${1:?usage: coverage-gate.sh <coverprofile>}"
[ -s "$profile" ] || { echo "::error::coverage profile '$profile' is missing or empty"; exit 1; }

filtered="$(mktemp)"
trap 'rm -f "$filtered"' EXIT
grep -vE "$EXCLUDE" "$profile" >"$filtered"
total="$(go tool cover -func="$filtered" | tail -1 | awk '{print $3}' | tr -d '%')"

echo "Total coverage (filtered): ${total}% (floor: ${FLOOR}%)"
if awk -v t="$total" -v f="$FLOOR" 'BEGIN { exit !(t < f) }'; then
	echo "::error::Coverage ${total}% is below floor ${FLOOR}% (see ADR 0011); add tests"
	exit 1
fi
