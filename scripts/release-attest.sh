#!/usr/bin/env bash
# release-attest.sh: the shell half of the `attest` job in release.yaml (#1562).
#
# A release already ships cosign signatures (checksums.txt, every image, the
# chart) and one syft SBOM per archive. This adds what an evaluator asks next:
# SLSA build provenance for the archives and the images, the archive SBOMs
# attested against the archive they describe, and an SPDX SBOM attached to each
# image as a cosign attestation. The GitHub attestations themselves are made by
# actions/attest in the workflow, because only the action can mint them; this
# script prepares its inputs and does the cosign part, so the logic is testable
# without a release.
#
#   release-attest.sh archives <dir> <tag>
#       Check that <dir> holds exactly one archive per published platform, each
#       with its SBOM, and nothing the workflow would not attest. A new platform
#       in .goreleaser.yaml fails here, so it cannot ship without its SBOM
#       attestation (the workflow names the four platforms one step each).
#   release-attest.sh images <tag> <subjects-file> <refs-file>
#       Resolve the digest of every image the release published (server, mcp, cli,
#       migrate, one runtime leg per Python line in the schema, and the py3.11
#       data leg). Writes
#       <subjects-file> in checksums format (`<hex>  <repository>`), which
#       actions/attest takes as `subject-checksums`, and <refs-file> with one
#       `<repository>@sha256:<hex>` per line.
#   release-attest.sh sbom <refs-file>
#       For each image: syft SPDX SBOM (linux/amd64), then
#       `cosign attest --type spdxjson` on the index digest, through the retry
#       wrapper the signing steps use.
#
# REGISTRY_PREFIX defaults to ghcr.io/<owner of GITHUB_REPOSITORY>.
# Needs: docker buildx, jq, syft, cosign, and a registry login that can push.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# The platforms .goreleaser.yaml builds (goos x goarch). The workflow has one
# SBOM attestation step per entry, in this order.
PLATFORMS="linux_amd64 linux_arm64 darwin_amd64 darwin_arm64"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

check_tag() {
	[[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "not a release tag: $1"
}

# runtime_pythons: the Python lines a release publishes a runtime leg for, read
# from the schema enum the CLI enforces; check-python-runtime-matrix.sh keeps
# the release matrix equal to it.
runtime_pythons() {
	jq -r '.. | objects | select(has("python_version")) | .python_version.enum? // empty | .[]' \
		"${RELEASE_ATTEST_SCHEMA:-$ROOT/internal/domain/schemas/leoflow-yaml-schema.json}"
}

image_refs() { # <prefix> <tag>
	local prefix="$1" tag="$2" py n=0
	printf '%s\n' "$prefix/dexaflow-server:$tag" "$prefix/dexaflow-mcp:$tag" "$prefix/dexaflow-cli:$tag" "$prefix/dexaflow-migrate:$tag"
	for py in $(runtime_pythons); do
		printf '%s\n' "$prefix/dexaflow-runtime:py$py-$tag"
		n=$((n + 1))
	done
	[ "$n" -ge 1 ] || die "no Python line found in the schema enum"
	# The data variant (runtime/data, RUNTIME_VARIANT=data): one leg, py3.11.
	printf '%s\n' "$prefix/dexaflow-runtime:py3.11-data-$tag"
}

cmd_archives() { # <dir> <tag>
	local dir="$1" tag="$2" ver p want got extra
	check_tag "$tag"
	ver="${tag#v}"
	[ -d "$dir" ] || die "no such directory: $dir"
	want=""
	for p in $PLATFORMS; do
		[ -f "$dir/dexaflow_${ver}_${p}.tar.gz" ] || die "missing archive dexaflow_${ver}_${p}.tar.gz"
		[ -f "$dir/dexaflow_${ver}_${p}.tar.gz.sbom.json" ] || die "missing SBOM dexaflow_${ver}_${p}.tar.gz.sbom.json"
		want="${want}dexaflow_${ver}_${p}.tar.gz"$'\n'
	done
	got="$(cd "$dir" && find . -maxdepth 1 -name '*.tar.gz' | sed 's#^\./##' | sort)"
	extra="$(comm -13 <(printf '%s' "$want" | sort) <(printf '%s\n' "$got"))"
	[ -z "$extra" ] || die "archives with no SBOM attestation step in release.yaml: $(echo "$extra" | tr '\n' ' ')(add the platform to PLATFORMS and a step to the attest job)"
	printf '%s\n' $PLATFORMS
}

# digest_of <ref>: the digest the tag points at (the index for a multi-arch image).
digest_of() {
	docker buildx imagetools inspect "$1" --format '{{json .Manifest}}' | jq -r .digest
}

cmd_images() { # <tag> <subjects-file> <refs-file>
	local tag="$1" subjects="$2" refs="$3" prefix ref repo digest list
	check_tag "$tag"
	if [ -n "${REGISTRY_PREFIX:-}" ]; then
		prefix="$REGISTRY_PREFIX"
	else
		prefix="ghcr.io/${GITHUB_REPOSITORY:?set GITHUB_REPOSITORY or REGISTRY_PREFIX}"
		prefix="${prefix%/*}"
	fi
	: >"$subjects"
	: >"$refs"
	list="$(image_refs "$prefix" "$tag")"
	for ref in $list; do
		digest="$(digest_of "$ref")" || die "cannot resolve $ref"
		[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "$ref resolved to '$digest', not a sha256 digest"
		repo="${ref%:*}"
		printf '%s  %s\n' "${digest#sha256:}" "$repo" >>"$subjects"
		printf '%s@%s\n' "$repo" "$digest" >>"$refs"
		echo "$ref -> $digest"
	done
}

cmd_sbom() { # <refs-file>
	local refs="$1" ref out n=0
	[ -s "$refs" ] || die "no image in $refs"
	while IFS= read -r ref; do
		[ -n "$ref" ] || continue
		[[ "$ref" =~ @sha256:[0-9a-f]{64}$ ]] || die "not pinned by digest: $ref"
		out="$(mktemp)"
		# One SBOM per index, for linux/amd64: the arm64 image is built from the
		# same Dockerfile and package set, and an attestation on the index is the
		# one `cosign verify-attestation <image>:<tag>` finds.
		syft scan "registry:$ref" --platform linux/amd64 -o "spdx-json=$out" >/dev/null
		"$ROOT/scripts/cosign-retry.sh" attest --yes --type spdxjson --predicate "$out" "$ref"
		rm -f "$out"
		echo "attested SBOM for $ref"
		n=$((n + 1))
	done <"$refs"
	echo "$n image SBOM attestation(s)"
}

self_test() {
	local fail=0 tmp out rc
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN
	_eq() { [ "$1" = "$2" ] || { printf '  FAIL %s\n    got:  %s\n    want: %s\n' "$3" "$1" "$2"; fail=1; return; }; printf '  ok   %s\n' "$3"; }
	_fails() { # <name> <substring> <cmd...>
		local name="$1" want="$2"
		shift 2
		rc=0
		out="$("$@" 2>&1)" || rc=$?
		if [ "$rc" = 0 ]; then printf '  FAIL %s (exit 0)\n    %s\n' "$name" "$out"; fail=1
		elif [[ "$out" != *"$want"* ]]; then printf '  FAIL %s\n    got: %s\n    want to contain: %s\n' "$name" "$out" "$want"; fail=1
		else printf '  ok   %s\n' "$name"; fi
	}

	# ── archives ──
	local d="$tmp/dist" p
	mkdir -p "$d"
	for p in $PLATFORMS; do
		: >"$d/dexaflow_1.2.3_${p}.tar.gz"
		: >"$d/dexaflow_1.2.3_${p}.tar.gz.sbom.json"
	done
	_eq "$( (cmd_archives "$d" v1.2.3) | tr '\n' ' ')" "linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 " "four platforms, each with its SBOM"
	rm "$d/dexaflow_1.2.3_darwin_arm64.tar.gz.sbom.json"
	_fails "a missing SBOM fails" "missing SBOM dexaflow_1.2.3_darwin_arm64" cmd_archives "$d" v1.2.3
	: >"$d/dexaflow_1.2.3_darwin_arm64.tar.gz.sbom.json"
	rm "$d/dexaflow_1.2.3_linux_arm64.tar.gz"
	_fails "a missing archive fails" "missing archive dexaflow_1.2.3_linux_arm64" cmd_archives "$d" v1.2.3
	: >"$d/dexaflow_1.2.3_linux_arm64.tar.gz"
	: >"$d/dexaflow_1.2.3_windows_amd64.tar.gz"
	_fails "a new platform fails until it has a step" "windows_amd64" cmd_archives "$d" v1.2.3
	rm "$d/dexaflow_1.2.3_windows_amd64.tar.gz"
	_fails "a malformed tag is refused" "not a release tag" cmd_archives "$d" 1.2.3
	_eq "$( (cmd_archives "$d" v1.2.3-rc.1 2>&1 || true) | head -1)" "error: missing archive dexaflow_1.2.3-rc.1_linux_amd64.tar.gz" "an rc tag keeps its suffix in the archive name"

	# ── images, with a fake docker that knows two digests ──
	local bin="$tmp/bin"
	mkdir -p "$bin"
	cat >"$bin/docker" <<'SH'
#!/bin/sh
# docker buildx imagetools inspect <ref> --format ...
ref="$4"
case "$ref" in
	*/dexaflow-server:*) d=1111111111111111111111111111111111111111111111111111111111111111 ;;
	*/dexaflow-unknown:*) exit 1 ;;
	*) d=2222222222222222222222222222222222222222222222222222222222222222 ;;
esac
[ -n "${FAKE_DIGEST:-}" ] && d="$FAKE_DIGEST"
printf '{"digest":"sha256:%s"}\n' "$d"
SH
	cat >"$bin/syft" <<'SH'
#!/bin/sh
printf 'syft %s\n' "$*" >>"$FAKE_LOG"
for a in "$@"; do case "$a" in spdx-json=*) printf '{"spdxVersion":"SPDX-2.3"}' >"${a#spdx-json=}" ;; esac; done
SH
	cat >"$bin/cosign" <<'SH'
#!/bin/sh
printf 'cosign %s\n' "$*" >>"$FAKE_LOG"
SH
	chmod +x "$bin/docker" "$bin/syft" "$bin/cosign"
	local schema="$tmp/schema.json"
	printf '{"properties":{"python_version":{"enum":["3.11","3.12"]}}}' >"$schema"

	out="$(PATH="$bin:$PATH" RELEASE_ATTEST_SCHEMA="$schema" REGISTRY_PREFIX=ghcr.io/o cmd_images v1.2.3 "$tmp/subj" "$tmp/refs" 2>&1)" || { echo "  FAIL images: $out"; fail=1; }
	_eq "$(awk '{print $2}' "$tmp/subj" | tr '\n' ' ')" "ghcr.io/o/dexaflow-server ghcr.io/o/dexaflow-mcp ghcr.io/o/dexaflow-cli ghcr.io/o/dexaflow-migrate ghcr.io/o/dexaflow-runtime ghcr.io/o/dexaflow-runtime ghcr.io/o/dexaflow-runtime " "every published image is a subject, one runtime leg per schema Python plus the data leg"
	_eq "$(head -1 "$tmp/subj")" "1111111111111111111111111111111111111111111111111111111111111111  ghcr.io/o/dexaflow-server" "subjects file is in checksums format"
	_eq "$(head -1 "$tmp/refs")" "ghcr.io/o/dexaflow-server@sha256:1111111111111111111111111111111111111111111111111111111111111111" "refs are pinned by digest"
	_eq "$(sed -n 5p "$tmp/refs")" "ghcr.io/o/dexaflow-runtime@sha256:2222222222222222222222222222222222222222222222222222222222222222" "runtime refs drop the tag, keep the digest"
	_fails "an unresolvable digest fails" "not a sha256 digest" env PATH="$bin:$PATH" RELEASE_ATTEST_SCHEMA="$schema" REGISTRY_PREFIX=ghcr.io/o FAKE_DIGEST=null bash "$ROOT/scripts/release-attest.sh" images v1.2.3 "$tmp/s2" "$tmp/r2"
	printf '{"properties":{"python_version":{"type":"string"}}}' >"$tmp/empty.json"
	_fails "an empty schema enum fails" "no Python line" env PATH="$bin:$PATH" RELEASE_ATTEST_SCHEMA="$tmp/empty.json" REGISTRY_PREFIX=ghcr.io/o bash "$ROOT/scripts/release-attest.sh" images v1.2.3 "$tmp/s3" "$tmp/r3"

	# ── sbom ──
	export FAKE_LOG="$tmp/log"
	: >"$FAKE_LOG"
	out="$(PATH="$bin:$PATH" COSIGN_RETRY_ATTEMPTS=1 cmd_sbom "$tmp/refs" 2>&1)" || { echo "  FAIL sbom: $out"; fail=1; }
	_eq "$(grep -c '^cosign attest --yes --type spdxjson --predicate ' "$FAKE_LOG")" "7" "one cosign spdxjson attestation per image"
	_eq "$(grep -c '^syft scan registry:ghcr.io/o/[a-z-]*@sha256:[0-9a-f]\{64\} --platform linux/amd64 -o spdx-json=' "$FAKE_LOG")" "7" "syft scans each image by digest"
	_eq "$(grep '^cosign' "$FAKE_LOG" | head -1 | awk '{print $NF}')" "ghcr.io/o/dexaflow-server@sha256:1111111111111111111111111111111111111111111111111111111111111111" "cosign attests the digest, not the tag"
	printf 'ghcr.io/o/dexaflow-server:v1.2.3\n' >"$tmp/tagref"
	_fails "a tag-only ref is refused" "not pinned by digest" env PATH="$bin:$PATH" bash "$ROOT/scripts/release-attest.sh" sbom "$tmp/tagref"
	: >"$tmp/none"
	_fails "an empty refs file is refused" "no image in" env PATH="$bin:$PATH" bash "$ROOT/scripts/release-attest.sh" sbom "$tmp/none"

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

case "${1:-}" in
	--self-test) self_test ;;
	archives) [ $# -eq 3 ] || die "usage: release-attest.sh archives <dir> <tag>"; cmd_archives "$2" "$3" ;;
	images) [ $# -eq 4 ] || die "usage: release-attest.sh images <tag> <subjects-file> <refs-file>"; cmd_images "$2" "$3" "$4" ;;
	sbom) [ $# -eq 2 ] || die "usage: release-attest.sh sbom <refs-file>"; cmd_sbom "$2" ;;
	*) die "usage: release-attest.sh {archives <dir> <tag> | images <tag> <subjects> <refs> | sbom <refs> | --self-test}" ;;
esac
