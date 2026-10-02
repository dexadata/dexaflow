#!/usr/bin/env bash
# Companion gate: scripts/check-migrate-cli-build-tags.sh reconciles
# deploy/Dockerfile.migrate's pin against security.yaml's GO_VERSION, which this
# gate in turn reconciles against go.mod. That file is covered here directly too
# (both its ARG default and its `golang:` literal), so the two overlap rather
# than depend on each other — but if you change either, read the other.
#
# The Go toolchain Dexaflow builds with is stated in eleven places. Until #1036 it
# said three different things: `toolchain go1.26.6` in go.mod, `1.26.3` in
# runtime/Dockerfile's GO_VERSION default, `1.26.4` in the Makefile's chaos
# pin, and `golang:1.27-bookworm` in deploy/Dockerfile.server after a Dependabot
# bump touched the one file with a literal tag.
#
# Nothing was broken by that, and that is the point. CI and release both pass
# their own literal, so the published artifacts were consistent; the drift only
# bit `make runtime-images` and `make chaos-dogfood-docker`, which nobody
# releases. What it did do is make every one of the four a false premise for the
# next reader — three people read three different files today and reasoned
# correctly to three different conclusions about which toolchain ships.
#
# Same shape as check-lite-prepull-matches-compose.sh: two copies of a version
# string with nothing between them. The source of truth here is go.mod's
# `toolchain` line, because it is the one the `go` command itself honors — every
# other copy exists to make some other tool agree with it.
#
# The prose is reconciled too, not just the declarations. security.yaml's
# comment argued at length about `golang:1.27-bookworm` being the right pin
# while go.mod said 1.26.6; a comment that asserts a version the tree does not
# use outlives every defect, because nothing tests it.
#
# Deliberately out of scope: prose in website/ and CHANGELOG.md. A released
# changelog section naming its own era's toolchain is a record, not drift, and
# the docs speak in major.minor ("Go 1.26") where this gate reconciles a patch.
#
# Usage: scripts/check-go-toolchain-pin.sh [--self-test]
#        scripts/check-go-toolchain-pin.sh <repo-root>
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
TRUTH_REL = "go.mod"

# Every file that must carry a declaration. A missing one is a FAIL, never a
# silent pass: a gate that stops gating after a file move still reports OK,
# which is worse than no gate at all.
REQUIRED = [
	"go.mod",
	"website/go.mod",
	"Makefile",
	"runtime/Dockerfile",
	"deploy/Dockerfile.server",
	"scripts/chaos/Dockerfile",
	".github/workflows/ci.yaml",
	".github/workflows/release.yaml",
]

PATCH_RE = re.compile(r"^\d+\.\d+\.\d+$")


def fail(msg):
	sys.exit("FAIL: " + msg)


def read(rel):
	path = os.path.join(root, rel)
	if not os.path.exists(path):
		fail(f"{rel} does not exist — this gate's scan target moved and it would otherwise report OK")
	with open(path) as fh:
		return fh.read()


def note(rel, what, got):
	problems.append(f"  {rel}\n    {what}: {got}\n    {TRUTH_REL} toolchain: {truth}")


for rel in REQUIRED:
	read(rel)

# ── the source of truth ──────────────────────────────────────────────────────
gomod = read("go.mod")
m = re.search(r"^toolchain go(\d+\.\d+\.\d+)\s*$", gomod, re.M)
if not m:
	fail(
		"go.mod declares no `toolchain go<X.Y.Z>` line. That line is what the `go` command\n"
		"itself honors, and this gate reconciles every other copy against it. Restore it,\n"
		"or move the source of truth deliberately and rewrite this gate to match."
	)
truth = m.group(1)
truth_minor = ".".join(truth.split(".")[:2])

# ── 1. go.mod's own language directive ───────────────────────────────────────
# `go 1.26.0` may legally sit below the toolchain, but then the two lines three
# rows apart in one file state different minors, which is the drift this gate is
# for. Equality of the MINOR is the invariant; the patch is free.
m = re.search(r"^go (\d+\.\d+)(?:\.\d+)?\s*$", gomod, re.M)
if not m:
	fail("go.mod declares no `go <X.Y>` directive")
if m.group(1) != truth_minor:
	note("go.mod", "`go` directive minor", m.group(1))

# ── 2. the website module, installed by go-version-file ──────────────────────
# website-build.yml points actions/setup-go at `website/go.mod`, so this
# directive IS the toolchain that job gets. website-deploy.yml uses
# `src/website/go.mod` — the same file under its own checkout path, not a
# second module. Stated precisely because this gate's whole thesis is that an
# untested sentence in a comment is the bug: the earlier wording claimed both
# workflows name the same path, and they do not.
website = read("website/go.mod")
m = re.search(r"^go (\d+\.\d+(?:\.\d+)?)\s*$", website, re.M)
if not m:
	fail("website/go.mod declares no `go <X.Y[.Z]>` directive")
if m.group(1) != truth:
	note("website/go.mod", "`go` directive (setup-go reads this via go-version-file)", m.group(1))

# ── 3. every Go version a workflow pins ──────────────────────────────────────
# Parsed as YAML rather than grepped, so a commented-out env cannot be read as a
# declaration. `${{ ... }}` expressions are skipped: they resolve to some other
# copy, which is itself checked here.
EXPR = re.compile(r"\$\{\{")


def walk(node, path, wf_rel, found):
	if isinstance(node, dict):
		env = node.get("env")
		if isinstance(env, dict) and "GO_VERSION" in env:
			v = str(env["GO_VERSION"])
			found.append(True)
			if not EXPR.search(v) and v != truth:
				note(wf_rel, f"{path}env.GO_VERSION", v)
		with_ = node.get("with")
		if isinstance(with_, dict) and "go-version" in with_:
			v = str(with_["go-version"])
			found.append(True)
			if not EXPR.search(v) and v != truth:
				note(wf_rel, f"{path}with.go-version", v)
		for k, v in node.items():
			walk(v, f"{path}{k}.", wf_rel, found)
	elif isinstance(node, list):
		for i, v in enumerate(node):
			walk(v, f"{path}[{i}].", wf_rel, found)


wfdir = os.path.join(root, ".github", "workflows")
if not os.path.isdir(wfdir):
	fail(".github/workflows/ does not exist — this gate cannot see the CI pins")
pinned_workflows = 0
for name in sorted(os.listdir(wfdir)):
	if not name.endswith((".yaml", ".yml")):
		continue
	rel = f".github/workflows/{name}"
	try:
		doc = yaml.safe_load(read(rel))
	except yaml.YAMLError as exc:
		fail(f"{rel} is not parseable YAML: {exc}")
	found = []
	walk(doc, "", rel, found)
	if found:
		pinned_workflows += 1
if pinned_workflows == 0:
	fail(
		"no workflow declares a Go version at all (neither `env.GO_VERSION` nor a setup-go\n"
		"`with.go-version`). Either the keys were renamed or this gate is scanning the wrong\n"
		"directory; it must not report OK on an empty scan."
	)

# ── 4. the Dockerfile ARG defaults ───────────────────────────────────────────
# deploy/Dockerfile.migrate arrived with #1039, after this gate was written, and
# was covered only by the `golang:` literal scan below — its ARG default could be
# moved alone and this gate still printed OK. Its pin is ALSO reconciled against
# security.yaml by scripts/check-migrate-cli-build-tags.sh, so the two gates form
# a two-hop chain; neither header said so, which is how a seam becomes a hole.
for rel in ("runtime/Dockerfile", "deploy/Dockerfile.server", "deploy/Dockerfile.migrate", "scripts/chaos/Dockerfile"):
	text = read(rel)
	m = re.search(r"^ARG GO_VERSION=(\S+)\s*$", text, re.M)
	if not m:
		fail(
			f"{rel} declares no `ARG GO_VERSION=<X.Y.Z>` default.\n"
			"That default is what a plain `docker build` of this file uses, which is the only\n"
			"path CI does not override — restore it, or drop this file from REQUIRED deliberately."
		)
	if not PATCH_RE.match(m.group(1)):
		problems.append(
			f"  {rel}\n"
			f"    ARG GO_VERSION default: {m.group(1)}\n"
			"    Pin a full patch version. A major.minor tag floats onto whatever upstream last\n"
			"    pushed, so the image nobody rebuilt is not the image the pin describes."
		)
	elif m.group(1) != truth:
		note(rel, "ARG GO_VERSION default", m.group(1))

# ── 5. the Makefile's chaos pin ──────────────────────────────────────────────
makefile = read("Makefile")
m = re.search(r"^CHAOS_GO_VERSION\s*\?=\s*(\S+)\s*$", makefile, re.M)
if not m:
	fail("Makefile declares no `CHAOS_GO_VERSION ?= <X.Y.Z>` — did the chaos target get renamed?")
if m.group(1) != truth:
	note("Makefile", "CHAOS_GO_VERSION", m.group(1))

# ── 6. every `golang:<version>` literal, comments included ───────────────────
# Prose counts here, deliberately. security.yaml carried three paragraphs
# arguing about `golang:1.27-bookworm` while go.mod said 1.26.6; that comment
# outlived the mismatch because nothing tested it. A suffix-free or
# deliberately generalized reference (`golang:1.x-bookworm`) does not match and
# is always accepted — it cannot go stale.
IMAGE_RE = re.compile(r"golang:(\d+\.\d+(?:\.\d+)?)(?![\w.])(?:-[A-Za-z0-9.]+)?")
scan = [
	"runtime/Dockerfile",
	"deploy/Dockerfile.server",
	"deploy/Dockerfile.server.release",
	"deploy/Dockerfile.migrate",
	"scripts/chaos/Dockerfile",
	"Makefile",
]
scan += [f".github/workflows/{n}" for n in sorted(os.listdir(wfdir)) if n.endswith((".yaml", ".yml"))]
for rel in scan:
	if not os.path.exists(os.path.join(root, rel)):
		continue
	for lineno, line in enumerate(read(rel).splitlines(), 1):
		for m in IMAGE_RE.finditer(line):
			if m.group(1) == truth:
				continue
			problems.append(
				f"  {rel}:{lineno}\n"
				f"    names {m.group(0)}\n"
				f"    {TRUTH_REL} toolchain: {truth}\n"
				f"    Write `golang:{truth}-...`, or drop the version from the literal if the\n"
				"    sentence is about the base image in general. A version in prose is read as a\n"
				"    fact and nothing else tests it."
			)

if problems:
	sys.exit(
		"FAIL: the Go toolchain pin has drifted.\n"
		+ "\n".join(problems)
		+ f"\n\ngo.mod's `toolchain go{truth}` is the source of truth: it is what the `go`\n"
		"command itself honors. Change it first, then bring the copies to match."
	)

print(f"OK: the Go toolchain pin agrees everywhere (go{truth})")
PY
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	_gomod() { printf 'module github.com/x/y\n\ngo %s\n\ntoolchain go%s\n' "$2" "$3" >"$1/go.mod"; }
	_website() { printf 'module github.com/x/y/website\n\ngo %s\n' "$2" >"$1/website/go.mod"; }
	_makefile() { printf 'CHAOS_GO_VERSION ?= %s\n\nbuild:\n\t@echo hi\n' "$2" >"$1/Makefile"; }
	_dockerfile() { # <dir> <rel> <arg-default> <from-line>
		printf 'ARG GO_VERSION=%s\n%s\nRUN go build ./...\n' "$3" "$4" >"$1/$2"
	}
	_mut_migrate_arg() { # <dir>
		printf 'ARG GO_VERSION=1.25.0\nFROM golang:${GO_VERSION}-bookworm AS build\nRUN go build ./...\n' >"$1/deploy/Dockerfile.migrate"
	}
	_ci() { printf 'name: CI\nenv:\n  GO_VERSION: "%s"\njobs:\n  build:\n    steps:\n      - uses: actions/setup-go@v7\n        with:\n          go-version: ${{ env.GO_VERSION }}\n' "$2" >"$1/.github/workflows/ci.yaml"; }
	_release() { printf 'name: Release\nenv:\n  GO_VERSION: "%s"\njobs:\n  release:\n    steps:\n      - run: go build ./...\n' "$2" >"$1/.github/workflows/release.yaml"; }

	# Builds a minimal repo root whose copies all say 1.26.6.
	_mkroot() { # <dir>
		local d="$1"
		mkdir -p "$d/website" "$d/.github/workflows" "$d/runtime" "$d/deploy" "$d/scripts/chaos"
		_gomod "$d" 1.26.0 1.26.6
		_website "$d" 1.26.6
		_makefile "$d" 1.26.6
		_dockerfile "$d" runtime/Dockerfile 1.26.6 'FROM golang:${GO_VERSION}-bookworm AS agent-build'
		_dockerfile "$d" deploy/Dockerfile.server 1.26.6 'FROM golang:${GO_VERSION}-bookworm AS build'
		_dockerfile "$d" deploy/Dockerfile.migrate 1.26.6 'FROM golang:${GO_VERSION}-bookworm AS build'
		_dockerfile "$d" scripts/chaos/Dockerfile 1.26.6 'FROM python:3.12-slim'
		_ci "$d" 1.26.6
		_release "$d" 1.26.6
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
	_dependabot_bumps_one_dockerfile() { _dockerfile "$1" deploy/Dockerfile.server 1.26.6 'FROM golang:1.27-bookworm AS build'; }
	_stale_runtime_arg() { _dockerfile "$1" runtime/Dockerfile 1.26.3 'FROM golang:${GO_VERSION}-bookworm AS agent-build'; }
	_floating_arg() { _dockerfile "$1" runtime/Dockerfile 1.26 'FROM golang:${GO_VERSION}-bookworm AS agent-build'; }
	_missing_arg() { printf 'FROM golang:1.26.6-bookworm\n' >"$1/runtime/Dockerfile"; }
	_stale_chaos_make() { _makefile "$1" 1.26.4; }
	_missing_chaos_make() { printf 'build:\n\t@echo hi\n' >"$1/Makefile"; }
	_stale_ci_env() { _ci "$1" 1.26.5; }
	_stale_website_gomod() { _website "$1" 1.26.5; }
	_language_directive_drifts() { _gomod "$1" 1.25 1.26.6; }
	_no_toolchain_line() { printf 'module github.com/x/y\n\ngo 1.26.0\n' >"$1/go.mod"; }
	_missing_release_workflow() { rm -f "$1/.github/workflows/release.yaml"; }
	# The motivating mutation for the prose arm: every declaration still agrees,
	# so every case above stays green, while the comment a maintainer reads while
	# editing the pin names a toolchain the tree has not used since the move.
	_stale_prose_in_a_comment() {
		printf '  # The server image compiles inside `golang:1.27-bookworm`, which is the\n  # release toolchain.\n' >>"$1/.github/workflows/release.yaml"
	}
	_suffix_free_prose_is_fine() {
		printf '  # The server image compiles inside `golang:1.x-bookworm`.\n' >>"$1/.github/workflows/release.yaml"
	}
	# A commented-out env is not a declaration: YAML drops it, so it must not be
	# read as a stale pin. Grepping for `GO_VERSION:` would fail this case.
	_commented_out_env_is_not_a_pin() {
		printf 'name: Extra\njobs:\n  x:\n    steps:\n      # - GO_VERSION: "1.27.0"\n      - run: true\n' >"$1/.github/workflows/extra.yaml"
	}
	_a_new_workflow_pins_its_own() {
		printf 'name: Extra\njobs:\n  x:\n    steps:\n      - uses: actions/setup-go@v7\n        with:\n          go-version: "1.27.0"\n' >"$1/.github/workflows/extra.yaml"
	}
	_no_workflow_pins_anything() {
		_ci "$1" 1.26.6
		printf 'name: CI\njobs:\n  build:\n    steps:\n      - run: true\n' >"$1/.github/workflows/ci.yaml"
		printf 'name: Release\njobs:\n  release:\n    steps:\n      - run: true\n' >"$1/.github/workflows/release.yaml"
	}

	_case "an agreeing tree passes"                    0 "agrees everywhere (go1.26.6)" _noop
	_case "a Dependabot bump to one Dockerfile"        1 "golang:1.27-bookworm" _dependabot_bumps_one_dockerfile
	# The migrate Dockerfile arrived after this gate and was covered only by the
	# `golang:` literal scan, so its ARG default could move alone and the gate
	# still said OK. Confirmed on the real tree before this case existed.
	_case "the migrate Dockerfile's ARG default is covered too" 1 "deploy/Dockerfile.migrate" \
		_mut_migrate_arg

	_case "a stale runtime ARG default"                1 "ARG GO_VERSION default: 1.26.3" _stale_runtime_arg
	_case "a floating major.minor ARG"                 1 "Pin a full patch version" _floating_arg
	_case "a dropped ARG fails loudly"                 1 "declares no \`ARG GO_VERSION" _missing_arg
	_case "a stale chaos pin in the Makefile"          1 "CHAOS_GO_VERSION: 1.26.4" _stale_chaos_make
	_case "a dropped CHAOS_GO_VERSION"                 1 "did the chaos target get renamed?" _missing_chaos_make
	_case "a stale workflow env"                       1 "env.GO_VERSION: 1.26.5" _stale_ci_env
	_case "a stale website/go.mod directive"           1 "go-version-file" _stale_website_gomod
	_case "the language directive drifts"              1 "\`go\` directive minor: 1.25" _language_directive_drifts
	_case "no toolchain line fails, not skips"         1 "declares no \`toolchain go" _no_toolchain_line
	_case "a missing required file fails, not skips"   1 "does not exist" _missing_release_workflow
	_case "a stale version named only in prose"        1 "names golang:1.27" _stale_prose_in_a_comment
	_case "version-agnostic prose is accepted"         0 "agrees everywhere" _suffix_free_prose_is_fine
	_case "a commented-out env is not a pin"           0 "agrees everywhere" _commented_out_env_is_not_a_pin
	_case "a new workflow pinning its own version"     1 "with.go-version: 1.27.0" _a_new_workflow_pins_its_own
	_case "an empty scan fails, not passes"            1 "no workflow declares a Go version" _no_workflow_pins_anything

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-$PWD}"
