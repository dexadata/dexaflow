#!/usr/bin/env bash
# Every third-party action and tool CI runs is pinned, and every checkout says
# whether it keeps the job token in .git/config (#1553).
#
# What a moving reference costs: whoever controls the tag or the registry
# decides what runs next with this repository's token. `uses: x/y@v7` runs
# whatever the v7 tag points at today, `go install ...@latest` and
# `npm install -g ...@latest` run whatever was published this morning, and a
# SHA whose comment names a branch (`# master`) is bumped to the branch tip by
# Dependabot, so it tracks the branch with one review step in between. OpenSSF
# Scorecard's Pinned-Dependencies check scores the same things.
#
# The gate, over .github/workflows/*.y*ml, .github/actions/*/action.y*ml and
# the Makefile:
#
#   1. a remote `uses:` is `owner/repo[/path]@<40-hex SHA> # vX[.Y.Z]`: a full
#      commit SHA, with a version comment Dependabot can bump (not a branch);
#   2. no `@latest` outside a comment, in a workflow or in the Makefile;
#   3. no Makefile `*_VERSION ?= latest`;
#   4. a workflow `go install <pkg>@<ver>` uses the version the Makefile pins
#      for the same package (one fact, one value), or an exact vX.Y.Z (or a Go
#      pseudo-version, for modules with no tags such as golang.org/x/exp) when
#      the Makefile does not install it;
#   5. a workflow `npm install -g <pkg>@<ver>` names an exact X.Y.Z;
#   6. every `actions/checkout` step sets `persist-credentials` explicitly.
#      `false` unless the job pushes with git: a token left in .git/config is
#      readable by every later step, including third-party actions.
#
# Usage: scripts/check-workflow-pins.sh [--self-test]
#        scripts/check-workflow-pins.sh <repo-root>
set -euo pipefail

check() { # <repo-root>
	python3 - "$1" <<'PY'
import glob
import os
import re
import sys

try:
	import yaml
except ImportError:
	sys.exit("FAIL: PyYAML unavailable; cannot parse the workflows (pip install pyyaml)")

root = sys.argv[1]
problems = []

files = sorted(
	glob.glob(os.path.join(root, ".github/workflows/*.yml"))
	+ glob.glob(os.path.join(root, ".github/workflows/*.yaml"))
	+ glob.glob(os.path.join(root, ".github/actions/*/action.yml"))
	+ glob.glob(os.path.join(root, ".github/actions/*/action.yaml"))
)
if not files:
	sys.exit("FAIL: no workflow found under .github/; this gate's scan target moved and it would otherwise report OK")
makefile = os.path.join(root, "Makefile")
if not os.path.exists(makefile):
	sys.exit("FAIL: Makefile does not exist; this gate's scan target moved and it would otherwise report OK")


def rel(path):
	return os.path.relpath(path, root)


def code(line):
	"""The part of a line before a YAML/Make comment (good enough for these files)."""
	stripped = line.lstrip()
	if stripped.startswith("#"):
		return ""
	return re.split(r"\s#", line, maxsplit=1)[0]


USES = re.compile(r"^\s*(?:-\s+)?uses:\s*['\"]?([^\s'\"#]+)['\"]?\s*(#.*)?$")
PINNED = re.compile(r"^[^@\s]+@[0-9a-f]{40}$")
VERSION_COMMENT = re.compile(r"^#\s*v?\d+(\.\d+)*\S*\s*$")
GO_INSTALL = re.compile(r"go install\s+(?:-\S+\s+(?:'[^']*'\s+|\"[^\"]*\"\s+|\S+\s+)?)*[\"']?([^\s@\"']+)@(\$\([^)]*\)|[^\s\"';)]+)")
# A version read from go.mod at run time is a pin: go.mod is reviewed, and
# Dependabot bumps it.
FROM_GO_MOD = re.compile(r"^\$\(go list -m ")
NPM_GLOBAL = re.compile(r"npm (?:install|i) +(?:-g|--global) +((?:@[^\s/]+/)?[^\s@]+)(?:@(\S+))?")
# An exact vX.Y.Z, or a Go pseudo-version (vX.Y.Z-[pre.]yyyymmddhhmmss-<12 hex>):
# it names one commit, so it is as fixed as a tag.
EXACT_GO = re.compile(r"^v\d+\.\d+\.\d+(?:-(?:[0-9A-Za-z-]+\.)*\d{14}-[0-9a-f]{12})?$")
EXACT_NPM = re.compile(r"^\d+\.\d+\.\d+$")

# The Makefile's pins: VAR ?= value, and which package each `go install` uses it for.
make_text = open(makefile).read()
make_vars = dict(re.findall(r"^([A-Z0-9_]+)\s*\?=\s*(\S+)", make_text, re.M))
make_pkgs = {}
for lineno, line in enumerate(make_text.splitlines(), 1):
	c = code(line)
	if "@latest" in c:
		problems.append(f"  Makefile:{lineno}\n    installs @latest: {line.strip()}")
	m = GO_INSTALL.search(c)
	if m:
		ver = m.group(2)
		var = re.fullmatch(r"\$\(([A-Z0-9_]+)\)", ver)
		make_pkgs[m.group(1)] = make_vars.get(var.group(1), "?") if var else ver
for var, value in make_vars.items():
	if var.endswith("_VERSION") and value == "latest":
		problems.append(f"  Makefile\n    {var} ?= latest: pin an exact version")

for path in files:
	text = open(path).read()
	for lineno, line in enumerate(text.splitlines(), 1):
		where = f"{rel(path)}:{lineno}"
		m = USES.match(line)
		if m:
			ref = m.group(1)
			if not (ref.startswith("./") or ref.startswith("docker://")):
				if not PINNED.match(ref):
					problems.append(f"  {where}\n    not pinned to a commit SHA: {ref}")
				elif not (m.group(2) and VERSION_COMMENT.match(m.group(2).strip())):
					problems.append(f"  {where}\n    SHA without a version comment (`# vX.Y.Z`), Dependabot tracks a branch: {line.strip()}")
		c = code(line)
		if "@latest" in c:
			problems.append(f"  {where}\n    installs @latest: {line.strip()}")
			continue
		g = GO_INSTALL.search(c)
		if g:
			pkg, ver = g.group(1), g.group(2)
			want = make_pkgs.get(pkg)
			if want is not None and ver != want:
				problems.append(f"  {where}\n    go install {pkg}@{ver}, the Makefile pins {want}")
			elif want is None and not (EXACT_GO.match(ver) or FROM_GO_MOD.match(ver)):
				problems.append(f"  {where}\n    go install {pkg}@{ver} is not an exact vX.Y.Z or a Go pseudo-version")
		n = NPM_GLOBAL.search(c)
		if n and not (n.group(2) and EXACT_NPM.match(n.group(2))):
			problems.append(f"  {where}\n    npm global install without an exact X.Y.Z: {line.strip()}")

	# persist-credentials, per checkout step.
	try:
		doc = yaml.safe_load(text) or {}
	except yaml.YAMLError as e:
		sys.exit(f"FAIL: {rel(path)} does not parse: {e}")
	steps = []
	for job_id, job in (doc.get("jobs") or {}).items():
		for i, step in enumerate((job or {}).get("steps") or []):
			steps.append((f"jobs.{job_id}.steps[{i}]", step))
	for i, step in enumerate(((doc.get("runs") or {}).get("steps")) or []):
		steps.append((f"runs.steps[{i}]", step))
	for name, step in steps:
		if not isinstance(step, dict):
			continue
		if str(step.get("uses", "")).startswith("actions/checkout@"):
			if "persist-credentials" not in (step.get("with") or {}):
				problems.append(f"  {rel(path)} {name}\n    actions/checkout without `persist-credentials` (false unless the job pushes with git)")

if problems:
	print("FAIL: unpinned or credential-persisting CI dependencies (#1553):")
	print("\n".join(problems))
	sys.exit(1)
print(f"OK: every action is SHA-pinned with a version, no @latest, every checkout sets persist-credentials ({len(files)} files)")
PY
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	local sha=3d3c42e5aac5ba805825da76410c181273ba90b1
	_makefile() { printf 'GOVULNCHECK_VERSION ?= %s\n\nsetup:\n\tgo install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)\n' "$2" >"$1/Makefile"; }
	_wf() { # <dir> <steps...>, written as one job
		{
			printf 'name: CI\non: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n'
			shift
			printf '%s\n' "$@"
		} >"$tmp/case/.github/workflows/ci.yaml"
	}
	_good_steps() {
		_wf "$1" \
			"      - uses: actions/checkout@$sha # v7.0.1" \
			"        with:" \
			"          persist-credentials: false" \
			"      - uses: ./.github/actions/local" \
			"      - run: go install golang.org/x/vuln/cmd/govulncheck@v1.8.0" \
			"      - run: npm install -g @redocly/cli@2.54.3"
	}
	_mkroot() { mkdir -p "$1/.github/workflows"; _makefile "$1" v1.8.0; _good_steps "$1"; }

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
	_tag_ref() { _wf "$1" "      - uses: actions/setup-go@v7" "      - uses: lycheeverse/lychee-action@v2.9.0 # v2.9.0"; }
	_short_sha() { _wf "$1" "      - uses: actions/setup-go@b7ad1da # v7.0.0"; }
	_branch_comment() { _wf "$1" "      - uses: aquasecurity/trivy-action@c03d123cb480c08c69b054af418ea5e69fe6e57e # master"; }
	_no_comment() { _wf "$1" "      - uses: aquasecurity/trivy-action@c03d123cb480c08c69b054af418ea5e69fe6e57e"; }
	_go_latest() { _wf "$1" "      - run: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest"; }
	_npm_latest() { _wf "$1" "      - run: npm install -g @redocly/cli@latest"; }
	_npm_unversioned() { _wf "$1" "      - run: npm install -g @redocly/cli"; }
	_npm_range() { _wf "$1" "      - run: npm install -g @redocly/cli@^2"; }
	_drift_from_make() { _wf "$1" "      - run: go install golang.org/x/vuln/cmd/govulncheck@v1.7.0"; }
	_unknown_pkg_branch() { _wf "$1" "      - run: go install example.com/tool@main"; }
	_from_go_mod() { _wf "$1" "      - run: go install \"google.golang.org/protobuf/cmd/protoc-gen-go@\$(go list -m -f '{{.Version}}' google.golang.org/protobuf)\""; }
	_make_echo_hint() { _makefile "$1" v1.8.0; printf 'vuln:\n\t@command -v govulncheck || (echo "run: go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION))"; exit 0)\n' >>"$1/Makefile"; }
	_unknown_pkg_exact() { _wf "$1" "      - run: go install -tags 'postgres' example.com/tool@v1.2.3"; }
	_pseudo_version() { _wf "$1" "      - run: go install golang.org/x/exp/cmd/gorelease@v0.0.0-20250106191152-7588d65b2ba8"; }
	_bad_pseudo_version() { _wf "$1" "      - run: go install golang.org/x/exp/cmd/gorelease@v0.0.0-2025-7588d65b"; }
	_make_latest_var() { _makefile "$1" latest; }
	_make_latest_literal() { printf 'tools:\n\tgo install example.com/tool@latest\n' >"$1/Makefile"; }
	_commented_latest_is_fine() { _good_steps "$1"; printf '      # was: go install x@latest\n' >>"$1/.github/workflows/ci.yaml"; }
	_checkout_no_with() { _wf "$1" "      - uses: actions/checkout@$sha # v7.0.1"; }
	_checkout_with_no_key() { _wf "$1" "      - uses: actions/checkout@$sha # v7.0.1" "        with:" "          fetch-depth: 0"; }
	_composite_action() {
		mkdir -p "$1/.github/actions/x"
		printf 'name: x\nruns:\n  using: composite\n  steps:\n    - uses: docker/login-action@v4\n' >"$1/.github/actions/x/action.yaml"
	}
	_no_workflows() { rm -rf "$1/.github"; }

	_case "a pinned tree passes"                      0 "every action is SHA-pinned" _noop
	_case "a tag ref is refused"                      1 "not pinned to a commit SHA: actions/setup-go@v7" _tag_ref
	_case "a short SHA is refused"                    1 "not pinned to a commit SHA: actions/setup-go@b7ad1da" _short_sha
	_case "a SHA tracking a branch is refused"        1 "Dependabot tracks a branch" _branch_comment
	_case "a SHA with no version comment is refused"  1 "without a version comment" _no_comment
	_case "go install @latest is refused"             1 "installs @latest" _go_latest
	_case "npm -g @latest is refused"                 1 "installs @latest" _npm_latest
	_case "npm -g with no version is refused"         1 "without an exact X.Y.Z" _npm_unversioned
	_case "npm -g with a range is refused"            1 "without an exact X.Y.Z" _npm_range
	_case "CI drifting from the Makefile pin"         1 "govulncheck@v1.7.0, the Makefile pins v1.8.0" _drift_from_make
	_case "a go install on a branch is refused"       1 "example.com/tool@main is not an exact" _unknown_pkg_branch
	_case "an exact go install not in Make passes"    0 "every action is SHA-pinned" _unknown_pkg_exact
	_case "a Go pseudo-version passes"                0 "every action is SHA-pinned" _pseudo_version
	_case "a malformed pseudo-version is refused"     1 "gorelease@v0.0.0-2025-7588d65b is not an exact" _bad_pseudo_version
	_case "a version read from go.mod passes"         0 "every action is SHA-pinned" _from_go_mod
	_case "a Make hint naming the pin parses"         0 "every action is SHA-pinned" _make_echo_hint
	_case "a Makefile VERSION ?= latest is refused"   1 "GOVULNCHECK_VERSION ?= latest" _make_latest_var
	_case "a literal @latest in Make is refused"      1 "Makefile:2" _make_latest_literal
	_case "a commented @latest is fine"               0 "every action is SHA-pinned" _commented_latest_is_fine
	_case "a checkout with no with: is refused"       1 "without \`persist-credentials\`" _checkout_no_with
	_case "a checkout without the key is refused"     1 "without \`persist-credentials\`" _checkout_with_no_key
	_case "composite actions are scanned too"         1 "action.yaml:5" _composite_action
	_case "no workflows fails, not passes"            1 "scan target moved" _no_workflows

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-$PWD}"
