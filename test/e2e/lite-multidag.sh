#!/usr/bin/env bash
# E2E for the multi-DAG materialization contract.
#
# Lima 2026-06-01 shipped v0.0.1-prealpha.21 with multi-DAG workspaces wired
# (PR #246) but the parser branch that captured dag.py contents into the
# spec's `source` field (PR #251) was orphaned and didn't merge with it.
# Every task on Lima failed with `ModuleNotFoundError: No module named 'dag'`
# because the subprocess executor's materialize step saw an empty Source.
#
# This script is the regression guard: it builds the CLI, scaffolds a subdir
# DAG project, runs `leoflow compile`, and asserts the resulting dag.json
# carries the source verbatim. No Postgres, no Redis, no agent — just the
# parser→spec contract that multi-DAG depends on. Fast (~3-5 s in CI), so it
# runs on every PR.
#
# Run from the repo root:  bash test/e2e/lite-multidag.sh
set -euo pipefail

HOME_DIR="$(mktemp -d)"
WS="$HOME_DIR/ws"

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; exit 1; }
cleanup() { rm -rf "$HOME_DIR"; }
trap cleanup EXIT

echo "==> building leoflow"
go build -o "$HOME_DIR/leoflow" ./cmd/dexaflow

echo "==> scaffolding a multi-DAG workspace (one DAG in a subdir)"
mkdir -p "$WS/hello"
cat > "$WS/hello/dexaflow.yaml" <<'EOF'
dag_id: hello
python_version: "3.11"
EOF
# Distinguishable content the source-roundtrip assertion will match exactly.
cat > "$WS/hello/dag.py" <<'EOF'
from airflow.sdk import DAG, task


@task
def t() -> str:
    return "ok-from-multidag-e2e"


with DAG("hello", schedule=None):
    t()
EOF

echo "==> compiling the subdir DAG"
OUT="$HOME_DIR/hello.json"
"$HOME_DIR/leoflow" compile "$WS/hello" --output "$OUT" >"$HOME_DIR/compile.log" 2>&1 \
  || fail "leoflow compile failed:\n$(cat "$HOME_DIR/compile.log")"
[ -s "$OUT" ] || fail "compile produced an empty dag.json"
pass "compile produced a non-empty dag.json"

# ─── Load-bearing assertion ──────────────────────────────────────────────
# The materialization contract: dag.json MUST embed dag.py contents in the
# `source` field. The subprocess executor reads `Source` from the resolved
# task spec and writes it to a per-TI temp dir, so the spawned agent's
# `python -m leoflow_runtime dag:<task>` finds the module. An empty Source
# means materialize is a no-op and the agent's CWD is the workspace root,
# where `import dag` resolves to nothing — the Lima failure mode.
# ──────────────────────────────────────────────────────────────────────────
echo "==> contract: dag.json.source carries the DAG source"
SOURCE_LEN=$(python3 -c "import json; print(len(json.load(open('$OUT')).get('source', '')))")
if [ "$SOURCE_LEN" -lt 50 ]; then
  fail "dag.json.source is empty/missing (len=$SOURCE_LEN). The subprocess executor cannot materialize → 'ModuleNotFoundError: No module named dag' for any subdir DAG. This is the Lima v0.0.1-prealpha.21 regression."
fi
pass "dag.json.source is non-trivial ($SOURCE_LEN bytes)"

echo "==> the source matches dag.py byte-for-byte"
DAG_PY="$(cat "$WS/hello/dag.py")"
JSON_SRC="$(python3 -c "import json; print(json.load(open('$OUT'))['source'], end='')")"
[ "$DAG_PY" = "$JSON_SRC" ] || fail "dag.json.source does not match dag.py contents"
pass "dag.json.source is byte-for-byte dag.py"

echo "==> sanity: a second subdir DAG compiles independently (multi-DAG layout)"
mkdir -p "$WS/second"
cat > "$WS/second/dexaflow.yaml" <<'EOF'
dag_id: second
python_version: "3.11"
EOF
cat > "$WS/second/dag.py" <<'EOF'
from airflow.sdk import DAG, task
@task
def s2() -> str:
    return "second"
with DAG("second", schedule=None):
    s2()
EOF
"$HOME_DIR/leoflow" compile "$WS/second" --output "$HOME_DIR/second.json" >"$HOME_DIR/compile-2.log" 2>&1 \
  || fail "second subdir compile failed:\n$(cat "$HOME_DIR/compile-2.log")"
SECOND_LEN=$(python3 -c "import json; print(len(json.load(open('$HOME_DIR/second.json')).get('source', '')))")
[ "$SECOND_LEN" -gt 50 ] || fail "second dag.json.source is empty"
pass "second subdir DAG also carries source (multi-DAG project independence)"

echo
echo "  ✅ Multi-DAG materialization contract verified."
