#!/usr/bin/env bash
# release-image-plan.sh: decides whether a release tag builds its container
# images or publishes the ones its last green rc validated (ADR 0109).
#
#   scripts/release-image-plan.sh v0.6.0       # prints mode=, rc=, reason=
#   scripts/release-image-plan.sh --self-test  # pure logic, no network
#
# An rc always builds. A GA promotes the images of the newest rc of the same
# version whose Release run ended green, when the commits of the two tags
# differ only in paths no image is built from (image_input_free). Anything else
# means the rc did not validate the GA's code, so the GA builds as before. The
# tag annotation line "Rebuild-images: true" (cut-release.sh --rebuild-images)
# forces the build.
#
# The green rc tags come from the Actions API, or from IMAGE_PLAN_GREEN_RCS
# (newline separated) when set, which is how a test or a dry run feeds them.
# The changed paths come from `git diff` between the two tags, so the checkout
# needs the tags and their history (fetch-depth: 0).
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

is_ga_tag() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; }
is_rc_tag() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]]; }

# image_input_free <path>: true for a path no image is built from. Release
# metadata (the changelog, its fragments, the chart, the docs, the tests) is
# what a GA commit normally adds to its rc.
image_input_free() {
  case "$1" in
    CHANGELOG.md | .changes/* | helm/* | test/* | website/*) return 0 ;;
    */*) return 1 ;;
    *.md) return 0 ;;
  esac
  return 1
}

# newest_rc <ga> <rc-tags>: the newest rc of <ga> in the list, or nothing.
newest_rc() {
  local ga="$1" list="$2"
  printf '%s\n' "$list" | grep -E "^${ga//./\\.}-rc\.[0-9]+$" | sort -V | tail -n 1 || true
}

# plan <tag> <green-rc-tags> <rebuild 0|1> <changed-paths>: prints mode=build
# or mode=promote, rc=<tag or empty> and reason=<text>. The changed paths are
# those between the chosen rc and the tag; the caller computes them after a
# first call names the rc.
plan() {
  local tag="$1" green="$2" rebuild="$3" changed="$4" rc p bad=()
  if is_rc_tag "$tag"; then
    printf 'mode=build\nrc=\nreason=%s is a release candidate\n' "$tag"; return 0
  fi
  is_ga_tag "$tag" || { echo "not a release tag: $tag" >&2; return 1; }
  if [ "$rebuild" = 1 ]; then
    printf 'mode=build\nrc=\nreason=the tag asks for a rebuild (Rebuild-images: true)\n'; return 0
  fi
  rc="$(newest_rc "$tag" "$green")"
  if [ -z "$rc" ]; then
    printf 'mode=build\nrc=\nreason=no rc of %s has a green Release run\n' "$tag"; return 0
  fi
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    image_input_free "$p" || bad+=("$p")
  done <<<"$changed"
  if [ "${#bad[@]}" -gt 0 ]; then
    printf 'mode=build\nrc=%s\nreason=%s changes image inputs since %s: %s\n' \
      "$rc" "$tag" "$rc" "$(printf '%s ' "${bad[@]:0:5}" | sed 's/ $//')"
    return 0
  fi
  printf 'mode=promote\nrc=%s\nreason=%s changes only release metadata since %s\n' "$rc" "$tag" "$rc"
}

self_test() {
  local fail=0
  _eq() { [ "$1" = "$2" ] || { printf 'FAIL: %s\n  got:  %s\n  want: %s\n' "$3" "$1" "$2"; fail=1; }; }
  _field() { sed -n "s/^$1=//p"; }
  local green=$'v0.6.0-rc.1\nv0.6.0-rc.2\nv0.5.12-rc.1\nv0.6.1-rc.1'
  local meta=$'CHANGELOG.md\nhelm/dexaflow/Chart.yaml\nhelm/dexaflow/README.md\ntest/release/results/v0.6.0-rc.2.md\n.changes/unreleased/x.yaml\nwebsite/content/x.md\nREADME.md'

  _eq "$(plan v0.6.0-rc.3 "$green" 0 '' | _field mode)" build "an rc builds"
  _eq "$(plan v0.6.0 "$green" 0 "$meta" | _field mode)" promote "a GA with only release metadata promotes"
  _eq "$(plan v0.6.0 "$green" 0 "$meta" | _field rc)" v0.6.0-rc.2 "from the newest green rc of its version"
  _eq "$(plan v0.6.0 "$green" 0 '' | _field mode)" promote "an empty diff promotes"
  _eq "$(plan v0.6.0 "$green" 1 "$meta" | _field mode)" build "Rebuild-images forces a build"
  _eq "$(plan v0.6.0 $'v0.5.12-rc.1\nv0.6.1-rc.1' 0 '' | _field mode)" build "no green rc of this version builds"
  _eq "$(plan v0.6.0 $'v0.6.0-rc.9\nv0.6.0-rc.10' 0 '' | _field rc)" v0.6.0-rc.10 "rc numbers compare numerically"
  _eq "$(plan v0.6.0 $'v0.6.00-rc.1\nv0.6.0-rc.1x' 0 '' | _field mode)" build "tags of another shape do not count"
  _eq "$(plan v0.6.0 "$green" 0 $'CHANGELOG.md\ninternal/api/x.go' | _field mode)" build "a code change since the rc builds"
  _eq "$(plan v0.6.0 "$green" 0 $'deploy/Dockerfile.server' | _field reason)" \
    "v0.6.0 changes image inputs since v0.6.0-rc.2: deploy/Dockerfile.server" "the reason names the path"
  _eq "$(plan v0.6.0 "$green" 0 $'.github/workflows/release.yaml' | _field mode)" build "a workflow change builds"
  _eq "$(plan v0.6.0 "$green" 0 $'go.mod' | _field mode)" build "a dependency change builds"
  _eq "$(plan v0.6.0 "$green" 0 $'runtime/python/README.md' | _field mode)" build "a nested .md outside the allowlist builds"
  _eq "$(plan v0.6.0 "$green" 0 $'helmfile.yaml' | _field mode)" build "a root file that only starts like helm/ builds"
  plan v0.6 "$green" 0 '' >/dev/null 2>&1 && { echo "FAIL: a malformed tag is refused"; fail=1; }

  [ "$fail" = 0 ] && echo "release-image-plan self-test: OK"
  return "$fail"
}

# green_rcs <tag>: the rc tags of <tag>'s version whose Release run ended green.
green_rcs() {
  local tag="$1" rc
  if [ -n "${IMAGE_PLAN_GREEN_RCS+set}" ]; then
    printf '%s\n' "$IMAGE_PLAN_GREEN_RCS"; return 0
  fi
  : "${GITHUB_REPOSITORY:?set GITHUB_REPOSITORY or IMAGE_PLAN_GREEN_RCS}"
  for rc in $(git tag -l "${tag}-rc.*"); do
    # A tag push runs the workflow with the tag as head_branch.
    if [ "$(gh api "repos/${GITHUB_REPOSITORY}/actions/workflows/release.yaml/runs?event=push&branch=${rc}&per_page=20" \
      --jq '[.workflow_runs[] | select(.head_branch == "'"$rc"'" and .conclusion == "success")] | length')" -gt 0 ]; then
      printf '%s\n' "$rc"
    fi
  done
}

main() {
  case "${1:-}" in
    --self-test) self_test; exit $? ;;
    ''|-*) die "usage: release-image-plan.sh <tag> | --self-test" ;;
  esac
  local tag="$1" green rebuild=0 first rc changed=""
  if is_ga_tag "$tag" && git tag -l --format='%(contents)' "$tag" | grep -qx 'Rebuild-images: true'; then
    rebuild=1
  fi
  green="$(is_ga_tag "$tag" && [ "$rebuild" = 0 ] && green_rcs "$tag" || true)"
  first="$(plan "$tag" "$green" "$rebuild" '')"
  rc="$(printf '%s\n' "$first" | sed -n 's/^rc=//p')"
  if [ -n "$rc" ]; then
    changed="$(git diff --name-only "${rc}^{commit}" "${tag}^{commit}")"
  fi
  plan "$tag" "$green" "$rebuild" "$changed"
}

main "$@"
