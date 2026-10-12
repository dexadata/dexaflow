#!/usr/bin/env bash
# Dexaflow Lite starts a Postgres container from docker-compose.dev.yaml, and
# then TELLS the user which one, in prose, from four other places:
#
#   internal/cli/... `dexaflow lite` flag help and the resolved-backend note
#   website/content/get-started/quickstart.md
#   website/content/concepts/editions.md
#   website/content/contribute/local-dev-loop.md
#
# The compose file is the only one of the five that does anything; the rest are
# a version string with nothing between them and the thing they describe. This
# is the same shape as check-lite-prepull-matches-compose.sh, which reconciles
# the SIXTH copy — the pre-pull tag baked into the cold-start release gate. That
# gate exists because a stale copy there wastes the boot budget. This one exists
# because a stale copy here tells the user, on their terminal, a fact about
# their own machine that is not true. They have a container running, we name a
# different one, and the mismatch shows up when they go looking for the volume
# or the port and find the wrong thing.
#
# Scope is deliberately narrow. Three other Postgres tags in this tree are NOT
# this fact and must not be dragged into it: docker-compose.yml runs 17-alpine
# for the Pro stack, the e2e scripts run their own warehouse container for dbt,
# and helm/dexaflow ships an evaluation datastore. Nothing says those should
# track Lite's, and a gate that forced them to would be inventing a policy
# rather than catching drift.
#
# website/content/project/ is excluded for the same reason
# check-python-runtime-matrix.sh excludes CHANGELOG.md: an ADR records the
# decision of its era and is immutable (AGENTS.md), so "correcting" one would be
# falsifying history.
#
# Usage: scripts/check-lite-postgres-tag.sh [--self-test]
#        scripts/check-lite-postgres-tag.sh <repo-root>
set -euo pipefail

COMPOSE_REL="docker-compose.dev.yaml"

check() { # <repo-root>
	python3 - "$1" "$COMPOSE_REL" <<'PY'
import os
import re
import sys

try:
	import yaml
except ImportError:
	sys.exit("FAIL: PyYAML unavailable; cannot parse the Lite compose file (pip install pyyaml)")

root, compose_rel = sys.argv[1:3]
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


# ── the source of truth: the container Lite actually starts ──────────────────
# Parsed as YAML, not grepped: a commented-out `image:` is not an image, and
# that fail-open is the one a reviewer reproduced in one line against an earlier
# version of the sibling gate.
compose = yaml.safe_load(read(compose_rel)) or {}
services = compose.get("services") or {}
if "postgres" not in services:
	fail(f"{compose_rel} declares no `postgres` service — Lite's datastore moved and this gate is blind")
image = (services["postgres"] or {}).get("image")
if not image:
	fail(f"{compose_rel}'s `postgres` service declares no `image:`")
image = str(image)
if not re.fullmatch(r"postgres:[\w.-]+", image):
	fail(
		f"{compose_rel}'s postgres image is {image!r}, which this gate cannot read as a\n"
		"`postgres:<tag>` reference. Widen the gate deliberately rather than letting it pass."
	)
truth = image

# ── the copies that only describe it ─────────────────────────────────────────
# `postgres://` connection strings do not match: the pattern requires a digit
# after the colon.
TAG_RE = re.compile(r"postgres:(\d[\w.-]*)")

targets = []
for base, _, names in os.walk(os.path.join(root, "internal", "cli")):
	for name in sorted(names):
		if name.endswith(".go"):
			targets.append(os.path.relpath(os.path.join(base, name), root))
if not targets:
	fail("internal/cli/ holds no .go files — the CLI moved and this gate is scanning nothing")

docs_root = os.path.join(root, "website", "content")
if not os.path.isdir(docs_root):
	fail("website/content/ does not exist — this gate cannot see the pages that state the tag")
doc_targets = []
for base, dirs, names in os.walk(docs_root):
	rel_base = os.path.relpath(base, root)
	# Records, not current truth: ADRs are immutable and the background pages
	# report what was observed at the time.
	if rel_base.startswith(os.path.join("website", "content", "project")):
		dirs[:] = []
		continue
	for name in sorted(names):
		if name.endswith(".md"):
			doc_targets.append(os.path.relpath(os.path.join(base, name), root))
targets += doc_targets

seen = 0
for rel in sorted(targets):
	for lineno, line in enumerate(read(rel).splitlines(), 1):
		for m in TAG_RE.finditer(line):
			seen += 1
			if m.group(0) == truth:
				continue
			problems.append(
				f"  {rel}:{lineno}\n"
				f"    names {m.group(0)}\n"
				f"    {compose_rel} starts: {truth}\n"
				"    This text is what a Lite user is told about a container running on their own\n"
				"    machine. Bring it to the compose tag, or move the compose tag first if the\n"
				"    intent was to change what Lite starts."
			)

if seen == 0:
	fail(
		f"no `postgres:<tag>` reference found in internal/cli/ or website/content/ at all.\n"
		f"The copies this gate reconciles against {compose_rel} are gone — either the prose\n"
		"stopped naming the image (fine, delete this gate in that change) or the scan roots\n"
		"moved. An empty scan must not report OK."
	)

if problems:
	sys.exit(
		"FAIL: the Lite Postgres tag has drifted from the container Lite starts.\n"
		+ "\n".join(problems)
		+ f"\n\n{compose_rel} is the source of truth: it is the only one of these copies that\n"
		"starts anything."
	)

print(f"OK: every Lite-facing copy of the Postgres tag says {truth} ({seen} reference(s))")
PY
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	_compose() { # <dir> <yaml-printf-format>
		# shellcheck disable=SC2059 # $2 IS the format: callers pass literal templates
		printf "$2" >"$1/docker-compose.dev.yaml"
	}
	_compose_default='services:\n  postgres:\n    image: postgres:16\n  redis:\n    image: redis:7\n'
	_cli() { printf 'package cli\n\n// help mentions the Docker %s container.\nconst help = "using the Docker Postgres (%s)"\n' "$2" "$2" >"$1/internal/cli/dev.go"; }
	_doc() { printf 'Lite spins up `%s` automatically.\n' "$2" >"$1/website/content/get-started/quickstart.md"; }
	_adr() { printf 'Lite default datastore is `%s`.\n' "$2" >"$1/website/content/project/adrs/0029-lite.md"; }

	_mkroot() { # <dir>
		local d="$1"
		mkdir -p "$d/internal/cli" "$d/website/content/get-started" "$d/website/content/project/adrs"
		_compose "$d" "$_compose_default"
		_cli "$d" postgres:16
		_doc "$d" postgres:16
		# An ADR naming the tag of its own era must never fail this gate.
		_adr "$d" postgres:15
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
	# The motivating mutation: bump the container Lite starts and leave the
	# sentences that describe it behind.
	_compose_bumped_alone() { _compose "$1" 'services:\n  postgres:\n    image: postgres:17-alpine\n'; }
	_cli_help_lags() { _cli "$1" postgres:15; }
	_doc_lags() { _doc "$1" postgres:17; }
	_commented_out_image() { _compose "$1" 'services:\n  postgres:\n    # image: postgres:16\n    restart: always\n'; }
	_no_postgres_service() { _compose "$1" 'services:\n  redis:\n    image: redis:7\n'; }
	_missing_compose() { rm -f "$1/docker-compose.dev.yaml"; }
	_nothing_names_the_tag() {
		printf 'package cli\n\nconst help = "using the Docker Postgres"\n' >"$1/internal/cli/dev.go"
		printf 'Lite spins up Postgres automatically.\n' >"$1/website/content/get-started/quickstart.md"
	}
	# A connection string is not an image tag.
	_connection_strings_are_not_tags() {
		printf 'package cli\n\nconst dsn = "postgres://leoflow:leoflow@localhost:5432/leoflow"\nconst help = "the Docker Postgres (postgres:16)"\n' >"$1/internal/cli/dev.go"
	}
	_a_new_doc_page_is_covered() {
		mkdir -p "$1/website/content/operate"
		printf 'Lite uses `postgres:15` under the hood.\n' >"$1/website/content/operate/new-page.md"
	}

	_case "an agreeing tree passes"                  0 "says postgres:16" _noop
	_case "the compose bump leaves prose behind"     1 "names postgres:16" _compose_bumped_alone
	_case "the CLI help lags"                        1 "internal/cli/dev.go" _cli_help_lags
	_case "a docs page lags"                         1 "names postgres:17" _doc_lags
	_case "a commented-out image is not an image"    1 "declares no \`image:\`" _commented_out_image
	_case "a renamed service fails, not skips"       1 "declares no \`postgres\` service" _no_postgres_service
	_case "a missing compose fails, not skips"       1 "does not exist" _missing_compose
	_case "an empty scan fails, not passes"          1 "no \`postgres:<tag>\` reference found" _nothing_names_the_tag
	_case "connection strings are not tags"          0 "says postgres:16" _connection_strings_are_not_tags
	_case "a new docs page is covered by the walk"   1 "operate/new-page.md" _a_new_doc_page_is_covered
	# The ADR fixture names postgres:15 in every case above and never fails one.
	_case "ADRs record their own era"                0 "says postgres:16" _noop

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-$PWD}"
