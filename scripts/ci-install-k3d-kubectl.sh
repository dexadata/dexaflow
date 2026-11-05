#!/usr/bin/env bash
# Installs k3d and kubectl for the CI k3d jobs, with retries and checksums.
#
# The jobs used to pipe k3d's install.sh into bash and fetch kubectl with a
# bare `curl -fsSL`. Neither retries, so a single 504 from GitHub's release
# CDN failed the whole e2e before it started (the merge queue run of #2368,
# run 42045388930: "curl: (22) The requested URL returned error: 504",
# "Failed to install k3d"). Here every download retries on any error, the k3d
# binary is pinned and checked against the sha256 below, and kubectl is checked
# against the .sha256 that dl.k8s.io publishes beside it.
#
# Usage: scripts/ci-install-k3d-kubectl.sh [--self-test]
set -euo pipefail

K3D_VERSION="v5.7.4"
# sha256 of k3d-linux-amd64 for K3D_VERSION, from the release's checksums.txt.
K3D_SHA256="1ac1da365236736a8df8c32107b54aca208384ab1d9a06771443c85ad698a5eb"

fetch() { # <url> <out>
	curl -fsSL --retry 5 --retry-all-errors --retry-delay 5 \
		--connect-timeout 20 --max-time 180 -o "$2" "$1"
}

# Fails, naming the file, unless <file> hashes to <sha256>.
verify_sha256() { # <file> <sha256>
	local got
	got="$(sha256sum "$1" | awk '{ print $1 }')"
	if [ "$got" != "$2" ]; then
		echo "checksum mismatch for $1: got $got, want $2" >&2
		return 1
	fi
}

self_test() {
	local dir
	dir="$(mktemp -d)"
	trap 'rm -rf "$dir"' RETURN
	printf 'k3d' >"$dir/f"
	verify_sha256 "$dir/f" "$(printf 'k3d' | sha256sum | awk '{ print $1 }')" || {
		echo "FAIL: verify_sha256 rejected a matching file"
		return 1
	}
	if verify_sha256 "$dir/f" "0000" 2>/dev/null; then
		echo "FAIL: verify_sha256 accepted a mismatching file"
		return 1
	fi
	echo "OK: verify_sha256"
}

main() {
	local dir kver
	dir="$(mktemp -d)"
	# shellcheck disable=SC2064 # expand now: $dir is local and gone at EXIT
	trap "rm -rf '$dir'" EXIT

	fetch "https://github.com/k3d-io/k3d/releases/download/$K3D_VERSION/k3d-linux-amd64" "$dir/k3d"
	verify_sha256 "$dir/k3d" "$K3D_SHA256"
	sudo install -m 0755 "$dir/k3d" /usr/local/bin/k3d

	fetch https://dl.k8s.io/release/stable.txt "$dir/stable.txt"
	kver="$(tr -d '[:space:]' <"$dir/stable.txt")"
	fetch "https://dl.k8s.io/release/$kver/bin/linux/amd64/kubectl" "$dir/kubectl"
	fetch "https://dl.k8s.io/release/$kver/bin/linux/amd64/kubectl.sha256" "$dir/kubectl.sha256"
	verify_sha256 "$dir/kubectl" "$(tr -d '[:space:]' <"$dir/kubectl.sha256")"
	sudo install -m 0755 "$dir/kubectl" /usr/local/bin/kubectl

	k3d version
	kubectl version --client
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
else
	main
fi
