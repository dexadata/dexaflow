#!/usr/bin/env bash
# The startup gate's budget is a claim about the server's boot, and the two
# halves of that claim live in different languages:
#
#   internal/storage/postgres.go   pgStartupBudget        how long the connect retry may spend
#   cmd/dexaflow-server/main.go     podInformerSyncTimeout how long the informer warm-up may spend
#   cmd/dexaflow-server/main.go     oidcDiscoveryTimeout   how long IdP discovery may spend
#   cmd/dexaflow-server/main.go     oidcNameCheckTimeout   how long the OIDC name lookups may spend
#   helm/dexaflow/values.yaml       probes.startup.*       what the kubelet actually allows
#
# The invariant is
#   startup budget >= (2 * pgStartupBudget) + podInformerSyncTimeout
#                     + oidcDiscoveryTimeout + oidcNameCheckTimeout + headroom.
# Twice the Postgres budget because NewPostgres runs the retry loop once for the
# request pool and once for the dedicated probe pool, so a slow-to-accept
# Postgres can spend both. OIDC discovery counts unconditionally even though it
# only runs under auth.provider: oidc, because the floor has to cover the worst
# boot the chart can be configured into, not the common one. The name check
# counts for the same reason and for one more: it runs AFTER the connect retry,
# as a further sequential phase, so the Postgres budget does not absorb it. Its
# bound exists precisely because a pool can be established and a query still
# never answer, which is the case the connect retry has already passed. The
# headroom covers what is left with no bound of its own: credential detection
# for an object-store log sink, and process start.
#
# It matters because a startup budget under the boot it is gating kills a pod
# that was about to come up, which is the restart loop the gate exists to end
# (#1083). Nothing reconciles a Go constant with a Helm value: raising
# pgStartupBudget to 60s "so a failover blip cannot fail boot" would put the
# code-defined floor at 145s under a 180s gate, leaving 35s of slack where the
# gate requires 60s, and the whole test suite green. The same drift shape this
# repo already gates for the readiness timeout, the Lite pre-pull tag and the
# Task SDK pin.
#
# The gate also re-asserts, on the shipped defaults, the relation the chart
# enforces per-render: the startup budget must EXCEED the liveness budget it
# replaces, or it only moves the kill from one probe to the other.
#
# Usage: scripts/check-startup-probe-budget.sh [--self-test]
#        scripts/check-startup-probe-budget.sh <postgres.go> <main.go> <values.yaml>
set -euo pipefail

# Seconds of slack the gate must leave above the bounds the code states for
# itself. Boot does work that no constant covers, and a budget sized to the
# sum alone would be exactly as tight as the worst boot that still succeeds.
HEADROOM_SECONDS=${HEADROOM_SECONDS:-60}

check() { # <postgres.go> <main.go> <values.yaml>
	HEADROOM_SECONDS="$HEADROOM_SECONDS" python3 - "$1" "$2" "$3" <<'PY'
import os
import re
import sys

pg_path, main_path, values_path = sys.argv[1], sys.argv[2], sys.argv[3]
headroom = int(os.environ["HEADROOM_SECONDS"])


def fail(msg):
	sys.exit("FAIL: " + msg)


def read(path):
	try:
		with open(path) as fh:
			return fh.read()
	except OSError as exc:
		fail(f"cannot read {path}: {exc}")


# Seconds only, in both cases. A bound expressed in minutes or milliseconds is a
# different change and deserves to fail here rather than be silently rounded.
m = re.search(r"^const\s+pgStartupBudget\s*=\s*(\d+)\s*\*\s*time\.Second\s*$", read(pg_path), re.M)
if not m:
	fail(f"{pg_path} has no `const pgStartupBudget = N * time.Second`: renamed, or no longer a whole number of seconds")
pg_budget = int(m.group(1))

main_src = read(main_path)


def boot_bound(name):
	m = re.search(r"^var\s+" + name + r"\s*=\s*(\d+)\s*\*\s*time\.Second\s*$", main_src, re.M)
	if not m:
		fail(f"{main_path} has no `var {name} = N * time.Second`: renamed, or no longer a whole number of seconds")
	return int(m.group(1))


informer = boot_bound("podInformerSyncTimeout")
discovery = boot_bound("oidcDiscoveryTimeout")
namecheck = boot_bound("oidcNameCheckTimeout")

try:
	import yaml
except ImportError:
	sys.exit("SKIP: PyYAML unavailable; cannot read the chart defaults")
values = yaml.safe_load(read(values_path)) or {}


def probe_int(section, key):
	try:
		return int(values["probes"][section][key])
	except (KeyError, TypeError, ValueError):
		fail(f"{values_path} has no integer probes.{section}.{key}")


startup = probe_int("startup", "periodSeconds") * probe_int("startup", "failureThreshold")
liveness = probe_int("liveness", "initialDelaySeconds") + probe_int("liveness", "periodSeconds") * probe_int("liveness", "failureThreshold")

# NewPostgres runs connectWithRetry twice: once for the request pool, once for
# the dedicated probe pool. A Postgres slow to accept connections can spend both.
code_floor = 2 * pg_budget + informer + discovery + namecheck
if startup < code_floor + headroom:
	fail(
		f"probes.startup allows boot {startup}s, but the server's own bounds already reserve {code_floor}s "
		f"(2 * pgStartupBudget {pg_budget}s for the request and probe pools, plus podInformerSyncTimeout {informer}s, "
		f"oidcDiscoveryTimeout {discovery}s and oidcNameCheckTimeout {namecheck}s) and boot does work no constant covers "
		f"on top of that: cloud credential "
		f"detection for an object-store log sink, process start. A gate tighter than the boot restarts a pod that was about to come up, "
		f"which is the loop the gate exists to end. Raise probes.startup.failureThreshold in {values_path} to at "
		f"least {-(-(code_floor + headroom) // probe_int('startup', 'periodSeconds'))}, or lower the bound in the Go "
		f"source that moved."
	)
if startup <= liveness:
	fail(
		f"probes.startup allows boot {startup}s, which is not more than the {liveness}s the liveness probe would have "
		f"allowed. At or below the liveness budget the gate only changes which probe kills the container. Raise "
		f"probes.startup.failureThreshold in {values_path}."
	)
print(f"startup probe budget: {startup}s >= code floor {code_floor}s + {headroom}s headroom, and > liveness {liveness}s")
PY
}

self_test() {
	local fail=0 tmp
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN

	_write() { # <pg-seconds> <informer-seconds> <startup-period> <startup-threshold> [discovery] [name-check]
		printf 'package storage\n\nconst pgStartupBudget = %s * time.Second\n' "$1" > "$tmp/postgres.go"
		printf 'package main\n\nvar podInformerSyncTimeout = %s * time.Second\nvar oidcDiscoveryTimeout = %s * time.Second\nvar oidcNameCheckTimeout = %s * time.Second\n' "$2" "${5:-15}" "${6:-5}" > "$tmp/main.go"
		printf 'probes:\n  startup:\n    periodSeconds: %s\n    failureThreshold: %s\n  liveness:\n    initialDelaySeconds: 10\n    periodSeconds: 20\n    failureThreshold: 3\n' "$3" "$4" > "$tmp/values.yaml"
	}

	_case() { # <name> <want-exit> <want-substr> <pg> <informer> <period> <threshold> [discovery] [name-check]
		local name=$1 want=$2 substr=$3
		_write "$4" "$5" "$6" "$7" "${8:-15}" "${9:-5}"
		local out rc
		out=$(check "$tmp/postgres.go" "$tmp/main.go" "$tmp/values.yaml" 2>&1) && rc=0 || rc=$?
		if [ "$rc" -ne "$want" ]; then
			echo "self-test FAIL: $name: exit $rc, wanted $want"; echo "  $out"; fail=1; return
		fi
		case "$out" in *"$substr"*) ;; *) echo "self-test FAIL: $name: output lacks \"$substr\""; echo "  $out"; fail=1 ;; esac
	}

	_case "today's values pass"                       0 "startup probe budget: 180s" 30 10 5 36
	_case "a budget under the code floor is caught"   1 "tighter than the boot"      30 10 5 20
	_case "a raised pgStartupBudget is caught"        1 "tighter than the boot"      60 10 5 36
	_case "a raised informer timeout is caught"       1 "tighter than the boot"      30 70 5 36
	_case "a raised discovery timeout is caught"      1 "tighter than the boot"      30 10 5 36 120
	_case "a raised name-check timeout is caught"     1 "tighter than the boot"      30 10 5 36  15 120
	_case "a budget at the liveness budget is caught" 1 "not more than the"           2  4 5 14   1   1
	_case "exactly floor plus headroom passes"        0 "startup probe budget: 150s" 30 10 5 30

	# Either side renamed away must fail loudly rather than pass by not matching.
	_write 30 10 5 36
	printf 'package storage\n\nconst pgConnectBudget = 30 * time.Second\n' > "$tmp/postgres.go"
	if check "$tmp/postgres.go" "$tmp/main.go" "$tmp/values.yaml" >/dev/null 2>&1; then
		echo "self-test FAIL: a renamed pgStartupBudget passes silently"; fail=1
	fi
	_write 30 10 5 36
	printf 'package main\n\nvar podInformerWarmup = 10 * time.Second\nvar oidcDiscoveryTimeout = 15 * time.Second\n' > "$tmp/main.go"
	if check "$tmp/postgres.go" "$tmp/main.go" "$tmp/values.yaml" >/dev/null 2>&1; then
		echo "self-test FAIL: a renamed podInformerSyncTimeout passes silently"; fail=1
	fi
	_write 30 10 5 36
	printf 'package main\n\nvar podInformerSyncTimeout = 10 * time.Second\nvar oidcDiscoveryBudget = 15 * time.Second\nvar oidcNameCheckTimeout = 5 * time.Second\n' > "$tmp/main.go"
	if check "$tmp/postgres.go" "$tmp/main.go" "$tmp/values.yaml" >/dev/null 2>&1; then
		echo "self-test FAIL: a renamed oidcDiscoveryTimeout passes silently"; fail=1
	fi
	_write 30 10 5 36
	printf 'package main\n\nvar podInformerSyncTimeout = 10 * time.Second\nvar oidcDiscoveryTimeout = 15 * time.Second\nvar oidcNameCheckBudget = 5 * time.Second\n' > "$tmp/main.go"
	if check "$tmp/postgres.go" "$tmp/main.go" "$tmp/values.yaml" >/dev/null 2>&1; then
		echo "self-test FAIL: a renamed oidcNameCheckTimeout passes silently"; fail=1
	fi

	if [ "$fail" -eq 0 ]; then echo "check-startup-probe-budget self-test: ok"; return 0; fi
	return 1
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-internal/storage/postgres.go}" "${2:-cmd/dexaflow-server/main.go}" "${3:-helm/dexaflow/values.yaml}"
