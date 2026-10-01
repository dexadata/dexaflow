#!/usr/bin/env bash
# Weekly watch on the Python lines we publish a base image for.
#
# #1031 was found by hand, seven weeks before python:3.10-slim stops being
# rebuilt. Nothing in the repo would have found it at all: an EOL is a date, and
# a date passes without any commit, any CI run and any diff. Every other supply
# chain signal we have (Dependabot, Trivy, govulncheck) reacts to a CVE that
# already exists, which for a frozen base image is precisely the signal that
# stops arriving — docker-library/python stops rebuilding an EOL line the day
# after it goes EOL (python:3.9-slim was last rebuilt 2025-11-01, one day after
# 3.9's EOL), so the image simply stops changing while the CVEs accumulate under
# it.
#
# So this asks the calendar instead, weekly, from the scheduled security
# workflow, and it does not gate anything. It is deliberately NOT named
# check-*.sh: cut-release.sh's run_gates() globs that prefix, and a release must
# never be blocked because a date is approaching or because a third-party JSON
# endpoint is down. The one thing it does exit non-zero for is its OWN failure
# (no network, unparseable feed) — a watchdog that cannot tell you it is broken
# is worse than no watchdog, and it is on a path where red costs nothing.
#
# Two findings are worth a human:
#   1. a line we still call supported is within EOL_WARN_DAYS of upstream EOL —
#      the one that would have raised #1031 in January instead of September;
#   2. a line the schema already marked deprecated is still in the enum after
#      its own remove_after date — the deprecation that quietly never happened.
# An already-deprecated line inside its window is a notice, not an alert: we
# know, the date is recorded, and repeating it weekly is how a real alert gets
# filtered out.
#
# Usage: scripts/python-eol-warn.sh [--self-test]
#        scripts/python-eol-warn.sh [<eol-json>] [<today YYYY-MM-DD>]
#
# Env: EOL_WARN_DAYS (default 270) — how far out a supported line raises.
set -euo pipefail

EOL_API="https://endoflife.date/api/python.json"
SCHEMA_REL="internal/domain/schemas/leoflow-yaml-schema.json"

# report <root> <eol-json> <today>
# Writes the markdown report to stdout, GitHub annotations to stderr, and
# `alert=true|false` to $GITHUB_OUTPUT when running in Actions.
report() {
	python3 - "$1" "$SCHEMA_REL" "$2" "${3:-}" "${EOL_WARN_DAYS:-270}" <<'PY'
import json
import os
import sys
from datetime import date, datetime

root, schema_rel, eol_path, today_arg, warn_days = sys.argv[1:6]
warn_days = int(warn_days)


def die(msg):
	sys.exit("ERROR: " + msg)


def parse_day(s, what):
	try:
		return datetime.strptime(s, "%Y-%m-%d").date()
	except (TypeError, ValueError):
		die(f"{what}: {s!r} is not a YYYY-MM-DD date")


today = parse_day(today_arg, "today") if today_arg else date.today()

with open(os.path.join(root, schema_rel)) as fh:
	py = ((json.load(fh).get("properties") or {}).get("python_version") or {})
published = py.get("enum") or die(f"{schema_rel} declares no python_version enum")
deprecations = py.get("x-leoflow-python-deprecations") or {}

try:
	with open(eol_path) as fh:
		cycles = json.load(fh)
except (OSError, ValueError) as e:
	die(f"cannot read the endoflife.date feed ({eol_path}): {e}")
if not isinstance(cycles, list) or not cycles:
	die("the endoflife.date feed is not a non-empty list — did the API shape change?")

eol_by_cycle = {}
for c in cycles:
	if isinstance(c, dict) and isinstance(c.get("cycle"), str):
		eol_by_cycle[c["cycle"]] = c.get("eol")

alerts, notices = [], []

for v in published:
	raw = eol_by_cycle.get(v)
	if raw is None:
		alerts.append(
			f"- **python {v}** — endoflife.date has no `{v}` cycle. We publish a base image for a "
			"line upstream does not list; either the version is wrong or the feed changed shape."
		)
		continue
	if raw is False:
		notices.append(f"- python {v} — no EOL announced upstream.")
		continue
	eol = parse_day(raw, f"endoflife.date eol for python {v}")
	days = (eol - today).days
	dep = deprecations.get(v)

	if dep is None:
		if days <= warn_days:
			when = f"in {days} day(s)" if days >= 0 else f"{-days} day(s) ago"
			alerts.append(
				f"- **python {v} reaches upstream EOL on {eol} ({when})** and is still listed as "
				f"supported in `{schema_rel}`.\n"
				f"  `docker-library/python` stops rebuilding the line the day after, so "
				f"`ghcr.io/dexadata/leoflow-runtime:py{v}` freezes and accumulates unfixed OS CVEs. "
				f"Users pin `base_image`, so our choice becomes theirs.\n"
				f"  Decide now: add `x-leoflow-python-deprecations` for `{v}` with a removal date, "
				f"announce it in the CHANGELOG, and keep publishing until then."
			)
		continue

	# Already deprecated: only the deprecation that never happened is an alert.
	recorded = dep.get("eol")
	if recorded and recorded != str(eol):
		alerts.append(
			f"- **python {v}** — the schema records eol `{recorded}`, endoflife.date says `{eol}`. "
			"One of the two is wrong and the CHANGELOG quotes ours."
		)
	remove_after = dep.get("remove_after")
	if remove_after and parse_day(remove_after, f"remove_after for python {v}") < today:
		alerts.append(
			f"- **python {v} is past its own removal date ({remove_after}) and is still in the "
			f"`python_version` enum.** The base has not been rebuilt since {eol}; drop the leg from "
			"the enum and from the release matrix."
		)
	else:
		notices.append(f"- python {v} — deprecated, EOL {eol}, published until {remove_after or 'unset'}.")

for line in notices:
	print(f"::notice::{line.lstrip('- ')}", file=sys.stderr)
for line in alerts:
	print(f"::warning::{line.splitlines()[0].lstrip('- ')}", file=sys.stderr)

title = "Python EOL watch: a published runtime base needs a decision"
body = [
	f"`scripts/python-eol-warn.sh` ran on {today} against {', '.join(published)} "
	f"(threshold: {warn_days} days).",
	"",
]
body += alerts if alerts else ["No published Python line needs a decision."]
if notices:
	body += ["", "<details><summary>Already handled</summary>", ""] + notices + ["", "</details>"]
body += ["", "Source of truth: `" + schema_rel + "`. Feed: <https://endoflife.date/api/python.json>."]
print("\n".join(body))

if out := os.environ.get("GITHUB_OUTPUT"):
	with open(out, "a") as fh:
		fh.write(f"alert={'true' if alerts else 'false'}\n")
		fh.write(f"title={title}\n")
PY
}

fetch_feed() { # <dest>
	local i
	for i in 1 2 3; do
		if curl -fsSL --max-time 30 "$EOL_API" -o "$1"; then return 0; fi
		echo "::warning::endoflife.date fetch attempt ${i} failed; retrying in 5s" >&2
		sleep 5
	done
	echo "::error::could not fetch ${EOL_API} after 3 attempts" >&2
	return 1
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	mkdir -p "$tmp/root/internal/domain/schemas"
	local schema="$tmp/root/internal/domain/schemas/leoflow-yaml-schema.json"

	_schema() { # <json>
		printf '%s' "$1" >"$schema"
	}
	_feed() { # <json>
		printf '%s' "$1" >"$tmp/feed.json"
	}

	_case() { # <name> <want-rc> <want-substring> <today>
		local name="$1" want_rc="$2" want_msg="$3" today="$4" rc=0 out
		out="$(report "$tmp/root" "$tmp/feed.json" "$today" 2>&1)" || rc=$?
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

	_case_absent() { # <name> <unwanted-substring> <today>
		local name="$1" unwanted="$2" today="$3" out
		out="$(report "$tmp/root" "$tmp/feed.json" "$today" 2>&1)" || {
			printf '  FAIL %s (unexpected non-zero exit)\n    out: %s\n' "$name" "$out"; fail=1; return
		}
		case "$out" in
			*"$unwanted"*) printf '  FAIL %s\n    out must NOT contain: %q\n    got: %q\n' "$name" "$unwanted" "$out"; fail=1 ;;
			*) printf '  ok   %s\n' "$name" ;;
		esac
	}

	_feed '[{"cycle":"3.13","eol":"2029-10-31"},{"cycle":"3.12","eol":"2028-10-31"},{"cycle":"3.11","eol":"2027-10-31"},{"cycle":"3.10","eol":"2026-10-31"}]'

	# A supported line far from EOL is silent.
	_schema '{"properties":{"python_version":{"enum":["3.12","3.13"]}}}'
	_case "a healthy matrix reports nothing to decide" 0 "No published Python line needs a decision" "2026-09-09"

	# The finding that would have raised #1031 in March instead of September.
	_schema '{"properties":{"python_version":{"enum":["3.10","3.11"]}}}'
	_case "an undeprecated line inside the window raises" 0 "python 3.10 reaches upstream EOL on 2026-10-31" "2026-03-01"
	_case_absent "and a far-off line does not" "python 3.11 reaches upstream EOL" "2026-03-01"

	# The threshold is the whole mechanism; a small one must go quiet again.
	EOL_WARN_DAYS=10
	_case "outside the threshold it stays quiet" 0 "No published Python line needs a decision" "2026-03-01"
	unset EOL_WARN_DAYS

	# The deprecation block belongs UNDER python_version. One a level up is a
	# no-op that must not be mistaken for a handled deprecation.
	_schema '{"properties":{"python_version":{"enum":["3.10","3.11"]},"x-leoflow-python-deprecations":{"3.10":{"eol":"2026-10-31","remove_after":"2026-10-31"}}}}'
	_case "a misplaced deprecation block still alerts" 0 "python 3.10 reaches upstream EOL" "2026-09-09"

	_schema '{"properties":{"python_version":{"enum":["3.10","3.11"],"x-leoflow-python-deprecations":{"3.10":{"eol":"2026-10-31","remove_after":"2026-10-31"}}}}}'
	_case "a correctly deprecated line is only a notice" 0 "No published Python line needs a decision" "2026-09-09"
	_case "the deprecation that never happened alerts" 0 "past its own removal date" "2026-12-01"

	# A recorded EOL that disagrees with upstream means the CHANGELOG is wrong.
	_schema '{"properties":{"python_version":{"enum":["3.10","3.11"],"x-leoflow-python-deprecations":{"3.10":{"eol":"2026-11-30","remove_after":"2026-11-30"}}}}}'
	_case "a schema/upstream EOL mismatch alerts" 0 "the schema records eol" "2026-09-09"

	# We publish a line upstream has never heard of.
	_schema '{"properties":{"python_version":{"enum":["3.99"]}}}'
	_case "a version upstream does not list alerts" 0 "endoflife.date has no \`3.99\` cycle" "2026-09-09"

	# Its own failures are loud: this is the watchdog telling you it is blind.
	_schema '{"properties":{"python_version":{"enum":["3.11"]}}}'
	_feed '{"not":"a list"}'
	_case "a reshaped feed exits non-zero" 1 "did the API shape change?" "2026-09-09"
	_feed 'not json at all'
	_case "an unparseable feed exits non-zero" 1 "cannot read the endoflife.date feed" "2026-09-09"
	_feed '[{"cycle":"3.11","eol":"soon"}]'
	_case "a non-date eol exits non-zero" 1 "is not a YYYY-MM-DD date" "2026-09-09"
	_feed '[{"cycle":"3.11","eol":"2027-10-31"}]'
	_schema '{"properties":{}}'
	_case "an enum-less schema exits non-zero" 1 "declares no python_version enum" "2026-09-09"

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
feed="${1:-}"
if [ -z "$feed" ]; then
	feed="$(mktemp)"
	trap 'rm -f "${feed:-}"' EXIT
	fetch_feed "$feed"
fi
report "$PWD" "$feed" "${2:-}"
