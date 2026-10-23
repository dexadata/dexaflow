#!/usr/bin/env bash
# Refresh the OpenAPI spec the Hugo site's Scalar reference reads.
#
# Scalar fetches the spec from the site root, so the source-of-truth spec
# is copied into website/static/ so Hugo publishes it at /openapi.yaml, next to
# the hand-written static Scalar page (website/static/api-reference.html, which
# loads it via data-url="openapi.yaml"). The content page reference/api.md links
# to that standalone page.
#
# The spec is hand-maintained (it drives oapi-codegen, not the reverse); this
# script only mirrors it, so the copy stays byte-identical to the source.
#
# Idempotent: reruns overwrite the copy.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SRC="${REPO_ROOT}/docs/api/openapi.yaml"
DST="${REPO_ROOT}/website/static/openapi.yaml"

if [ ! -f "${SRC}" ]; then
  echo "gen-openapi: source spec not found: ${SRC}" >&2
  exit 1
fi

cp "${SRC}" "${DST}"
echo "gen-openapi: copied openapi.yaml -> website/static/openapi.yaml"
