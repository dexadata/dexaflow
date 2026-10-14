#!/usr/bin/env bash
# Runs every native Go fuzz target in the repository, one after the other, for
# FUZZTIME each (#1571). The targets cover the inputs most exposed to untrusted
# or malformed data: dexaflow.yaml, dag.json and its schedule, the agent RPC
# credential and termination message, and the API's paging parameters.
#
# Fuzzing is not part of the per-PR gate: plain `go test ./...` already replays
# every seed and every committed crasher under testdata/fuzz/ as a regression
# test. This script is the long run, nightly in .github/workflows/fuzz.yaml and
# by hand with `make fuzz`.
#
# Targets are discovered, not listed: every `func FuzzXxx(f *testing.F)` in a
# *_test.go file under cmd/, internal/ or pkg/ runs, so a new target needs no
# edit here. `go test -fuzz` takes one package and one target per invocation,
# hence the loop. A failure writes the crasher to the package's testdata/fuzz/
# and the script carries on, then exits 1 naming every failed target.
#
# Usage: scripts/fuzz.sh [--list | --self-test]
#   FUZZTIME  time per target (default 30s)
#   FUZZ      only targets whose name matches this regexp (default: all)
set -euo pipefail

# list_targets prints "<package dir> <target>" for every fuzz target under the
# given root.
list_targets() { # <root>
	local root="$1"
	(cd "$root" && grep -rE --include='*_test.go' '^func Fuzz[A-Za-z0-9_]*\(f \*testing\.F\)' cmd internal pkg 2>/dev/null || true) |
		sed -E 's#^(.*)/[^/]*_test\.go:func (Fuzz[A-Za-z0-9_]*)\(.*#./\1 \2#' |
		sort -u
}

self_test() {
	local fail=0 tmp out want
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN
	mkdir -p "$tmp/internal/a" "$tmp/internal/b/c" "$tmp/cmd/x" "$tmp/test/load"
	printf 'package a\n\nfunc FuzzOne(f *testing.F) {}\nfunc FuzzTwo(f *testing.F) {}\n' >"$tmp/internal/a/a_test.go"
	printf 'package c\n\nfunc FuzzDeep(f *testing.F) {}\n// func FuzzCommented(f *testing.F) {}\n' >"$tmp/internal/b/c/c_test.go"
	printf 'package a\n\nfunc FuzzNotATest(f *testing.F) {}\n' >"$tmp/internal/a/a.go"
	printf 'package x\n\nfunc FuzzHelper(t *testing.T) {}\n' >"$tmp/cmd/x/x_test.go"
	printf 'package load\n\nfunc FuzzOutside(f *testing.F) {}\n' >"$tmp/test/load/l_test.go"
	out="$(list_targets "$tmp")"
	want="$(printf './internal/a FuzzOne\n./internal/a FuzzTwo\n./internal/b/c FuzzDeep')"
	if [ "$out" = "$want" ]; then
		echo "  ok   discovery finds test-file targets only"
	else
		printf '  FAIL discovery\n    got:\n%s\n    want:\n%s\n' "$out" "$want"
		fail=1
	fi
	if [ -z "$(list_targets "$tmp/test")" ]; then
		echo "  ok   a tree with no target lists nothing"
	else
		echo "  FAIL a tree with no target listed something"
		fail=1
	fi
	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
targets="$(list_targets .)"
if [ -n "${FUZZ:-}" ]; then
	targets="$(printf '%s\n' "$targets" | awk -v re="$FUZZ" '$2 ~ re')"
fi
if [ -z "$targets" ]; then
	echo "FAIL: no fuzz target found${FUZZ:+ matching $FUZZ}" >&2
	exit 1
fi
if [ "${1:-}" = "--list" ]; then
	printf '%s\n' "$targets"
	exit 0
fi

fuzztime="${FUZZTIME:-30s}"
failed=()
while read -r pkg name; do
	echo "::group::$name ($pkg, $fuzztime)"
	if ! go test -run='^$' -fuzz="^${name}\$" -fuzztime="$fuzztime" "$pkg"; then
		failed+=("$name ($pkg)")
	fi
	echo "::endgroup::"
done <<<"$targets"

if [ "${#failed[@]}" -gt 0 ]; then
	echo "FAIL: ${#failed[@]} fuzz target(s) found a failing input; the crashers are under testdata/fuzz/:" >&2
	printf '  %s\n' "${failed[@]}" >&2
	exit 1
fi
echo "OK: every fuzz target ran for $fuzztime without a failure"
