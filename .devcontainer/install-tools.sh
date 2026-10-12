#!/usr/bin/env bash
# Installs the toolchain of the devcontainer image (#1574) at the versions the
# repository already pins, read from the file that pins each one, so the
# container never carries a version of its own that could drift:
#
#   Go            go.mod `toolchain` line
#   Hugo          .github/workflows/website-build.yml env.HUGO_VERSION
#   Helm          .github/workflows/helm-ci.yaml azure/setup-helm `version`
#   k3d           .github/workflows/ci.yaml `TAG=` of the k3d install script
#
# The Go tools (golangci-lint, changie, govulncheck, migrate, ...) are not
# installed here: post-create.sh runs `make setup`, which installs them at the
# Makefile's pins. A version that cannot be read fails the build loudly rather
# than installing whatever is newest.
#
# Usage (as root, from the repository root): .devcontainer/install-tools.sh
#        .devcontainer/install-tools.sh --versions   # print what would install
#        .devcontainer/install-tools.sh --self-test
set -euo pipefail

# pin <name> <value>: fails when a pin could not be read.
pin() {
	if [ -z "$2" ]; then
		echo "install-tools: could not read the $1 version from its source file" >&2
		return 1
	fi
	printf '%s\n' "$2"
}

go_version() { # <root>
	pin Go "$(sed -n 's/^toolchain go\([0-9][0-9.]*\)[[:space:]]*$/\1/p' "$1/go.mod")"
}
hugo_version() { # <root>
	pin Hugo "$(sed -n 's/^  HUGO_VERSION: "\([0-9][0-9.]*\)"[[:space:]]*$/\1/p' "$1/.github/workflows/website-build.yml" | head -n1)"
}
helm_version() { # <root>
	pin Helm "$(grep -A2 'uses: azure/setup-helm@' "$1/.github/workflows/helm-ci.yaml" |
		sed -n 's/^[[:space:]]*version: \(v[0-9][0-9.]*\)[[:space:]]*$/\1/p' | head -n1)"
}
k3d_version() { # <root>
	pin k3d "$(sed -n 's/.*k3d-io\/k3d\/\(v[0-9][0-9.]*\)\/install\.sh.*/\1/p' "$1/.github/workflows/ci.yaml" | head -n1)"
}

install_all() { # <root>
	local root="$1" arch go hugo helm k3d
	case "$(uname -m)" in
	x86_64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*)
		echo "install-tools: unsupported architecture $(uname -m)" >&2
		return 1
		;;
	esac
	go="$(go_version "$root")"
	hugo="$(hugo_version "$root")"
	helm="$(helm_version "$root")"
	k3d="$(k3d_version "$root")"

	echo "install-tools: Go $go"
	curl -fsSL "https://go.dev/dl/go${go}.linux-${arch}.tar.gz" | tar -C /usr/local -xz

	echo "install-tools: Hugo extended $hugo"
	curl -fsSL "https://github.com/gohugoio/hugo/releases/download/v${hugo}/hugo_extended_${hugo}_linux-${arch}.tar.gz" |
		tar -C /usr/local/bin -xz hugo

	echo "install-tools: Helm $helm"
	curl -fsSL "https://get.helm.sh/helm-${helm}-linux-${arch}.tar.gz" |
		tar -C /usr/local/bin -xz --strip-components=1 "linux-${arch}/helm"

	echo "install-tools: k3d $k3d"
	curl -fsSL -o /usr/local/bin/k3d "https://github.com/k3d-io/k3d/releases/download/${k3d}/k3d-linux-${arch}"
	chmod 0755 /usr/local/bin/k3d
}

self_test() {
	local fail=0 tmp got
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN
	mkdir -p "$tmp/.github/workflows"
	printf 'module x\n\ngo 1.26.0\n\ntoolchain go1.26.9\n' >"$tmp/go.mod"
	printf 'env:\n  HUGO_VERSION: "0.165.0"\n' >"$tmp/.github/workflows/website-build.yml"
	printf '    steps:\n      - uses: azure/setup-helm@abc # v5.0.1\n        with:\n          version: v4.2.0\n' >"$tmp/.github/workflows/helm-ci.yaml"
	printf '          curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/v5.7.4/install.sh | TAG=v5.7.4 bash\n' >"$tmp/.github/workflows/ci.yaml"

	_expect() { # <name> <want> <function>
		if got="$("$3" "$tmp" 2>&1)" && [ "$got" = "$2" ]; then
			echo "  ok   $1"
		else
			printf '  FAIL %s: got %q, want %q\n' "$1" "$got" "$2"
			fail=1
		fi
	}
	_expect "Go from go.mod's toolchain line" 1.26.9 go_version
	_expect "Hugo from website-build.yml" 0.165.0 hugo_version
	_expect "Helm from helm-ci.yaml" v4.2.0 helm_version
	_expect "k3d from ci.yaml" v5.7.4 k3d_version

	printf 'module x\n\ngo 1.26.0\n' >"$tmp/go.mod"
	if go_version "$tmp" >/dev/null 2>&1; then
		echo "  FAIL a missing toolchain line must fail, not install the newest Go"
		fail=1
	else
		echo "  ok   a missing pin fails"
	fi

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

case "${1:-}" in
--self-test)
	self_test
	exit $?
	;;
--versions)
	root="$(cd "$(dirname "$0")/.." && pwd)"
	printf 'Go %s\nHugo %s\nHelm %s\nk3d %s\n' "$(go_version "$root")" "$(hugo_version "$root")" \
		"$(helm_version "$root")" "$(k3d_version "$root")"
	;;
*)
	install_all "$(cd "$(dirname "$0")/.." && pwd)"
	;;
esac
