#!/usr/bin/env bash
# release-verify-images.sh: proves every image a release's public pointers
# reference exists and carries this repository's release signature.
#
#   scripts/release-verify-images.sh v0.5.4            # server, mcp, migrate
#   scripts/release-verify-images.sh v0.5.4 --runtime  # plus every py3.N-v0.5.4 leg and py3.11-data-v0.5.4
#
# ADR 0071. The Helm chart references dexaflow-server, dexaflow-mcp and
# dexaflow-migrate by the release tag, and the moving `py3.N` runtime tags are
# re-pointed at `py3.N-<tag>`. Before ADR 0071 the chart was pushed about 30
# seconds after the tag, with no image built yet, and on v0.5.3 `helm upgrade`
# failed on ImagePullBackOff for over an hour (#1684). The `promote` job in
# release.yaml and the manual dispatch of helm-release.yaml both run this
# before moving anything, so a reordered job or a manual re-run cannot push a
# chart whose images are missing or unsigned.
#
# `cosign verify` fails on a missing image as well as on a missing or foreign
# signature, so one call per image answers both questions. The identity is
# pinned to release.yaml at a tag ref, the same pins verify-server-signature
# uses, so a signature from any other Sigstore identity does not count.
#
# Needs: cosign, jq, and a registry login that can read the images.
# REGISTRY_PREFIX defaults to ghcr.io/<owner of GITHUB_REPOSITORY>.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCHEMA="$ROOT/internal/domain/schemas/leoflow-yaml-schema.json"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# runtime_pythons: the Python lines a release publishes a runtime leg for, read
# from the schema enum the CLI enforces. check-python-runtime-matrix.sh keeps
# the release matrix equal to it, so this list cannot drift from what was built.
runtime_pythons() {
  jq -r '.. | objects | select(has("python_version")) | .python_version.enum? // empty | .[]' "$SCHEMA"
}

# image_refs <prefix> <tag> <with-runtime>: one reference per line.
image_refs() { # <prefix> <tag> <0|1>
  local prefix="$1" tag="$2" runtime="$3" py
  printf '%s\n' "$prefix/dexaflow-server:$tag" "$prefix/dexaflow-mcp:$tag" "$prefix/dexaflow-migrate:$tag"
  [ "$runtime" = 1 ] || return 0
  for py in $(runtime_pythons); do printf '%s\n' "$prefix/dexaflow-runtime:py$py-$tag"; done
  # The data variant (runtime/data/README.md): one leg, Python 3.11 only.
  printf '%s\n' "$prefix/dexaflow-runtime:py3.11-data-$tag"
}

self_test() {
  local fail=0 got
  _eq() { [ "$1" = "$2" ] || { printf 'FAIL: %s\n  got:  %s\n  want: %s\n' "$3" "$1" "$2"; fail=1; }; }
  got="$(image_refs ghcr.io/o v1.2.3 0 | tr '\n' ' ')"
  _eq "$got" "ghcr.io/o/dexaflow-server:v1.2.3 ghcr.io/o/dexaflow-mcp:v1.2.3 ghcr.io/o/dexaflow-migrate:v1.2.3 " "the chart's three images"
  got="$(image_refs ghcr.io/o v1.2.3 1 | grep -c '/dexaflow-runtime:py3\.[0-9]*-v1\.2\.3$')"
  _eq "$got" "$(runtime_pythons | wc -l | tr -d ' ')" "one runtime leg per Python line in the schema"
  got="$(image_refs ghcr.io/o v1.2.3 1 | grep -c '/dexaflow-runtime:py3\.11-data-v1\.2\.3$')"
  _eq "$got" "1" "the data variant's one leg"
  got="$(image_refs ghcr.io/o v1.2.3 0 | grep -c data || true)"
  _eq "$got" "0" "no data leg without --runtime"
  [ "$(runtime_pythons | wc -l | tr -d ' ')" -ge 1 ] || { echo "FAIL: the schema enum yielded no Python line"; fail=1; }
  [ "$fail" = 0 ] && echo "release-verify-images self-test: OK"
  return "$fail"
}

main() {
  local tag="" runtime=0 arg prefix repo ref failed=0
  for arg in "$@"; do
    case "$arg" in
      --self-test) self_test; exit $? ;;
      --runtime)   runtime=1 ;;
      -*)          die "unknown flag: $arg" ;;
      *)           tag="$arg" ;;
    esac
  done
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] || die "usage: release-verify-images.sh vX.Y.Z[-rc.N] [--runtime]"
  repo="${GITHUB_REPOSITORY:?set GITHUB_REPOSITORY}"
  prefix="${REGISTRY_PREFIX:-ghcr.io/${repo%%/*}}"
  for ref in $(image_refs "$prefix" "$tag" "$runtime"); do
    if "$ROOT/scripts/cosign-retry.sh" verify \
        --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
        --certificate-identity-regexp "^https://github\.com/${repo}/\.github/workflows/release\.yaml@refs/tags/" \
        "$ref" >/dev/null; then
      echo "verified $ref"
    else
      echo "::error::$ref is missing or not signed by this repository's release workflow"
      failed=1
    fi
  done
  return "$failed"
}

main "$@"
