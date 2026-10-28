#!/usr/bin/env bash
# Run the CI gates locally before a push, and say which ones did not run
# (#1574). `make ci-local` runs this.
#
# A gate whose tool is missing is skipped with how to install it, never
# failed, so the script runs on a fresh clone and in the devcontainer. It
# used to print each skip inline in a long run, where a reader saw the
# final "clean" line and took every gate as passed; the skipped gates are
# now listed again at the end. No gate needs a repository secret or a cloud
# credential: govulncheck reads its advisory database over the network,
# nothing more.
#
# Usage:
#   scripts/ci-local.sh
#   scripts/ci-local.sh --self-test
set -euo pipefail

SKIPPED=()

# have <tool>: true when tool is on PATH.
have() { command -v "$1" >/dev/null 2>&1; }

# gate <name> <command...>: runs one gate and stops the run when it fails.
gate() {
	local name="$1"
	shift
	echo "▸ $name"
	if ! "$@"; then
		echo "FAIL: $name" >&2
		exit 1
	fi
}

# skip <name> <why and how to install>: records a gate that did not run.
skip() {
	echo "▸ $1: skipped, $2"
	SKIPPED+=("$1: $2")
}

# summary: the closing lines, naming every gate that did not run.
summary() {
	if [ "${#SKIPPED[@]}" -eq 0 ]; then
		echo "ci-local clean, every gate ran: push when ready"
		return 0
	fi
	echo "ci-local clean, but ${#SKIPPED[@]} gate(s) did not run and CI will run them:"
	printf '  - %s\n' "${SKIPPED[@]}"
}

gofmt_clean() {
	local out
	out=$(gofmt -l .)
	[ -z "$out" ] || {
		printf '%s\n' "$out" >&2
		return 1
	}
}

govulncheck_bin() {
	if have govulncheck; then
		echo govulncheck
	elif [ -x "$(go env GOPATH)/bin/govulncheck" ]; then
		echo "$(go env GOPATH)/bin/govulncheck"
	fi
}

helm_unittest_available() {
	have helm && { have helm-unittest || helm plugin list 2>/dev/null | grep -q unittest; }
}

run_all() {
	gate "gofmt" gofmt_clean
	gate "go build" go build ./...
	if have golangci-lint; then gate "golangci-lint" golangci-lint run ./...; else
		skip "golangci-lint" "not installed (make setup)"
	fi
	gate "go test" go test ./...
	local vuln
	vuln=$(govulncheck_bin)
	if [ -n "$vuln" ]; then gate "govulncheck" "$vuln" ./...; else
		skip "govulncheck" "not installed (make setup)"
	fi
	if helm_unittest_available; then gate "helm unittest" bash -c 'cd helm/dexaflow && helm unittest .'; else
		skip "helm unittest" "helm or its unittest plugin is not installed (helm plugin install https://github.com/helm-unittest/helm-unittest)"
	fi
	if have python3 && python3 -c 'import pytest' 2>/dev/null; then gate "python parser tests" bash -c 'cd parser && python3 -m pytest -q'; else
		skip "python parser tests" "python3 with pytest is not installed (make setup)"
	fi
	if ! have hugo; then
		skip "hugo build" "hugo extended is not installed (https://gohugo.io/installation/)"
	elif [ ! -x website/node_modules/.bin/postcss ]; then
		skip "hugo build" "the Docsy PostCSS toolchain is missing (npm --prefix website ci)"
	else
		gate "hugo build" bash -c 'cd website && hugo --gc --minify --quiet'
	fi
	summary
}

self_test() {
	local fail=0 out
	_expect() { # <name> <want-substring>
		case "$out" in *"$2"*) printf '  ok   %s\n' "$1" ;; *)
			printf '  FAIL %s: want %q in:\n%s\n' "$1" "$2" "$out"
			fail=1
			;;
		esac
	}
	SKIPPED=()
	out=$(summary)
	_expect "a run with no skip says every gate ran" "every gate ran"
	SKIPPED=()
	out=$(
		skip "hugo build" "hugo extended is not installed"
		skip "govulncheck" "not installed (make setup)"
		summary
	)
	_expect "a skip is printed where it happens" "▸ hugo build: skipped, hugo extended is not installed"
	_expect "the summary counts the skipped gates" "2 gate(s) did not run"
	_expect "the summary names each skipped gate" "  - govulncheck: not installed (make setup)"
	out=$(PATH=/nonexistent have golangci-lint && echo found || echo missing)
	_expect "a tool off PATH is missing" "missing"
	out=$( (gate "failing gate" false) 2>&1 || echo "exit=$?")
	_expect "a failing gate stops the run and names itself" "FAIL: failing gate"
	_expect "a failing gate exits non-zero" "exit=1"
	if [ "$fail" -eq 0 ]; then
		echo "ci-local self-test: ok"
		return 0
	fi
	echo "ci-local self-test: FAIL"
	return 1
}

case "${1:-}" in
--self-test) self_test ;;
"") run_all ;;
*)
	echo "usage: $0 [--self-test]" >&2
	exit 2
	;;
esac
