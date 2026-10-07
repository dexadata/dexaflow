#!/usr/bin/env bash
# docs-gap.sh: lists the user-facing changes a release would ship without docs.
#
#   scripts/docs-gap.sh 0.5.2            # changes on origin/release-0.5 since v0.5.1
#   scripts/docs-gap.sh 0.6.0            # changes on origin/main since v0.5.0
#   scripts/docs-gap.sh 0.5.2 --fetch    # refresh the branch and the tags first
#   scripts/docs-gap.sh 0.5.2 --offline  # no gh: judge by the skip file alone
#   scripts/docs-gap.sh --self-test      # fixture repository, no network
#
# The docs guard (scripts/check-docs-updated.sh) judges one pull request when it
# is opened. v0.5.1 showed what it cannot see: a change can land without docs
# under a `skip-docs` label nobody had to justify, before the guard covered its
# paths at all, or with the promise of a docs PR that never came. Nothing looked
# again before the tag, so server settings, API behaviour and migrations
# reached the CHANGELOG and not the docs site. This script is that second look,
# over everything the release ships, and cut-release.sh refuses to cut on it.
#
# Every commit on the base branch's first-parent line since the previous GA tag
# reachable from it (v0.5.1 for 0.5.2, v0.5.0 for 0.6.0 cut from main) is
# judged by the guard's own rule (user_facing() in check-docs-updated.sh): a
# commit that changes user-facing surface passes when it also edits
# website/content/, or when its pull request carries the `skip-docs` label and
# a `Skip-docs: <reason>` line in its description. For a cherry-pick, the
# label and the reason of the original pull request on main count as well.
#
# A change documented by a later pull request, or one held on purpose, is
# listed with its reason in .github/docs-skip.txt ON THE BASE BRANCH, so the
# decision is reviewed in a pull request to that branch:
#
#   #1352 documented in #1477 (API reference, pagination)
#   0123abcd internal only, the fragment kind is wrong
#
# The first field is a pull request number (#N) or a commit sha prefix of at
# least 7 characters; the rest of the line is the reason. A line without a
# reason, or with a first field of any other shape, is refused.
#
# Exit status: 0 when nothing is missing, 1 when something is (one
# "<short sha> <subject> [<why>]" per line on stdout), 2 on a usage or git error.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SKIP_FILE=".github/docs-skip.txt"
REPO="dexadata/dexaflow"
OFFLINE=0

# shellcheck source=check-docs-updated.sh
. "$ROOT/scripts/check-docs-updated.sh" || { printf 'docs-gap: cannot load scripts/check-docs-updated.sh\n' >&2; exit 2; }
set +e

die() { printf 'docs-gap: %s\n' "$*" >&2; exit 2; }

# pr_of and release_mechanics read subjects exactly as release-gap.sh does.
pr_of() {
  printf '%s\n' "$1" | sed -nE -e 's/^Merge pull request #([0-9]+) from .*/\1/p' \
    -e 't' -e 's/.*\(#([0-9]+)\)[[:space:]]*$/\1/p'
}
release_mechanics() {
  printf '%s\n' "$1" | grep -qE '^(release: (prepare|promote) v[0-9]|docs: publish v[0-9][^ ]* at the documentation root)'
}

base_of() { # <version>: the branch the version is cut from (ADR 0062)
  local v="${1#v}" patch minor
  v="${v%%-*}"; patch="${v##*.}"; minor="${v%.*}"
  if [ "$patch" = 0 ]; then printf 'main'; else printf 'release-%s' "$minor"; fi
}

# since_tag <repo> <ref> <version>: the newest GA tag reachable from <ref>
# that is older than <version>, so a re-run after the tag still judges the
# range the release shipped.
since_tag() {
  local repo="$1" ref="$2" v="${3#v}" t
  v="${v%%-*}"
  for t in $(git -C "$repo" tag --merged "$ref" --list 'v[0-9]*.[0-9]*.[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -rV); do
    [ "${t#v}" = "$v" ] && continue
    [ "$(printf '%s\n%s\n' "${t#v}" "$v" | sort -V | tail -1)" = "$v" ] && { printf '%s' "$t"; return 0; }
  done
}

# pr_meta <pr>: "<labels, comma separated>\t<skip-docs reason>" for a pull
# request, read with gh. The self-test replaces it.
META_WARNED=0
pr_meta() {
  [ "$OFFLINE" = 1 ] && return 0
  local out
  if command -v gh >/dev/null && out="$(gh api "repos/$REPO/pulls/$1" -q '[([.labels[].name] | join(",")), (.body // "")] | @json' 2>/dev/null)"; then
    local labels body
    labels="$(printf '%s' "$out" | jq -r '.[0]')"
    body="$(printf '%s' "$out" | jq -r '.[1]')"
    printf '%s\t%s' "$labels" "$(skip_docs_reason "$body")"
    return 0
  fi
  if [ "$META_WARNED" = 0 ]; then
    printf 'docs-gap: cannot read pull request #%s with gh; skip-docs labels are not counted\n' "$1" >&2
    META_WARNED=1
  fi
}

# origin_pr <repo> <sha>: the pull request on main a cherry-pick came from.
origin_pr() {
  local src
  src="$(git -C "$1" log -1 --format=%B "$2" | sed -nE 's/.*cherry picked from commit ([0-9a-f]{40}).*/\1/p' | head -1)"
  [ -n "$src" ] || return 0
  pr_of "$(git -C "$1" log -1 --format=%s "$src" 2>/dev/null)"
}

# gap <repo> <version> <ref>: prints the user-facing commits on <ref> since
# the previous GA tag that carry no docs and no justified skip.
gap() {
  local repo="$1" version="$2" ref="$3" since
  git -C "$repo" rev-parse -q --verify "$ref^{commit}" >/dev/null || die "$ref not found"
  since="$(since_tag "$repo" "$ref" "$version")"
  [ -n "$since" ] || die "no GA tag older than $version is reachable from $ref"

  local skips line key reason
  skips="$(git -C "$repo" show "$ref:$SKIP_FILE" 2>/dev/null | sed -E -e 's/^#([^0-9].*)?$//' -e '/^[[:space:]]*$/d')" || skips=""
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    key="${line%%[[:space:]]*}"; reason="${line#"$key"}"
    [[ "$key" =~ ^#[0-9]+$ || "$key" =~ ^[0-9a-f]{7,40}$ ]] || die "$SKIP_FILE: '$key' is neither #<pull request> nor a commit sha of 7 or more characters"
    [ -n "${reason//[[:space:]]/}" ] || die "$SKIP_FILE: '$key' has no reason"
  done <<<"$skips"

  local sha subject pr opr files surface k meta labels why skipped
  while IFS=$'\t' read -r sha subject; do
    [ -n "$sha" ] || continue
    release_mechanics "$subject" && continue
    files="$(git -C "$repo" diff --name-only "$sha^1" "$sha" 2>/dev/null)"
    surface="$(cd "$repo" && user_facing "$files" "$sha")"
    [ -n "$surface" ] || continue
    touches_docs "$files" && continue
    pr="$(pr_of "$subject")"
    opr="$(origin_pr "$repo" "$sha")"
    skipped=0
    while IFS= read -r line; do
      k="${line%%[[:space:]]*}"
      [ -n "$k" ] || continue
      if { [ -n "$pr" ] && [ "$k" = "#$pr" ]; } || { [ -n "$opr" ] && [ "$k" = "#$opr" ]; } ||
        { [ "${k#\#}" = "$k" ] && [ "${sha#"$k"}" != "$sha" ]; }; then skipped=1; break; fi
    done <<<"$skips"
    [ "$skipped" = 1 ] && continue
    why="no docs"
    for k in $pr $opr; do
      meta="$(pr_meta "$k")"
      labels="${meta%%$'\t'*}"; reason="${meta#*$'\t'}"
      [ "$meta" = "$labels" ] && reason=""
      if printf ',%s,' "$labels" | grep -q ',skip-docs,'; then
        if [ -n "$reason" ]; then skipped=1; break; fi
        why="skip-docs without a reason"
      fi
    done
    [ "$skipped" = 1 ] && continue
    printf '%s %s [%s: %s]\n' "${sha:0:8}" "$subject" "$why" "$(printf '%s' "$surface" | head -3 | paste -sd ',' - | sed 's/,/, /g')"
  done < <(git -C "$repo" log --first-parent --reverse --format='%H%x09%s' "$since..$ref")
}

self_test() {
  local fail=0 tmp out
  _eq() { [ "$1" = "$2" ] || { printf 'FAIL: %s\n  got:  %s\n  want: %s\n' "$3" "$1" "$2"; fail=1; }; }
  _eq "$(base_of 0.5.2)" "release-0.5" "a patch is cut from its release branch"
  _eq "$(base_of 0.6.0-rc.1)" "main" "a minor is cut from main"
  # Pull request metadata comes from a table here, not from GitHub.
  pr_meta() { case "$1" in 3) printf 'skip-docs\tinternal only' ;; 4) printf 'skip-docs\t' ;; 10) printf 'skip-docs\tcovered by the guide' ;; esac; }

  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' RETURN
  _g() { git -C "$tmp" -c user.name=t -c user.email=t@t -c commit.gpgsign=false "$@" >/dev/null 2>&1; }
  _c() { mkdir -p "$tmp/$(dirname "$1")"; printf '%s\n' "${3:-x$RANDOM}" >"$tmp/$1"; _g add -A; _g commit -qm "$2"; }
  _g init -q -b main
  _c README "release: v0.9.0"; _g tag v0.9.0
  _c internal/config/server.go "feat: a setting (#1)"
  _c internal/config/server.go "feat: a documented setting (#2)"; _c website/content/x.md "feat: a documented setting (#2)"
  _g reset -q --soft HEAD~2; _g commit -qm "feat: a documented setting (#2)"
  _c migrations/041_x.up.sql "perf: an index (#3)"
  _c internal/api/openapi.yaml "feat: an endpoint (#4)"
  _c .changes/unreleased/a.yaml "fix: changes behaviour (#5)" $'kind: Changed\nbody: x'
  _c .changes/unreleased/b.yaml "fix: restores behaviour (#6)" $'kind: Fixed\nbody: x'
  _c internal/scheduler/x.go "perf: internals (#7)"
  _c CHANGELOG.md "release: prepare v0.9.1-rc.1"
  _g tag v0.9.1-rc.1
  _c internal/cli/run.go "feat: a flag (#8)"

  out="$(gap "$tmp" 0.9.1 main | sed -E 's/^[0-9a-f]+ //; s/ \[.*//')"
  _eq "$out" "$(printf 'feat: a setting (#1)\nfeat: an endpoint (#4)\nfix: changes behaviour (#5)\nfeat: a flag (#8)')" \
    "surface without docs is listed; docs, a justified skip, a Fixed fragment, internals and release commits are not"
  _eq "$(gap "$tmp" 0.9.1 main | grep -c 'skip-docs without a reason')" "1" "a label without a reason is named as such"

  mkdir -p "$tmp/.github"
  printf '# held on purpose\n#\n#1 documented in #9\n%s flag documented later\n' "$(git -C "$tmp" rev-parse --short=8 HEAD)" >"$tmp/$SKIP_FILE"
  _g add -A; _g commit -qm "chore: docs skips (#11)"
  out="$(gap "$tmp" 0.9.1 main | sed -E 's/^[0-9a-f]+ //; s/ \[.*//')"
  _eq "$out" "$(printf 'feat: an endpoint (#4)\nfix: changes behaviour (#5)')" "the skip file holds by pull request and by sha; a comment line is ignored"

  # A cherry-pick counts the original pull request's justified skip.
  _g checkout -q -b side v0.9.0
  _c internal/config/server.go "feat: setting on main (#10)"; local src; src="$(git -C "$tmp" rev-parse HEAD)"
  _g checkout -q -b release-0.9 v0.9.0
  _g cherry-pick -x "$src"; _g commit -q --amend -m "[release-0.9] feat: setting on main (#12)" -m "(cherry picked from commit $src)"
  out="$(gap "$tmp" 0.9.1 release-0.9)"
  _eq "$out" "" "a pick inherits the original pull request's skip-docs reason"
  _g checkout -q main

  # The range starts at the newest older GA tag, not at an rc.
  _g tag v0.9.1 HEAD
  _c internal/config/server.go "feat: after the GA (#13)"
  out="$(gap "$tmp" 0.9.2 main | sed -E 's/^[0-9a-f]+ //; s/ \[.*//')"
  _eq "$out" "feat: after the GA (#13)" "the next patch starts after the previous GA"

  printf '#13\n' >"$tmp/$SKIP_FILE"; _g add -A; _g commit -qm "skip without reason"
  ( gap "$tmp" 0.9.2 main ) >/dev/null 2>&1; _eq "$?" "2" "a skip without a reason is refused"

  if [ "$fail" = 0 ]; then echo "self-test: PASS"; else echo "self-test: FAIL"; return 1; fi
}

main() {
  local version="" fetch=0 arg base
  for arg in "$@"; do
    case "$arg" in
      --self-test) self_test; exit $? ;;
      --fetch) fetch=1 ;;
      --offline) OFFLINE=1 ;;
      -*) die "unknown flag: $arg" ;;
      *) version="${arg#v}" ;;
    esac
  done
  version="${version%%-*}"
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "usage: docs-gap.sh <X.Y.Z> [--fetch] [--offline]"
  base="$(base_of "$version")"
  if [ "$fetch" = 1 ]; then
    git -C "$ROOT" fetch -q origin --tags "+refs/heads/$base:refs/remotes/origin/$base" || die "cannot reach origin"
  fi
  local out
  out="$(gap "$ROOT" "$version" "origin/$base")" || exit 2
  if [ -n "$out" ]; then
    printf '%s\n' "$out"
    printf 'docs-gap: %s user-facing change(s) on %s ship without docs, a justified skip-docs label, or a line in its %s\n' \
      "$(printf '%s\n' "$out" | wc -l | tr -d ' ')" "$base" "$SKIP_FILE" >&2
    exit 1
  fi
  printf 'docs-gap: every user-facing change on %s since the previous release is documented or skipped with a reason\n' "$base" >&2
}

main "$@"
