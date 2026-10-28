#!/usr/bin/env bash
# The data variant of the task image (runtime/data/README.md) installs its lock
# on top of the base with `pip --no-deps`, so whatever the lock pins wins. The
# base pins the Task SDK and certifi as ARG defaults in runtime/Dockerfile, and
# the data lock repeats both: runtime/data/requirements.in states them and
# runtime/data/requirements.txt locks them. The README says "bump them in
# both", and a sentence cannot fail a build. A bump of the Dockerfile alone
# would ship a data image that silently moves the SDK or the CA bundle back
# under every source-mode DAG.
#
# runtime/Dockerfile is the source of truth: it is what the base image runs.
#
# Usage: scripts/check-runtime-data-pins.sh [--self-test]
#        scripts/check-runtime-data-pins.sh <repo-root>
set -euo pipefail

check() { # <repo-root>
	python3 - "$1" <<'PY'
import os
import re
import sys

root = sys.argv[1]
PINS = {
	"AIRFLOW_TASK_SDK_VERSION": "apache-airflow-task-sdk",
	"CERTIFI_VERSION": "certifi",
}


def fail(msg):
	sys.exit("FAIL: " + msg)


def read(rel):
	path = os.path.join(root, rel)
	if not os.path.exists(path):
		fail(f"{rel} does not exist; this gate's scan target moved and it would otherwise report OK")
	with open(path) as fh:
		return fh.read()


dockerfile = read("runtime/Dockerfile")
inputs = read("runtime/data/requirements.in")
lock = read("runtime/data/requirements.txt")


def pinned(text, package):
	found = re.findall(rf"^{re.escape(package)}==([^\s\\;]+)", text, re.M)
	return found


problems = []
for arg, package in PINS.items():
	m = re.findall(rf"^ARG {arg}=(\S+)$", dockerfile, re.M)
	if len(m) != 1:
		fail(f"runtime/Dockerfile declares ARG {arg}= {len(m)} time(s), want exactly 1")
	truth = m[0]
	for rel, text in (("runtime/data/requirements.in", inputs), ("runtime/data/requirements.txt", lock)):
		got = pinned(text, package)
		if got != [truth]:
			problems.append(
				f"  {rel}\n"
				f"    pins {package}: {', '.join(got) or 'nothing'}\n"
				f"    runtime/Dockerfile ARG {arg}: {truth}"
			)

if problems:
	sys.exit(
		"FAIL: the data variant's lock has drifted from the base image's pins.\n"
		+ "\n".join(problems)
		+ "\n\nEdit runtime/data/requirements.in and recompile the lock (runtime/data/README.md)."
	)

print("OK: the data variant pins the base's " + ", ".join(PINS.values()))
PY
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	_mkroot() { # <dir>
		mkdir -p "$1/runtime/data"
		printf 'ARG AIRFLOW_TASK_SDK_VERSION=1.3.1\nARG CERTIFI_VERSION=2026.10.19\n' >"$1/runtime/Dockerfile"
		printf 'apache-airflow-task-sdk==1.3.1\ncertifi==2026.10.19\ndbt-core\n' >"$1/runtime/data/requirements.in"
		printf 'apache-airflow-task-sdk==1.3.1 \\\n    --hash=sha256:aa\ncertifi==2026.10.19 \\\n    --hash=sha256:bb\n' >"$1/runtime/data/requirements.txt"
	}

	_case() { # <name> <want-rc> <want-substring> <mutator>
		local name="$1" want_rc="$2" want_msg="$3" rc=0 out d
		d="$tmp/case"
		rm -rf "$d"
		_mkroot "$d"
		"$4" "$d"
		out="$(check "$d" 2>&1)" || rc=$?
		if [ "$rc" != "$want_rc" ]; then
			printf '  FAIL %s (exit)\n    got:  %s\n    want: %s\n    out: %s\n' "$name" "$rc" "$want_rc" "$out"
			fail=1
			return
		fi
		case "$out" in
			*"$want_msg"*) printf '  ok   %s\n' "$name" ;;
			*) printf '  FAIL %s (message)\n    got: %q\n    want to contain: %q\n' "$name" "$out" "$want_msg"; fail=1 ;;
		esac
	}

	_noop() { :; }
	_base_bumped_alone() { sed -i.bak 's/CERTIFI_VERSION=2026.10.19/CERTIFI_VERSION=2026.11.1/' "$1/runtime/Dockerfile"; }
	_lock_not_recompiled() { sed -i.bak 's/sdk==1.3.1/sdk==1.3.2/' "$1/runtime/data/requirements.in"; }
	_input_unpinned() { sed -i.bak 's/^certifi==.*/certifi/' "$1/runtime/data/requirements.in"; }
	_missing_lock() { rm -f "$1/runtime/data/requirements.txt"; }
	_arg_renamed() { sed -i.bak 's/CERTIFI_VERSION/CA_VERSION/' "$1/runtime/Dockerfile"; }

	_case "an agreeing tree passes"            0 "OK: the data variant pins" _noop
	_case "a base bump alone fails"            1 "ARG CERTIFI_VERSION: 2026.11.1" _base_bumped_alone
	_case "an input bump without the lock"     1 "requirements.in" _lock_not_recompiled
	_case "an unpinned input fails"            1 "pins certifi: nothing" _input_unpinned
	_case "a missing lock fails, not skips"    1 "does not exist" _missing_lock
	_case "a renamed ARG fails, not skips"     1 "0 time(s)" _arg_renamed

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-$PWD}"
