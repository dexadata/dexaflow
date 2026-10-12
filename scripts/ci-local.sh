#!/usr/bin/env bash
# `make ci-local`: the CI gates a contributor can run on their own machine,
# before pushing, with no repository secret (#1574).
#
# It used to assume the maintainer's environment: a missing tool either failed
# the run or was skipped with a line that scrolled away, and the gates that CI
# runs with a repository secret were simply absent, so a green local run said
# nothing about them. Every step now ends in one of three states, and the run
# closes with a summary of each step that did not pass and why:
#
#   PASS  the gate ran and passed
#   FAIL  the gate ran and failed (the run exits 1, after the other steps)
#   SKIP  the gate did not run: a tool or service is missing (the message names
#         how to get it, `make setup` or the devcontainer provides them all), or
#         CI runs it with a repository secret a contributor does not have
#
# A step that needs a secret runs when the credential it needs is in the
# environment (the link check with GITHUB_TOKEN set, for instance) and is
# skipped by name otherwise. Nothing here reads a secret from a file.
#
# Required gates (gofmt, build, golangci-lint, unit tests, the script
# self-tests) fail when their tool is missing instead of skipping: without
# them a green summary would mean nothing.
#
# Usage: scripts/ci-local.sh [--self-test]
set -uo pipefail

declare -a SUMMARY=()
FAILED=0

# record <state> <name> <detail>
record() {
	printf '%s  %s%s\n' "$1" "$2" "${3:+: $3}"
	if [ "$1" != PASS ]; then
		SUMMARY+=("$1  $2${3:+: $3}")
	fi
	if [ "$1" = FAIL ]; then
		FAILED=1
	fi
}

# run_step <name> <command...>: runs the command, PASS or FAIL.
run_step() {
	local name="$1"
	shift
	echo "▸ $name"
	if "$@"; then
		record PASS "$name"
	else
		record FAIL "$name"
	fi
}

# require <name> <tool> <hint> <command...>: a required gate; a missing tool
# fails it.
require() {
	local name="$1" tool="$2" hint="$3"
	shift 3
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "▸ $name"
		record FAIL "$name" "$tool not found ($hint)"
		return
	fi
	run_step "$name" "$@"
}

# optional <name> <skip-reason or empty> <command...>: skipped with the reason
# when one is given.
optional() {
	local name="$1" reason="$2"
	shift 2
	if [ -n "$reason" ]; then
		echo "▸ $name"
		record SKIP "$name" "$reason"
		return
	fi
	run_step "$name" "$@"
}

# needs_secret <name> <env var> <what CI uses it for> <command...>: runs only
# when the credential is in the environment.
needs_secret() {
	local name="$1" var="$2" why="$3"
	shift 3
	if [ -z "${!var:-}" ]; then
		echo "▸ $name"
		record SKIP "$name" "needs the $var repository secret in CI ($why); export $var to run it"
		return
	fi
	run_step "$name" "$@"
}

# github_only <name> <why>: a gate that only runs on GitHub, never locally.
github_only() {
	echo "▸ $1"
	record SKIP "$1" "$2"
}

finish() {
	echo
	if [ "${#SUMMARY[@]}" -eq 0 ]; then
		echo "✅ ci-local clean: every gate ran and passed. Push when ready."
		return 0
	fi
	echo "Steps that did not pass:"
	printf '  %s\n' "${SUMMARY[@]}"
	if [ "$FAILED" -ne 0 ]; then
		echo "❌ ci-local failed."
		return 1
	fi
	echo "✅ ci-local clean for every gate that ran. CI still runs the skipped ones above."
	return 0
}

# ── the gates ────────────────────────────────────────────────────────────────

gofmt_clean() { test -z "$(gofmt -l . | tee /dev/stderr)"; }

govulncheck_bin() {
	if command -v govulncheck >/dev/null 2>&1; then
		echo govulncheck
	elif [ -x "$(go env GOPATH)/bin/govulncheck" ]; then
		echo "$(go env GOPATH)/bin/govulncheck"
	fi
}

helm_unittest_missing() {
	if ! command -v helm >/dev/null 2>&1; then
		echo "helm not installed (https://helm.sh/docs/intro/install/, or the devcontainer)"
	elif ! helm plugin list 2>/dev/null | grep -q unittest; then
		echo "helm-unittest plugin missing (helm plugin install https://github.com/helm-unittest/helm-unittest)"
	fi
}

hugo_missing() {
	if ! command -v hugo >/dev/null 2>&1; then
		echo "hugo not installed (https://gohugo.io/installation/, the extended build, or the devcontainer)"
	elif [ ! -x website/node_modules/.bin/postcss ]; then
		echo "Docsy PostCSS toolchain missing (npm --prefix website ci)"
	fi
}

# postgres_missing names why the integration tests cannot run, if they cannot.
postgres_missing() {
	if ! command -v migrate >/dev/null 2>&1; then
		echo "migrate not installed (make setup)"
	elif ! command -v pg_isready >/dev/null 2>&1; then
		echo "pg_isready not installed (the PostgreSQL client), so Postgres cannot be probed"
	elif ! pg_isready -q -d "$TEST_DATABASE_URL" >/dev/null 2>&1; then
		echo "no Postgres at $TEST_DATABASE_URL (make dev-up, or the devcontainer)"
	fi
}

integration_tests() {
	migrate -path migrations -database "$TEST_DATABASE_URL" up &&
		DATABASE_URL="$TEST_DATABASE_URL" go test -tags integration ./...
}

run_gates() {
	cd "$(dirname "$0")/.." || exit 1
	: "${TEST_DATABASE_URL:=postgres://leoflow:leoflow@localhost:5432/leoflow_test?sslmode=disable}"
	local vuln
	vuln="$(govulncheck_bin)"

	require "gofmt" gofmt "install Go" gofmt_clean
	require "go build" go "install Go" go build ./...
	require "golangci-lint" golangci-lint "make setup" golangci-lint run ./...
	require "go test" go "install Go" go test ./...
	require "script self-tests" python3 "install python3" bash scripts/check-script-selftests.sh
	optional "govulncheck (fetches the advisory database)" \
		"$([ -z "$vuln" ] && echo "govulncheck not installed (make setup)")" "$vuln" ./...
	optional "integration tests" "$(postgres_missing)" integration_tests
	optional "helm unittest (chart contracts)" "$(helm_unittest_missing)" \
		bash -c 'cd helm/dexaflow && helm unittest .'
	optional "python parser tests" \
		"$(command -v pytest >/dev/null 2>&1 || echo "pytest not installed (make setup)")" \
		bash -c 'cd parser && python3 -m pytest -q'
	optional "hugo --gc --minify (website/)" "$(hugo_missing)" \
		bash -c 'cd website && hugo --gc --minify --quiet'
	needs_secret "link check (lychee)" GITHUB_TOKEN \
		"authenticated github.com requests in link-check.yml" \
		lychee --config lychee.toml --no-progress README.md CONTRIBUTING.md CODE_OF_CONDUCT.md SECURITY.md
	github_only "OpenSSF Scorecard" \
		"runs on GitHub only, with the SCORECARD_READ_TOKEN repository secret (scorecard.yaml)"
	github_only "GCP cluster validation (test/gcp)" \
		"needs GCP credentials and creates billed clusters; run test/gcp/provision.sh by hand when a change needs it"
	finish
}

# The _case functions below are called indirectly, by name.
# shellcheck disable=SC2329
self_test() {
	local fail=0 out rc

	_case() { # <name> <want-rc> <want-substring> <function>
		local name="$1" want_rc="$2" want="$3" fn="$4"
		rc=0
		out="$( (SUMMARY=() FAILED=0 && "$fn" && finish) 2>&1)" || rc=$?
		if [ "$rc" != "$want_rc" ] || [[ "$out" != *"$want"* ]]; then
			printf '  FAIL %s\n    rc %s (want %s)\n    want substring: %s\n    out:\n%s\n' "$name" "$rc" "$want_rc" "$want" "$out"
			fail=1
		else
			echo "  ok   $name"
		fi
	}

	_all_pass() { run_step a true && run_step b true; }
	_case "every step passes" 0 "every gate ran and passed" _all_pass

	_one_fails() { run_step a false; run_step b true; }
	_case "a failure fails the run after the other steps" 1 "FAIL  a" _one_fails
	_case "a failure still runs the later steps" 1 "PASS  b" _one_fails

	_missing_required() { require lint no-such-tool-xyz "make setup" true; }
	_case "a missing required tool fails" 1 "no-such-tool-xyz not found (make setup)" _missing_required

	_optional_skip() { optional docs "hugo not installed" false; }
	_case "an optional step with a reason is skipped, not run" 0 "SKIP  docs: hugo not installed" _optional_skip

	_secret_unset() { unset CI_LOCAL_SELFTEST_SECRET; needs_secret links CI_LOCAL_SELFTEST_SECRET "rate limits" false; }
	_case "a secret step without its credential is skipped by name" 0 \
		"needs the CI_LOCAL_SELFTEST_SECRET repository secret in CI (rate limits)" _secret_unset

	_secret_set() { CI_LOCAL_SELFTEST_SECRET=x needs_secret links CI_LOCAL_SELFTEST_SECRET "rate limits" false; }
	_case "a secret step with its credential runs" 1 "FAIL  links" _secret_set

	_github_only() { github_only scorecard "runs on GitHub only"; }
	_case "a GitHub-only gate is reported as skipped" 0 "SKIP  scorecard: runs on GitHub only" _github_only

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

run_gates
