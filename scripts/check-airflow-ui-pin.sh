#!/usr/bin/env bash
# The Airflow UI bundle is pinned twice, and only one of the two is real.
#
#   Makefile AIRFLOW_UI_VERSION   the tag `make fetch-airflow-ui` pulls
#   internal/ui/assets/VERSION    the marker COMMITTED next to the bundle,
#                                 which `ui.Version()` returns at runtime
#
# The marker is the source of truth: it is a byte in the binary, next to the
# SPA the binary serves. The Makefile is intent. Bump the Makefile and forget to
# re-run the target — the most ordinary way this goes wrong, because the target
# needs Docker and a multi-GB pull — and every declaration in the tree says one
# version while the server ships another. Nothing fails: the old bundle keeps
# working, `ui.Version()` keeps answering, and the next person debugging a UI
# behaviour reads the Makefile and compares against the wrong upstream release.
#
# The prose is reconciled too, in the places a reader takes as current truth:
# the `internal/ui` package godoc (which states the pin three times), README.md,
# and docker-compose.yml's comment on the server service.
#
# Deliberately NOT scanned:
#   - internal/api/**, where ~20 DTO godocs name the Airflow release whose
#     response shape they mirror. Those are 20 copies of this same fact, and a
#     UI bump genuinely should revisit them — but mechanically, the useful check
#     is that the SHAPE still matches, and a gate that only demanded the string
#     be rewritten would produce a 20-file sed on every bump for a reviewer to
#     wave through. That is the rubber-stamp failure this class of gate exists
#     to avoid, so it stays a human step; this gate's header is where the next
#     bump reads that it is one.
#   - website/content/project/** and CHANGELOG.md, which are records of their
#     own era (ADRs are immutable, AGENTS.md).
#   - scripts/connectors-providers*.txt, whose `apache-airflow==` pin happens to
#     match today but governs the provider package set, not the SPA.
#
# Usage: scripts/check-airflow-ui-pin.sh [--self-test]
#        scripts/check-airflow-ui-pin.sh <repo-root>
set -euo pipefail

MARKER_REL="internal/ui/assets/VERSION"

check() { # <repo-root>
	python3 - "$1" "$MARKER_REL" <<'PY'
import os
import re
import sys

root, marker_rel = sys.argv[1:3]
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


# ── the source of truth: the marker committed next to the bundle ─────────────
truth = read(marker_rel).strip()
if not re.fullmatch(r"\d+\.\d+\.\d+", truth):
	fail(
		f"{marker_rel} holds {truth!r}, which is not an `X.Y.Z` upstream tag.\n"
		"That file is what `ui.Version()` reports; a placeholder there means the bundle was\n"
		"never fetched, and this gate must not reconcile the tree against a placeholder."
	)

# ── 1. the Makefile pin that produced it ─────────────────────────────────────
m = re.search(r"^AIRFLOW_UI_VERSION\s*\?=\s*(\S+)\s*$", read("Makefile"), re.M)
if not m:
	fail(
		"Makefile declares no `AIRFLOW_UI_VERSION ?= <X.Y.Z>` — did `fetch-airflow-ui` get\n"
		"renamed? A gate that cannot find the pin must not report OK."
	)
if m.group(1) != truth:
	problems.append(
		f"  Makefile\n"
		f"    AIRFLOW_UI_VERSION: {m.group(1)}\n"
		f"    {marker_rel}:      {truth}\n"
		"    The pin moved but the bundle did not. Run `make fetch-airflow-ui` and commit the\n"
		"    result, or put the pin back — the server serves the marker's version either way."
	)

# ── 2. the prose that states it as current truth ─────────────────────────────
# `Airflow 3.2.x` and other deliberately generalized forms do not match and are
# always accepted: they cannot go stale on a patch bump.
VERSION_RE = re.compile(r"(?:Apache Airflow|Airflow|apache/airflow:)\s*(\d+\.\d+\.\d+)")

targets = ["README.md", "docker-compose.yml"]
uidir = os.path.join(root, "internal", "ui")
if not os.path.isdir(uidir):
	fail("internal/ui/ does not exist — the package that owns the bundle moved")
for name in sorted(os.listdir(uidir)):
	if name.endswith(".go"):
		targets.append(f"internal/ui/{name}")

seen = 0
for rel in targets:
	for lineno, line in enumerate(read(rel).splitlines(), 1):
		for m in VERSION_RE.finditer(line):
			seen += 1
			if m.group(1) == truth:
				continue
			problems.append(
				f"  {rel}:{lineno}\n"
				f"    names Airflow {m.group(1)}\n"
				f"    {marker_rel}: {truth}\n"
				"    This sentence is read as a statement about the UI the binary actually serves.\n"
				"    Use the marker's version, or generalize the sentence (`3.2.x`) if the exact\n"
				"    patch is not what it is about."
			)

if seen == 0:
	fail(
		"no `Airflow <X.Y.Z>` reference found in README.md, docker-compose.yml or the\n"
		"internal/ui package. Either the prose stopped naming the pin (fine — delete this arm\n"
		"in that change) or the scan targets moved; an empty scan must not report OK."
	)

if problems:
	sys.exit(
		"FAIL: the embedded Airflow UI pin has drifted.\n"
		+ "\n".join(problems)
		+ f"\n\n{marker_rel} is the source of truth: it ships in the binary next to the\n"
		"bundle it describes, and `ui.Version()` reports it."
	)

print(f"OK: the embedded Airflow UI pin agrees everywhere ({truth}; {seen} prose reference(s))")
PY
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	_marker() { printf '%s\n' "$2" >"$1/internal/ui/assets/VERSION"; }
	_makefile() { printf 'AIRFLOW_UI_VERSION ?= %s\n\nfetch-airflow-ui:\n\t@echo fetch\n' "$2" >"$1/Makefile"; }
	_embed() { printf '// Package ui embeds the pinned Apache Airflow %s React SPA bundle,\n// extracted from the apache/airflow:%s image.\npackage ui\n' "$2" "$2" >"$1/internal/ui/embed.go"; }
	_readme() { printf 'The Airflow %s UI ships embedded in the server.\n' "$2" >"$1/README.md"; }
	_compose() { printf 'services:\n  server:\n    # serves the embedded Airflow %s UI\n    image: leoflow-server\n' "$2" >"$1/docker-compose.yml"; }

	_mkroot() { # <dir>
		local d="$1"
		mkdir -p "$d/internal/ui/assets"
		_marker "$d" 3.2.1
		_makefile "$d" 3.2.1
		_embed "$d" 3.2.1
		_readme "$d" 3.2.1
		_compose "$d" 3.2.1
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
	# The motivating mutation: bump the pin, do not re-run the fetch. Nothing
	# breaks; the binary just keeps serving the old bundle.
	_pin_bumped_without_the_bundle() { _makefile "$1" 3.3.0; }
	_godoc_lags() { _embed "$1" 3.2.0; }
	_readme_lags() { _readme "$1" 3.1.4; }
	_compose_comment_lags() { _compose "$1" 3.1.4; }
	_generalized_prose_is_accepted() { _readme "$1" 3.2.x; }
	_placeholder_marker() { _marker "$1" unknown; }
	_missing_marker() { rm -f "$1/internal/ui/assets/VERSION"; }
	_missing_makefile_pin() { printf 'fetch-airflow-ui:\n\t@echo fetch\n' >"$1/Makefile"; }
	_nothing_names_the_pin() {
		printf '// Package ui embeds the pinned Airflow SPA bundle.\npackage ui\n' >"$1/internal/ui/embed.go"
		_readme "$1" 3.2.x
		printf 'services:\n  server:\n    image: leoflow-server\n' >"$1/docker-compose.yml"
	}
	_a_new_ui_file_is_covered() { printf '// Server serves the embedded Airflow 3.1.4 SPA.\npackage ui\n' >"$1/internal/ui/handler.go"; }

	_case "an agreeing tree passes"                 0 "agrees everywhere (3.2.1" _noop
	_case "the pin moved but the bundle did not"    1 "AIRFLOW_UI_VERSION: 3.3.0" _pin_bumped_without_the_bundle
	_case "a lagging package godoc"                 1 "names Airflow 3.2.0" _godoc_lags
	_case "a lagging README sentence"               1 "README.md:1" _readme_lags
	_case "a lagging compose comment"               1 "docker-compose.yml:3" _compose_comment_lags
	_case "generalized prose is accepted"           0 "agrees everywhere" _generalized_prose_is_accepted
	_case "a placeholder marker is refused"         1 "not an \`X.Y.Z\` upstream tag" _placeholder_marker
	_case "a missing marker fails, not skips"       1 "does not exist" _missing_marker
	_case "a dropped Makefile pin fails loudly"     1 "declares no \`AIRFLOW_UI_VERSION" _missing_makefile_pin
	_case "an empty prose scan fails, not passes"   1 "no \`Airflow <X.Y.Z>\` reference found" _nothing_names_the_pin
	_case "a new file in internal/ui is covered"    1 "internal/ui/handler.go" _a_new_ui_file_is_covered

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-$PWD}"
