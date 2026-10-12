#!/usr/bin/env bash
# First-start setup of the devcontainer (#1574), run once by postCreateCommand
# from the repository root: the Go tools at the Makefile's pins, the Python
# parser and runtime, the docs site's npm packages, the helm-unittest plugin at
# helm-ci.yaml's pin, and both databases migrated. Afterwards `make lint test`
# and `make ci-local` run with nothing else to install.
set -euo pipefail
cd "$(dirname "$0")/.."

# git refuses to work in a bind-mounted tree owned by another uid unless told
# it is safe; the Makefile's version stamp and the script gates call git.
git config --global --add safe.directory "$PWD"

make setup

unittest="$(sed -n 's/.*helm plugin install --version \(v[0-9][0-9.]*\) .*helm-unittest.*/\1/p' .github/workflows/helm-ci.yaml | head -n1)"
if [ -z "$unittest" ]; then
	echo "post-create: could not read the helm-unittest version from helm-ci.yaml" >&2
	exit 1
fi
helm plugin list 2>/dev/null | grep -q unittest ||
	helm plugin install --version "$unittest" --verify=false https://github.com/helm-unittest/helm-unittest

npm --prefix website ci

# Postgres is healthy before this container starts (docker-compose.yml).
make migrate-up
migrate -path migrations -database "postgres://leoflow:leoflow@localhost:5432/leoflow_test?sslmode=disable" up

echo "post-create: ready. Try: make lint test, or make ci-local"
