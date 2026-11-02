#!/usr/bin/env bash
# The Hugo the PR gate builds with must be the Hugo the deploy publishes with.
#
# website-build.yml (per-PR) and website-deploy.yml (on merge) each declare their
# own `HUGO_VERSION` and each interpolates it into the SAME download URL:
#
#   https://github.com/gohugoio/hugo/releases/download/v${HUGO_VERSION}/hugo_extended_${HUGO_VERSION}_linux-amd64.deb
#
# Two copies of a version string with nothing between them. If they drift, the
# PR gate validates a site the deploy never builds — a Docsy shortcode, a
# template function or a config key that works in one Hugo and not the other
# passes review and then breaks production, and the diagnostic arrives on `main`
# rather than on the PR that caused it.
#
# #1036 first skipped this on three grounds, and review took all three apart:
# the copies are in adjacent files but not adjacent lines; hugo.toml's
# `module.hugoVersion.min` is a FLOOR, a different relation this gate can
# declare out of scope the same way the Go gate scopes out prose; and "it fails
# loudly on the next deploy" is the argument the golangci-lint pin gate already
# rejected, because failing on the deploy is failing in the wrong place.
#
# The floor is checked too, but only as a floor: a pin BELOW the minimum the site
# config demands is a configuration that cannot build, and that is worth catching
# here rather than in a Hugo error message.
#
# Usage:
#   scripts/check-hugo-version-pin.sh
#   scripts/check-hugo-version-pin.sh --self-test
set -euo pipefail

check() { # <root>
	python3 - "$1" <<'PY'
import os
import re
import sys

root = sys.argv[1]

try:
	import yaml
except ImportError:
	sys.exit("FAIL: PyYAML unavailable; cannot read the workflow HUGO_VERSION values (pip install pyyaml)")


def fail(msg):
	sys.exit("FAIL: " + msg)


def read(rel):
	path = os.path.join(root, rel)
	try:
		with open(path) as fh:
			return fh.read()
	except OSError as exc:
		fail(f"{rel} does not exist ({exc}) — this gate's scan target moved and it would otherwise report OK")


WORKFLOWS = ("website-build.yml", "website-deploy.yml")
pins = {}
for name in WORKFLOWS:
	rel = f".github/workflows/{name}"
	text = read(rel)
	# Parsed as YAML, not grepped: a commented-out `# HUGO_VERSION: "0.1.0"` is
	# the most common way a value gets disabled while debugging, and a grep
	# happily reads it back as the live value.
	doc = yaml.safe_load(text) or {}
	env = doc.get("env") or {}
	v = env.get("HUGO_VERSION")
	if v is None:
		fail(
			f"{rel} declares no top-level `env.HUGO_VERSION`.\n"
			"Either it was renamed or moved into a job, or this gate is reading the wrong key;\n"
			"it must not report OK on a value it could not find."
		)
	v = str(v).strip()
	if not re.fullmatch(r"\d+\.\d+\.\d+", v):
		fail(f"{rel}'s HUGO_VERSION is {v!r}, not an `X.Y.Z` Hugo release. The download URL interpolates it verbatim.")
	pins[rel] = v
	# The pin has to be the one the URL actually uses, not merely present.
	if "hugo_extended_${HUGO_VERSION}" not in text:
		fail(
			f"{rel} declares HUGO_VERSION but no longer interpolates it into the hugo_extended\n"
			"download URL, so the value this gate reconciles is not the version that gets installed."
		)

distinct = sorted(set(pins.values()))
if len(distinct) != 1:
	detail = "\n".join(f"    {rel}: {v}" for rel, v in sorted(pins.items()))
	fail(
		"the two website workflows pin different Hugo versions:\n"
		f"{detail}\n"
		"    The per-PR gate would validate the site with one Hugo and the deploy publish it\n"
		"    with another, so a shortcode or config key that works in one and not the other\n"
		"    passes review and breaks on main. Set both to the same X.Y.Z."
	)
pin = distinct[0]

# The floor in hugo.toml is a different relation — a minimum, not a copy — so it
# is only checked for consistency in the one direction that is unbuildable.
floor_rel = "website/hugo.toml"
floor_text = read(floor_rel)
fm = re.search(r"\[module\.hugoVersion\][^\[]*?\bmin\s*=\s*\"([0-9.]+)\"", floor_text, re.S)
if not fm:
	fail(f"{floor_rel} declares no `[module.hugoVersion] min = \"X.Y.Z\"` — this gate's floor target moved.")
floor = fm.group(1)


def parts(v):
	return tuple(int(x) for x in v.split("."))


if parts(pin) < parts(floor[:]):
	fail(
		f"the workflows pin Hugo {pin} but {floor_rel} requires at least {floor}.\n"
		"    That configuration cannot build; Hugo refuses the module at startup."
	)

print(f"hugo pin: {pin} in both website workflows (>= the {floor} floor in {floor_rel})")
PY
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	_wf() { # <dir> <name> <version> [extra]
		printf 'name: x\nenv:\n  HUGO_VERSION: "%s"\njobs:\n  b:\n    steps:\n      - run: |\n          url="https://github.com/gohugoio/hugo/releases/download/v${HUGO_VERSION}/hugo_extended_${HUGO_VERSION}_linux-amd64.deb"\n' "$3" >"$1/.github/workflows/$2"
	}
	_toml() { printf '[module]\n  [module.hugoVersion]\n    extended = true\n    min = "%s"\n' "$2" >"$1/website/hugo.toml"; }

	_mkroot() { # <dir>
		mkdir -p "$1/.github/workflows" "$1/website"
		_wf "$1" website-build.yml 0.165.0
		_wf "$1" website-deploy.yml 0.165.0
		_toml "$1" 0.110.0
	}

	_case() { # <name> <want-rc> <want-substr> <mutator...>
		local name="$1" want="$2" substr="$3" rc=0 out d
		shift 3
		d="$tmp/case"; rm -rf "$d"; _mkroot "$d"; "$@" "$d"
		out="$(check "$d" 2>&1)" || rc=$?
		if [ "$rc" != "$want" ]; then
			printf '  FAIL %s — exit %s, wanted %s\n    %s\n' "$name" "$rc" "$want" "$out"; fail=1; return
		fi
		case "$out" in *"$substr"*) printf '  ok   %s\n' "$name" ;;
		*) printf '  FAIL %s — output lacks %q\n    %s\n' "$name" "$substr" "$out"; fail=1 ;; esac
	}

	_noop() { :; }
	_drift() { _wf "$1" website-deploy.yml 0.140.0; }
	_below_floor() { _wf "$1" website-build.yml 0.100.0; _wf "$1" website-deploy.yml 0.100.0; }
	_commented_out() { printf 'name: x\nenv:\n  # HUGO_VERSION: "0.165.0"\njobs:\n  b:\n    steps:\n      - run: echo hugo_extended_${HUGO_VERSION}\n' >"$1/.github/workflows/website-build.yml"; }
	_not_semver() { _wf "$1" website-build.yml latest; }
	_url_no_longer_uses_it() { printf 'name: x\nenv:\n  HUGO_VERSION: "0.165.0"\njobs:\n  b:\n    steps:\n      - run: apt-get install hugo\n' >"$1/.github/workflows/website-deploy.yml"; }
	_moved() { rm -f "$1/.github/workflows/website-deploy.yml"; }

	_case "an agreeing tree passes"                      0 "0.165.0 in both website workflows" _noop
	_case "drift between the two workflows is caught"    1 "pin different Hugo versions"        _drift
	_case "a pin below the hugo.toml floor is caught"    1 "requires at least 0.110.0"          _below_floor
	_case "a commented-out declaration fails loudly"     1 "declares no top-level"              _commented_out
	_case "a non-semver pin is refused"                  1 "not an \`X.Y.Z\` Hugo release"      _not_semver
	_case "a pin the URL stops using is caught"          1 "no longer interpolates it"          _url_no_longer_uses_it
	_case "a moved workflow fails, not skips"            1 "does not exist"                     _moved
	# A missing PyYAML is a gate that could not check, so it fails (#969). It
	# used to print SKIP and exit 1, which cut-release reported as a FAIL anyway.
	mkdir -p "$tmp/noyaml"
	printf 'raise ImportError("PyYAML hidden by the self-test")\n' >"$tmp/noyaml/yaml.py"
	_without_yaml() { :; }
	PYTHONPATH="$tmp/noyaml" _case "a missing PyYAML fails and says so" 1 "FAIL: PyYAML unavailable" _without_yaml

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; fi
	echo "self-test: FAIL"; return 1
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "$(pwd)"
