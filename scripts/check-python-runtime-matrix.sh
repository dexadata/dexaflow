#!/usr/bin/env bash
# The set of Python lines Dexaflow publishes a task base image for is stated in
# five files, and only one of them is enforced by anything.
#
# The enforced one is the `python_version` enum in
# internal/domain/schemas/leoflow-yaml-schema.json: it is embedded into the CLI
# and `LeoflowConfig.Validate()` rejects anything else. (docs/api/ carries the
# byte-equal canonical mirror of the same file; TestEmbeddedSchemasMatchDocs
# already binds the two, so this gate reads the copy that actually ships in the
# binary.) The other four are the
# release matrix that actually pushes the images, the Makefile target that
# builds them locally, the comment in runtime/Dockerfile, and the configuration
# reference the user reads. Nothing reconciled them, and by #1031 they had
# already drifted in both directions: the release published py3.10 while the
# host detector probed for a 3.13 nobody published.
#
# Both directions of drift fail in the same expensive place. A version in the
# enum with no published leg is a `docker pull ... 404` inside the USER's build,
# minutes after their compile started, naming an image they never typed. A
# published leg missing from the enum is a `dexaflow compile` that refuses a
# perfectly good image with "value must be one of" — and the user cannot fix
# either one, because both live in our repo.
#
# This is the same "two copies of a fact with nothing between them" shape as
# check-lite-prepull-matches-compose.sh, and it takes the same two lessons from
# it. The workflow is parsed as YAML, not grepped, so a commented-out matrix
# cannot keep the gate green. And a missing file, a renamed job or an
# unrecognized declaration is a FAIL, never a silent pass — a gate that stops
# gating after a file move is worse than no gate, because it still reports OK.
#
# The floor in runtime/python/pyproject.toml is checked too: the runtime helper
# is pip-installed INTO the base image, so a `requires-python` above the oldest
# published line makes that leg unbuildable, and one below it advertises support
# for an interpreter we ship no image for.
#
# The gate reconciles the deprecation STATUS AND DATES as well as the list, and
# that second half is not optional. Reconciling only the set of versions leaves
# the half a user acts on unguarded: move a deprecation from 3.10 to 3.11 in the
# schema and every version still appears in every file, so the list arms stay
# green while the support table in the configuration reference — the only place
# a user learns the date — says the exact opposite of what the CLI enforces and
# what the weekly watchdog asserts. The same date is hardcoded in release.yaml's
# comment above the matrix, which is the copy a maintainer reads while editing
# the matrix that publishes the leg, so it is reconciled too. A wrong date is
# worse than a missing one: it answers the question the reader came with.
#
# The same argument runs one step further, and that is the last arm. The
# deprecation `reason` is printed VERBATIM to the user on compile, validate and
# deploy, and it named a full base-image tag including the Debian suite. Move
# the task base from one Debian release to the next and every arm above stays
# green — the versions and the dates did not change — while that sentence now
# describes an image nobody builds. A reader checks the Dockerfile the compiler
# generated, sees a different suite, and dismisses the warning as stale tooling,
# which retires the one mechanism there is for reaching them before the leg
# stops being published. So the suite named in the schema and in the
# configuration reference is reconciled against runtime/Dockerfile's stage-2
# FROM — and the fix that failure recommends is to stop naming a suite at all,
# because `python:3.10-slim` is correct on every one of them.
#
# Usage: scripts/check-python-runtime-matrix.sh [--self-test]
#        scripts/check-python-runtime-matrix.sh <repo-root>
set -euo pipefail

SCHEMA_REL="internal/domain/schemas/leoflow-yaml-schema.json"
WORKFLOW_REL=".github/workflows/release.yaml"
MAKEFILE_REL="Makefile"
DOCKERFILE_REL="runtime/Dockerfile"
PYPROJECT_REL="runtime/python/pyproject.toml"
DOCS_REL="website/content/reference/configuration.md"
JOB="runtime-image"

check() { # <repo-root>
	python3 - "$1" "$SCHEMA_REL" "$WORKFLOW_REL" "$MAKEFILE_REL" "$DOCKERFILE_REL" "$PYPROJECT_REL" "$DOCS_REL" "$JOB" <<'PY'
import json
import os
import re
import sys

try:
	import yaml
except ImportError:
	sys.exit("FAIL: PyYAML unavailable; cannot parse the release workflow (pip install pyyaml)")

root, schema_rel, workflow_rel, makefile_rel, dockerfile_rel, pyproject_rel, docs_rel, job_name = sys.argv[1:9]

problems = []
VERSION_RE = re.compile(r"3\.\d+")


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


def note(rel, what, got, want):
	problems.append(f"  {rel}\n    {what}: {got}\n    schema enum:  {want}")


# ── the source of truth ──────────────────────────────────────────────────────
schema = json.loads(read(schema_rel))
py = ((schema.get("properties") or {}).get("python_version") or {})
want = py.get("enum")
if not want:
	fail(f"{schema_rel} declares no python_version enum — the source of truth is gone")
if sorted(set(want)) != sorted(want) or len(want) != len(set(want)):
	fail(f"{schema_rel}'s python_version enum repeats a value: {want}")
for v in want:
	if not VERSION_RE.fullmatch(v):
		fail(f"{schema_rel}'s python_version enum contains {v!r}, which is not a 3.<minor> line")

default = py.get("default")
if default not in want:
	fail(f"{schema_rel}'s python_version default {default!r} is not in its own enum {want}")

deprecations = py.get("x-leoflow-python-deprecations") or {}
for v in deprecations:
	if v not in want:
		fail(
			f"{schema_rel} deprecates python_version {v!r}, which is not in the enum {want}.\n"
			"A deprecation note for a version nobody can select is a note nobody reads;\n"
			"drop the note in the same change that drops the leg."
		)
if default in deprecations:
	fail(f"{schema_rel} defaults python_version to {default!r}, which it also marks deprecated")

wantset = set(want)
sorted_want = sorted(want, key=lambda v: [int(p) for p in v.split(".")])
if want != sorted_want:
	fail(f"{schema_rel}'s python_version enum is not in ascending order: {want}")

# ── 1. the release matrix that pushes the images ─────────────────────────────
workflow = yaml.safe_load(read(workflow_rel)) or {}
jobs = workflow.get("jobs") or {}
if job_name not in jobs:
	fail(f"{workflow_rel} has no `{job_name}` job — did it get renamed?")
matrix = (((jobs[job_name].get("strategy") or {}).get("matrix")) or {}).get("python")
if not matrix:
	fail(
		f"{workflow_rel}'s `{job_name}` job declares no `strategy.matrix.python`.\n"
		"Without it no base image is published at all, and every `dexaflow compile`\n"
		"that does not override base_image fails on the pull."
	)
# YAML reads an unquoted 3.10 as the float 3.1, which is the exact typo this
# gate exists to catch; compare as written, not as parsed.
got = [str(v) for v in matrix]
if set(got) != wantset:
	note(workflow_rel, f"`{job_name}` matrix.python", got, want)

# ── 2. the Makefile target that builds them locally ──────────────────────────
make_text = read(makefile_rel)
recipe = None
in_target = False
for line in make_text.splitlines():
	if re.match(r"^runtime-images\s*:", line):
		in_target = True
		continue
	if in_target:
		# A recipe line is TAB-indented; the first line that is not ends the recipe.
		if not line.startswith("\t"):
			if line.strip() == "" or line.startswith("#"):
				continue
			break
		if line.lstrip().startswith("#"):
			continue
		if "for v in" in line:
			recipe = line
			break
if recipe is None:
	fail(
		f"{makefile_rel} has no `runtime-images` recipe with a `for v in ...` loop — did it get renamed?\n"
		"`make runtime-images` is how the base images are built outside CI; a gate that\n"
		"cannot find it must not report OK."
	)
loop = recipe.split("for v in", 1)[1].split(";", 1)[0]
got = VERSION_RE.findall(loop)
if set(got) != wantset:
	note(makefile_rel, "`runtime-images` loop", got, want)

# ── 3. the runtime Dockerfile's own statement of the list ────────────────────
dockerfile_text = read(dockerfile_rel)
marker = "PYTHON_VERSION selects the interpreter"
lines = [ln for ln in dockerfile_text.splitlines() if marker in ln]
if not lines:
	fail(
		f"{dockerfile_rel} no longer carries the `{marker} (...)` comment.\n"
		"It is the list a reader of the Dockerfile sees; either restore it or delete\n"
		"this arm of the gate deliberately."
	)
got = VERSION_RE.findall(lines[0])
if set(got) != wantset:
	note(dockerfile_rel, f"`{marker}` comment", got, want)

# ── 4. the runtime helper's floor ────────────────────────────────────────────
pyproject_text = read(pyproject_rel)
m = re.search(r"^\s*requires-python\s*=\s*[\"']>=\s*(3\.\d+)", pyproject_text, re.M)
if not m:
	fail(f"{pyproject_rel} declares no `requires-python = \">=3.<minor>\"` — did the key change shape?")
floor = m.group(1)
oldest = sorted_want[0]
if floor != oldest:
	problems.append(
		f"  {pyproject_rel}\n"
		f"    requires-python floor: >={floor}\n"
		f"    oldest published leg:  {oldest}\n"
		"    The runtime helper is pip-installed into the base image: a floor above the\n"
		"    oldest published line makes that leg unbuildable; one below it advertises an\n"
		"    interpreter no base image exists for."
	)

# ── 5. the configuration reference the user reads ────────────────────────────
docs_text = read(docs_rel)
rows = [ln for ln in docs_text.splitlines() if ln.startswith("|") and "`python_version`" in ln]
if not rows:
	fail(f"{docs_rel} has no table row naming `python_version` — did the reference move?")
for row in rows:
	got = VERSION_RE.findall(row)
	if set(got) != wantset:
		note(docs_rel, f"row {row.strip()[:60]!r}", got, want)

status_rows = [ln for ln in docs_text.splitlines() if re.match(r"^\|\s*`3\.\d+`\s*\|", ln)]
if not status_rows:
	fail(
		f"{docs_rel} has no Python-support status table (rows of the shape `| \\`3.11\\` | ... |`).\n"
		"That table is where a user learns which lines are deprecated and until when."
	)
got = [VERSION_RE.search(ln).group(0) for ln in status_rows]
if set(got) != wantset:
	note(docs_rel, "Python version support table", got, want)

# ── 6. the deprecation STATUS and DATES, not just the list ───────────────────
# Reconciling only the set of versions left the half that a user actually acts
# on unguarded: move the deprecation from 3.10 to 3.11 in the schema and this
# table keeps saying `3.11 | Supported (default)` / `3.10 | Deprecated` with a
# green gate. The table is the only place a user learns the date, so a stale one
# is worse than a missing one — it answers the question, wrongly.


def cells(row):
	"""Split a markdown table row into its cells, markdown emphasis stripped."""
	parts = [c.strip() for c in row.strip().strip("|").split("|")]
	return [re.sub(r"[`*_]", "", c).strip() for c in parts]


header = None
for ln in docs_text.splitlines():
	if not ln.startswith("|"):
		continue
	c = cells(ln)
	if len(c) >= 4 and c[0] == "Line" and c[1] == "Status":
		header = c
		break
if header is None:
	fail(
		f"{docs_rel} has no Python-support table header of the shape\n"
		"`| Line | Status | Upstream EOL | Published until |` — the status/date arm of this\n"
		"gate cannot locate its columns and must not report OK."
	)
# Index by name rather than position: a reordered column would otherwise be
# compared against the wrong schema field and pass for the wrong reason.
for required in ("Upstream EOL", "Published until"):
	if required not in header:
		fail(f"{docs_rel}'s Python-support table has no {required!r} column (header: {header})")
eol_col = header.index("Upstream EOL")
until_col = header.index("Published until")

for row in status_rows:
	c = cells(row)
	version = VERSION_RE.search(c[0]).group(0)
	if len(c) <= max(eol_col, until_col):
		problems.append(f"  {docs_rel}\n    status row for {version} has {len(c)} cells, header declares {len(header)}")
		continue
	dep = deprecations.get(version)
	says_deprecated = "deprecated" in c[1].lower()
	if dep is None:
		if says_deprecated:
			problems.append(
				f"  {docs_rel}\n"
				f"    status row for {version}: {c[1]!r}\n"
				f"    schema:  {version} carries no x-leoflow-python-deprecations entry\n"
				"    A line the docs call deprecated and the schema does not is a line the CLI\n"
				"    will never warn about — the user is told to move off something nothing enforces."
			)
		if re.search(r"\d{4}-\d{2}-\d{2}", c[until_col]):
			problems.append(
				f"  {docs_rel}\n"
				f"    status row for {version} declares a removal date ({c[until_col]}) but the schema\n"
				"    marks the line supported. Record it in x-leoflow-python-deprecations or drop it."
			)
		continue
	if not says_deprecated:
		problems.append(
			f"  {docs_rel}\n"
			f"    status row for {version}: {c[1]!r}\n"
			f"    schema:  deprecated (eol {dep.get('eol')}, remove_after {dep.get('remove_after')})\n"
			"    This table is where a user learns a line is going away."
		)
	for label, col, key in (("Upstream EOL", eol_col, "eol"), ("Published until", until_col, "remove_after")):
		expected = dep.get(key)
		if not expected:
			fail(f"{schema_rel}'s deprecation of {version} declares no {key!r}")
		if c[col] != expected:
			problems.append(
				f"  {docs_rel}\n"
				f"    status row for {version}, {label}: {c[col]!r}\n"
				f"    schema {key}: {expected}\n"
				"    The date in the docs is the one the user plans around; the one in the schema is\n"
				"    the one the CLI prints and the watchdog asserts. They cannot be two dates."
			)

# The prose paragraph beneath the table repeats the argument and the dates. It
# is what a user reads after the table tells them there is something to read.
for version, dep in deprecations.items():
	marker = f"`{version}` is deprecated"
	if marker not in docs_text:
		problems.append(
			f"  {docs_rel}\n"
			f"    no prose paragraph saying {marker!r}\n"
			f"    schema:  {version} is deprecated, remove_after {dep.get('remove_after')}\n"
			"    The table states the verdict; the paragraph is where the reason and the fix live."
		)
		continue
	para = docs_text.split(marker, 1)[1][:1600]
	for key in ("eol", "remove_after", "replacement"):
		if dep[key] not in para:
			problems.append(
				f"  {docs_rel}\n"
				f"    the {version} deprecation paragraph never mentions {key} = {dep[key]!r}"
			)

# ── 7. the release workflow's own prose about the deprecation ────────────────
# release.yaml hardcodes the EOL date in the comment above the matrix. It is the
# copy a maintainer reads while editing the matrix, so it is the copy most likely
# to be trusted and least likely to be revisited.
workflow_text = read(workflow_rel)
for version in want:
	dep = deprecations.get(version)
	found = re.search(rf"py{re.escape(version)} is DEPRECATED", workflow_text, re.I)
	if dep and not found:
		problems.append(
			f"  {workflow_rel}\n"
			f"    no `py{version} is DEPRECATED` note above the matrix\n"
			f"    schema:  {version} is deprecated, remove_after {dep.get('remove_after')}\n"
			"    This is the comment a maintainer reads while editing the matrix that publishes it."
		)
	elif dep is None and found:
		problems.append(
			f"  {workflow_rel}\n"
			f"    calls py{version} DEPRECATED, but the schema does not"
		)
	elif dep and found:
		tail = workflow_text[found.start():found.start() + 700]
		if dep["eol"] not in tail:
			problems.append(
				f"  {workflow_rel}\n"
				f"    the py{version} deprecation note never states its EOL date ({dep['eol']})"
			)

# ── 8. the Debian suite named in prose vs the one stage 2 is built on ────────
# The list arms above are blind to the suite: move the base image from one
# Debian release to the next and every version still appears in every file, so
# they all stay green while the schema's deprecation `reason` — which the CLI
# prints VERBATIM on compile, validate and deploy — keeps naming the old one.
# That is worse than a stale doc. A py3.10 author reads the warning, checks the
# Dockerfile the compiler generated, finds a different suite, and files the
# whole warning under stale tooling — which retires the one mechanism we have
# for reaching them before the leg stops being published.
#
# The fix a suite move should make is to stop naming a suite: `python:3.10-slim`
# is literally correct on every suite, because that tag has always resolved to
# whatever the current one is. So this arm does not demand the CURRENT suite
# anywhere; it only refuses a literal that names one stage 2 does not use. A
# suffix-free reference is always accepted.
#
# Only the two files a user reads as CURRENT truth are scanned. The docs/api
# mirror of the schema needs no arm of its own: TestEmbeddedSchemasMatchDocs
# binds it byte-for-byte to the copy embedded in the binary, which is the copy
# read here. CHANGELOG.md is deliberately excluded — a released section naming
# the suite of its own era is a record, and correcting it would be falsifying
# history rather than fixing drift.
dockerfile_from = None
for line in dockerfile_text.splitlines():
	m = re.match(r"\s*FROM\s+python:(?:\$\{PYTHON_VERSION\}|3\.\d+)-slim(-[A-Za-z0-9.]+)?\s*(?:AS\s+\S+)?\s*$", line)
	if m:
		dockerfile_from = (line.strip(), m.group(1))
		break
if dockerfile_from is None:
	fail(
		f"{dockerfile_rel} has no `FROM python:...-slim...` line — this gate cannot tell which\n"
		"Debian suite the task base is built on, so it cannot reconcile the suite named in the\n"
		"schema's deprecation reason against it. Either restore the line or delete this arm\n"
		"deliberately; a gate that stops gating after a refactor is worse than no gate."
	)
from_line, from_suffix = dockerfile_from
if not from_suffix:
	fail(
		f"{dockerfile_rel}'s stage-2 base `{from_line}` pins no Debian suite.\n"
		"The suffix is carried deliberately (see the comment above that FROM): with it, moving\n"
		"the task base to a new Debian release is a reviewable one-line diff; without it the\n"
		"move happens the day upstream retags, in someone else's build, with nothing in our\n"
		"history to point at. Pin it, or remove this arm in the change that decides otherwise."
	)
suite = from_suffix.lstrip("-")
# `3.x` as well as `3.10`: prose generalizes over the lines, and a generalized
# literal goes stale exactly the same way a specific one does.
IMAGE_SUITE_RE = re.compile(r"python:3\.(?:\d+|x)-slim-([A-Za-z0-9.]+)")
for rel, text in ((schema_rel, read(schema_rel)), (docs_rel, docs_text)):
	for m in IMAGE_SUITE_RE.finditer(text):
		if m.group(1) == suite:
			continue
		problems.append(
			f"  {rel}\n"
			f"    names {m.group(0)}\n"
			f"    {dockerfile_rel} stage 2: {from_line}\n"
			f"    Drop the suite from the literal ({m.group(0).rsplit('-', 1)[0]}) rather than\n"
			f"    swapping it for {suite!r}: the suffix-free tag is correct on every suite, so it\n"
			"    cannot go stale on the next move. This text is printed to users verbatim."
		)

if problems:
	sys.exit(
		"FAIL: the published-Python list has drifted from the schema enum.\n"
		+ "\n".join(problems)
		+ f"\n\nThe schema ({schema_rel}) is the source of truth: it is what the CLI\n"
		"enforces. Change it first, then bring the copies to match."
	)

deprecated = ", ".join(sorted(deprecations)) or "none"
print(f"OK: published Python matrix agrees everywhere ({', '.join(want)}; deprecated: {deprecated})")
PY
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	# The fixture deliberately gives 3.10 an `eol` (2026-10-31) DIFFERENT from its
	# `remove_after` (2026-12-31). In the real repo the two happen to be the same
	# date, which would let a gate that compares the wrong column against the wrong
	# schema field pass every case below for the wrong reason.
	#
	# The `reason` carries a `python:3.10-slim-trixie` literal so the happy path
	# exercises the suite arm positively rather than only vacuously: a fixture
	# with no image literal at all would pass that arm whether it worked or not.
	_dep_json='{"3.10":{"eol":"2026-10-31","remove_after":"2026-12-31","replacement":"3.11","reason":"docker-library stops rebuilding python:3.10-slim-trixie after its EOL."}}'
	_schema() { # <dir> <deprecations-json>
		printf '{"properties":{"python_version":{"enum":["3.10","3.11"],"default":"3.11",\n "x-leoflow-python-deprecations":%s}}}\n' \
			"$2" >"$1/internal/domain/schemas/leoflow-yaml-schema.json"
	}

	# release.yaml carries both the matrix and the prose note about the deprecated
	# leg, so a mutator that rewrites one must restate the other or it is changing
	# two things at once and the case proves nothing about either.
	_matrix_both='jobs:\n  runtime-image:\n    strategy:\n      matrix:\n        python: ["3.10", "3.11"]\n'
	_note_default='py3.10 is DEPRECATED: Python 3.10 goes EOL 2026-10-31.'
	_release() { # <dir> <matrix-yaml-printf-format> <deprecation-note>
		{
			printf '# %s\n' "$3"
			# shellcheck disable=SC2059 # $2 IS the format: callers pass literal templates
			printf "$2"
		} >"$1/.github/workflows/release.yaml"
	}

	_rows_default='| `3.10` | **Deprecated** | 2026-10-31 | 2026-12-31 |\n| `3.11` | Supported *(default)* | 2027-10-31 | — |\n'
	_prose_default='**`3.10` is deprecated.** Upstream EOL 2026-10-31; published until 2026-12-31. Set `python_version` to `3.11`.\n'
	_docs() { # <dir> <status-rows-printf-format> <prose-printf-format>
		{
			printf '| `python_version` | `3.10`\\|`3.11` | Base image Python. |\n'
			printf '| `python_version` | `"3.11"` | Pick `3.10` or `3.11`. |\n\n'
			printf '| Line | Status | Upstream EOL | Published until |\n|---|---|---|---|\n'
			# shellcheck disable=SC2059 # $2 IS the format: callers pass literal templates
			printf "$2"
			printf '\n'
			# shellcheck disable=SC2059 # $3 IS the format: callers pass literal templates
			printf "$3"
		} >"$1/website/content/reference/configuration.md"
	}

	# The Dockerfile fixture carries a stage-1 `golang:*-bookworm` alongside the
	# stage-2 `python:*-slim-trixie`, because the real one does and because that
	# is the pair the suite arm has to tell apart: an arm that grepped the file
	# for a suite name would read the builder's and reconcile against the wrong
	# stage, passing every case below for the wrong reason.
	_dfcomment_default='# PYTHON_VERSION selects the interpreter (3.10 / 3.11).'
	_dffrom_default='FROM python:${PYTHON_VERSION}-slim-trixie'
	_dockerfile() { # <dir> <comment-line> <stage-2-FROM-line>
		printf '%s\nARG PYTHON_VERSION=3.11\nFROM golang:1.26.3-bookworm AS agent-build\n%s\n' \
			"$2" "$3" >"$1/runtime/Dockerfile"
	}

	# Builds a minimal repo root whose copies all agree on 3.10/3.11.
	_mkroot() { # <dir>
		local d="$1"
		mkdir -p "$d/internal/domain/schemas" "$d/.github/workflows" "$d/runtime/python" \
			"$d/website/content/reference"
		_schema "$d" "$_dep_json"
		_release "$d" "$_matrix_both" "$_note_default"
		printf 'runtime-images:\n\tfor v in 3.10 3.11; do \\\n\t\tdocker build . ; \\\n\tdone\n' >"$d/Makefile"
		_dockerfile "$d" "$_dfcomment_default" "$_dffrom_default"
		printf '[project]\nrequires-python = ">=3.10"\n' >"$d/runtime/python/pyproject.toml"
		_docs "$d" "$_rows_default" "$_prose_default"
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
	_drop_from_matrix() { _release "$1" 'jobs:\n  runtime-image:\n    strategy:\n      matrix:\n        python: ["3.10"]\n' "$_note_default"; }
	_comment_out_matrix() { _release "$1" 'jobs:\n  runtime-image:\n    strategy:\n      matrix:\n        # python: ["3.10", "3.11"]\n        os: [ubuntu-latest]\n' "$_note_default"; }
	_rename_job() { _release "$1" 'jobs:\n  runtime-base:\n    strategy:\n      matrix:\n        python: ["3.10", "3.11"]\n' "$_note_default"; }
	_unquoted_matrix() { _release "$1" 'jobs:\n  runtime-image:\n    strategy:\n      matrix:\n        python: [3.10, 3.11]\n' "$_note_default"; }
	_stale_makefile() { printf 'runtime-images:\n\tfor v in 3.10; do \\\n\t\tdocker build . ; \\\n\tdone\n' >"$1/Makefile"; }
	_commented_makefile_loop() { printf 'runtime-images:\n\t# for v in 3.10 3.11; do \\\n\t@echo skip\n' >"$1/Makefile"; }
	_other_target_loop() { printf 'other-images:\n\tfor v in 3.10 3.11; do \\\n\tdone\n\nruntime-images:\n\t@echo nothing\n' >"$1/Makefile"; }
	_stale_dockerfile() { _dockerfile "$1" '# PYTHON_VERSION selects the interpreter (3.10 / 3.11 / 3.12).' "$_dffrom_default"; }
	_dropped_dockerfile_comment() { _dockerfile "$1" '# nothing to see here' "$_dffrom_default"; }
	_raised_floor() { printf '[project]\nrequires-python = ">=3.11"\n' >"$1/runtime/python/pyproject.toml"; }
	_stale_docs_row() {
		_docs "$1" "$_rows_default" "$_prose_default"
		printf '| `python_version` | `3.10`\\|`3.11`\\|`3.12` | drifted |\n' >>"$1/website/content/reference/configuration.md"
	}
	_stale_status_table() { _docs "$1" '| `3.10` | **Deprecated** | 2026-10-31 | 2026-12-31 |\n' "$_prose_default"; }
	_missing_schema() { rm -f "$1/internal/domain/schemas/leoflow-yaml-schema.json"; }
	_empty_enum() { printf '{"properties":{"python_version":{"default":"3.11"}}}\n' >"$1/internal/domain/schemas/leoflow-yaml-schema.json"; }
	_unlisted_deprecation() { _schema "$1" '{"3.9":{}}'; }
	_deprecated_default() { printf '{"properties":{"python_version":{"enum":["3.10","3.11"],"default":"3.10","x-leoflow-python-deprecations":{"3.10":{}}}}}\n' >"$1/internal/domain/schemas/leoflow-yaml-schema.json"; }
	_unsorted_enum() { printf '{"properties":{"python_version":{"enum":["3.11","3.10"],"default":"3.11"}}}\n' >"$1/internal/domain/schemas/leoflow-yaml-schema.json"; }

	# ── the status/date arm: the half the list check cannot see ─────────────────
	# The motivating mutation is _deprecation_moves_in_schema_only: move the
	# deprecation from 3.10 to 3.11 in the schema and the list of versions is
	# still identical everywhere, so every case above stays green while the table
	# the user reads now says the exact opposite of what the CLI enforces.
	_deprecation_moves_in_schema_only() {
		_schema "$1" '{"3.11":{"eol":"2027-10-31","remove_after":"2027-12-31","replacement":"3.12"}}'
		# Keep the default off the newly deprecated line so this case fails on the
		# docs disagreement and not on the deprecated-default rule.
		sed -i.bak 's/"default":"3.11"/"default":"3.10"/' "$1/internal/domain/schemas/leoflow-yaml-schema.json"
		rm -f "$1/internal/domain/schemas/leoflow-yaml-schema.json.bak"
	}
	_docs_call_it_supported() { _docs "$1" '| `3.10` | Supported | 2026-10-31 | 2026-12-31 |\n| `3.11` | Supported *(default)* | 2027-10-31 | — |\n' "$_prose_default"; }
	_docs_deprecate_a_supported_line() { _docs "$1" '| `3.10` | **Deprecated** | 2026-10-31 | 2026-12-31 |\n| `3.11` | **Deprecated** | 2027-10-31 | — |\n' "$_prose_default"; }
	_docs_stale_eol() { _docs "$1" '| `3.10` | **Deprecated** | 2025-10-31 | 2026-12-31 |\n| `3.11` | Supported *(default)* | 2027-10-31 | — |\n' "$_prose_default"; }
	_docs_stale_removal_date() { _docs "$1" '| `3.10` | **Deprecated** | 2026-10-31 | 2027-06-30 |\n| `3.11` | Supported *(default)* | 2027-10-31 | — |\n' "$_prose_default"; }
	_docs_removal_date_on_supported_line() { _docs "$1" '| `3.10` | **Deprecated** | 2026-10-31 | 2026-12-31 |\n| `3.11` | Supported *(default)* | 2027-10-31 | 2028-01-01 |\n' "$_prose_default"; }
	_docs_no_status_header() {
		_docs "$1" "$_rows_default" "$_prose_default"
		sed -i.bak 's/^| Line | Status .*/| Version | State |/' "$1/website/content/reference/configuration.md"
		rm -f "$1/website/content/reference/configuration.md.bak"
	}
	_docs_no_prose() { _docs "$1" "$_rows_default" 'The table above is the whole story.\n'; }
	_docs_prose_omits_removal_date() { _docs "$1" "$_rows_default" '**`3.10` is deprecated.** Upstream EOL 2026-10-31. Set `python_version` to `3.11`.\n'; }
	_release_drops_deprecation_note() { _release "$1" "$_matrix_both" 'One leg per supported Python.'; }
	_release_deprecates_a_supported_line() { _release "$1" "$_matrix_both" 'py3.11 is DEPRECATED: EOL 2027-10-31. Also py3.10 is DEPRECATED: Python 3.10 goes EOL 2026-10-31.'; }
	_release_note_omits_eol() { _release "$1" "$_matrix_both" 'py3.10 is DEPRECATED: it goes away at some point.'; }

	# ── the Debian-suite arm ───────────────────────────────────────────────────
	# The motivating mutation is _schema_names_a_stale_suite: every version still
	# appears in every file, every date still agrees, so every case above stays
	# green — while the `reason` the CLI prints VERBATIM on compile/validate/deploy
	# names a suite the base image has not been built on since the move. A reader
	# checks their generated Dockerfile, sees a different suite, and files the
	# whole warning under stale tooling.
	_dep_with_reason() { # <dir> <reason>
		_schema "$1" "{\"3.10\":{\"eol\":\"2026-10-31\",\"remove_after\":\"2026-12-31\",\"replacement\":\"3.11\",\"reason\":\"$2\"}}"
	}
	_schema_names_a_stale_suite() { _dep_with_reason "$1" 'docker-library stops rebuilding python:3.10-slim-bookworm after its EOL.'; }
	_schema_is_suite_agnostic() { _dep_with_reason "$1" 'docker-library stops rebuilding python:3.10-slim after its EOL.'; }
	_docs_name_a_stale_suite() {
		_docs "$1" "$_rows_default" \
			'**`3.10` is deprecated.** Upstream EOL 2026-10-31; published until 2026-12-31. From then `python:3.10-slim-bookworm` receives no updates. Set `python_version` to `3.11`.\n'
	}
	_dockerfile_from_omits_the_suite() { _dockerfile "$1" "$_dfcomment_default" 'FROM python:${PYTHON_VERSION}-slim'; }
	_dockerfile_has_no_python_from() { _dockerfile "$1" "$_dfcomment_default" 'FROM scratch'; }

	_case "an agreeing tree passes"                  0 "agrees everywhere (3.10, 3.11; deprecated: 3.10)" _noop
	_case "a leg missing from the release matrix"    1 "matrix.python" _drop_from_matrix
	_case "a commented-out matrix is not a matrix"   1 "declares no \`strategy.matrix.python\`" _comment_out_matrix
	_case "a renamed job fails loudly"               1 "did it get renamed?" _rename_job
	# YAML turns an unquoted 3.10 into the float 3.1 — the typo this gate is for.
	_case "an unquoted 3.10 is caught"               1 "matrix.python" _unquoted_matrix
	_case "a stale Makefile loop"                    1 "runtime-images\` loop" _stale_makefile
	_case "a commented-out Makefile loop"            1 "did it get renamed?" _commented_makefile_loop
	_case "another target's loop does not count"     1 "did it get renamed?" _other_target_loop
	_case "a stale Dockerfile comment"               1 "selects the interpreter\` comment" _stale_dockerfile
	_case "a dropped Dockerfile comment"             1 "no longer carries the" _dropped_dockerfile_comment
	_case "a raised requires-python floor"           1 "oldest published leg" _raised_floor
	_case "a stale docs row"                         1 "configuration.md" _stale_docs_row
	_case "a stale docs status table"                1 "Python version support table" _stale_status_table
	_case "a missing schema fails, not skips"        1 "does not exist" _missing_schema
	_case "an empty enum fails"                      1 "declares no python_version enum" _empty_enum
	_case "a deprecation outside the enum"           1 "is not in the enum" _unlisted_deprecation
	_case "a deprecated default"                     1 "also marks deprecated" _deprecated_default
	_case "an out-of-order enum"                     1 "not in ascending order" _unsorted_enum
	_case "the deprecation moves in the schema only" 1 "status row for 3.11" _deprecation_moves_in_schema_only
	_case "the docs call a deprecated line supported" 1 "This table is where a user learns" _docs_call_it_supported
	_case "the docs deprecate a supported line"      1 "carries no x-leoflow-python-deprecations entry" _docs_deprecate_a_supported_line
	_case "a stale Upstream EOL cell"                1 "schema eol: 2026-10-31" _docs_stale_eol
	_case "a stale Published until cell"             1 "schema remove_after: 2026-12-31" _docs_stale_removal_date
	_case "a removal date on a supported line"       1 "declares a removal date" _docs_removal_date_on_supported_line
	_case "a renamed status table header"            1 "has no Python-support table header" _docs_no_status_header
	_case "no deprecation prose at all"              1 "no prose paragraph saying" _docs_no_prose
	_case "prose that omits the removal date"        1 "never mentions remove_after" _docs_prose_omits_removal_date
	_case "release.yaml drops the deprecation note"  1 "no \`py3.10 is DEPRECATED\` note" _release_drops_deprecation_note
	_case "release.yaml deprecates a supported leg"  1 "calls py3.11 DEPRECATED" _release_deprecates_a_supported_line
	_case "a release note with no EOL date"          1 "never states its EOL date" _release_note_omits_eol
	_case "the schema names a stale Debian suite"    1 "python:3.10-slim-bookworm" _schema_names_a_stale_suite
	_case "the docs name a stale Debian suite"       1 "python:3.10-slim-bookworm" _docs_name_a_stale_suite
	_case "a suite-agnostic reason passes"           0 "agrees everywhere" _schema_is_suite_agnostic
	_case "a stage-2 FROM with no suite"             1 "pins no Debian suite" _dockerfile_from_omits_the_suite
	_case "no stage-2 python FROM at all"            1 "no \`FROM python:" _dockerfile_has_no_python_from

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-$PWD}"
