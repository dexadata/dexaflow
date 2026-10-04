#!/usr/bin/env bash
# release-gap.sh: lists what main carries that a release branch does not.
#
#   scripts/release-gap.sh 0.5.1            # gap between v0.5.0..origin/main and origin/release-0.5
#   scripts/release-gap.sh 0.5.1 --fetch    # refresh main, the branch and the tags first
#   scripts/release-gap.sh 0.5.1 --offline  # ignore milestones, judge by the skip file alone
#   scripts/release-gap.sh --self-test      # fixture repository, no network
#
# Under ADR 0062 every change lands on main first and reaches release-X.Y by
# cherry-pick. Nothing carries a change across on its own, so with several
# people merging in parallel the failure mode is a fix that is on main and
# silently missing from the patch release. This script makes that list
# explicit: every commit on main's first-parent line since vX.Y.0 must be
# either cherry-picked onto release-X.Y or skipped on purpose.
#
# A commit counts as cherry-picked when a commit on release-X.Y carries
# `(cherry picked from commit <full sha>)`, which `git cherry-pick -x` writes
# and a squash merge keeps in the body, or names its pull request as `(#N)` at
# the end of its SUBJECT. A `(#N)` in a body does not count: squash bodies list
# the commits of the branch, and those mention other pull requests in passing.
#
# A pull request merged with a merge commit shows on main's first-parent line
# as "Merge pull request #N from ...", and is judged by that number like a
# squash merge. The commits cut-release.sh makes itself ("release: prepare
# vX", "release: promote vX GA", "docs: publish vX at the documentation root")
# are release mechanics, not changes, and never count as missing.
#
# A commit that should not ship in the patch (an ADR, a feature held for the
# next minor) is skipped by a line in .github/release-skip.txt ON THE RELEASE
# BRANCH, so the decision is reviewed in a pull request to that branch:
#
#   #1307 ADR only, nothing to ship
#   0123abcd held for 0.6: new API surface
#
# The first field is a pull request number (#N) or a commit sha prefix of at
# least 7 characters; the rest of the line is the reason. A line without a
# reason, or with a first field of any other shape, is refused.
#
# Most holds need no skip line: every pull request to main carries the
# milestone of the release it is planned for (checked by the milestone guard
# workflow), and a commit whose pull request is milestoned for a later release
# than the one being cut (v0.5.2 while cutting 0.5.1) is held by that decision.
# A pull request without a milestone, or milestoned for this release or an
# earlier one, stays in the gap. Milestones are
# read with gh; --offline (or no gh) judges by the skip file alone.
#
# Exit status: 0 when nothing is missing, 1 when something is (the list goes to
# stdout, one "<short sha> <subject>" per line), 2 on a usage or git error.
# cut-release.sh runs it before every patch cut and refuses to cut on 1.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SKIP_FILE=".github/release-skip.txt"
REPO="dexadata/dexaflow"
OFFLINE=0

die() { printf 'release-gap: %s\n' "$*" >&2; exit 2; }

# pr_of: the pull request a subject names: "fix: x (#12)" -> "12" for a
# squash merge, "Merge pull request #12 from o/b" -> "12" for a merge commit.
pr_of() {
  printf '%s\n' "$1" | sed -nE -e 's/^Merge pull request #([0-9]+) from .*/\1/p' \
    -e 't' -e 's/.*\(#([0-9]+)\)[[:space:]]*$/\1/p'
}

# release_mechanics: true for the commits cut-release.sh lands on main itself.
release_mechanics() {
  printf '%s\n' "$1" | grep -qE '^(release: (prepare|promote) v[0-9]|docs: publish v[0-9][^ ]* at the documentation root)'
}

# milestone_of <pr>: the milestone title of a pull request, empty when it has
# none or cannot be read. A failed read is reported once on stderr: without it
# every held pull request would move into the gap with no explanation. The
# self-test replaces it.
MILESTONE_WARNED=0
milestone_of() {
  [ "$OFFLINE" = 1 ] && return 0
  local m
  if command -v gh >/dev/null && m="$(gh api "repos/$REPO/issues/$1" -q '.milestone.title // empty' 2>/dev/null)"; then
    printf '%s' "$m"; return 0
  fi
  if [ "$MILESTONE_WARNED" = 0 ]; then
    printf 'release-gap: cannot read milestones with gh (pull request #%s); commits held by a later milestone are listed as missing\n' "$1" >&2
    MILESTONE_WARNED=1
  fi
}

# held_for_later <milestone> <version>: true when the milestone names a
# release after <version>, so the commit was planned for it on purpose.
held_for_later() {
  local m="${1#v}" v="${2#v}"
  [[ "$m" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
  [ "$m" != "$v" ] && [ "$(printf '%s\n%s\n' "$m" "$v" | sort -V | tail -1)" = "$m" ]
}

# gap <repo> <version> <main-ref> <release-ref>: prints the commits main has
# since vX.Y.0 that the release branch lacks and no decision holds back.
gap() {
  local repo="$1" version="$2" main_ref="$3" rel_ref="$4" minor base
  minor="${version%.*}"; base="v$minor.0"
  git -C "$repo" rev-parse -q --verify "refs/tags/$base^{commit}" >/dev/null || die "tag $base not found"
  git -C "$repo" rev-parse -q --verify "$main_ref^{commit}" >/dev/null || die "$main_ref not found"
  git -C "$repo" rev-parse -q --verify "$rel_ref^{commit}" >/dev/null || die "$rel_ref not found"

  local picked prs skips
  picked="$(git -C "$repo" log --format=%B "$base..$rel_ref" | sed -nE 's/.*cherry picked from commit ([0-9a-f]{40}).*/\1/p' | sort -u)"
  prs="$(git -C "$repo" log --format=%s "$base..$rel_ref" | grep -oE '\(#[0-9]+\)' | tr -d '(#)' | sort -u)"
  skips="$(git -C "$repo" show "$rel_ref:$SKIP_FILE" 2>/dev/null | sed -e 's/#[^0-9].*$//' -e '/^[[:space:]]*$/d')" || skips=""

  local line key reason
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    key="${line%%[[:space:]]*}"; reason="${line#"$key"}"
    [[ "$key" =~ ^#[0-9]+$ || "$key" =~ ^[0-9a-f]{7,40}$ ]] || die "$SKIP_FILE: '$key' is neither #<pull request> nor a commit sha of 7 or more characters"
    [ -n "${reason//[[:space:]]/}" ] || die "$SKIP_FILE: '$key' has no reason"
  done <<<"$skips"

  local sha subject pr k
  while IFS=$'\t' read -r sha subject; do
    [ -n "$sha" ] || continue
    grep -qx "$sha" <<<"$picked" && continue
    release_mechanics "$subject" && continue
    pr="$(pr_of "$subject")"
    [ -n "$pr" ] && grep -qx "$pr" <<<"$prs" && continue
    local skipped=0
    while IFS= read -r line; do
      k="${line%%[[:space:]]*}"
      [ -n "$k" ] || continue
      if [ "$k" = "#$pr" ] || { [ "${k#\#}" = "$k" ] && [ "${sha#"$k"}" != "$sha" ]; }; then skipped=1; break; fi
    done <<<"$skips"
    [ "$skipped" = 1 ] && continue
    [ -n "$pr" ] && held_for_later "$(milestone_of "$pr")" "$version" && continue
    printf '%s %s\n' "${sha:0:8}" "$subject"
  done < <(git -C "$repo" log --first-parent --reverse --format='%H%x09%s' "$base..$main_ref")
}

self_test() {
  local fail=0 tmp out
  _eq() { [ "$1" = "$2" ] || { printf 'FAIL: %s\n  got:  %s\n  want: %s\n' "$3" "$1" "$2"; fail=1; }; }
  _eq "$(pr_of 'fix(x): y (#1355)')" "1355" "pr_of reads the squash suffix"
  _eq "$(pr_of 'fix(x): see #12 for context')" "" "pr_of ignores a mention that is not the suffix"
  _eq "$(pr_of 'Merge pull request #77 from o/b')" "77" "pr_of reads a merge commit"
  _rm() { if release_mechanics "$1"; then echo yes; else echo no; fi; }
  _eq "$(_rm 'release: prepare v0.9.1-rc.1')" "yes" "a prepare commit is release mechanics"
  _eq "$(_rm 'release: promote v0.9.1 GA')" "yes" "a promote commit is release mechanics"
  _eq "$(_rm 'docs: publish v0.9.1 at the documentation root (#9)')" "yes" "the docs promotion is release mechanics"
  _eq "$(_rm 'fix(release): prepare step')" "no" "an ordinary fix is not"
  _held() { if held_for_later "$1" "$2"; then echo held; else echo ships; fi; }
  _eq "$(_held v0.5.2 0.5.1)"  "held"  "a milestone for the next patch holds the commit"
  _eq "$(_held v0.6.0 0.5.1)"  "held"  "a milestone for the next minor holds it"
  _eq "$(_held v0.5.10 0.5.9)" "held"  "versions compare as numbers, not text"
  _eq "$(_held v0.5.1 0.5.1)"  "ships" "a milestone for this release ships it"
  _eq "$(_held v0.5.1 0.5.2)"  "ships" "a milestone for an earlier patch must already be there"
  _eq "$(_held "" 0.5.1)"      "ships" "no milestone ships it (the gap shows it)"
  # Milestones come from a table here, not from GitHub.
  milestone_of() { case "$1" in 4) echo v0.9.2 ;; 2) echo v0.9.1 ;; esac; }

  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' RETURN
  _g() { git -C "$tmp" -c user.name=t -c user.email=t@t -c commit.gpgsign=false "$@" >/dev/null 2>&1; }
  _c() { echo "$1" >"$tmp/$1"; _g add "$1"; _g commit -qm "$2"; }
  _g init -q -b main
  _c base "release: v0.9.0"; _g tag v0.9.0
  _c a "fix: a (#1)"; local a; a="$(git -C "$tmp" rev-parse HEAD)"
  _c b "fix: b (#2)"; local b; b="$(git -C "$tmp" rev-parse HEAD)"
  _c c "docs: adr (#3)"
  _c d "feat: d (#4)"
  _c rc "release: prepare v0.9.1-rc.1"
  _g checkout -q -b feat-e; _c e "feat: e"; _g checkout -q main
  _g merge -q --no-ff -m "Merge pull request #5 from o/feat-e" feat-e
  _g checkout -q -b release-0.9 v0.9.0
  # #1 arrives by `cherry-pick -x` (the sha line), #2 by a squash merge whose
  # subject is the backport PR but whose body still names the original.
  _g cherry-pick -x "$a"
  echo b >"$tmp/b"; _g add b; _g commit -qm "[release-0.9] fix: b (#20)" -m "(cherry picked from commit $b)"
  # A squash body that mentions #3 in passing does not ship #3.
  _c notes "chore: notes (#21)"; _g commit -q --amend -m "chore: notes (#21)" -m "* docs: adr (#3)"
  _g checkout -q main

  out="$(gap "$tmp" 0.9.1 main release-0.9)"
  _eq "$(printf '%s\n' "$out" | sed 's/^[0-9a-f]* //')" "$(printf 'docs: adr (#3)\nMerge pull request #5 from o/feat-e')" \
    "cherry-picked commits, a PR milestoned for a later patch and release commits drop out; a merge commit stays"
  milestone_of() { :; }
  out="$(gap "$tmp" 0.9.1 main release-0.9)"
  _eq "$(printf '%s\n' "$out" | sed 's/^[0-9a-f]* //')" "$(printf 'docs: adr (#3)\nfeat: d (#4)\nMerge pull request #5 from o/feat-e')" "without milestones the rest is the gap"

  _g checkout -q release-0.9; mkdir -p "$tmp/.github"
  printf '# held on purpose\n#3 ADR only\n' >"$tmp/$SKIP_FILE"; _g add -A; _g commit -qm "skip"
  _g checkout -q main
  out="$(gap "$tmp" 0.9.1 main release-0.9)"
  _eq "$(printf '%s\n' "$out" | sed 's/^[0-9a-f]* //')" "$(printf 'feat: d (#4)\nMerge pull request #5 from o/feat-e')" "a skipped PR leaves the gap, a comment line is ignored"

  _g checkout -q release-0.9; printf '#4\n' >>"$tmp/$SKIP_FILE"; _g add -A; _g commit -qm "skip without reason"
  _g checkout -q main
  ( gap "$tmp" 0.9.1 main release-0.9 ) >/dev/null 2>&1; _eq "$?" "2" "a skip without a reason is refused"

  _g checkout -q release-0.9; printf '#3 ADR only\n0 typo\n' >"$tmp/$SKIP_FILE"; _g add -A; _g commit -qm "bad key"
  _g checkout -q main
  ( gap "$tmp" 0.9.1 main release-0.9 ) >/dev/null 2>&1; _eq "$?" "2" "a skip key that is neither #N nor a 7+ character sha is refused"

  _g checkout -q release-0.9; printf '#3 ADR only\n#4 held for 1.0\n%s held for 1.0\n' "$(git -C "$tmp" rev-parse --short=8 main)" >"$tmp/$SKIP_FILE"; _g add -A; _g commit -qm "skip by sha"
  _g checkout -q main
  _eq "$(gap "$tmp" 0.9.1 main release-0.9)" "" "a sha prefix skips too; an empty gap prints nothing"

  if [ "$fail" = 0 ]; then echo "self-test: PASS"; else echo "self-test: FAIL"; return 1; fi
}

main() {
  local version="" fetch=0 arg minor
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
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "usage: release-gap.sh <X.Y.Z> [--fetch] [--offline]"
  minor="${version%.*}"
  if [ "$fetch" = 1 ]; then
    git -C "$ROOT" fetch -q origin --tags "+refs/heads/main:refs/remotes/origin/main" \
      "+refs/heads/release-$minor:refs/remotes/origin/release-$minor" || die "cannot reach origin"
  fi
  local out
  out="$(gap "$ROOT" "$version" origin/main "origin/release-$minor")" || exit 2
  if [ -n "$out" ]; then
    printf '%s\n' "$out"
    printf 'release-gap: %s commit(s) on main are neither on release-%s nor in its %s\n' \
      "$(printf '%s\n' "$out" | wc -l | tr -d ' ')" "$minor" "$SKIP_FILE" >&2
    exit 1
  fi
  printf 'release-gap: release-%s carries everything on main since v%s.0\n' "$minor" "$minor" >&2
}

main "$@"
