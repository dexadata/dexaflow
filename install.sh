#!/bin/sh
# Dexaflow installer: downloads the release archive for this OS/arch, verifies
# its checksum, installs the binaries (plus leoflow* links to them, the names
# before the rename) and runs `dexaflow setup` to bootstrap the managed runtime
# (Python, workspace).
#
#   curl -fsSL https://raw.githubusercontent.com/dexadata/leoflow/main/install.sh | sh
#
# Environment overrides (the LEOFLOW_* names are still accepted):
#   DEXAFLOW_VERSION=v0.5.0          pin a specific release (default: latest)
#   DEXAFLOW_NO_SETUP=1              install binaries only, skip `dexaflow setup`
#   DEXAFLOW_INSTALL_DIR=~/.dexaflow/bin
set -eu

REPO="dexadata/leoflow"

# Choose where to put the binaries. Prefer a directory ALREADY on PATH so the
# user needs no `source`/new shell — the common "command not found" trap. Order:
# an explicit override; /usr/local/bin when writable (root); ~/.local/bin when
# it is already on PATH; otherwise the managed ~/.dexaflow/bin (we then edit the
# profile). ON_PATH=1 means no profile edit is needed.
resolve_install_dir() {
	if [ -n "${DEXAFLOW_INSTALL_DIR:-${LEOFLOW_INSTALL_DIR:-}}" ]; then
		printf '%s' "${DEXAFLOW_INSTALL_DIR:-$LEOFLOW_INSTALL_DIR}"
		return
	fi
	if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
		printf '/usr/local/bin'
		return
	fi
	case ":${PATH}:" in
		*":${HOME}/.local/bin:"*) printf '%s' "${HOME}/.local/bin"; return ;;
	esac
	printf '%s' "${HOME}/.dexaflow/bin"
}
INSTALL_DIR="$(resolve_install_dir)"

info() { printf '\033[36m==>\033[0m %s\n' "$1"; }
err() { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# ── Detect platform (Go binaries are static, so libc does not matter here) ──
os=$(uname -s)
case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) err "unsupported OS '$os' (Dexaflow ships linux and darwin; on Windows use WSL2)" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) err "unsupported architecture '$arch'" ;;
esac

# ── Pick a downloader ──
if have curl; then
	dl() { curl -fsSL "$1" -o "$2"; }
	fetch() { curl -fsSL "$1"; }
elif have wget; then
	dl() { wget -qO "$2" "$1"; }
	fetch() { wget -qO - "$1"; }
else
	err "need curl or wget to download Dexaflow"
fi

# tar extracts the release archive (tar -xzf below). Minimal images (e.g. openSUSE
# Leap) ship without it; check up front so we fail with a clear, actionable
# message instead of a raw "tar: command not found" mid-install.
have tar || err "need tar to extract the release archive (install it, e.g. 'apk add tar', 'zypper in tar', 'dnf install tar')"

# ── Resolve version ──
# Pin any version (incl. a pre-release once stable exists): LEOFLOW_VERSION=...
version="${DEXAFLOW_VERSION:-${LEOFLOW_VERSION:-}}"
if [ -z "$version" ]; then
	info "resolving latest release..."
	# Prefer the latest STABLE release: /releases/latest excludes pre-releases AND
	# drafts, so once a stable ships, users get it by default (the right behavior).
	version=$(fetch "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
		| grep '"tag_name"' | head -1 | sed 's/.*: *"//; s/".*//')
	# No stable yet (the pre-alpha phase): fall back to the highest published
	# pre-release. Don't trust the list order (a retracted draft reorders it) —
	# take the highest tag with sort -V over the non-draft list.
	if [ -z "$version" ]; then
		version=$(fetch "https://api.github.com/repos/${REPO}/releases?per_page=50" \
			| grep '"tag_name"' | sed 's/.*: *"//; s/".*//' | sort -V | tail -1)
	fi
	[ -n "$version" ] || err "could not resolve the latest release tag (set DEXAFLOW_VERSION)"
fi
# GoReleaser archive names drop the leading 'v' from the version.
ver_nov=$(printf '%s' "$version" | sed 's/^v//')

base="https://github.com/${REPO}/releases/download/${version}"
info "installing Dexaflow ${version} (${os}/${arch})"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

dl "${base}/checksums.txt" "${tmp}/checksums.txt" || err "downloading checksums.txt failed"
# Releases before the rename ship leoflow_* archives with leoflow* binaries.
archive="dexaflow_${ver_nov}_${os}_${arch}.tar.gz"
name=dexaflow
if ! grep -q " ${archive}\$" "${tmp}/checksums.txt"; then
	archive="leoflow_${ver_nov}_${os}_${arch}.tar.gz"
	name=leoflow
fi
dl "${base}/${archive}" "${tmp}/${archive}" || err "downloading ${archive} failed"

info "verifying checksum..."
expected=$(grep " ${archive}\$" "${tmp}/checksums.txt" | awk '{print $1}')
[ -n "$expected" ] || err "no checksum entry for ${archive}"
if have sha256sum; then
	actual=$(sha256sum "${tmp}/${archive}" | awk '{print $1}')
elif have shasum; then
	actual=$(shasum -a 256 "${tmp}/${archive}" | awk '{print $1}')
else
	err "need sha256sum or shasum to verify the download"
fi
[ "$actual" = "$expected" ] || err "checksum mismatch for ${archive} (got ${actual}, want ${expected})"

# ── Extract and install ──
tar -xzf "${tmp}/${archive}" -C "$tmp"
mkdir -p "$INSTALL_DIR"
for suffix in "" -server -agent; do
	bin="${name}${suffix}"
	[ -f "${tmp}/${bin}" ] || err "archive is missing ${bin}"
	install -m 0755 "${tmp}/${bin}" "${INSTALL_DIR}/${bin}"
	# The pre-rename names keep working: scripts and habits that call leoflow*
	# reach the same binaries.
	if [ "$name" = dexaflow ]; then
		ln -sf "${bin}" "${INSTALL_DIR}/leoflow${suffix}"
	fi
done
info "installed binaries to ${INSTALL_DIR}"

# ── PATH ──
# Persist INSTALL_DIR on PATH in the user's shell profile so `leoflow` works in
# new shells without a manual step. Idempotent; opt out with LEOFLOW_NO_PATH=1.
add_to_profile() {
	dir="$1"
	# Detect the user's login shell from /etc/passwd, not $SHELL: under
	# `curl | sh` the script runs in /bin/sh and $SHELL is unreliable (it caused
	# the PATH to land in ~/.profile while the user's bash reads ~/.bashrc).
	login_shell="${SHELL:-}"
	if command -v getent >/dev/null 2>&1; then
		ls_shell=$(getent passwd "$(id -un)" 2>/dev/null | cut -d: -f7)
		[ -n "$ls_shell" ] && login_shell="$ls_shell"
	fi
	case "$(basename "${login_shell:-sh}")" in
		zsh) profile="${HOME}/.zshrc" ;;
		bash) profile="${HOME}/.bashrc" ;;
		*) profile="${HOME}/.profile" ;;
	esac
	if [ -f "$profile" ] && grep -qF "$dir" "$profile" 2>/dev/null; then
		info "${dir} already on PATH in ${profile}"
		return
	fi
	printf '\n# added by dexaflow install.sh\nexport PATH="%s:$PATH"\n' "$dir" >>"$profile" || {
		info "could not update ${profile}; add this to your shell profile:"
		printf '    export PATH="%s:$PATH"\n' "$dir"
		return
	}
	info "added ${dir} to PATH in ${profile}"
	PATH_PROFILE="$profile"
}

case ":${PATH}:" in
	*":${INSTALL_DIR}:"*) ;;
	*)
		if [ "${DEXAFLOW_NO_PATH:-${LEOFLOW_NO_PATH:-}}" = "1" ]; then
			info "add ${INSTALL_DIR} to your PATH:"
			printf '    export PATH="%s:$PATH"\n' "$INSTALL_DIR"
		else
			add_to_profile "$INSTALL_DIR"
		fi
		# Make the CLI usable for the rest of this script too.
		export PATH="${INSTALL_DIR}:${PATH}"
		;;
esac

# ── Bootstrap the managed runtime ──
if [ "${DEXAFLOW_NO_SETUP:-${LEOFLOW_NO_SETUP:-}}" = "1" ]; then
	info "skipping setup (DEXAFLOW_NO_SETUP=1); run '${INSTALL_DIR}/${name} setup' when ready"
else
	info "running '${name} setup'..."
	# Attach the controlling terminal so the interactive setup wizard (where your
	# DAGs live, how tasks run, the admin login) can prompt even under `curl | sh`,
	# where stdin is the piped script, not a TTY. With no terminal (CI) or
	# LEOFLOW_NONINTERACTIVE=1, fall back to non-interactive defaults.
	#
	# Probe by actually OPENING /dev/tty in a subshell — a `[ -r /dev/tty ]` test
	# passes on the device node's permissions even in a CI container that has no
	# controlling terminal, where the `</dev/tty` redirect then fails ("No such
	# device or address") and aborts the installer.
	if [ "${DEXAFLOW_NONINTERACTIVE:-${LEOFLOW_NONINTERACTIVE:-}}" != "1" ] && (exec 3</dev/tty) 2>/dev/null; then
		"${INSTALL_DIR}/${name}" setup </dev/tty
	else
		"${INSTALL_DIR}/${name}" setup
	fi
fi

printf '\n'
info "Dexaflow Lite is installed."
if [ -n "${PATH_PROFILE:-}" ]; then
	info "next steps:"
	printf '    1) reload your shell:  source %s   (or open a new terminal)\n' "$PATH_PROFILE"
	printf '    2) start Dexaflow:      %s lite\n' "$name"
else
	info "next: ${name} lite"
fi
printf '    %s lite scaffolds a starter DAG (if needed), prints the URL + login,\n' "$name"
printf '    and hot-reloads on save. Add --host 0.0.0.0 to reach it from your network.\n'
