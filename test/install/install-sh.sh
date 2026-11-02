#!/usr/bin/env bash
# Tests for the release signature check in install.sh (#1563).
#
# install.sh runs as `curl | sh` on machines we never see, so it is exercised
# here against a fake release: a local directory served by a fake `curl`, a
# fake `cosign` that records its arguments and passes or fails on request, and
# a PATH that holds only the tools listed below, so a cosign installed on the
# machine running the test cannot leak in.
#
# What it pins:
#   - cosign on PATH: checksums.txt is verified with the exact identity of the
#     release workflow at that tag (dexadata/dexaflow from v0.5.0 on,
#     neochaotic/leoflow before the transfer) and the GitHub OIDC issuer, and
#     a failed verification, or a missing .sig/.pem, aborts before anything is
#     installed;
#   - no cosign: the install goes on and prints one line saying the signature
#     was not checked; DEXAFLOW_INSTALL_REQUIRE_SIGNATURE=1 (or the LEOFLOW_
#     name) makes that fatal;
#   - DEXAFLOW_INSTALL_SKIP_SIGNATURE=1 skips the check, loudly;
#   - the checksum check still runs and still fails on a mismatch.
#
# Usage: test/install/install-sh.sh [--self-test]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	# ── a PATH with only what install.sh needs ──
	local sysbin="$tmp/sysbin" tool
	mkdir -p "$sysbin"
	for tool in sh bash tar gzip grep sed awk head tail sort mktemp rm install ln mkdir \
		sha256sum shasum uname cat cut id basename dirname tr env chmod cp printf; do
		if command -v "$tool" >/dev/null 2>&1; then
			ln -sf "$(command -v "$tool")" "$sysbin/$tool"
		fi
	done

	# ── fake curl: serves $FAKE_RELEASES/<tag>/<file> for the release URLs ──
	local fakebin="$tmp/fakebin"
	mkdir -p "$fakebin"
	cat >"$fakebin/curl" <<'SH'
#!/bin/sh
out="" url=""
while [ $# -gt 0 ]; do
	case "$1" in
		-o) out="$2"; shift 2 ;;
		https://*) url="$1"; shift ;;
		*) shift ;;
	esac
done
case "$url" in
	https://github.com/*/releases/download/*)
		rest="${url#https://github.com/*/releases/download/}"
		src="${FAKE_RELEASES}/${rest}"
		;;
	*) echo "fake curl: unexpected URL $url" >&2; exit 22 ;;
esac
[ -f "$src" ] || exit 22
if [ -n "$out" ]; then cat "$src" >"$out"; else cat "$src"; fi
SH
	# ── fake cosign: logs its argv, fails when FAKE_COSIGN_FAIL=1 ──
	local cosignbin="$tmp/cosignbin"
	mkdir -p "$cosignbin"
	cat >"$cosignbin/cosign" <<'SH'
#!/bin/sh
if [ "$1" = version ]; then
	printf 'GitVersion:    %s\n' "${FAKE_COSIGN_VERSION:-v2.4.3}"
	exit 0
fi
printf '%s\n' "$*" >>"$FAKE_COSIGN_LOG"
[ "${FAKE_COSIGN_FAIL:-0}" = 1 ] && { echo "Error: none of the expected identities matched" >&2; exit 1; }
exit 0
SH
	chmod +x "$fakebin/curl" "$cosignbin/cosign"

	# ── fake releases ──
	_release() { # <tag> <name> [suffix...]: the binaries default to CLI, server and agent
		local tag="$1" name="$2" d stage nov="${1#v}" platform
		shift 2
		local -a suffixes=("" -server -agent) files=()
		[ $# -gt 0 ] && suffixes=("$@")
		d="$tmp/releases/$tag"
		stage="$tmp/stage-$tag"
		mkdir -p "$d" "$stage"
		for suffix in "${suffixes[@]}"; do
			printf '#!/bin/sh\necho %s%s\n' "$name" "$suffix" >"$stage/${name}${suffix}"
			chmod +x "$stage/${name}${suffix}"
			files+=("${name}${suffix}")
		done
		for platform in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
			tar -czf "$d/${name}_${nov}_${platform}.tar.gz" -C "$stage" "${files[@]}"
		done
		(cd "$d" && sha256sum ./*.tar.gz | sed 's# \./# #' >checksums.txt)
		printf 'sig\n' >"$d/checksums.txt.sig"
		printf 'pem\n' >"$d/checksums.txt.pem"
	}
	# Current archives carry the MCP server too (#976); v0.4.8 shows that an
	# archive without it still installs.
	_release v0.5.4 dexaflow "" -server -agent -mcp
	_release v0.4.8 leoflow

	local out rc home log
	# _run <with-cosign:0|1> <env assignments...>: runs install.sh, sets out, rc, log, home.
	_run() {
		local with_cosign="$1" path
		shift
		home="$tmp/home"
		rm -rf "$home"
		mkdir -p "$home"
		log="$tmp/cosign.log"
		: >"$log"
		path="$fakebin:$sysbin"
		[ "$with_cosign" = 1 ] && path="$cosignbin:$path"
		rc=0
		out="$(env -i PATH="$path" HOME="$home" FAKE_RELEASES="$tmp/releases" FAKE_COSIGN_LOG="$log" \
			DEXAFLOW_INSTALL_DIR="$home/bin" DEXAFLOW_NO_SETUP=1 DEXAFLOW_NO_PATH=1 \
			"$@" sh "$ROOT/install.sh" 2>&1)" || rc=$?
	}
	_ok() { printf '  ok   %s\n' "$1"; }
	_bad() { printf '  FAIL %s\n    %s\n    output:\n%s\n' "$1" "$2" "$(printf '%s\n' "$out" | sed 's/^/      /')"; fail=1; }
	_installed() { [ -x "$home/bin/$1" ]; }

	local dx_id='https://github.com/dexadata/dexaflow/.github/workflows/release.yaml@refs/tags/v0.5.4'
	local lf_id='https://github.com/neochaotic/leoflow/.github/workflows/release.yaml@refs/tags/v0.4.8'
	local issuer='https://token.actions.githubusercontent.com'

	# 1. cosign present and the signature verifies.
	_run 1 DEXAFLOW_VERSION=v0.5.4
	if [ "$rc" != 0 ]; then _bad "verified install succeeds" "exit $rc"
	elif ! _installed dexaflow; then _bad "verified install succeeds" "dexaflow not installed"
	elif ! grep -q -- "verify-blob" "$log"; then _bad "verified install succeeds" "cosign verify-blob was not run"
	elif ! grep -qF -- "--certificate-identity $dx_id " "$log"; then _bad "verified install succeeds" "identity is not the exact release workflow at the tag: $(cat "$log")"
	elif ! grep -qF -- "--certificate-oidc-issuer $issuer" "$log"; then _bad "verified install succeeds" "issuer missing: $(cat "$log")"
	elif ! grep -qE -- "--signature [^ ]*checksums\.txt\.sig" "$log" || ! grep -qE -- "--certificate [^ ]*checksums\.txt\.pem" "$log"; then _bad "verified install succeeds" "sig/pem not passed: $(cat "$log")"
	elif ! printf '%s' "$out" | grep -q 'signature verified'; then _bad "verified install succeeds" "no 'signature verified' line"
	else _ok "cosign present: checksums.txt verified with the exact identity, install goes on"; fi

	# 1b. Every binary in the archive is installed, the MCP server included,
	# under its pre-rename name too (#976).
	if ! _installed dexaflow-mcp; then _bad "the MCP server is installed" "dexaflow-mcp missing from $home/bin"
	elif ! _installed leoflow-mcp; then _bad "the MCP server is installed" "leoflow-mcp link missing"
	else _ok "the archive's MCP server is installed (dexaflow-mcp, leoflow-mcp)"; fi

	# 2. cosign present and the signature does not verify: nothing installed.
	_run 1 DEXAFLOW_VERSION=v0.5.4 FAKE_COSIGN_FAIL=1
	if [ "$rc" = 0 ]; then _bad "a bad signature aborts" "exit 0"
	elif _installed dexaflow; then _bad "a bad signature aborts" "dexaflow was installed anyway"
	elif ! printf '%s' "$out" | grep -q 'signature verification of checksums.txt failed'; then _bad "a bad signature aborts" "error does not name the failure"
	else _ok "cosign present, bad signature: aborts before installing"; fi

	# 3. Releases before the transfer were signed by neochaotic/leoflow.
	_run 1 DEXAFLOW_VERSION=v0.4.8
	if [ "$rc" != 0 ] || ! _installed leoflow; then _bad "a pre-transfer release verifies" "exit $rc"
	elif ! grep -qF -- "--certificate-identity $lf_id " "$log"; then _bad "a pre-transfer release verifies" "identity: $(cat "$log")"
	else _ok "pre-transfer release (v0.4.8): neochaotic/leoflow identity at that tag"; fi

	# 4. A missing .sig with cosign present fails closed.
	rm "$tmp/releases/v0.5.4/checksums.txt.sig"
	_run 1 DEXAFLOW_VERSION=v0.5.4
	if [ "$rc" = 0 ] || _installed dexaflow; then _bad "a missing signature aborts" "exit $rc"
	else _ok "cosign present, release has no .sig: aborts"; fi
	printf 'sig\n' >"$tmp/releases/v0.5.4/checksums.txt.sig"

	# 5. No cosign: installs, with one line saying the signature was not checked.
	_run 0 DEXAFLOW_VERSION=v0.5.4
	if [ "$rc" != 0 ] || ! _installed dexaflow; then _bad "no cosign still installs" "exit $rc"
	elif [ "$(printf '%s\n' "$out" | grep -c 'signature of checksums.txt was not checked')" != 1 ]; then _bad "no cosign still installs" "want exactly one 'not checked' line"
	elif [ -s "$log" ]; then _bad "no cosign still installs" "cosign ran"
	else _ok "no cosign: installs and says once that the signature was not checked"; fi

	# 6. No cosign and a signature required: fatal, under either prefix.
	local var
	for var in DEXAFLOW_INSTALL_REQUIRE_SIGNATURE LEOFLOW_INSTALL_REQUIRE_SIGNATURE; do
		_run 0 DEXAFLOW_VERSION=v0.5.4 "$var=1"
		if [ "$rc" = 0 ] || _installed dexaflow; then _bad "$var=1 without cosign fails" "exit $rc"
		elif ! printf '%s' "$out" | grep -q 'cosign'; then _bad "$var=1 without cosign fails" "error does not mention cosign"
		else _ok "no cosign, $var=1: fails before installing"; fi
	done

	# 7. A cosign too old for --certificate-identity counts as no cosign.
	_run 1 DEXAFLOW_VERSION=v0.5.4 FAKE_COSIGN_VERSION=v1.13.1
	if [ "$rc" != 0 ] || ! _installed dexaflow || [ -s "$log" ]; then _bad "cosign v1 is not used" "exit $rc, log: $(cat "$log")"
	elif ! printf '%s' "$out" | grep -q 'not checked'; then _bad "cosign v1 is not used" "no 'not checked' line"
	else _ok "cosign v1 (no --certificate-identity): treated as absent"; fi
	_run 1 DEXAFLOW_VERSION=v0.5.4 FAKE_COSIGN_VERSION=v1.13.1 DEXAFLOW_INSTALL_REQUIRE_SIGNATURE=1
	if [ "$rc" = 0 ] || _installed dexaflow; then _bad "cosign v1 with a signature required fails" "exit $rc"
	else _ok "cosign v1 with a signature required: fails"; fi

	# 8. An explicit skip, under either prefix, does not run cosign and says so.
	for var in DEXAFLOW_INSTALL_SKIP_SIGNATURE LEOFLOW_INSTALL_SKIP_SIGNATURE; do
		_run 1 DEXAFLOW_VERSION=v0.5.4 FAKE_COSIGN_FAIL=1 "$var=1"
		if [ "$rc" != 0 ] || ! _installed dexaflow || [ -s "$log" ]; then _bad "$var=1 skips" "exit $rc"
		elif ! printf '%s' "$out" | grep -q 'not checked'; then _bad "$var=1 skips" "no 'not checked' line"
		else _ok "$var=1: cosign not run, said so"; fi
	done

	# 9. Skip and require together is a contradiction, refused.
	_run 1 DEXAFLOW_VERSION=v0.5.4 DEXAFLOW_INSTALL_SKIP_SIGNATURE=1 DEXAFLOW_INSTALL_REQUIRE_SIGNATURE=1
	if [ "$rc" = 0 ] || _installed dexaflow; then _bad "skip plus require is refused" "exit $rc"
	else _ok "skip and require together: refused"; fi

	# 10. The checksum check still runs after a good signature.
	printf 'tampered\n' >>"$tmp/releases/v0.5.4/dexaflow_0.5.4_$(uname -s | tr 'A-Z' 'a-z')_$(case "$(uname -m)" in x86_64|amd64) echo amd64;; *) echo arm64;; esac).tar.gz"
	_run 1 DEXAFLOW_VERSION=v0.5.4
	if [ "$rc" = 0 ] || _installed dexaflow; then _bad "a checksum mismatch still fails" "exit $rc"
	elif ! printf '%s' "$out" | grep -q 'checksum mismatch'; then _bad "a checksum mismatch still fails" "no mismatch error"
	else _ok "signature good, archive tampered: checksum mismatch still fails"; fi

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

self_test
