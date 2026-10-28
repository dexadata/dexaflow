#!/usr/bin/env bash
# release-promote-plan.sh: decides which public pointers a release tag may move.
#
#   scripts/release-promote-plan.sh v0.5.4         # prints latest=true|false and reason=...
#   scripts/release-promote-plan.sh --self-test    # pure logic, no network
#
# ADR 0071: a tag stages versioned artifacts, the gate verifies them, and the
# `promote` job in release.yaml is the only thing that moves a public pointer.
# The chart is pushed for every tag. The two pointers an unpinned user follows,
# the GitHub release marked Latest (what install.sh resolves without a pinned
# version) and the moving `dexaflow-runtime:py3.N` tags, move only for a GA
# that is not older than any published GA. Before ADR 0071 GoReleaser marked
# every GA Latest at publish time and every tag, rc included, moved py3.N
# before the gate ran (#1684).
#
# An rc never moves them. A patch of an older minor, cut from release-X.Y after
# a newer minor shipped, is published and pinnable but must not become what
# `curl | sh` installs; the docs root promotion in cut-release.sh applies the
# same "not older than what is served" rule.
#
# The published GA tags come from the releases API (drafts and prereleases
# excluded), or from PROMOTE_PUBLISHED_TAGS (newline separated) when set, which
# is how a dry run on a fork or a test feeds them in.
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# is_ga_tag <tag>: a final release tag, vX.Y.Z with no suffix.
is_ga_tag() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; }
# is_rc_tag <tag>: a release candidate, vX.Y.Z-rc.N.
is_rc_tag() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]]; }

# promote_plan <tag> <published-tags>: prints "latest=<bool>" then
# "reason=<text>". Fails on a tag that is neither a GA nor an rc, so a typo in
# a manual dispatch cannot mark something Latest.
promote_plan() { # <tag> <published-tags, newline separated>
  local tag="$1" published="${2:-}" newest
  if is_rc_tag "$tag"; then
    printf 'latest=false\nreason=%s is a release candidate\n' "$tag"; return 0
  fi
  is_ga_tag "$tag" || { echo "not a release tag: $tag" >&2; return 1; }
  # Only well-formed GA tags take part: a stray `v0.5.0-alpha.1` or a tag of
  # another shape must not decide what is newest.
  newest="$({ printf '%s\n' "$published"; printf '%s\n' "$tag"; } \
    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1)"
  if [ "$newest" = "$tag" ]; then
    printf 'latest=true\nreason=%s is the newest published GA\n' "$tag"
  else
    printf 'latest=false\nreason=%s is older than the published %s\n' "$tag" "$newest"
  fi
}

self_test() {
  local fail=0 out
  _eq() { [ "$1" = "$2" ] || { printf 'FAIL: %s\n  got:  %s\n  want: %s\n' "$3" "$1" "$2"; fail=1; }; }
  _latest() { promote_plan "$1" "$2" | sed -n 's/^latest=//p'; }

  _eq "$(_latest v0.5.4-rc.1 $'v0.5.3\nv0.5.2')" "false" "an rc never moves Latest"
  _eq "$(_latest v0.5.4-rc.1 '')" "false" "an rc with nothing published still does not"
  _eq "$(_latest v0.5.4 $'v0.5.3\nv0.5.2')" "true" "a GA newer than every published GA moves Latest"
  _eq "$(_latest v0.5.4 $'v0.5.4\nv0.5.3')" "true" "its own release in the list does not make it older"
  _eq "$(_latest v0.5.4 '')" "true" "the first GA moves Latest"
  _eq "$(_latest v0.5.9 $'v0.6.0\nv0.5.8')" "false" "a patch of an older minor never moves Latest"
  _eq "$(_latest v0.5.10 $'v0.5.9\nv0.5.8')" "true" "versions compare numerically, not as text"
  _eq "$(_latest v0.5.9 $'v0.5.10')" "false" "0.5.9 is older than 0.5.10"
  _eq "$(_latest v1.0.0 $'v0.9.12')" "true" "a new major is newest"
  _eq "$(_latest v0.5.4 $'v0.6.0-rc.1\nv0.5.3')" "true" "an unparsed rc in the list does not count"
  _eq "$(_latest v0.5.4 $'v0.5.5-alpha.1\nv0.5.3')" "true" "a tag of another shape does not count"
  out="$(promote_plan v0.5.9 $'v0.6.0' | sed -n 's/^reason=//p')"
  _eq "$out" "v0.5.9 is older than the published v0.6.0" "the reason names what is newer"
  promote_plan v0.5 '' >/dev/null 2>&1 && { echo "FAIL: a malformed tag is refused"; fail=1; }
  promote_plan 0.5.4 '' >/dev/null 2>&1 && { echo "FAIL: a tag without the v prefix is refused"; fail=1; }
  promote_plan v0.5.4-rc1 '' >/dev/null 2>&1 && { echo "FAIL: a malformed rc suffix is refused"; fail=1; }

  [ "$fail" = 0 ] && echo "release-promote-plan self-test: OK"
  return "$fail"
}

main() {
  case "${1:-}" in
    --self-test) self_test; exit $? ;;
    ''|-*) die "usage: release-promote-plan.sh <tag> | --self-test" ;;
  esac
  local tag="$1" published
  if [ -n "${PROMOTE_PUBLISHED_TAGS+set}" ]; then
    published="$PROMOTE_PUBLISHED_TAGS"
  else
    : "${GITHUB_REPOSITORY:?set GITHUB_REPOSITORY or PROMOTE_PUBLISHED_TAGS}"
    # The REST listing rather than `gh release list`, which goes through
    # GraphQL; a draft is only visible to a token that can push, so the
    # filter is explicit rather than trusted to the token's reach.
    published="$(gh api --paginate "repos/${GITHUB_REPOSITORY}/releases?per_page=100" \
      --jq '.[] | select(.draft | not) | select(.prerelease | not) | .tag_name')" || die "listing published releases failed"
  fi
  promote_plan "$tag" "$published"
}

main "$@"
