#!/usr/bin/env bash
# Scan the data image a GA will publish, with today's advisories, before the
# GA tag exists.
#
# release.yaml rebuilds the data variant of the task image on every tag and
# refuses to publish it on a fixable HIGH or CRITICAL finding in its language
# packages (#2247). check-runtime-data-lock.sh answers that question before the
# tag for the pins of runtime/data/requirements.txt, but not for the packages a
# wheel vendors: ray 2.59.0 carries its own aiohttp under
# ray/_private/runtime_env/agent/thirdparty_files, which no lock names. v0.5.10
# went red that way: eight aiohttp advisories were published between the
# v0.5.10-rc.5 scan (green) and the GA build, and a tag cannot be cut twice
# (ADR 0033), so the version was spent.
#
# A GA ships the image of the rc it promotes, rebuilt from the same inputs, so
# the rc's published image is the GA's image to within the advisory database.
# This scans it, both platforms, with release.yaml's Trivy version and
# criterion and the .trivyignore.yaml of the commit being tagged. The rc is the
# newest vX.Y.Z-rc.N of the version, or, when the GA has none, the newest rc
# of any version. Either way its data image inputs (runtime/, go.mod, go.sum
# and release.yaml's GO_VERSION) must equal the commit's, or the scan would
# vouch for a different image and the script refuses. An rc tag needs no scan:
# it builds its own image and a finding there costs only the next rc.
#
# cut-release.sh runs this between the merge commit's CI gate and the tag.
#
# Usage:
#   scripts/release-data-image-scan.sh <tag> <sha>   (from the repository root)
#   scripts/release-data-image-scan.sh --self-test
set -euo pipefail

WORKFLOW=".github/workflows/release.yaml"
IGNOREFILE=".trivyignore.yaml"
IMAGE="ghcr.io/dexadata/dexaflow-runtime"
TRIVY_IMAGE="mirror.gcr.io/aquasec/trivy"
# What the data image is built from, besides the Go toolchain: the Dockerfile,
# the lock and the runtime package under runtime/, and the agent's module graph.
INPUTS=(runtime go.mod go.sum)

# go_version reads release.yaml on stdin and prints its GO_VERSION.
go_version() {
	sed -n 's/^[[:space:]]*GO_VERSION:[[:space:]]*"\{0,1\}\([0-9][0-9.]*\)"\{0,1\}.*/\1/p' | head -1
}

# trivy_version reads release.yaml on stdin and prints its TRIVY_VERSION without
# the leading v (the image tags and `trivy --version` both omit it).
trivy_version() {
	sed -n 's/^[[:space:]]*TRIVY_VERSION:[[:space:]]*"\{0,1\}v\{0,1\}\([0-9][0-9.]*\)"\{0,1\}.*/\1/p' | head -1
}

# promoted_rc <version> prints the rc whose image the GA of <version> ships:
# its newest rc, else the newest rc of any version. Nothing when there is none.
promoted_rc() {
	local rc
	rc="$(git tag -l "v$1-rc.*" | sort -V | tail -1)"
	[ -n "$rc" ] || rc="$(git tag -l 'v*-rc.*' | sort -V | tail -1)"
	printf '%s' "$rc"
}

# same_inputs <rc> <sha>: whether the two commits build the same data image.
same_inputs() {
	git diff --quiet "$1" "$2" -- "${INPUTS[@]}" &&
		[ "$(git show "$1:$WORKFLOW" | go_version)" = "$(git show "$2:$WORKFLOW" | go_version)" ]
}

# scan_args prints the arguments of one `trivy image` run, release.yaml's
# criterion: library packages, CRITICAL and HIGH, unfixed ignored.
scan_args() { # <image-ref> <arch> <ignorefile>
	printf '%s\n' image --image-src remote --platform "linux/$2" --scanners vuln \
		--pkg-types library --severity CRITICAL,HIGH --ignore-unfixed \
		--ignorefile "$3" --exit-code 1 --quiet "$1"
}

# run_trivy <version> <dir> <args...>: trivy at <version>, with <dir> as the
# working directory (it holds the ignore file). A local binary of that version
# is used, else the pinned image through Docker.
run_trivy() {
	local want="$1" dir="$2" have
	shift 2
	if command -v trivy >/dev/null 2>&1; then
		have="$(trivy --version 2>/dev/null | sed -n 's/^Version:[[:space:]]*//p' | head -1)"
		[ "$have" = "$want" ] && { (cd "$dir" && trivy "$@"); return; }
	fi
	command -v docker >/dev/null 2>&1 || { echo "FAIL: neither trivy $want nor docker is available; this scan cannot be skipped silently" >&2; return 1; }
	docker run --rm -v "$dir:/src:ro" -w /src "$TRIVY_IMAGE:$want" "$@"
}

scan() { # <tag> <sha>
	local tag="$1" sha="$2" version rc want dir arch fail=0
	case "$tag" in
	*-rc.*) echo "skip: $tag is a release candidate, which builds and scans its own data image"; return 0 ;;
	esac
	sha="$(git rev-parse -q --verify "$sha^{commit}")" || { echo "FAIL: $2 is not a commit here" >&2; return 1; }
	version="${tag#v}"
	rc="$(promoted_rc "$version")"
	[ -n "$rc" ] || { echo "FAIL: no rc tag to take the data image of $tag from; cut an rc first" >&2; return 1; }
	same_inputs "$rc" "$sha" || {
		echo "FAIL: $rc built its data image from other inputs than ${sha:0:8} (${INPUTS[*]} or GO_VERSION differ); cut an rc of $version first so its image is the one this GA ships" >&2
		return 1
	}
	want="$(git show "$sha:$WORKFLOW" | trivy_version)"
	[ -n "$want" ] || { echo "FAIL: no TRIVY_VERSION in $WORKFLOW at ${sha:0:8}" >&2; return 1; }
	dir="$(mktemp -d)"
	git show "$sha:$IGNOREFILE" >"$dir/$IGNOREFILE" 2>/dev/null || : >"$dir/$IGNOREFILE"
	for arch in amd64 arm64; do
		local -a args=()
		local a
		while IFS= read -r a; do args+=("$a"); done < <(scan_args "$IMAGE:py3.11-data-$rc" "$arch" "$IGNOREFILE")
		if run_trivy "$want" "$dir" "${args[@]}"; then
			echo "OK: $IMAGE:py3.11-data-$rc ($arch) has no fixable HIGH or CRITICAL finding (trivy $want, ${sha:0:8}'s $IGNOREFILE)"
		else
			echo "FAIL: $IMAGE:py3.11-data-$rc ($arch) carries a finding release.yaml would refuse to publish for $tag (or the scan could not run); upgrade the package or accept it in $IGNOREFILE, merge that, and resume the cut" >&2
			fail=1
		fi
	done
	rm -rf "$dir"
	return "$fail"
}

self_test() {
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_PREFIX
	local fail=0 got tmp
	_eq() { # <name> <got> <want>
		if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got '$2', want '$3'"; fail=1; fi
	}
	got="$(printf 'env:\n  GO_VERSION: "1.26.11"\n  TRIVY_VERSION: "v0.75.0"\n' | go_version)"
	_eq "reads GO_VERSION" "$got" "1.26.11"
	got="$(printf 'env:\n  TRIVY_VERSION: "v0.75.0"\n' | trivy_version)"
	_eq "reads TRIVY_VERSION without the v" "$got" "0.75.0"
	got="$(scan_args ref:tag arm64 ignore.yaml | tr '\n' ' ')"
	_eq "scans one platform with release.yaml's criterion" "$got" \
		"image --image-src remote --platform linux/arm64 --scanners vuln --pkg-types library --severity CRITICAL,HIGH --ignore-unfixed --ignorefile ignore.yaml --exit-code 1 --quiet ref:tag "

	tmp="$(mktemp -d)"
	(
		set -e
		export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
		cd "$tmp"
		git init -q -b main .
		git config user.email t@t
		git config user.name t
		mkdir -p runtime .github/workflows
		printf 'env:\n  GO_VERSION: "1.26.10"\n  TRIVY_VERSION: "v0.75.0"\n' >"$WORKFLOW"
		echo a >runtime/requirements.txt
		echo m >go.mod
		echo s >go.sum
		echo docs >README.md
		echo 'ignores: []' >"$IGNOREFILE"
		git add -A
		git commit -qm base
		git tag v1.0.0-rc.9
		git commit -q --allow-empty -m rc10
		git tag v1.0.0-rc.10
		git tag v0.9.0-rc.3 HEAD~1
		echo more >README.md
		echo 'ignores: [accepted]' >"$IGNOREFILE"
		git commit -qam docs
		git tag same
		echo b >runtime/requirements.txt
		git commit -qam lock
		git tag lock
		git checkout -q same
		sed -i.bak 's/1.26.10/1.26.11/' "$WORKFLOW" && rm -f "$WORKFLOW.bak"
		git commit -qam go
		git tag go
	) >/dev/null 2>&1 || { echo "FAIL could not build the fixture repository"; fail=1; }

	got="$(cd "$tmp" && promoted_rc 1.0.0)"
	_eq "promotes the newest rc of the version (rc.10 after rc.9)" "$got" "v1.0.0-rc.10"
	got="$(cd "$tmp" && promoted_rc 1.0.1)"
	_eq "a GA with no rc of its own falls back to the newest rc" "$got" "v1.0.0-rc.10"
	got="$(cd "$tmp" && same_inputs v1.0.0-rc.10 same && echo same || echo differ)"
	_eq "a change outside the image inputs keeps the rc's image" "$got" "same"
	got="$(cd "$tmp" && same_inputs v1.0.0-rc.10 lock && echo same || echo differ)"
	_eq "a lock change is another image" "$got" "differ"
	got="$(cd "$tmp" && same_inputs v1.0.0-rc.10 go && echo same || echo differ)"
	_eq "a GO_VERSION change is another image" "$got" "differ"

	# scan() with the scanner stubbed: it records what it was asked and fails
	# when told to.
	run_trivy() {
		printf '%s %s ignore=%s\n' "$1" "$(printf '%s ' "${@:3}" | sed 's/.*--platform \([^ ]*\).* \([^ ]*\) $/\1 \2/')" \
			"$(cat "$2/$IGNOREFILE")" >>"$tmp/calls"
		[ -z "${STUB_FAIL_ARCH:-}" ] || ! printf '%s ' "$@" | grep -q "linux/$STUB_FAIL_ARCH"
	}
	got="$(cd "$tmp" && scan v1.0.0-rc.11 same 2>&1; echo "rc=$?")"
	_eq "an rc tag is not scanned" "$(printf '%s' "$got" | tail -1)" "rc=0"
	rm -f "$tmp/calls"
	got="$(cd "$tmp" && scan v1.0.0 "$(git rev-parse same)" >/dev/null 2>&1; echo "rc=$?")"
	_eq "a clean GA scan passes" "$got" "rc=0"
	_eq "both platforms of the promoted rc's image, with the commit's ignore file" "$(cat "$tmp/calls" 2>/dev/null)" \
		"$(printf '0.75.0 linux/amd64 %s:py3.11-data-v1.0.0-rc.10 ignore=ignores: [accepted]\n0.75.0 linux/arm64 %s:py3.11-data-v1.0.0-rc.10 ignore=ignores: [accepted]' "$IMAGE" "$IMAGE")"
	got="$(cd "$tmp" && STUB_FAIL_ARCH=arm64 scan v1.0.0 same >/dev/null 2>&1; echo "rc=$?")"
	_eq "a finding on one platform stops the GA" "$got" "rc=1"
	got="$(cd "$tmp" && scan v1.0.0 lock 2>&1; echo "rc=$?")"
	_eq "a GA whose image inputs moved past its rc is refused" "$(printf '%s' "$got" | tail -1)" "rc=1"
	_eq "and the refusal says to cut an rc" "$(printf '%s' "$got" | grep -c 'cut an rc of 1.0.0 first')" "1"
	got="$(cd "$tmp" && scan v1.0.0 deadbeef 2>&1; echo "rc=$?")"
	_eq "a sha that is not a commit is refused as such" "$(printf '%s' "$got" | head -1)" "FAIL: deadbeef is not a commit here"
	rm -rf "$tmp"

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

[ $# -eq 2 ] || { echo "usage: $0 <tag> <sha> | --self-test" >&2; exit 2; }
scan "$1" "$2"
