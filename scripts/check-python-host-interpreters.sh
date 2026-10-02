#!/usr/bin/env bash
# The CLI states two more Python facts that the published-image matrix does not
# cover, and they are NOT the same fact as it (#1031, #1036).
#
# check-python-runtime-matrix.sh reconciles the copies of one list: the Python
# lines Dexaflow publishes a task base image for. These two answer a different
# question — which interpreter runs on the DEVELOPER's host, in Lite:
#
#   internal/setup/python.go   the managed CPython we download when the host has none
#   internal/setup/detect.go   the interpreter binary names we probe for on PATH
#
# So the assertions here are RELATIONSHIPS, not equality:
#
#   1. The managed CPython's minor is a MEMBER of the published matrix. If it is
#      not, a Lite user who let us install their interpreter develops on a minor
#      we publish no base image for, and finds out at `dexaflow compile` when the
#      pull 404s — inside their build, naming an image they never typed.
#   2. Every probe candidate is either in the matrix, or carries an explicit
#      `// lite-only` marker on its line — an author assertion nothing else
#      verifies, so this gate checks it BOTH ways and fails a marker on a line
#      that IS published, rather than letting it become a mute button.
#      The probe list is deliberately WIDER:
#      it reaches forward to minors that have not shipped yet, because the Lite
#      parser shim is stdlib-only (ADR 0024) and any of them can parse a dag.py
#      on this host. A candidate that is neither published nor marked is the
#      thing #1031 actually got wrong: a version in one list, absent from the
#      other, and nothing saying whether that was a decision or a mistake.
#   3. The first (highest-priority) probe candidate is the managed CPython's
#      minor. detect.go's own comment states this — "3.11 wins when present
#      because it's the version the managed CPython matches, keeping dev/prod
#      parity" — and it is the arm most likely to rot, because bumping the
#      managed interpreter is a one-line change in a different file.
#
# The marker is per-line rather than positional on purpose. A gate that assumed
# "the published ones come first" would silently exempt everything the day
# somebody reordered the slice for readability.
#
# Usage: scripts/check-python-host-interpreters.sh [--self-test]
#        scripts/check-python-host-interpreters.sh <repo-root>
set -euo pipefail

SCHEMA_REL="internal/domain/schemas/leoflow-yaml-schema.json"
MANAGED_REL="internal/setup/python.go"
DETECT_REL="internal/setup/detect.go"

check() { # <repo-root>
	python3 - "$1" "$SCHEMA_REL" "$MANAGED_REL" "$DETECT_REL" <<'PY'
import json
import os
import re
import sys

root, schema_rel, managed_rel, detect_rel = sys.argv[1:5]
problems = []


def fail(msg):
	sys.exit("FAIL: " + msg)


def read(rel):
	path = os.path.join(root, rel)
	if not os.path.exists(path):
		fail(f"{rel} does not exist — this gate's scan target moved and it would otherwise report OK")
	with open(path) as fh:
		text = fh.read()
	if not text.strip():
		fail(f"{rel} is empty")
	return text


# ── the published matrix (the same source of truth the sibling gate uses) ────
schema = json.loads(read(schema_rel))
published = ((schema.get("properties") or {}).get("python_version") or {}).get("enum")
if not published:
	fail(f"{schema_rel} declares no python_version enum — the published matrix is gone")
published = [str(v) for v in published]

# ── 1. the managed CPython ───────────────────────────────────────────────────
managed_text = read(managed_rel)
m = re.search(r'^\s*pyVersion\s*=\s*"(\d+\.\d+)\.(\d+)"', managed_text, re.M)
if not m:
	fail(
		f'{managed_rel} declares no `pyVersion = "<X.Y.Z>"` constant.\n'
		"That constant is the interpreter Lite downloads when the host has none; a gate that\n"
		"cannot find it must not report OK."
	)
managed_minor = m.group(1)
if managed_minor not in published:
	problems.append(
		f"  {managed_rel}\n"
		f"    managed CPython: {m.group(1)}.{m.group(2)} (minor {managed_minor})\n"
		f"    published matrix: {', '.join(published)}\n"
		"    A Lite user who let us install their interpreter would develop on a minor we\n"
		"    publish no task base image for. They find out at `dexaflow compile`, when the pull\n"
		"    404s inside their own build, naming an image they never typed."
	)

# ── 2 and 3. the probe list ──────────────────────────────────────────────────
detect_text = read(detect_rel)
m = re.search(r"^var pythonCandidates = \[\]string\{\n(.*?)^\}", detect_text, re.M | re.S)
if not m:
	fail(
		f"{detect_rel} declares no `var pythonCandidates = []string{{...}}` literal.\n"
		"Either it was renamed or its shape changed; this gate cannot reconcile a list it\n"
		"cannot find, and reporting OK on that is worse than not running at all."
	)

CANDIDATE_RE = re.compile(r'"python(\d+\.\d+)"')
ordered = []
for lineno, line in enumerate(m.group(1).splitlines(), 1):
	entries = CANDIDATE_RE.findall(line)
	if not entries:
		continue
	# The marker is per LINE: it covers every candidate written on it, which is
	# how the forward-looking range is grouped in the source.
	lite_only = re.search(r"//\s*lite-only\b", line) is not None
	for v in entries:
		ordered.append(v)
		if lite_only and v in published:
			# The marker is an author assertion nobody verifies, so it has to be
			# checked in BOTH directions or it decays into a mute button. The
			# moment we start publishing a line that carries the marker, the
			# marker is a false statement sitting in the source — and left
			# one-way this gate would go on printing OK over it forever, which
			# is the precise shape of the drift #1036 is about.
			problems.append(
				f"  {detect_rel} (pythonCandidates, line {lineno} of the literal)\n"
				f"    probes for python{v}, marked `// lite-only`\n"
				f"    published matrix: {', '.join(published)}\n"
				f"    but {v} IS published now, so the marker is stale. Drop `// lite-only`\n"
				"    from that line. The marker means \"Lite deliberately accepts a host\n"
				"    interpreter we ship no image for\"; it is not a way to exempt an entry\n"
				"    from this gate, and nothing else verifies it."
			)
			continue
		if lite_only or v in published:
			continue
		problems.append(
			f"  {detect_rel} (pythonCandidates, line {lineno} of the literal)\n"
			f"    probes for python{v}\n"
			f"    published matrix: {', '.join(published)}\n"
			f"    {v} is neither published nor marked. Add it to the schema enum if we publish a\n"
			"    base image for it, or append `// lite-only` to its line to record that Lite\n"
			"    deliberately accepts a host interpreter we ship no image for. An unmarked\n"
			"    difference between these two lists is what #1031 misread as drift."
		)

if not ordered:
	fail(
		f"{detect_rel}'s pythonCandidates literal contains no `\"python<X.Y>\"` entries.\n"
		"An empty probe list means Lite finds no host interpreter at all, and an empty scan\n"
		"must never report OK."
	)

if ordered[0] != managed_minor:
	problems.append(
		f"  {detect_rel}\n"
		f"    first probe candidate: python{ordered[0]}\n"
		f"    {managed_rel} managed CPython minor: {managed_minor}\n"
		"    The list is in priority order and its own comment says the managed minor wins when\n"
		"    present, so a host that has both gets the same interpreter as a host that has\n"
		"    neither. Reorder the list or bump the managed pin — but not one without the other."
	)

if problems:
	sys.exit(
		"FAIL: the host-interpreter facts disagree with the published matrix.\n"
		+ "\n".join(problems)
		+ f"\n\nThese are relationships, not equality: the probe list is deliberately wider than\n"
		f"the matrix in {schema_rel}. What must hold is membership and the marker."
	)

lite = [v for v in ordered if v not in published]
print(
	f"OK: managed CPython {managed_minor} is published; {len(ordered)} probe candidate(s), "
	f"{len(lite)} marked lite-only ({', '.join(lite) or 'none'})"
)
PY
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	_schema() { printf '{"properties":{"python_version":{"enum":["3.10","3.11","3.12"],"default":"3.11"}}}\n' >"$1/internal/domain/schemas/leoflow-yaml-schema.json"; }
	_managed() { printf 'package setup\n\nconst (\n\tpyReleaseTag = "20260510"\n\tpyVersion    = "%s"\n)\n' "$2" >"$1/internal/setup/python.go"; }
	_detect() { # <dir> <literal-body>
		{
			printf 'package setup\n\n// pythonCandidates lists the interpreter binary names Detect probes for.\n'
			printf 'var pythonCandidates = []string{\n'
			# shellcheck disable=SC2059 # $2 IS the format: callers pass literal templates
			printf "$2"
			printf '}\n'
		} >"$1/internal/setup/detect.go"
	}

	# The fixture's published matrix is 3.10/3.11/3.12 and the probe list reaches
	# to 3.13/3.14, so the happy path exercises the marker arm positively rather
	# than vacuously: a fixture with nothing to exempt would pass that arm whether
	# it worked or not.
	_body_default='\t"python3.11", "python3.12",\n\t"python3.13", "python3.14", // lite-only\n'

	_mkroot() { # <dir>
		local d="$1"
		mkdir -p "$d/internal/domain/schemas" "$d/internal/setup"
		_schema "$d"
		_managed "$d" 3.11.15
		_detect "$d" "$_body_default"
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
	# The motivating mutation: bump the managed interpreter to a minor nobody
	# publishes. Every list still looks reasonable in isolation.
	_managed_off_the_matrix() {
		_managed "$1" 3.13.2
		_detect "$1" '\t"python3.13", "python3.11", "python3.12",\n\t"python3.14", // lite-only\n'
	}
	_managed_minor_not_first() { _managed "$1" 3.12.9; }
	_missing_managed_const() { printf 'package setup\n\nconst pyReleaseTag = "20260510"\n' >"$1/internal/setup/python.go"; }
	_unmarked_future_minor() { _detect "$1" '\t"python3.11", "python3.12",\n\t"python3.13", "python3.14",\n'; }
	_marker_covers_only_its_line() { _detect "$1" '\t"python3.11", "python3.12",\n\t"python3.13", // lite-only\n\t"python3.14",\n'; }
	_reordered_list_keeps_its_markers() { _detect "$1" '\t"python3.11",\n\t"python3.14", // lite-only\n\t"python3.12",\n\t"python3.13", // lite-only\n'; }
	_renamed_literal() { printf 'package setup\n\nvar probes = []string{"python3.11"}\n' >"$1/internal/setup/detect.go"; }
	_empty_literal() { _detect "$1" '\t// nothing yet\n'; }
	_missing_schema() { rm -f "$1/internal/domain/schemas/leoflow-yaml-schema.json"; }
	_empty_enum() { printf '{"properties":{"python_version":{"default":"3.11"}}}\n' >"$1/internal/domain/schemas/leoflow-yaml-schema.json"; }
	# A line comment that merely mentions Lite is not the marker. The gate looks
	# for the token, so prose cannot exempt an entry by accident.
	_prose_is_not_a_marker() { _detect "$1" '\t"python3.11", "python3.12",\n\t"python3.13", "python3.14", // only Lite uses these\n'; }

	# The marker checked the other way: once a marked line IS published, the
	# marker is a false statement and the gate must say so instead of exempting
	# it. Left one-way, a marker silences the entry forever.
	_stale_marker() { _detect "$1" '\t"python3.11", // lite-only\n\t"python3.12", "python3.13",\n'; }
	_case "a marker on a published line is stale, not an exemption" 1 "marker is stale" _stale_marker

	_case "an agreeing tree passes"                  0 "2 marked lite-only (3.13, 3.14)" _noop
	_case "a managed minor nobody publishes"         1 "publish no task base image for" _managed_off_the_matrix
	_case "the managed minor is not the first probe" 1 "first probe candidate: python3.11" _managed_minor_not_first
	_case "a dropped pyVersion fails, not skips"     1 "declares no \`pyVersion" _missing_managed_const
	_case "an unmarked future minor"                 1 "neither published nor marked" _unmarked_future_minor
	_case "the marker covers only its own line"      1 "probes for python3.14" _marker_covers_only_its_line
	_case "a reordered list keeps its markers"       0 "2 marked lite-only" _reordered_list_keeps_its_markers
	_case "a renamed literal fails loudly"           1 "declares no \`var pythonCandidates" _renamed_literal
	_case "an empty literal fails, not passes"       1 "contains no" _empty_literal
	_case "a missing schema fails, not skips"        1 "does not exist" _missing_schema
	_case "an empty enum fails"                      1 "declares no python_version enum" _empty_enum
	_case "prose is not the marker"                  1 "neither published nor marked" _prose_is_not_a_marker

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-$PWD}"
