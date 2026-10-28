#!/usr/bin/env bash
# setup-dexaflow.sh: the shell half of the setup-dexaflow composite action
# (action.yml next to this file).
#
# Pipelines used to install the CLI with `curl .../install.sh | sh` (#1708):
# an unpinned script from `main` piped into a shell, a version that followed
# /releases/latest unless someone remembered DEXAFLOW_VERSION, and a signature
# that was checked only when cosign happened to be installed. This script
# installs one pinned release for the runner's platform and refuses to install
# anything it could not verify.
#
#   setup-dexaflow.sh resolve-version <version input> <action ref>
#       Print the release tag to install (vX.Y.Z or vX.Y.Z-rc.N). The input
#       wins; without one, the ref the action was used at (`@v0.6.0`) is the
#       version. Nothing resolves to "latest": a pipeline that does not say
#       which CLI it runs gets a different one the day a release ships.
#   setup-dexaflow.sh platform <runner.os> <runner.arch>
#       Print the archive platform (linux_amd64, darwin_arm64, ...).
#   setup-dexaflow.sh install <tag> <platform> <dir>
#       Make <dir>/bin/dexaflow the CLI of <tag>. Downloads checksums.txt,
#       its signature and certificate, and the archive into <dir> unless they
#       are already there (the action caches them per version), then, on
#       every run, cached or not, verifies the cosign signature of
#       checksums.txt (when VERIFY_SIGNATURE=true) and the archive against
#       checksums.txt.
#
# Environment for `install`:
#   VERIFY_SIGNATURE    true (default) or false.
#   COSIGN              the cosign binary to run (default: cosign on PATH).
#   DEXAFLOW_RELEASES   base URL of the release downloads
#                       (default https://github.com/dexadata/dexaflow/releases/download).
#   DEXAFLOW_SIGNER     repository whose release.yaml signed the release
#                       (default dexadata/dexaflow).
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# tag_re: the release tags the project cuts (ADR 0037): GA and rc.
tag_re='^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$'

cmd_resolve_version() { # <input> <action ref>
	local in="${1:-}" ref="${2:-}" v
	if [ -n "$in" ]; then
		case "$in" in
			latest | stable | main | '*') die "version '$in' is not a release: pin one, for example 'version: v0.6.0'" ;;
		esac
		v="v${in#v}"
		[[ "$v" =~ $tag_re ]] || die "version '$in' is not a release tag (vX.Y.Z or vX.Y.Z-rc.N)"
		printf '%s\n' "$v"
		return
	fi
	if [[ "$ref" =~ $tag_re ]]; then
		printf '%s\n' "$ref"
		return
	fi
	die "set the version input (for example 'version: v0.6.0'): the action was used at '${ref:-a local path}', which is not a release tag"
}

cmd_platform() { # <runner.os> <runner.arch>
	local os arch
	case "${1:-}" in
		Linux) os=linux ;;
		macOS) os=darwin ;;
		*) die "unsupported runner OS '${1:-}': Dexaflow ships Linux and macOS binaries" ;;
	esac
	case "${2:-}" in
		X64) arch=amd64 ;;
		ARM64) arch=arm64 ;;
		*) die "unsupported runner architecture '${2:-}': Dexaflow ships amd64 and arm64 binaries" ;;
	esac
	printf '%s_%s\n' "$os" "$arch"
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

download() { # <url> <file>
	curl -fsSL --retry 3 --retry-delay 2 "$1" -o "$2.part" || { rm -f "$2.part"; return 1; }
	mv "$2.part" "$2"
}

# forget <dir>: drop everything in <dir> that a signature vouches for, so a
# directory that failed a check is downloaded again rather than trusted.
forget() {
	rm -f "$1/checksums.txt" "$1/checksums.txt.sig" "$1/checksums.txt.pem" "$1/cosign.log"
	rm -rf "${1:?}/bin"
}

# verify_signature <dir> <base url> <tag>: checks <dir>/checksums.txt against
# its keyless signature, on every run: a cached or leftover checksums.txt is
# only as trustworthy as whoever wrote the cache. The signature and the
# certificate are downloaded when <dir> does not hold them. The identity is
# exact, workflow file and v-prefixed tag included (the ref the release
# workflow ran at), as install.sh pins it, so a signature made by any other
# workflow, branch or tag does not verify. COSIGN names the binary to run
# (the action passes the pinned one it installed); the default is cosign on
# PATH.
verify_signature() {
	local dir="$1" base="$2" tag="$3" cosign="${COSIGN:-cosign}" identity f
	command -v "$cosign" >/dev/null 2>&1 || die "cosign ($cosign) is not installed, so the signature of checksums.txt cannot be checked (set verify-signature: false to skip it)"
	for f in checksums.txt.sig checksums.txt.pem; do
		[ -f "$dir/$f" ] && continue
		download "$base/$f" "$dir/$f" || { forget "$dir"; die "downloading $f for $tag failed; not installing an unsigned release"; }
	done
	identity="https://github.com/${DEXAFLOW_SIGNER:-dexadata/dexaflow}/.github/workflows/release.yaml@refs/tags/${tag}"
	if ! "$cosign" verify-blob \
		--certificate "$dir/checksums.txt.pem" \
		--signature "$dir/checksums.txt.sig" \
		--certificate-identity "$identity" \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		"$dir/checksums.txt" >"$dir/cosign.log" 2>&1; then
		cat "$dir/cosign.log" >&2
		forget "$dir"
		die "the signature of checksums.txt for $tag does not verify (expected signer $identity); nothing was installed"
	fi
	rm -f "$dir/cosign.log"
	echo "signature of checksums.txt verified ($identity)"
}

cmd_install() { # <tag> <platform> <dir>
	local tag="$1" platform="$2" dir="$3" base archive want got
	[[ "$tag" =~ $tag_re ]] || die "not a release tag: $tag"
	base="${DEXAFLOW_RELEASES:-https://github.com/dexadata/dexaflow/releases/download}/$tag"
	archive="dexaflow_${tag#v}_${platform}.tar.gz"
	mkdir -p "$dir"
	if [ ! -f "$dir/checksums.txt" ]; then
		download "$base/checksums.txt" "$dir/checksums.txt" || die "downloading checksums.txt for $tag failed (is $tag a published release?)"
	fi
	if [ "${VERIFY_SIGNATURE:-true}" = true ]; then
		verify_signature "$dir" "$base" "$tag"
	else
		echo "signature of checksums.txt not checked (verify-signature: false)"
	fi
	want="$(awk -v a="$archive" '$2 == a {print $1}' "$dir/checksums.txt")"
	[ -n "$want" ] || die "checksums.txt of $tag lists no $archive (the action supports releases from v0.5.0 on)"
	if [ ! -f "$dir/$archive" ]; then
		download "$base/$archive" "$dir/$archive" || die "downloading $archive failed"
	fi
	got="$(sha256_of "$dir/$archive")"
	if [ "$got" != "$want" ]; then
		rm -f "$dir/$archive"
		die "checksum mismatch for $archive (got $got, want $want); nothing was installed"
	fi
	rm -rf "${dir:?}/bin"
	mkdir -p "$dir/bin"
	tar -xzf "$dir/$archive" -C "$dir/bin" dexaflow || die "$archive holds no dexaflow binary"
	chmod 0755 "$dir/bin/dexaflow"
	echo "dexaflow $tag ($platform) installed in $dir/bin"
}

self_test() {
	local fail=0 tmp out rc bin rel
	tmp="$(mktemp -d)"
	# shellcheck disable=SC2064 # expand tmp now: the trap runs after it goes out of scope.
	trap "rm -rf '$tmp'" EXIT
	_eq() { if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else printf '  FAIL %s\n    got:  %s\n    want: %s\n' "$3" "$1" "$2"; fail=1; fi; }
	_fails() { # <name> <substring> <cmd...>
		local name="$1" want="$2"
		shift 2
		rc=0
		out="$("$@" 2>&1)" || rc=$?
		if [ "$rc" = 0 ]; then printf '  FAIL %s (exit 0)\n    %s\n' "$name" "$out"; fail=1
		elif [[ "$out" != *"$want"* ]]; then printf '  FAIL %s\n    got: %s\n    want to contain: %s\n' "$name" "$out" "$want"; fail=1
		else printf '  ok   %s\n' "$name"; fi
	}
	local self="${BASH_SOURCE[0]}"

	# ── resolve-version ──
	_eq "$(bash "$self" resolve-version v0.5.3 '')" "v0.5.3" "a tag is kept"
	_eq "$(bash "$self" resolve-version 0.5.3 '')" "v0.5.3" "a version gets its v"
	_eq "$(bash "$self" resolve-version v0.6.0-rc.2 main)" "v0.6.0-rc.2" "an rc is a release"
	_eq "$(bash "$self" resolve-version '' v0.6.0)" "v0.6.0" "no input: the action ref is the version"
	_eq "$(bash "$self" resolve-version v0.5.3 v0.6.0)" "v0.5.3" "the input wins over the action ref"
	_fails "no input and a branch ref" "set the version input" bash "$self" resolve-version '' main
	_fails "no input and a local action" "a local path" bash "$self" resolve-version '' ''
	_fails "no input and a SHA ref" "set the version input" bash "$self" resolve-version '' 3d3c42e5aac5ba805825da76410c181273ba90b1
	_fails "latest is refused" "pin one" bash "$self" resolve-version latest v0.6.0
	_fails "a partial version is refused" "not a release tag" bash "$self" resolve-version v0.5 ''

	# ── platform ──
	_eq "$(bash "$self" platform Linux X64)" "linux_amd64" "Linux X64"
	_eq "$(bash "$self" platform Linux ARM64)" "linux_arm64" "Linux ARM64"
	_eq "$(bash "$self" platform macOS ARM64)" "darwin_arm64" "macOS ARM64"
	_eq "$(bash "$self" platform macOS X64)" "darwin_amd64" "macOS X64"
	_fails "Windows is refused" "unsupported runner OS" bash "$self" platform Windows X64
	_fails "32-bit ARM is refused" "unsupported runner architecture" bash "$self" platform Linux ARM

	# ── install, against a fake release served from file:// ──
	rel="$tmp/rel/v1.2.3"
	mkdir -p "$rel" "$tmp/pkg"
	printf '#!/bin/sh\necho "dexaflow 1.2.3 (fake)"\n' >"$tmp/pkg/dexaflow"
	chmod +x "$tmp/pkg/dexaflow"
	tar -czf "$rel/dexaflow_1.2.3_linux_amd64.tar.gz" -C "$tmp/pkg" dexaflow
	(cd "$rel" && printf '%s  %s\n' "$(sha256_of dexaflow_1.2.3_linux_amd64.tar.gz)" dexaflow_1.2.3_linux_amd64.tar.gz >checksums.txt)
	printf 'sig' >"$rel/checksums.txt.sig"
	printf 'pem' >"$rel/checksums.txt.pem"
	bin="$tmp/fakebin"
	mkdir -p "$bin"
	cat >"$bin/cosign" <<'SH'
#!/bin/sh
printf 'cosign %s\n' "$*" >>"$FAKE_LOG"
exit "${FAKE_COSIGN_EXIT:-0}"
SH
	chmod +x "$bin/cosign"
	export FAKE_LOG="$tmp/cosign.log"
	local env_rel="DEXAFLOW_RELEASES=file://$tmp/rel"

	out="$(env "$env_rel" VERIFY_SIGNATURE=false bash "$self" install v1.2.3 linux_amd64 "$tmp/i1" 2>&1)" || { echo "  FAIL install without signature: $out"; fail=1; }
	_eq "$("$tmp/i1/bin/dexaflow" 2>&1)" "dexaflow 1.2.3 (fake)" "the archive's dexaflow is installed in <dir>/bin"
	_eq "$(cmp -s "$tmp/i1/checksums.txt" "$rel/checksums.txt" && echo same)" "same" "checksums.txt is kept for the next cached run"

	: >"$FAKE_LOG"
	out="$(env "$env_rel" PATH="$bin:$PATH" bash "$self" install v1.2.3 linux_amd64 "$tmp/i2" 2>&1)" || { echo "  FAIL install with signature: $out"; fail=1; }
	_eq "$(grep -c -- '--certificate-identity https://github.com/dexadata/dexaflow/.github/workflows/release.yaml@refs/tags/v1.2.3 ' "$FAKE_LOG")" "1" "the signature is checked against the exact release workflow and tag"
	_eq "$(grep -c -- '--certificate-oidc-issuer https://token.actions.githubusercontent.com' "$FAKE_LOG")" "1" "the signature is checked against GitHub's OIDC issuer"
	_eq "$([ -x "$tmp/i2/bin/dexaflow" ] && echo yes)" "yes" "a verified release is installed"

	_fails "a bad signature installs nothing" "does not verify" env "$env_rel" PATH="$bin:$PATH" FAKE_COSIGN_EXIT=1 bash "$self" install v1.2.3 linux_amd64 "$tmp/i3"
	_eq "$([ -e "$tmp/i3/bin/dexaflow" ] || [ -e "$tmp/i3/checksums.txt" ] || [ -e "$tmp/i3/checksums.txt.new" ] || echo none)" "none" "after a bad signature no binary and no checksums.txt remain"

	mkdir -p "$tmp/nocosign"
	ln -s "$(command -v curl)" "$tmp/nocosign/curl"
	ln -s "$(command -v mv)" "$tmp/nocosign/mv"
	ln -s "$(command -v mkdir)" "$tmp/nocosign/mkdir"
	ln -s "$(command -v rm)" "$tmp/nocosign/rm"
	ln -s "$(command -v cat)" "$tmp/nocosign/cat"
	_fails "a missing cosign is fatal while verify-signature is on" "is not installed" env "$env_rel" PATH="$tmp/nocosign" "$(command -v bash)" "$self" install v1.2.3 linux_amd64 "$tmp/i4"

	_fails "a platform the release does not list fails" "lists no dexaflow_1.2.3_darwin_arm64.tar.gz" env "$env_rel" VERIFY_SIGNATURE=false bash "$self" install v1.2.3 darwin_arm64 "$tmp/i5"
	_fails "a release that does not exist fails" "downloading checksums.txt for v9.9.9 failed" env "$env_rel" VERIFY_SIGNATURE=false bash "$self" install v9.9.9 linux_amd64 "$tmp/i6"

	# A cached directory is installed with no download at all, and its archive
	# is still checked against checksums.txt.
	out="$(env DEXAFLOW_RELEASES=file:///nonexistent VERIFY_SIGNATURE=false bash "$self" install v1.2.3 linux_amd64 "$tmp/i1" 2>&1)" || { echo "  FAIL cached install: $out"; fail=1; }
	_eq "$("$tmp/i1/bin/dexaflow" 2>&1)" "dexaflow 1.2.3 (fake)" "a cached directory installs without the network"
	printf 'tampered' >>"$tmp/i1/dexaflow_1.2.3_linux_amd64.tar.gz"
	_fails "a cached archive that changed is refused" "checksum mismatch" env DEXAFLOW_RELEASES=file:///nonexistent VERIFY_SIGNATURE=false bash "$self" install v1.2.3 linux_amd64 "$tmp/i1"
	_eq "$([ -e "$tmp/i1/dexaflow_1.2.3_linux_amd64.tar.gz" ] || echo gone)" "gone" "a mismatching archive is removed, so the next run downloads it again"

	# ── the signature is checked on every run, cached or not ──
	# A cache entry or a leftover directory in the tool cache is not a
	# verified release: whoever could write it could write checksums.txt.
	: >"$FAKE_LOG"
	out="$(env DEXAFLOW_RELEASES=file:///nonexistent PATH="$bin:$PATH" bash "$self" install v1.2.3 linux_amd64 "$tmp/i2" 2>&1)" || { echo "  FAIL cached verified install: $out"; fail=1; }
	_eq "$(grep -c '^cosign verify-blob ' "$FAKE_LOG")" "1" "a cached directory has its signature checked again, from the cached .sig and .pem"
	_fails "a cached checksums.txt with a bad signature is refused" "does not verify" env DEXAFLOW_RELEASES=file:///nonexistent PATH="$bin:$PATH" FAKE_COSIGN_EXIT=1 bash "$self" install v1.2.3 linux_amd64 "$tmp/i2"
	_eq "$(n=0; for f in "$tmp"/i2/checksums.txt* "$tmp/i2/cosign.log" "$tmp/i2/bin"; do [ -e "$f" ] && n=$((n + 1)); done; echo "$n")" "0" "after a bad signature the checksums, signature, certificate, log and binary are removed"

	out="$(env "$env_rel" VERIFY_SIGNATURE=false bash "$self" install v1.2.3 linux_amd64 "$tmp/i7" 2>&1)" || { echo "  FAIL unverified install: $out"; fail=1; }
	_fails "an unverified leftover with no signature is refused by a verifying run" "checksums.txt.sig" env DEXAFLOW_RELEASES=file:///nonexistent PATH="$bin:$PATH" bash "$self" install v1.2.3 linux_amd64 "$tmp/i7"
	: >"$FAKE_LOG"
	out="$(env "$env_rel" VERIFY_SIGNATURE=false bash "$self" install v1.2.3 linux_amd64 "$tmp/i8" 2>&1)" || { echo "  FAIL unverified install: $out"; fail=1; }
	out="$(env "$env_rel" PATH="$bin:$PATH" bash "$self" install v1.2.3 linux_amd64 "$tmp/i8" 2>&1)" || { echo "  FAIL verifying an unverified leftover: $out"; fail=1; }
	_eq "$(grep -c '^cosign verify-blob ' "$FAKE_LOG")" "1" "an unverified leftover is verified before it is used"

	# COSIGN names the binary the action installed; one on PATH is not used.
	mkdir -p "$tmp/pinned"
	cat >"$tmp/pinned/cosign" <<'SH'
#!/bin/sh
printf 'pinned %s\n' "$*" >>"$FAKE_LOG"
SH
	chmod +x "$tmp/pinned/cosign"
	: >"$FAKE_LOG"
	out="$(env "$env_rel" PATH="$bin:$PATH" COSIGN="$tmp/pinned/cosign" bash "$self" install v1.2.3 linux_amd64 "$tmp/i9" 2>&1)" || { echo "  FAIL install with COSIGN: $out"; fail=1; }
	_eq "$(cut -d' ' -f1 "$FAKE_LOG" | sort -u | tr '\n' ' ')" "pinned " "COSIGN is the cosign that runs, not the one on PATH"

	if [ "$fail" -eq 0 ]; then echo "setup-dexaflow self-test: PASS"; return 0; fi
	echo "setup-dexaflow self-test: FAIL"
	return 1
}

case "${1:-}" in
	--self-test) self_test ;;
	resolve-version) [ $# -eq 3 ] || die "usage: setup-dexaflow.sh resolve-version <version> <action ref>"; cmd_resolve_version "$2" "$3" ;;
	platform) [ $# -eq 3 ] || die "usage: setup-dexaflow.sh platform <runner.os> <runner.arch>"; cmd_platform "$2" "$3" ;;
	install) [ $# -eq 4 ] || die "usage: setup-dexaflow.sh install <tag> <platform> <dir>"; cmd_install "$2" "$3" "$4" ;;
	*) die "usage: setup-dexaflow.sh {resolve-version <version> <ref> | platform <os> <arch> | install <tag> <platform> <dir> | --self-test}" ;;
esac
