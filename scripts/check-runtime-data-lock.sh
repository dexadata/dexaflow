#!/usr/bin/env bash
# Scan the data image's Python lock before a release candidate is tagged.
#
# release.yaml refuses to publish the data variant of the task image when a
# fixable HIGH or CRITICAL finding sits in its language packages (#2247). Those
# packages are exactly the pins of runtime/data/requirements.txt, so the lock
# alone answers the question the release asks, in seconds and without building
# anything. Without this gate the answer arrived only after the tag: v0.5.8-rc.1
# failed on pyasn1 0.6.4, whose advisories were published between the prepare
# pull request and the tag, and the fix cost a pull request to main, a pick, a
# new prepare and a second tag (about ninety minutes).
#
# cut-release.sh's run_gates() globs scripts/check-*.sh, so this runs in every
# cut's pre-flight, right before the tag. It uses the Trivy version and the
# criterion release.yaml uses (library packages, CRITICAL and HIGH, unfixed
# ignored, .trivyignore.yaml applied). A local `trivy` binary is used when it
# is that version; otherwise the pinned image runs through Docker. With neither,
# the gate fails: a skipped scan is how the window above stays open.
#
# Usage:
#   scripts/check-runtime-data-lock.sh [repo-root]
#   scripts/check-runtime-data-lock.sh --self-test
set -euo pipefail

LOCK="runtime/data/requirements.txt"
WORKFLOW=".github/workflows/release.yaml"
IGNOREFILE=".trivyignore.yaml"
IMAGE_REPO="mirror.gcr.io/aquasec/trivy"

# trivy_version reads release.yaml on stdin and prints its TRIVY_VERSION without
# the leading v (the image tags and `trivy --version` both omit it).
trivy_version() {
	sed -n 's/^[[:space:]]*TRIVY_VERSION:[[:space:]]*"\{0,1\}v\{0,1\}\([0-9][0-9.]*\)"\{0,1\}.*/\1/p' | head -1
}

# scan_args prints the arguments of one `trivy fs` run over the lock.
scan_args() { # <lock> <ignorefile>
	printf '%s\n' fs --scanners vuln --pkg-types library --severity CRITICAL,HIGH \
		--ignore-unfixed --ignorefile "$2" --exit-code 1 --quiet "$1"
}

check() { # <root>
	local root="$1" want have
	cd "$root"
	[ -f "$LOCK" ] || { echo "FAIL: $LOCK does not exist" >&2; return 1; }
	[ -f "$WORKFLOW" ] || { echo "FAIL: $WORKFLOW does not exist" >&2; return 1; }
	want="$(trivy_version <"$WORKFLOW")"
	[ -n "$want" ] || { echo "FAIL: no TRIVY_VERSION in $WORKFLOW" >&2; return 1; }
	local -a args
	mapfile -t args < <(scan_args "$LOCK" "$IGNOREFILE")
	if command -v trivy >/dev/null 2>&1; then
		have="$(trivy --version 2>/dev/null | sed -n 's/^Version:[[:space:]]*//p' | head -1)"
		if [ "$have" = "$want" ]; then
			trivy "${args[@]}" && { echo "OK: $LOCK has no fixable HIGH or CRITICAL finding (trivy $want)"; return 0; }
			echo "FAIL: $LOCK carries a finding release.yaml refuses to publish; upgrade the package in the lock or accept it in $IGNOREFILE" >&2
			return 1
		fi
	fi
	if command -v docker >/dev/null 2>&1; then
		docker run --rm -v "$PWD:/src:ro" -w /src "$IMAGE_REPO:$want" "${args[@]}" &&
			{ echo "OK: $LOCK has no fixable HIGH or CRITICAL finding (trivy $want in Docker)"; return 0; }
		echo "FAIL: $LOCK carries a finding release.yaml refuses to publish (or the scan could not run); upgrade the package in the lock or accept it in $IGNOREFILE" >&2
		return 1
	fi
	echo "FAIL: neither trivy $want nor docker is available; this gate cannot be skipped silently" >&2
	return 1
}

self_test() {
	local fail=0 got
	_eq() { # <name> <got> <want>
		if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got '$2', want '$3'"; fail=1; fi
	}
	got="$(printf 'env:\n  GO_VERSION: "1.26"\n  TRIVY_VERSION: "v0.75.0"\n' | trivy_version)"
	_eq "reads a quoted v-prefixed version" "$got" "0.75.0"
	got="$(printf 'env:\n  TRIVY_VERSION: 0.72.1 # pinned\n' | trivy_version)"
	_eq "reads a bare version with a comment" "$got" "0.72.1"
	got="$(printf 'env:\n  GO_VERSION: "1.26"\n' | trivy_version)"
	_eq "no pin prints nothing, so check fails" "$got" ""
	got="$(scan_args lock.txt ignore.yaml | tr '\n' ' ')"
	_eq "scans libraries only, the release criterion" "$got" \
		"fs --scanners vuln --pkg-types library --severity CRITICAL,HIGH --ignore-unfixed --ignorefile ignore.yaml --exit-code 1 --quiet lock.txt "
	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

check "${1:-$(cd "$(dirname "$0")/.." && pwd)}"
