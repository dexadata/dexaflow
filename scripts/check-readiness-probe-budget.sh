#!/usr/bin/env bash
# The readiness timeout is one relationship written in three places, and none of
# them can see the other two.
#
#   internal/api/health.go        probeBudget          — how long /readyz will spend
#   helm/.../_helpers.tpl         $min                 — the floor the chart refuses to go under
#   helm/dexaflow/values.yaml      probes.readiness.timeoutSeconds — what the kubelet actually gets
#
# The invariant is  probeBudget < floor <= default:  the server has to give up
# before the kubelet does, or the kubelet cancels a probe that was about to
# answer and the operator gets a bare timeout instead of a 503 naming the
# dependency (#1023, #1040, #1041). The floor is what makes that true for every
# operator, and the default has to satisfy its own floor.
#
# Nothing reconciles a Go constant with a Helm template. Lowering probeBudget to
# 4s to "give a slow database more room" would leave the floor at 3 and silently
# invert the relationship for everyone on the default, with the whole test suite
# green — the same shape as the drift this repo already gates for the Lite
# pre-pull tag and the Task SDK pin.
#
# Usage: scripts/check-readiness-probe-budget.sh [--self-test]
#        scripts/check-readiness-probe-budget.sh <health.go> <_helpers.tpl> <values.yaml>
set -euo pipefail

check() { # <health.go> <helpers.tpl> <values.yaml>
	python3 - "$1" "$2" "$3" <<'PY'
import re
import sys

health_path, helpers_path, values_path = sys.argv[1], sys.argv[2], sys.argv[3]


def fail(msg):
	sys.exit("FAIL: " + msg)


def read(path):
	try:
		with open(path) as fh:
			return fh.read()
	except OSError as exc:
		fail(f"cannot read {path}: {exc}")


# Seconds only. A budget expressed in milliseconds would be a different change
# and deserves to fail here rather than be silently rounded.
m = re.search(r"^const\s+probeBudget\s*=\s*(\d+)\s*\*\s*time\.Second\s*$", read(health_path), re.M)
if not m:
	fail(f"{health_path} has no `const probeBudget = N * time.Second` — renamed, or no longer a whole number of seconds")
budget = int(m.group(1))

m = re.search(r'define\s+"leoflow\.readinessTimeoutSeconds"(.*?){{-\s*end\s*-}}\s*$', read(helpers_path), re.S)
if not m:
	fail(f'{helpers_path} has no `leoflow.readinessTimeoutSeconds` definition — did the floor helper get renamed?')
fm = re.search(r"\$min\s*:=\s*(\d+)", m.group(1))
if not fm:
	fail(f'{helpers_path}\'s leoflow.readinessTimeoutSeconds no longer sets `$min := <int>`')
floor = int(fm.group(1))

try:
	import yaml
except ImportError:
	sys.exit("SKIP: PyYAML unavailable; cannot read the chart default")
values = yaml.safe_load(read(values_path)) or {}
try:
	default = int(values["probes"]["readiness"]["timeoutSeconds"])
except (KeyError, TypeError, ValueError):
	fail(f"{values_path} has no integer probes.readiness.timeoutSeconds")

if budget >= floor:
	fail(
		f"probeBudget is {budget}s but the chart floor is {floor}s. The server must give up BEFORE the kubelet: "
		f"as written, a probe against a slow dependency is cancelled by the kubelet while the handler is still "
		f"working, so it logs nothing and the operator sees a timeout instead of a 503 naming the dependency. "
		f"Either lower probeBudget in {health_path} below {floor}, or raise $min in {helpers_path} above {budget} "
		f"(and the default in {values_path} with it)."
	)
if default < floor:
	fail(
		f"the chart default probes.readiness.timeoutSeconds={default} is below its own floor of {floor}s, so a "
		f"default install would not render. Raise it in {values_path}."
	)
print(f"readiness probe budget: server {budget}s < chart floor {floor}s <= default {default}s")
PY
}

self_test() {
	local fail=0 tmp
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN

	_case() { # <name> <want-exit> <want-substr> <budget-go> <floor-tpl> <default-yaml>
		local name=$1 want=$2 substr=$3
		printf 'package api\n\nconst probeBudget = %s * time.Second\n' "$4" > "$tmp/health.go"
		printf '{{- define "leoflow.readinessTimeoutSeconds" -}}\n{{- $min := %s -}}\n{{- end -}}\n' "$5" > "$tmp/helpers.tpl"
		printf 'probes:\n  readiness:\n    timeoutSeconds: %s\n' "$6" > "$tmp/values.yaml"
		local out rc
		out=$(check "$tmp/health.go" "$tmp/helpers.tpl" "$tmp/values.yaml" 2>&1) && rc=0 || rc=$?
		if [ "$rc" -ne "$want" ]; then
			echo "self-test FAIL: $name — exit $rc, wanted $want"; echo "  $out"; fail=1; return
		fi
		case "$out" in *"$substr"*) ;; *) echo "self-test FAIL: $name — output lacks \"$substr\""; echo "  $out"; fail=1 ;; esac
	}

	_case "today's values pass" 0 "server 2s < chart floor 3s" 2 3 3
	_case "a budget equal to the floor is caught" 1 "must give up BEFORE the kubelet" 3 3 3
	_case "a budget above the floor is caught" 1 "must give up BEFORE the kubelet" 4 3 3
	_case "a default under its own floor is caught" 1 "below its own floor" 2 3 2
	_case "headroom is allowed" 0 "server 2s < chart floor 3s <= default 10s" 2 3 10

	# Each side renamed away must fail loudly rather than pass by not matching.
	printf 'package api\n\nconst probeDeadline = 2 * time.Second\n' > "$tmp/health.go"
	printf '{{- define "leoflow.readinessTimeoutSeconds" -}}\n{{- $min := 3 -}}\n{{- end -}}\n' > "$tmp/helpers.tpl"
	printf 'probes:\n  readiness:\n    timeoutSeconds: 3\n' > "$tmp/values.yaml"
	if check "$tmp/health.go" "$tmp/helpers.tpl" "$tmp/values.yaml" >/dev/null 2>&1; then
		echo "self-test FAIL: a renamed probeBudget passes silently"; fail=1
	fi
	printf 'package api\n\nconst probeBudget = 2 * time.Second\n' > "$tmp/health.go"
	printf '{{- define "leoflow.readinessTimeout" -}}\n{{- $min := 3 -}}\n{{- end -}}\n' > "$tmp/helpers.tpl"
	if check "$tmp/health.go" "$tmp/helpers.tpl" "$tmp/values.yaml" >/dev/null 2>&1; then
		echo "self-test FAIL: a renamed floor helper passes silently"; fail=1
	fi

	if [ "$fail" -eq 0 ]; then echo "check-readiness-probe-budget self-test: ok"; return 0; fi
	return 1
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-internal/api/health.go}" "${2:-helm/dexaflow/templates/_helpers.tpl}" "${3:-helm/dexaflow/values.yaml}"
