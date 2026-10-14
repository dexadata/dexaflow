#!/usr/bin/env bash
# release-check-public.sh: proves a published GHCR image can be pulled
# without credentials.
#
#   scripts/release-check-public.sh <owner/name> <tag>
#   scripts/release-check-public.sh --self-test
#
# A package GHCR creates on its first push can start out private, and every
# check in release.yaml runs logged in, so a private image passes all of them
# while every user's pull fails with "denied". dexaflow-cli (#1708) is the
# image pipelines pull with no login at all. This asks the registry the way an
# anonymous client does: an anonymous pull token, then the manifest.
#
# On an rc a private image is a warning: the first rc of a new image is the
# run that creates the package, and nobody can make it public before it
# exists (RELEASING.md says when to). On a GA it fails the job, and the gate
# retracts the release.
#
# Needs: curl, python3. REGISTRY defaults to ghcr.io.
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 2; }

# anonymous_status <owner/name> <tag>: the HTTP status of an anonymous
# manifest read, or 000 when the registry hands out no anonymous token.
anonymous_status() {
	local repo="$1" tag="$2" registry="${REGISTRY:-ghcr.io}" token
	token="$(curl -fsS "https://${registry}/token?scope=repository:${repo}:pull" 2>/dev/null \
		| python3 -c 'import json,sys; print(json.load(sys.stdin).get("token", ""))' 2>/dev/null)" || token=""
	[ -n "$token" ] || { echo 000; return; }
	curl -s -o /dev/null -w '%{http_code}' \
		-H "Authorization: Bearer ${token}" \
		-H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json" \
		"https://${registry}/v2/${repo}/manifests/${tag}"
}

check() { # <owner/name> <tag>
	local repo="$1" tag="$2" status
	[[ "$repo" =~ ^[a-z0-9._-]+/[a-z0-9._-]+$ ]] || die "not an owner/name: $repo"
	[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] || die "not a release tag: $tag"
	status="$(anonymous_status "$repo" "$tag")"
	if [ "$status" = 200 ]; then
		echo "ghcr.io/${repo}:${tag} is public (anonymous pull: 200)"
		return 0
	fi
	if [[ "$tag" == *-rc.* ]]; then
		echo "::warning::ghcr.io/${repo}:${tag} cannot be pulled without credentials (anonymous pull: ${status}). Make the package public before the GA (RELEASING.md); the GA fails here otherwise."
		return 0
	fi
	echo "::error::ghcr.io/${repo}:${tag} cannot be pulled without credentials (anonymous pull: ${status}): the package is private or missing. Make it public (RELEASING.md), then re-run this job."
	return 1
}

self_test() {
	local fail=0 tmp out rc
	tmp="$(mktemp -d)"
	# shellcheck disable=SC2064 # expand tmp now.
	trap "rm -rf '$tmp'" EXIT
	cat >"$tmp/curl" <<'SH'
#!/bin/sh
for a in "$@"; do case "$a" in *token\?scope=*) [ "$FAKE_TOKEN" = none ] && exit 22; echo '{"token":"t"}'; exit 0 ;; esac; done
printf '%s' "$FAKE_STATUS"
SH
	chmod +x "$tmp/curl"
	_case() { # <name> <want rc> <want substring> <status> <token> <tag>
		rc=0
		out="$(PATH="$tmp:$PATH" FAKE_STATUS="$4" FAKE_TOKEN="$5" bash "${BASH_SOURCE[0]}" dexadata/dexaflow-cli "$6" 2>&1)" || rc=$?
		if [ "$rc" = "$2" ] && [[ "$out" == *"$3"* ]]; then printf '  ok   %s\n' "$1"
		else printf '  FAIL %s\n    rc %s (want %s): %s\n' "$1" "$rc" "$2" "$out"; fail=1; fi
	}
	_case "a public image passes" 0 "is public" 200 t v1.2.3
	_case "a private image fails a GA" 1 "::error::" 401 t v1.2.3
	_case "a missing manifest fails a GA" 1 "::error::" 404 t v1.2.3
	_case "no anonymous token fails a GA" 1 "anonymous pull: 000" 200 none v1.2.3
	_case "a private image warns on an rc" 0 "::warning::" 401 t v1.2.3-rc.1
	_case "a bad tag is refused" 2 "not a release tag" 200 t latest
	if [ "$fail" -eq 0 ]; then echo "release-check-public self-test: PASS"; return 0; fi
	echo "release-check-public self-test: FAIL"
	return 1
}

case "${1:-}" in
	--self-test) self_test ;;
	*) [ $# -eq 2 ] || die "usage: release-check-public.sh <owner/name> <tag> | --self-test"; check "$1" "$2" ;;
esac
