#!/usr/bin/env bash
# release-promote-images.sh: publishes every image of an rc under its GA tags
# without rebuilding them (ADR 0109).
#
#   scripts/release-promote-images.sh <rc-tag> <ga-tag>           # promote, sign
#   scripts/release-promote-images.sh <rc-tag> <ga-tag> --list    # print the plan
#   scripts/release-promote-images.sh <rc-tag> <ga-tag> --dry-run # promote nothing, check every source
#   scripts/release-promote-images.sh --self-test
#
# For each image the release publishes (pairs), the rc signature is verified
# first, then internal/tools/imagepromote writes the GA references with a new
# configuration and the rc's layers, and cosign signs every GA reference at
# the digest the tool printed. REGISTRY_PREFIX names the owner's registry path
# (ghcr.io/<owner>); IMAGEPROMOTE the tool binary (default: go run). With
# --dry-run the tool checks every source and builds every promoted image but
# writes nothing, so an rc rehearses its own GA. COSIGN_IDENTITY_REGEXP is the
# certificate identity the rc signatures must carry.
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# runtime_pythons: the Python lines a release publishes a runtime leg for, the
# schema enum release-verify-images.sh also reads.
runtime_pythons() {
  local root; root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  jq -r '.. | objects | select(has("python_version")) | .python_version.enum? // empty | .[]' \
    "$root/internal/domain/schemas/leoflow-yaml-schema.json" | sort -u
}

# pairs <prefix> <rc> <ga>: one line per source, "<source> <dest> [<dest>...]".
# GoReleaser publishes the server, MCP and CLI images as a list under vX and X
# and one image per architecture under vX-<arch> and X-<arch>; the migrate
# image is a list under vX and X; the runtime legs are lists under
# py3.N-vX and py3.11-data-vX. The pre-rename leoflow-* names get the same.
pairs() {
  local p="$1" rc="$2" ga="$3" v="${3#v}" img arch py
  for img in server mcp cli; do
    local legacy=""
    [ "$img" = server ] && legacy="$p/leoflow-server"
    printf '%s %s %s' "$p/dexaflow-$img:$rc" "$p/dexaflow-$img:$ga" "$p/dexaflow-$img:$v"
    [ -n "$legacy" ] && printf ' %s %s' "$legacy:$ga" "$legacy:$v"
    printf '\n'
    for arch in amd64 arm64; do
      printf '%s %s %s' "$p/dexaflow-$img:$rc-$arch" "$p/dexaflow-$img:$ga-$arch" "$p/dexaflow-$img:$v-$arch"
      [ -n "$legacy" ] && printf ' %s %s' "$legacy:$ga-$arch" "$legacy:$v-$arch"
      printf '\n'
    done
  done
  printf '%s %s %s %s %s\n' "$p/dexaflow-migrate:$rc" \
    "$p/dexaflow-migrate:$ga" "$p/dexaflow-migrate:$v" "$p/leoflow-migrate:$ga" "$p/leoflow-migrate:$v"
  for py in $(runtime_pythons); do
    printf '%s %s %s\n' "$p/dexaflow-runtime:py$py-$rc" "$p/dexaflow-runtime:py$py-$ga" "$p/leoflow-runtime:py$py-$ga"
  done
  printf '%s %s %s\n' "$p/dexaflow-runtime:py3.11-data-$rc" \
    "$p/dexaflow-runtime:py3.11-data-$ga" "$p/leoflow-runtime:py3.11-data-$ga"
}

self_test() {
  local fail=0 out n
  _eq() { [ "$1" = "$2" ] || { printf 'FAIL: %s\n  got:  %s\n  want: %s\n' "$3" "$1" "$2"; fail=1; }; }
  out="$(pairs ghcr.io/o v0.6.0-rc.2 v0.6.0)"
  _eq "$(printf '%s\n' "$out" | grep -c '^ghcr.io/o/dexaflow-server:v0.6.0-rc.2 ')" 1 "the server list"
  _eq "$(printf '%s\n' "$out" | grep '^ghcr.io/o/dexaflow-server:v0.6.0-rc.2 ')" \
    "ghcr.io/o/dexaflow-server:v0.6.0-rc.2 ghcr.io/o/dexaflow-server:v0.6.0 ghcr.io/o/dexaflow-server:0.6.0 ghcr.io/o/leoflow-server:v0.6.0 ghcr.io/o/leoflow-server:0.6.0" \
    "the server list goes to both names, with and without v"
  _eq "$(printf '%s\n' "$out" | grep '^ghcr.io/o/dexaflow-cli:v0.6.0-rc.2-arm64 ')" \
    "ghcr.io/o/dexaflow-cli:v0.6.0-rc.2-arm64 ghcr.io/o/dexaflow-cli:v0.6.0-arm64 ghcr.io/o/dexaflow-cli:0.6.0-arm64" \
    "the per-architecture CLI image"
  _eq "$(printf '%s\n' "$out" | grep -c 'leoflow-mcp\|leoflow-cli')" 0 "no legacy name for mcp and cli"
  _eq "$(printf '%s\n' "$out" | grep '^ghcr.io/o/dexaflow-migrate:')" \
    "ghcr.io/o/dexaflow-migrate:v0.6.0-rc.2 ghcr.io/o/dexaflow-migrate:v0.6.0 ghcr.io/o/dexaflow-migrate:0.6.0 ghcr.io/o/leoflow-migrate:v0.6.0 ghcr.io/o/leoflow-migrate:0.6.0" \
    "the migrate image"
  n="$(runtime_pythons | wc -l | tr -d ' ')"
  [ "$n" -ge 1 ] || { echo "FAIL: the schema enum yielded no Python line"; fail=1; }
  _eq "$(printf '%s\n' "$out" | grep -c '^ghcr.io/o/dexaflow-runtime:py3\.[0-9]*-v0\.6\.0-rc\.2 ')" "$n" "one runtime leg per Python line"
  _eq "$(printf '%s\n' "$out" | grep '^ghcr.io/o/dexaflow-runtime:py3.11-data-')" \
    "ghcr.io/o/dexaflow-runtime:py3.11-data-v0.6.0-rc.2 ghcr.io/o/dexaflow-runtime:py3.11-data-v0.6.0 ghcr.io/o/leoflow-runtime:py3.11-data-v0.6.0" \
    "the data leg"
  _eq "$(printf '%s\n' "$out" | wc -l | tr -d ' ')" "$((3 * 3 + 1 + n + 1))" "every image once"
  _eq "$(printf '%s\n' "$out" | awk '{print $1}' | grep -vc 'v0\.6\.0-rc\.2')" 0 "every source is the rc"
  _eq "$(printf '%s\n' "$out" | cut -d' ' -f2- | tr ' ' '\n' | grep -c 'rc')" 0 "no destination names the rc"
  [ "$fail" = 0 ] && echo "release-promote-images self-test: OK"
  return "$fail"
}

promote() { # <rc> <ga> <dry>
  local rc="$1" ga="$2" dry="$3" src rest dst out digest args
  : "${REGISTRY_PREFIX:?set REGISTRY_PREFIX (ghcr.io/<owner>)}"
  local tool=(go run ./internal/tools/imagepromote)
  [ -n "${IMAGEPROMOTE:-}" ] && tool=("$IMAGEPROMOTE")
  while read -r src rest; do
    if [ -n "${COSIGN_IDENTITY_REGEXP:-}" ]; then
      ./scripts/cosign-retry.sh verify \
        --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
        --certificate-identity-regexp "$COSIGN_IDENTITY_REGEXP" "$src" >/dev/null ||
        die "$src: the rc signature does not verify; refusing to promote it"
    fi
    args=(-release "$ga" -from "$src")
    for dst in $rest; do args+=(-to "$dst"); done
    [ "$dry" = 1 ] && args+=(-dry-run)
    out="$("${tool[@]}" "${args[@]}")" || die "promoting $src failed"
    printf '%s\n' "$out"
    [ "$dry" = 1 ] && continue
    while read -r dst digest; do
      ./scripts/cosign-retry.sh sign --yes "${dst}@${digest}"
    done <<<"$out"
    if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
      read -r dst digest <<<"$(printf '%s\n' "$out" | head -1)"
      printf -- "- %s -> %s (%s)\n" "$src" "$dst" "$digest" >>"$GITHUB_STEP_SUMMARY"
    fi
  done < <(pairs "$REGISTRY_PREFIX" "$rc" "$ga")
}

main() {
  case "${1:-}" in
    --self-test) self_test; exit $? ;;
    ''|-*) die "usage: release-promote-images.sh <rc-tag> <ga-tag> [--list|--dry-run] | --self-test" ;;
  esac
  [ "$#" -ge 2 ] || die "usage: release-promote-images.sh <rc-tag> <ga-tag> [--list|--dry-run]"
  local rc="$1" ga="$2" mode="${3:-}"
  [[ "$rc" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]] || die "not an rc tag: $rc"
  [[ "$ga" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "not a GA tag: $ga"
  [ "${rc%-rc.*}" = "$ga" ] || die "$rc is not an rc of $ga"
  case "$mode" in
    --list) pairs "${REGISTRY_PREFIX:-ghcr.io/OWNER}" "$rc" "$ga" ;;
    --dry-run) promote "$rc" "$ga" 1 ;;
    '') promote "$rc" "$ga" 0 ;;
    *) die "unknown flag: $mode" ;;
  esac
}

main "$@"
