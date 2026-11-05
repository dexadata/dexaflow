#!/usr/bin/env bash
# Installs the pinned Playwright Chromium for the CI browser jobs (ui-smoke,
# e2e-sso-login), and its OS libraries only when the runner lacks one.
#
# Both jobs used `npx playwright install --with-deps chromium`. On the GitHub
# ubuntu-24.04 image every shared library Chromium links against is already
# installed, so --with-deps only ever apt-got fonts: fonts-ipafont-gothic,
# fonts-wqy-zenhei, fonts-unifont, fonts-tlwg-loma-otf, fonts-freefont-ttf and
# the xfonts packages, about 21 MB. No test renders CJK or Thai text or compares
# pixels. The step normally takes about 15s, but when the Ubuntu archive mirror
# stalls mid download the apt-get hangs until the step's 6 minute ceiling, and
# that turned three merge queue and PR runs red in two days (runs 41857104833,
# 41868894340, 42038536960).
#
# So the browser comes from Playwright (cached by the jobs under
# ~/.cache/ms-playwright), and apt runs only when `ldd` reports a library the
# browser cannot resolve, which is what a new runner image or a Playwright bump
# needing a new library would look like. That fallback gets apt retries and its
# own timeout, so a stalled mirror fails fast and names the cause.
#
# Usage: scripts/ci-install-playwright-deps.sh [--self-test]
# Expects playwright installed in the current npm project (the job does that).
set -euo pipefail

# Reads `ldd` output on stdin and prints each unresolved library, once.
missing_libs() {
	awk '/=> not found/ { print $1 }' | sort -u
}

browser_binaries() {
	local root="${PLAYWRIGHT_BROWSERS_PATH:-$HOME/.cache/ms-playwright}"
	find "$root" -type f \( -name chrome -o -name chrome-headless-shell \) 2>/dev/null
}

self_test() {
	local got
	got="$(printf '%s\n' \
		'	linux-vdso.so.1 (0x00007ffd)' \
		'	libnss3.so => /lib/x86_64-linux-gnu/libnss3.so (0x00007f)' \
		'	libgbm.so.1 => not found' \
		'	libasound.so.2 => not found' \
		'	libgbm.so.1 => not found' | missing_libs)"
	[ "$got" = "$(printf 'libasound.so.2\nlibgbm.so.1')" ] || {
		echo "FAIL: missing_libs reported: $got"
		return 1
	}
	got="$(printf '\tlibc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x00007f)\n' | missing_libs)"
	[ -z "$got" ] || {
		echo "FAIL: missing_libs flagged a resolved library: $got"
		return 1
	}
	echo "OK: missing_libs"
}

main() {
	npx playwright install chromium

	local bins bin out missing=""
	bins="$(browser_binaries)"
	[ -n "$bins" ] || {
		echo "no Chromium binary under the Playwright browsers path after install" >&2
		exit 1
	}
	while IFS= read -r bin; do
		# An ldd that cannot read the binary proves nothing, so it counts as
		# missing and takes the apt path rather than skipping it.
		if ! out="$(ldd "$bin" 2>&1)"; then
			missing+="(ldd failed on $bin: $out)"$'\n'
			continue
		fi
		missing+="$(printf '%s\n' "$out" | missing_libs)"$'\n'
	done <<<"$bins"
	missing="$(printf '%s' "$missing" | sed '/^$/d')"
	if [ -z "$missing" ]; then
		echo "every library Chromium links against is on the runner; skipping apt"
		return 0
	fi

	echo "Chromium cannot resolve these libraries, installing its OS dependencies:"
	echo "$missing"
	printf 'Acquire::Retries "3";\nAcquire::http::Timeout "30";\nAcquire::https::Timeout "30";\n' |
		sudo tee /etc/apt/apt.conf.d/80-ci-retries >/dev/null
	timeout --kill-after=15 240 sudo -E env "PATH=$PATH" npx playwright install-deps chromium
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
else
	main
fi
