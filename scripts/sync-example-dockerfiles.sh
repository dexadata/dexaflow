#!/usr/bin/env bash
# sync-example-dockerfiles.sh — keep each examples/<dag>/Dockerfile aligned with
# its dexaflow.yaml (#318). Idempotent: re-running on an in-sync tree is a no-op.
#
# The generated Dockerfile follows the same template `leoflow lite` uses
# in-process (internal/cli/dev.go devDockerfile): FROM the matching task base,
# pip install declared dependencies, COPY the DAG source, set PYTHONPATH.
#
# "Same template" includes the pip line's SHAPE, not just its instructions: the
# specifiers are single-quoted and the option list is terminated with `--`, the
# same as shellArgs does. This drifted once already, and a drift gate that pins
# the old shape is a gate certifying the bug.
#
# These examples FROM the LOCAL base `leoflow-base:py<ver>` on purpose: they are
# the Lite learning track — `leoflow lite examples/<x>` builds that base locally,
# so the examples build and run offline, no registry needed. The real Pro pipeline
# is yaml-driven and FROMs the PUBLISHED base ghcr.io/dexadata/leoflow-runtime
# (internal/cli/compile_build.go resolveBaseImage) so it builds anywhere — that is
# the deliberate Lite/Pro split, documented in docs/deploy.md.
#
# Usage:
#   scripts/sync-example-dockerfiles.sh                # write/overwrite all
#   scripts/sync-example-dockerfiles.sh --check        # CI mode: exit non-zero on drift
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

mode="${1:-write}"
[ "$mode" = "--check" ] && mode="check"

# sq_escape renders a value for a single-quoted shell word, the same way
# shellQuote does in-process: each embedded quote closes the string, escapes a
# literal quote, and reopens it. Verified to produce exactly one argv element
# for a PEP 508 marker and for a value engineered to break out.
sq_escape() { printf '%s' "$1" | sed "s/'/'\\\\''/g"; }

gen_dockerfile() {
  local yaml="$1"
  local py
  py="$(awk -F'[ "]+' '/^python_version:/ {print $2; exit}' "$yaml")"
  py="${py:-3.11}"

  # Collect declared pip deps. Each line under `dependencies:` looks like
  # `  - package==version`; we take everything after the leading `- `.
  local deps_args=""
  if awk '/^dependencies:/ {found=1; next} found && /^[^ ]/ {exit} found && /- / {sub(/^[ ]*-[ ]*/, "", $0); print}' "$yaml" \
       | grep -q .; then
    while IFS= read -r dep; do
      # Single quotes and a `--` terminator, matching devDockerfile's shellArgs
      # (#1064): an entry beginning with a dash is a package name, not an option
      # to pip, and `--dry-run` slipping through builds green with the package
      # absent.
      #
      # The embedded-quote escaping matters and a plain "'$dep'" does not have
      # it. A PEP 508 marker legitimately carries single quotes
      # (`requests; python_version < '3.9'`) and would re-concatenate into one
      # malformed argv element, while an odd quote count escapes the quoting
      # entirely: a dep of `a'; touch /pwned; '` renders `'a'; touch /pwned; ''`,
      # which runs at docker build time. Only a committed example can reach this,
      # so it is a PR-review concern rather than a user-facing one, but claiming
      # parity with shellArgs while not having it is how it would stay unnoticed.
      # A carriage return only: `read` hands us one line at a time, so a bare
      # newline cannot arrive here, but a CRLF yaml leaves the CR on the end and
      # Docker ends the instruction on it. Checking for the newline too would be
      # a branch nothing can reach.
      if [ "${dep#*$'\r'}" != "$dep" ]; then
        echo "sync-example-dockerfiles: dependency in $yaml contains a carriage return, which ends the RUN instruction: $(printf %q "$dep")" >&2
        return 1
      fi
      deps_args+=" '$(sq_escape "$dep")'"
    done < <(awk '/^dependencies:/ {found=1; next} found && /^[^ ]/ {exit} found && /- / {sub(/^[ ]*-[ ]*/, "", $0); print}' "$yaml")
  fi

  cat <<EOF
# Standard DAG image (#318). Built by 'leoflow compile --build' or by hand:
#   docker build -t my-registry/$(basename "$(dirname "$yaml")"):<tag> .
# Synthesized by scripts/sync-example-dockerfiles.sh from dexaflow.yaml; do
# not hand-edit — re-run the script after changing python_version or
# dependencies, or CI's drift check fails.
FROM leoflow-base:py${py}
EOF
  if [ -n "$deps_args" ]; then
    printf 'RUN pip install --no-cache-dir --%s\n' "$deps_args"
  fi
  cat <<EOF
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
EOF
}

drift=0
# Find every example at any depth, so category folders (e.g. examples/gcp/<name>/)
# are covered alongside flat examples/<name>/.
while IFS= read -r yaml; do
  dir="$(dirname "$yaml")"
  dockerfile="$dir/Dockerfile"
  expected="$(gen_dockerfile "$yaml")"
  if [ "$mode" = "check" ]; then
    actual="$(cat "$dockerfile" 2>/dev/null || true)"
    if [ "$actual" != "$expected" ]; then
      echo "::error::$(realpath --relative-to=. "$dockerfile") is out of sync with dexaflow.yaml" >&2
      drift=1
    fi
  else
    printf '%s\n' "$expected" > "$dockerfile"
  fi
done < <(find examples -name dexaflow.yaml | sort)

if [ "$mode" = "check" ] && [ "$drift" -ne 0 ]; then
  echo "" >&2
  echo "Fix: run scripts/sync-example-dockerfiles.sh and commit the result." >&2
  exit 1
fi
