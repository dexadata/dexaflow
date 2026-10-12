#!/usr/bin/env bash
# The golangci-lint version is pinned in four places, and scripts/chaos/Dockerfile
# already carries the sentence a gate should have carried: "bumping one must bump
# the other". A comment cannot fail a build.
#
#   Makefile GOLANGCI_LINT_VERSION      what `make lint` installs and runs
#   Makefile CHAOS_LINT_VERSION         what the chaos image is built with
#   scripts/chaos/Dockerfile ARG        that image's default, for a bare docker build
#   .github/workflows/*.yaml            what the CI job that decides the grade runs
#
# ADR 0012 makes golangci-lint the arbiter of the A+ floor and AGENTS.md makes
# "make lint is clean" the precondition for a commit. Both sentences are only
# true while the two run the same binary. Drift here does not break anything
# loudly — it makes a clean local run stop predicting CI, in whichever direction
# the newer version added or dropped a check, and the contributor who hits it
# has no reason to suspect the pin.
#
# The Makefile is the source of truth because it is the copy a contributor runs
# before pushing; everything else exists to reproduce it.
#
# Prose is deliberately NOT scanned here, unlike check-go-toolchain-pin.sh. The
# chaos Dockerfile records that one specific version's arm64 build had a flaky
# installer checksum — a statement about that version, still true after a bump,
# and a gate that demanded it be rewritten would be demanding a lie.
#
# Usage: scripts/check-golangci-lint-pin.sh [--self-test]
#        scripts/check-golangci-lint-pin.sh <repo-root>
set -euo pipefail

check() { # <repo-root>
	python3 - "$1" <<'PY'
import os
import re
import sys

try:
	import yaml
except ImportError:
	sys.exit("FAIL: PyYAML unavailable; cannot parse the workflows (pip install pyyaml)")

root = sys.argv[1]
problems = []
EXPR = re.compile(r"\$\{\{")


def fail(msg):
	sys.exit("FAIL: " + msg)


def read(rel):
	path = os.path.join(root, rel)
	if not os.path.exists(path):
		fail(f"{rel} does not exist — this gate's scan target moved and it would otherwise report OK")
	with open(path) as fh:
		return fh.read()


def note(rel, what, got):
	problems.append(f"  {rel}\n    {what}: {got}\n    Makefile GOLANGCI_LINT_VERSION: {truth}")


# ── the source of truth ──────────────────────────────────────────────────────
makefile = read("Makefile")
m = re.search(r"^GOLANGCI_LINT_VERSION\s*\?=\s*(\S+)\s*$", makefile, re.M)
if not m:
	fail(
		"Makefile declares no `GOLANGCI_LINT_VERSION ?= v<X.Y.Z>`.\n"
		"That is the pin `make lint` installs, which is the run a contributor is told to make\n"
		"clean before pushing. Restore it, or move the source of truth deliberately."
	)
truth = m.group(1)
if not re.fullmatch(r"v\d+\.\d+\.\d+", truth):
	fail(
		f"Makefile pins GOLANGCI_LINT_VERSION to {truth!r}, which is not a `vX.Y.Z` release.\n"
		"`latest` or a floating major would make the local run and the CI run disagree by\n"
		"calendar date rather than by a commit anyone can point at."
	)

# ── 1. the chaos image's pin, in the Makefile ────────────────────────────────
m = re.search(r"^CHAOS_LINT_VERSION\s*\?=\s*(\S+)\s*$", makefile, re.M)
if not m:
	fail("Makefile declares no `CHAOS_LINT_VERSION ?= ...` — did the chaos target get renamed?")
if m.group(1) != truth:
	note("Makefile", "CHAOS_LINT_VERSION", m.group(1))

# ── 2. the chaos image's own default ─────────────────────────────────────────
rel = "scripts/chaos/Dockerfile"
m = re.search(r"^ARG GOLANGCI_LINT_VERSION=(\S+)\s*$", read(rel), re.M)
if not m:
	fail(
		f"{rel} declares no `ARG GOLANGCI_LINT_VERSION=...` default.\n"
		"The Makefile always passes one, so this default only shows up on a bare `docker build`\n"
		"— which is exactly the path nobody watches."
	)
if m.group(1) != truth:
	note(rel, "ARG GOLANGCI_LINT_VERSION default", m.group(1))

# ── 3. every workflow copy ───────────────────────────────────────────────────
# Parsed as YAML, so a commented-out env is not read as a declaration, and a
# `${{ ... }}` reference is skipped because it resolves to a copy checked here.
ACTION = "golangci/golangci-lint-action"


def walk(node, path, rel, found):
	if isinstance(node, dict):
		env = node.get("env")
		if isinstance(env, dict) and "GOLANGCI_LINT_VERSION" in env:
			v = str(env["GOLANGCI_LINT_VERSION"])
			found.append(True)
			if not EXPR.search(v) and v != truth:
				note(rel, f"{path}env.GOLANGCI_LINT_VERSION", v)
		uses = node.get("uses")
		with_ = node.get("with")
		if isinstance(uses, str) and uses.startswith(ACTION) and isinstance(with_, dict):
			v = str(with_.get("version", ""))
			found.append(True)
			if not v:
				problems.append(
					f"  {rel}\n"
					f"    {path}uses {ACTION} with no `version:`\n"
					"    An unpinned action installs whatever it defaults to that week, so the run that\n"
					"    decides the A+ grade stops being the run a contributor can reproduce."
				)
			elif not EXPR.search(v) and v != truth:
				note(rel, f"{path}with.version", v)
		for k, v in node.items():
			walk(v, f"{path}{k}.", rel, found)
	elif isinstance(node, list):
		for i, v in enumerate(node):
			walk(v, f"{path}[{i}].", rel, found)


wfdir = os.path.join(root, ".github", "workflows")
if not os.path.isdir(wfdir):
	fail(".github/workflows/ does not exist — this gate cannot see the CI pin")
found = []
for name in sorted(os.listdir(wfdir)):
	if not name.endswith((".yaml", ".yml")):
		continue
	rel = f".github/workflows/{name}"
	try:
		doc = yaml.safe_load(read(rel))
	except yaml.YAMLError as exc:
		fail(f"{rel} is not parseable YAML: {exc}")
	walk(doc, "", rel, found)
if not found:
	fail(
		f"no workflow declares a golangci-lint version (neither `env.GOLANGCI_LINT_VERSION` nor\n"
		f"a `{ACTION}` step). ADR 0012 makes that job the arbiter of the A+ floor; a gate that\n"
		"finds no CI copy at all must not report OK."
	)

if problems:
	sys.exit(
		"FAIL: the golangci-lint pin has drifted.\n"
		+ "\n".join(problems)
		+ f"\n\nThe Makefile's `GOLANGCI_LINT_VERSION ?= {truth}` is the source of truth: it is the\n"
		"binary `make lint` runs, and 'make lint is clean' only predicts CI while the two match."
	)

print(f"OK: the golangci-lint pin agrees everywhere ({truth})")
PY
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	_makefile() { printf 'GOLANGCI_LINT_VERSION ?= %s\nCHAOS_LINT_VERSION   ?= %s\n\nlint:\n\t@echo lint\n' "$2" "$3" >"$1/Makefile"; }
	_chaos() { printf 'ARG GOLANGCI_LINT_VERSION=%s\nFROM python:3.12-slim\n' "$2" >"$1/scripts/chaos/Dockerfile"; }
	_ci() { # <dir> <env-value> <action-version>
		printf 'name: CI\nenv:\n  GOLANGCI_LINT_VERSION: "%s"\njobs:\n  lint:\n    steps:\n      - uses: golangci/golangci-lint-action@abc # v8\n        with:\n          version: %s\n' \
			"$2" "$3" >"$1/.github/workflows/ci.yaml"
	}

	_mkroot() { # <dir>
		local d="$1"
		mkdir -p "$d/.github/workflows" "$d/scripts/chaos"
		_makefile "$d" v2.12.2 v2.12.2
		_chaos "$d" v2.12.2
		_ci "$d" v2.12.2 '${{ env.GOLANGCI_LINT_VERSION }}'
	}

	_case() { # <name> <want-rc> <want-substring> <mutator...>
		local name="$1" want_rc="$2" want_msg="$3" rc=0 out d
		shift 3
		d="$tmp/case"
		rm -rf "$d"
		_mkroot "$d"
		"$@" "$d"
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
	_ci_bumped_alone() { _ci "$1" v2.13.0 '${{ env.GOLANGCI_LINT_VERSION }}'; }
	_action_pins_its_own() { _ci "$1" v2.12.2 v2.13.0; }
	_action_pins_nothing() { printf 'name: CI\njobs:\n  lint:\n    steps:\n      - uses: golangci/golangci-lint-action@abc # v8\n        with:\n          install-mode: goinstall\n' >"$1/.github/workflows/ci.yaml"; }
	_chaos_lags() { _makefile "$1" v2.12.2 v2.11.0; }
	_chaos_dockerfile_lags() { _chaos "$1" v2.11.0; }
	_missing_chaos_arg() { printf 'FROM python:3.12-slim\n' >"$1/scripts/chaos/Dockerfile"; }
	_missing_chaos_make() { printf 'GOLANGCI_LINT_VERSION ?= v2.12.2\n\nlint:\n\t@echo lint\n' >"$1/Makefile"; }
	_no_makefile_pin() { printf 'lint:\n\t@echo lint\n' >"$1/Makefile"; }
	_floating_makefile_pin() { _makefile "$1" latest latest; }
	_no_ci_copy() { printf 'name: CI\njobs:\n  lint:\n    steps:\n      - run: true\n' >"$1/.github/workflows/ci.yaml"; }
	# A version named in prose is left alone here on purpose: the chaos Dockerfile
	# records a defect in one specific build, which stays true after a bump.
	_prose_naming_an_old_version_is_fine() {
		printf '# The installer checksum was flaky on the v2.11.0 arm64 build.\n' >>"$1/scripts/chaos/Dockerfile"
	}

	_case "an agreeing tree passes"                 0 "agrees everywhere (v2.12.2)" _noop
	_case "CI bumped on its own"                    1 "env.GOLANGCI_LINT_VERSION: v2.13.0" _ci_bumped_alone
	_case "the action pins its own version"         1 "with.version: v2.13.0" _action_pins_its_own
	_case "the action pins nothing at all"          1 "with no \`version:\`" _action_pins_nothing
	_case "the chaos make pin lags"                 1 "CHAOS_LINT_VERSION: v2.11.0" _chaos_lags
	_case "the chaos Dockerfile default lags"       1 "ARG GOLANGCI_LINT_VERSION default: v2.11.0" _chaos_dockerfile_lags
	_case "a dropped ARG fails, not skips"          1 "declares no \`ARG GOLANGCI_LINT_VERSION" _missing_chaos_arg
	_case "a dropped CHAOS_LINT_VERSION"            1 "did the chaos target get renamed?" _missing_chaos_make
	_case "no Makefile pin fails, not skips"        1 "declares no \`GOLANGCI_LINT_VERSION" _no_makefile_pin
	_case "a floating Makefile pin is refused"      1 "not a \`vX.Y.Z\` release" _floating_makefile_pin
	_case "an empty CI scan fails, not passes"      1 "no workflow declares a golangci-lint version" _no_ci_copy
	_case "prose about an old version is fine"      0 "agrees everywhere" _prose_naming_an_old_version_is_fine

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-$PWD}"
