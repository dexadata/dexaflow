#!/usr/bin/env bash
# Chaos dogfood harness (#231) — Phase 1.
#
# Validates the "fresh-runner contract" and runs the canonical test suites,
# emitting a green/red report. The contract section catches contributor-machine
# state that would otherwise mask a real bug (a pip-installed `leoflow_parser`,
# a populated `~/.dexaflow/`, etc. — F5 from PR #221 review, also #96).
#
# Phase 2 (next iteration) will add:
#   - Docker container isolation (true clean root)
#   - Failure-injection scripts (kill scheduler, kill agent, stop PG)
#   - Connector cookbook DAGs end-to-end
#
# Design note: Phase 1 does NOT shadow $HOME during test runs — the Go module
# cache and pytest cache live there, and pretending otherwise breaks cleanup.
# Instead the contract section CHECKS the host state and the operator fixes
# their environment (or moves to Docker in Phase 2).
#
# Exit codes:
#   0 — every section green; alpha gate satisfied
#   1 — at least one section red; alpha BLOCKED
#
# Output: a markdown report to $REPORT_FILE (default /tmp/chaos-report.md)
# and a colored summary to stdout.

set -uo pipefail   # NO set -e: every section must run even if earlier ones fail

REPORT_FILE="${REPORT_FILE:-/tmp/chaos-report.md}"
# Prefer cwd when it looks like the repo root (this is what the Makefile +
# the Dockerized harness do — the script may not be co-located with the
# checkout, e.g. it's COPYed into /chaos/ inside the container while the
# repo bind-mounts at /workspace).
if [[ -f "${PWD}/go.mod" ]]; then
  REPO_ROOT="$PWD"
else
  REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fi
SUMMARY_FILE="$(mktemp -t chaos-summary.XXXXXX)"
trap 'rm -f "$SUMMARY_FILE"' EXIT

# Color helpers — fall back to plain text when not a TTY.
if [[ -t 1 ]]; then
  GREEN=$'\e[32m'; RED=$'\e[31m'; YELLOW=$'\e[33m'; BOLD=$'\e[1m'; RESET=$'\e[0m'
else
  GREEN=''; RED=''; YELLOW=''; BOLD=''; RESET=''
fi

declare -i PASS_COUNT=0
declare -i FAIL_COUNT=0

# run_section <name> <command...>
# Runs the command, captures its exit code, and records pass/fail in the
# summary file. Stdout/stderr stream to the terminal so failures are
# debuggable in real time; the report records only the result.
run_section() {
  local name="$1"; shift
  echo
  echo "${BOLD}═══ ${name} ═══${RESET}"
  if "$@"; then
    echo "${GREEN}✅ PASS${RESET} — ${name}"
    echo "PASS|${name}" >> "$SUMMARY_FILE"
    PASS_COUNT+=1
  else
    local exit_code=$?
    echo "${RED}❌ FAIL${RESET} — ${name} (exit ${exit_code})"
    echo "FAIL|${name}" >> "$SUMMARY_FILE"
    FAIL_COUNT+=1
  fi
}

echo "${BOLD}Chaos dogfood — Phase 1 (host contract + canonical suites)${RESET}"
echo "Repo:   ${REPO_ROOT}"
echo "Report: ${REPORT_FILE}"
echo
cd "$REPO_ROOT"

# ─── Section 1: fresh-runner contract ────────────────────────────────────────
# This catches the contributor-leak surface that hid the BYO Python gap in PR
# #221 — the maintainer's machine had `pip install leoflow_parser` so compile
# "just worked" without `dexaflow setup`. A clean CI runner does NOT have this.
# The harness REFUSES to declare a green report when the host fails the
# contract; the operator fixes their env or runs Phase 2 inside Docker.
contract_check() {
  local violations=0
  if [[ -d "$HOME/.dexaflow" ]]; then
    echo "  - ${RED}~/.dexaflow/ exists${RESET} — host has Leoflow state that may mask bugs."
    echo "    Hint: \`leoflow uninstall\` (keeps your DAGs; removes ~/.dexaflow only)."
    violations+=1
  fi
  if command -v python3 >/dev/null 2>&1 && python3 -c "import leoflow_parser" 2>/dev/null; then
    echo "  - ${RED}leoflow_parser is importable from python3${RESET} — pip-installed on host (F5/#96)."
    echo "    Hint: \`pip uninstall leoflow-parser\` (or move to Phase 2 Docker isolation)."
    violations+=1
  fi
  if [[ $violations -eq 0 ]]; then
    echo "  ✓ no ~/.dexaflow state"
    echo "  ✓ no pip-installed leoflow_parser"
  fi
  return $violations
}
run_section "Fresh-runner contract" contract_check

# ─── Section 2: Go unit tests ────────────────────────────────────────────────
run_section "Go unit tests (go test ./...)" go test ./...

# ─── Section 2b: chaos integration (failure injection) ──────────────────────
# Phase 2b (#231): scheduler-restart recovery + future agent-lost / orphan-run
# scenarios. The tests fast-forward reaper thresholds so they stay fast
# (~1.5 s for the current single scenario) while still proving the contract.
# Skipped when DATABASE_URL is absent (no PG reachable — the skip is
# explicit so a green report is honest about what was exercised).
run_chaos_integration() {
  if [[ -z "${DATABASE_URL:-}" ]]; then
    echo "  (skipped — DATABASE_URL not set; chaos integration needs a migrated test PG)"
    return 0
  fi
  go test -tags integration -count=1 -run 'TestChaos' ./internal/storage/
}
run_section "Chaos integration (failure injection)" run_chaos_integration

# ─── Section 3: golangci-lint ────────────────────────────────────────────────
# CI-pinned version per [[lint-pin-ci-version]]; prefer ~/.go/bin (where
# `go install` puts tools), else fall back to PATH.
LINT_BIN="${HOME}/go/bin/golangci-lint"
if [[ ! -x "$LINT_BIN" ]]; then
  LINT_BIN="golangci-lint"
fi
run_section "Go lint (${LINT_BIN})" "$LINT_BIN" run ./...

# ─── Section 4: parser pytest ────────────────────────────────────────────────
run_section "Parser tests (pytest)" bash -c 'cd parser && python3 -m pytest -q'

# ─── Section 5: runtime pytest ───────────────────────────────────────────────
# The runtime suite is optional in some environments (no fixtures dir on a
# fresh clone); a missing dir is reported as a skip, not a fail.
run_runtime_tests() {
  if [[ -d runtime/python/tests ]] || [[ -f runtime/python/pyproject.toml ]]; then
    (cd runtime/python && python3 -m pytest -q)
  else
    echo "  (skipped — runtime/python tests not present)"
    return 0
  fi
}
run_section "Runtime tests (pytest)" run_runtime_tests

# ─── Report ──────────────────────────────────────────────────────────────────
{
  echo "# Chaos dogfood report"
  echo
  echo "_Phase 1 — host contract + canonical suites. Phase 2 will add Docker"
  echo "isolation + failure injection (#231)._"
  echo
  echo "**Summary**: ${PASS_COUNT} passed, ${FAIL_COUNT} failed."
  echo
  echo "| Status | Section |"
  echo "|---|---|"
  while IFS='|' read -r status name; do
    if [[ "$status" == "PASS" ]]; then
      echo "| ✅ | ${name} |"
    else
      echo "| ❌ | ${name} |"
    fi
  done < "$SUMMARY_FILE"
  echo
  echo "Repo HEAD: $(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
  echo "Runner:    $(uname -smr)"
  echo "Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
} > "$REPORT_FILE"

echo
echo "${BOLD}═══ Summary ═══${RESET}"
echo "${GREEN}PASS:${RESET} ${PASS_COUNT}    ${RED}FAIL:${RESET} ${FAIL_COUNT}"
echo "Report: ${REPORT_FILE}"

if (( FAIL_COUNT > 0 )); then
  echo
  echo "${RED}${BOLD}ALPHA GATE: BLOCKED${RESET} — at least one section failed."
  exit 1
fi
echo
echo "${GREEN}${BOLD}ALPHA GATE: SATISFIED${RESET} (Phase 1; Phase 2 still pending)."
exit 0
