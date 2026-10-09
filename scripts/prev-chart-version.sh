#!/usr/bin/env bash
# prev-chart-version.sh: the released chart an upgrade test should start from.
#
#   scripts/prev-chart-version.sh <head chart version> < tags
#   scripts/prev-chart-version.sh --self-test
#
# Reads the chart's registry tags, one per line, on stdin and prints the newest
# final release (no -rc or other pre-release suffix) of the same minor as the
# head chart. A release branch upgrades within its own minor: once main
# publishes 0.6.x, "the newest chart in the registry" would make release-0.5's
# upgrade test install 0.6.x and then downgrade it to 0.5.x, and the 0.5.x
# migrate image refuses a schema it does not ship (#1403).
#
# When the minor has no final release yet (main bumped to the next minor before
# its first GA), it falls back to the newest final release below that minor.
# Signature tags (sha256-...) and anything that is not X.Y.Z are ignored. Exits
# 1 with nothing printed when no tag qualifies.
set -euo pipefail

pick() {
  local head="$1" major minor finals same below
  head="${head%%-*}"
  head="${head%%+*}"
  [[ "$head" =~ ^([0-9]+)\.([0-9]+)\.[0-9]+$ ]] || { echo "prev-chart-version: bad head version: $1" >&2; return 2; }
  major="${BASH_REMATCH[1]}" minor="${BASH_REMATCH[2]}"
  finals="$(grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | sort -V || true)"
  same="$(grep -E "^${major}\.${minor}\." <<<"$finals" | tail -n1 || true)"
  if [[ -n "$same" ]]; then
    echo "$same"
    return 0
  fi
  below="$(awk -F. -v M="$major" -v m="$minor" '$1 < M || ($1 == M && $2 < m)' <<<"$finals" | tail -n1)"
  [[ -n "$below" ]] || return 1
  echo "$below"
}

self_test() {
  local tags fail=0 got
  tags=$'sha256-a1.sig\n0.4.8\n0.5.0-rc.1\n0.5.0\n0.5.1-rc.1\n0.5.1\n0.5.10\n0.6.0-rc.1\n0.6.0\nlatest'
  check() {
    local want="$1" head="$2" in="$3"
    got="$(pick "$head" <<<"$in" || true)"
    if [[ "$got" != "$want" ]]; then
      echo "FAIL: head $head: got '$got', want '$want'" >&2
      fail=1
    fi
  }
  check 0.5.10 0.5.2 "$tags"          # same minor, 0.6.x ignored, sorted by version not text
  check 0.5.10 0.5.2-rc.1 "$tags"     # a pre-release head still picks its minor
  check 0.6.0 0.6.1 "$tags"           # rc of the same minor never picked
  check 0.6.0 0.7.0 "$tags"           # no final in 0.7: newest below
  check 0.4.8 0.5.0 $'0.4.8\n0.5.0-rc.1' # only an rc in the minor: falls back
  check "" 0.3.0 "$tags"              # nothing in or below the minor: empty
  if pick bogus <<<"$tags" >/dev/null 2>&1; then
    echo "FAIL: a bad head version was accepted" >&2
    fail=1
  fi
  [[ "$fail" == 0 ]] && echo "prev-chart-version self-test: ok"
  return "$fail"
}

case "${1:-}" in
  --self-test) self_test ;;
  "" | -h | --help) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
  *) pick "$1" ;;
esac
