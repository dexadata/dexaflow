#!/usr/bin/env bash
# The Lite cold-start release gate pre-pulls the Postgres image by tag, and the
# tag must equal the one Lite actually starts.
#
# That job has no checkout — it installs from the published release, which is
# the point of it — so it cannot read docker-compose.dev.yaml at runtime and the
# tag is written into the workflow. Two copies of a version string with nothing
# between them is how the docs/adr links and the dependabot directories rotted,
# so this is the gate for it. The tree already carries two Postgres majors
# (docker-compose.yml runs 17-alpine, docker-compose.dev.yaml runs 16), so
# "someone bumps the wrong file" is not hypothetical here.
#
# If they drift, the pre-pull silently fetches an image nobody starts, the real
# one is downloaded inside the /readyz budget again, and a slow registry eats
# the boot margin (#977).
#
# Both sides are parsed as YAML rather than grepped. An earlier awk version had
# a fail-open a reviewer reproduced in one line: commenting out the pull step —
# the most common way a step gets disabled while debugging — left the gate
# green, because a `#`-prefixed line still matched. Scoping to the job's parsed
# steps and dropping comment lines is what closes that, and it is also what
# allows the pull to be a multi-line retry block.
#
# Usage: scripts/check-lite-prepull-matches-compose.sh [--self-test]
#        scripts/check-lite-prepull-matches-compose.sh <compose> <workflow>
set -euo pipefail

JOB="lite-cold-start-smoke"

check() { # <compose> <workflow>
	python3 - "$1" "$2" "$JOB" <<'PY'
import sys

try:
	import yaml
except ImportError:
	sys.exit("FAIL: PyYAML unavailable; cannot verify the Lite pre-pull tag (pip install pyyaml)")

compose_path, workflow_path, job_name = sys.argv[1], sys.argv[2], sys.argv[3]


def fail(msg):
	sys.exit("FAIL: " + msg)


with open(compose_path) as fh:
	compose = yaml.safe_load(fh) or {}
services = (compose.get("services") or {})
if "postgres" not in services:
	fail(f"{compose_path} has no `postgres` service")
want = (services["postgres"] or {}).get("image")
if not want:
	fail(f"{compose_path}'s postgres service declares no image")

with open(workflow_path) as fh:
	workflow = yaml.safe_load(fh) or {}
jobs = workflow.get("jobs") or {}
if job_name not in jobs:
	fail(f"{workflow_path} has no `{job_name}` job — did it get renamed?")

# Only this job's steps. Another job in the same workflow may legitimately pull
# something else, and a file-wide scan would read whichever came first.
pulled = []
for step in (jobs[job_name].get("steps") or []):
	run = step.get("run")
	if not isinstance(run, str):
		continue
	for line in run.splitlines():
		stripped = line.strip()
		if stripped.startswith("#"):
			continue  # a commented-out pull is not a pull
		if "docker pull " not in stripped:
			continue
		tail = stripped.split("docker pull ", 1)[1].split("&&")[0].split(";")[0]
		# Skip flags so `docker pull --quiet postgres:16` still resolves.
		for tok in tail.split():
			if not tok.startswith("-"):
				pulled.append(tok)
				break

if not pulled:
	fail(
		f"{workflow_path}'s `{job_name}` job has no `docker pull <image>` step.\n"
		"The image must be pulled before the /readyz budget starts; without it the\n"
		"download happens inside the budget and eats the boot margin (#977)."
	)
if len(set(pulled)) > 1:
	fail(f"`{job_name}` pulls more than one image ({', '.join(sorted(set(pulled)))}); this gate expects one")

got = pulled[0]
if got != want:
	fail(
		"the Lite pre-pull image does not match the compose Lite starts.\n"
		f"  {compose_path}  postgres image: {want}\n"
		f"  {workflow_path}  `{job_name}` pulls: {got}\n"
		"Pulling the wrong tag is the same as not pulling: the real image is then\n"
		"downloaded inside the /readyz budget."
	)

print(f"OK: Lite pre-pull matches the compose image ({want})")
PY
}

self_test() {
	local fail=0 tmp out
	_case() { # <name> <want-rc> <want-substring> <compose-yaml> <workflow-yaml>
		local name="$1" want_rc="$2" want_msg="$3" cy="$4" wy="$5" rc=0 out
		printf '%s' "$cy" >"$tmp/compose.yaml"
		printf '%s' "$wy" >"$tmp/wf.yaml"
		out="$(check "$tmp/compose.yaml" "$tmp/wf.yaml" 2>&1)" || rc=$?
		if [ "$rc" != "$want_rc" ]; then
			printf '  FAIL %s (exit)\n    got:  %s\n    want: %s\n    out: %s\n' "$name" "$rc" "$want_rc" "$out"; fail=1; return
		fi
		case "$out" in
			*"$want_msg"*) printf '  ok   %s\n' "$name" ;;
			*) printf '  FAIL %s (message)\n    got: %q\n    want to contain: %q\n' "$name" "$out" "$want_msg"; fail=1 ;;
		esac
	}

	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	local ok_compose ok_wf
	ok_compose=$'services:\n  postgres:\n    image: postgres:16\n  redis:\n    image: redis:7\n'
	ok_wf=$'jobs:\n  lite-cold-start-smoke:\n    steps:\n      - name: pre-pull\n        run: docker pull postgres:16\n'

	_case "matching tags pass" 0 "matches the compose image (postgres:16)" "$ok_compose" "$ok_wf"

	# Without PyYAML nothing was checked, so the gate fails and says why (#969).
	mkdir -p "$tmp/noyaml"
	printf 'raise ImportError("PyYAML hidden by the self-test")\n' >"$tmp/noyaml/yaml.py"
	PYTHONPATH="$tmp/noyaml" _case "a missing PyYAML fails and says so" 1 "FAIL: PyYAML unavailable" "$ok_compose" "$ok_wf"

	# The reviewer's one-line defeat of the previous parser.
	_case "a commented-out pull is not a pull" 1 "has no \`docker pull <image>\` step" "$ok_compose" \
		$'jobs:\n  lite-cold-start-smoke:\n    steps:\n      - name: pre-pull\n        run: |\n          # docker pull postgres:16\n          true\n'

	# A pull in another job must not be read as this job's.
	_case "another job's pull does not count" 1 "has no \`docker pull <image>\` step" "$ok_compose" \
		$'jobs:\n  release:\n    steps:\n      - run: docker pull postgres:16\n  lite-cold-start-smoke:\n    steps:\n      - run: true\n'

	_case "drift is caught" 1 "does not match the compose" \
		$'services:\n  postgres:\n    image: postgres:17-alpine\n' "$ok_wf"

	# The retry wrapper the pull needs is a multi-line block.
	_case "a multi-line retry block still resolves" 0 "matches the compose image (postgres:16)" "$ok_compose" \
		$'jobs:\n  lite-cold-start-smoke:\n    steps:\n      - run: |\n          for i in 1 2 3; do\n            docker pull postgres:16 && exit 0\n            sleep 10\n          done\n          exit 1\n'

	_case "flags are skipped" 0 "matches the compose image (postgres:16)" "$ok_compose" \
		$'jobs:\n  lite-cold-start-smoke:\n    steps:\n      - run: docker pull --quiet postgres:16\n'

	# Quoting and service order are YAML concerns now, not regex concerns.
	_case "a quoted image compares by value" 0 "matches the compose image (postgres:16)" \
		$'services:\n  postgres:\n    image: "postgres:16"\n' "$ok_wf"
	_case "service order does not matter" 0 "matches the compose image (postgres:16)" \
		$'services:\n  redis:\n    image: redis:7\n  postgres:\n    image: postgres:16\n' "$ok_wf"

	_case "a renamed job fails loudly" 1 "did it get renamed?" "$ok_compose" \
		$'jobs:\n  lite-cold-start:\n    steps:\n      - run: docker pull postgres:16\n'

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-docker-compose.dev.yaml}" "${2:-.github/workflows/release.yaml}"
