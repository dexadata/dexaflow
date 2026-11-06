#!/usr/bin/env bash
# release-check-agent-stamp.sh: proves the agent baked into a published task
# base image reports the release it was cut for.
#
#   scripts/release-check-agent-stamp.sh <image-ref> <tag>
#   scripts/release-check-agent-stamp.sh --self-test
#
# The task base (runtime/Dockerfile) stamps /usr/local/bin/dexaflow-agent with
# -ldflags -X on internal/version. That stamp is correct but silent when it
# breaks: a renamed variable in internal/version, or a build that drops the
# VERSION build argument, links an agent that reports `dev`, and nothing fails
# until someone reads `dexaflow-agent --version` in a task pod (#846, #894).
# The runtime-image job in release.yaml runs this right after it creates the
# manifest, so a broken stamp fails the job and the release gate retracts the
# release before promote moves any pointer.
#
# Needs: docker, and a registry login that can pull the image. The amd64 leg is
# checked; both legs are built from the same Dockerfile and build arguments.
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# stamp_problem <version output> <tag>: prints why the output does not name
# the tag as a stamped release, or nothing when it does. The output has the
# shape of version.Info.String():
#   dexaflow v0.5.10 (commit 1a2b3c, built 2026-11-13T00:00:00Z, go1.26.10)
stamp_problem() {
	local out="$1" tag="$2" ver commit
	ver="$(printf '%s\n' "$out" | sed -n 's/^dexaflow \([^ ]*\) (commit \([^,]*\), built .*/\1/p' | head -n1)"
	commit="$(printf '%s\n' "$out" | sed -n 's/^dexaflow \([^ ]*\) (commit \([^,]*\), built .*/\2/p' | head -n1)"
	if [ -z "$ver" ]; then
		echo "the agent printed no version line: $out"
	elif [ "$ver" != "$tag" ]; then
		echo "the agent reports version $ver, want $tag"
	elif [ -z "$commit" ] || [ "$commit" = "none" ]; then
		echo "the agent reports version $ver with no commit stamp"
	fi
}

self_test() {
	local got
	got="$(stamp_problem 'dexaflow v0.5.10 (commit 1a2b3c4d, built 2026-11-13T00:00:00Z, go1.26.10)' v0.5.10)"
	[ -z "$got" ] || { echo "FAIL: a stamped agent was refused: $got"; return 1; }
	got="$(stamp_problem 'dexaflow dev (commit none, built unknown, go1.26.10)' v0.5.10)"
	[ "$got" = "the agent reports version dev, want v0.5.10" ] || { echo "FAIL: dev accepted or misnamed: $got"; return 1; }
	got="$(stamp_problem 'dexaflow v0.5.9 (commit 1a2b3c4d, built 2026-11-06T00:00:00Z, go1.26.10)' v0.5.10)"
	[ "$got" = "the agent reports version v0.5.9, want v0.5.10" ] || { echo "FAIL: another release accepted: $got"; return 1; }
	got="$(stamp_problem 'dexaflow v0.5.10 (commit none, built unknown, go1.26.10)' v0.5.10)"
	[ "$got" = "the agent reports version v0.5.10 with no commit stamp" ] || { echo "FAIL: missing commit accepted: $got"; return 1; }
	got="$(stamp_problem 'exec format error' v0.5.10)"
	[ "$got" = "the agent printed no version line: exec format error" ] || { echo "FAIL: garbage accepted: $got"; return 1; }
	echo "OK: stamp_problem"
}

main() {
	[ "$#" -eq 2 ] || die "usage: $0 <image-ref> <tag> | --self-test"
	local ref="$1" tag="$2" out problem
	out="$(docker run --rm --platform linux/amd64 --entrypoint /usr/local/bin/dexaflow-agent "$ref" --version 2>&1)" ||
		die "running the agent in $ref failed: $out"
	echo "$ref: $out"
	problem="$(stamp_problem "$out" "$tag")"
	[ -z "$problem" ] || die "$ref: $problem"
	echo "OK: the agent in $ref is stamped $tag"
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
else
	main "$@"
fi
