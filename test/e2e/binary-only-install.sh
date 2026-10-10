#!/usr/bin/env bash
# Genuine binary-only install of Dexaflow (#587): a built binary run from OUTSIDE
# the repo, with a clean HOME and NO PYTHONPATH — the real "download the release
# and run it" UX. Every other e2e job presets `PYTHONPATH: parser` (repo path),
# which masks the exact parser-resolution path this exercises.
#
# Asserts:
#   1. `dexaflow compile` resolves the embedded parser with no PYTHONPATH and no
#      repo on any path (the primary symptom: `No module named leoflow_parser`).
#   2. the binary self-extracts BOTH the parser AND the runtime sources the Lite
#      per-DAG venv is built from — the venv-before-extract ordering precondition
#      (the second symptom: `'<home>/.dexaflow/pysrc/runtime/python' does not exist`).
#
# No Postgres/Redis/agent needed: the failure was in resolution + extraction, not
# in any runtime state, so this gate stays fast and deterministic.
set -euo pipefail

fail() { printf '  \033[31mFAIL\033[0m %b\n' "$1"; exit 1; }
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
BINDIR="$(mktemp -d)"
HOME_DIR="$(mktemp -d)"
WORK="$(mktemp -d)"
cleanup() { chmod -R u+w "$HOME_DIR" 2>/dev/null || true; rm -rf "$BINDIR" "$HOME_DIR" "$WORK"; }
trap cleanup EXIT

echo "==> building the leoflow binary (parser + runtime embedded)"
( cd "$REPO" && go build -o "$BINDIR/leoflow" ./cmd/dexaflow )

# The genuine binary-only environment: a clean HOME, NO PYTHONPATH, and a working
# directory OUTSIDE the repo so the repo-relative parser/ and runtime/python are
# never on any path. Only the binary's own extraction can satisfy the parser.
unset PYTHONPATH
export HOME="$HOME_DIR"
cd "$WORK"

echo "==> dexaflow init + compile (no PYTHONPATH, outside the repo)"
"$BINDIR/leoflow" init proj >/dev/null || fail "dexaflow init failed"
"$BINDIR/leoflow" compile proj --output proj/dag.json --image test:v1 \
  >compile.log 2>&1 || fail "dexaflow compile failed:\n$(cat compile.log)"
grep -q '"dag_id"' proj/dag.json \
  || fail "compile produced no valid dag.json:\n$(cat compile.log)"
pass "compile resolved the embedded parser without PYTHONPATH"

echo "==> asserting the binary self-extracted parser + runtime sources"
[ -d "$HOME_DIR/.dexaflow/pysrc/parser/leoflow_parser" ] \
  || fail "parser sources not extracted under ~/.dexaflow/pysrc/parser"
[ -f "$HOME_DIR/.dexaflow/pysrc/runtime/python/pyproject.toml" ] \
  || fail "runtime sources not extracted — the Lite venv build would abort with 'does not exist'"
pass "binary extracted both parser and runtime sources for a binary-only install"

# The companion binaries must answer --version WITHOUT a runnable config (#593):
# an operator has to be able to ask a deployed binary its version. server/agent
# carry it in internal/version; leoflow-mcp in package main.
echo "==> building companion binaries and asserting they report a version"
( cd "$REPO" && go build \
    -ldflags "-X github.com/dexadata/dexaflow/internal/version.version=e2e-ver -X main.version=e2e-ver" \
    -o "$BINDIR/leoflow-server" ./cmd/dexaflow-server )
( cd "$REPO" && go build \
    -ldflags "-X github.com/dexadata/dexaflow/internal/version.version=e2e-ver" \
    -o "$BINDIR/leoflow-agent" ./cmd/dexaflow-agent )
( cd "$REPO" && go build -ldflags "-X main.version=e2e-ver" \
    -o "$BINDIR/leoflow-mcp" ./cmd/dexaflow-mcp )
for b in leoflow-server leoflow-agent leoflow-mcp; do
  out="$("$BINDIR/$b" --version 2>&1)" || fail "$b --version exited non-zero: $out"
  printf '%s' "$out" | grep -q 'e2e-ver' \
    || fail "$b --version did not report the injected version (got: $out)"
done
pass "leoflow-server/agent/mcp all answer --version and exit 0"

# --help must print usage and exit 0 WITHOUT trying to boot (#593). Guard against
# the regression where --help fell through to a config load / connect that errored.
echo "==> asserting --help prints usage and exits 0 (no boot attempt)"
for b in leoflow-server leoflow-agent leoflow-mcp; do
  out="$("$BINDIR/$b" --help 2>&1)" || fail "$b --help exited non-zero: $out"
  printf '%s' "$out" | grep -qiE 'usage|flags|leoflow-' \
    || fail "$b --help did not print usage (got: $out)"
  printf '%s' "$out" | grep -qiE 'required|connecting to control plane|jwt.secret' \
    && fail "$b --help attempted to boot instead of printing help (got: $out)"
done
pass "leoflow-server/agent/mcp all print --help usage and exit 0"

echo "ALL PASS"
