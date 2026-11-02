#!/usr/bin/env bash
# Verify every directory .github/dependabot.yaml points at actually exists.
#
# Dependabot does not complain about a directory with no manifest — it finds
# nothing and reports nothing. A config entry for a path that was renamed or
# never existed therefore looks configured and silently updates nothing, which
# is how this repository went its whole life without a single base-image update:
# three docker entries pointed at /runtime/python-3.1{0,1,2}, none of which are
# in the tree.
#
# Nothing else can catch this. Dependabot's own validation checks syntax, not
# whether the path resolves, and a missing update is invisible by definition.
#
# Without PyYAML the gate cannot check anything, so it fails and says why
# (#969). It used to call that a SKIP while exiting 1, which cut-release's
# run_gates reports as a FAIL: the message and the result disagreed.
#
# Usage: scripts/check-dependabot-dirs.sh [--self-test]
set -euo pipefail

check() { # <root>
	local config="$1/.github/dependabot.yaml"
	if [[ ! -f "$config" ]]; then
		echo "FAIL: $config not found" >&2
		return 1
	fi
	(cd "$1" && python3 - .github/dependabot.yaml <<'PY'
import os
import sys

try:
	import yaml
except ImportError:
	sys.exit("FAIL: PyYAML unavailable; cannot verify dependabot directories (pip install pyyaml)")

config = sys.argv[1]
with open(config, encoding="utf-8") as fh:
	cfg = yaml.safe_load(fh)

missing = []
checked = 0
for update in cfg.get("updates", []):
	eco = update.get("package-ecosystem", "?")
	dirs = update.get("directories") or [update.get("directory")]
	for d in dirs:
		if d is None or "*" in d:
			# A glob is resolved by Dependabot at run time; nothing to verify.
			continue
		checked += 1
		# "/" is the repo root: lstrip leaves an empty string, which is not a dir.
		if not os.path.isdir(d.lstrip("/") or "."):
			missing.append(f"  {eco}: {d}")

if missing:
	print(f"FAIL: {config} points at directories that do not exist:")
	print("\n".join(missing))
	print("\nDependabot silently finds no manifest there, so those updates never run.")
	sys.exit(1)

print(f"OK: all {checked} dependabot directories resolve")
PY
	)
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	_case() { # <name> <want-rc> <want-substr> [env...]
		local name="$1" want="$2" substr="$3" rc=0 out
		shift 3
		out="$(env "$@" bash -c "$(declare -f check); check '$tmp/root'" 2>&1)" || rc=$?
		if [ "$rc" != "$want" ]; then
			printf '  FAIL %s: exit %s, wanted %s\n    %s\n' "$name" "$rc" "$want" "$out"; fail=1; return
		fi
		case "$out" in *"$substr"*) printf '  ok   %s\n' "$name" ;;
		*) printf '  FAIL %s: output lacks %q\n    %s\n' "$name" "$substr" "$out"; fail=1 ;; esac
	}

	mkdir -p "$tmp/root/.github" "$tmp/root/deploy" "$tmp/noyaml"
	printf 'raise ImportError("PyYAML hidden by the self-test")\n' >"$tmp/noyaml/yaml.py"
	printf 'version: 2\nupdates:\n  - package-ecosystem: docker\n    directory: /deploy\n' >"$tmp/root/.github/dependabot.yaml"
	_case "a resolving directory passes"            0 "OK: all 1 dependabot directories resolve"
	_case "a missing PyYAML fails and says so"      1 "FAIL: PyYAML unavailable" PYTHONPATH="$tmp/noyaml"
	printf 'version: 2\nupdates:\n  - package-ecosystem: docker\n    directory: /gone\n' >"$tmp/root/.github/dependabot.yaml"
	_case "a directory that does not exist fails"   1 "/gone"

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; fi
	echo "self-test: FAIL"; return 1
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "$(pwd)"
