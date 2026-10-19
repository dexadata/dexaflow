#!/usr/bin/env bash
# Finish the devcontainer (#1574): the tools whose versions the repository
# already pins, read from where they are pinned, so a bump in one place is
# the only bump.
#
#   Go tools, changie, the parser and runtime, the git hooks  make setup (Makefile pins)
#   Hugo extended                                             HUGO_VERSION in website-build.yml
#   k3d                                                       the TAG ci.yaml installs
#   the website's PostCSS toolchain                           website/package-lock.json
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

gopath=$(go env GOPATH)
export PATH="$gopath/bin:$HOME/.local/bin:$PATH"

echo "==> make setup"
make setup

arch=$(dpkg --print-architecture)

hugo=$(sed -n 's/^[[:space:]]*HUGO_VERSION:[[:space:]]*"\{0,1\}\([0-9.]*\)"\{0,1\}[[:space:]]*$/\1/p' .github/workflows/website-build.yml | head -n1)
[ -n "$hugo" ] || { echo "no HUGO_VERSION in .github/workflows/website-build.yml" >&2; exit 1; }
echo "==> hugo extended ${hugo}"
tmp=$(mktemp -d)
curl -fsSL -o "$tmp/hugo.deb" "https://github.com/gohugoio/hugo/releases/download/v${hugo}/hugo_extended_${hugo}_linux-${arch}.deb"
sudo dpkg -i "$tmp/hugo.deb"
rm -rf "$tmp"

k3d=$(grep -o 'TAG=v[0-9][0-9.]*' .github/workflows/ci.yaml | head -n1 | cut -d= -f2)
[ -n "$k3d" ] || { echo "no k3d TAG in .github/workflows/ci.yaml" >&2; exit 1; }
echo "==> k3d ${k3d}"
curl -fsSL "https://raw.githubusercontent.com/k3d-io/k3d/${k3d}/install.sh" | TAG="$k3d" bash

echo "==> website toolchain"
npm --prefix website ci

echo "==> ready: make lint test, make ci-local"
