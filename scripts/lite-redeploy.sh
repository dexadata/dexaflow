#!/usr/bin/env bash
# lite-redeploy.sh — local Mac/Linux dev loop for `dexaflow lite`.
#
# Rebuilds leoflow / leoflow-server / leoflow-agent from the current source,
# stops the running lite (if any), swaps the binaries in BOTH places lite
# resolves them from (./bin and ~/.dexaflow/bin — see resolveBinary in
# internal/cli/dev.go), and restarts `dexaflow lite --postgres managed`. Polls
# /readyz and prints the URL + a tail of the boot log.
#
# Why both dirs? `dexaflow lite` shells out to `leoflow-server` and resolves it
# via: explicit flag → PATH → ./bin/leoflow-server. Same for leoflow-agent
# (subprocess executor's agent_path). If only one is updated, the OTHER stale
# binary silently runs and the loop debugging gets confusing.
#
# Usage:
#   scripts/lite-redeploy.sh           # rebuild, restart, tail
#   PORT=9090 scripts/lite-redeploy.sh # custom port
#
# Stop with: kill $(cat /tmp/leoflow-lite.pid)
set -euo pipefail

PORT="${PORT:-8088}"
LOG_LEVEL="${LOG_LEVEL:-info}"
LOG_FILE="/tmp/leoflow-lite.log"
PID_FILE="/tmp/leoflow-lite.pid"
BUILD_DIR="/tmp/leoflow-dev"

cd "$(git rev-parse --show-toplevel)"

echo "==> building binaries (linux/darwin native)…"
mkdir -p "$BUILD_DIR" bin
go build -trimpath -o "$BUILD_DIR/leoflow" ./cmd/dexaflow &
go build -trimpath -o "$BUILD_DIR/leoflow-server" ./cmd/dexaflow-server &
go build -trimpath -o "$BUILD_DIR/leoflow-agent" ./cmd/dexaflow-agent &
wait

# macOS Sequoia (14+) refuses to run a freshly-produced binary that lacks
# a code signature OR carries com.apple.provenance — the process is killed
# at exec with SIGKILL (exit 137) and no output. Strip the attr and ad-hoc
# sign so the binary runs locally. No-op on Linux (xattr/codesign absent).
if [ "$(uname -s)" = "Darwin" ]; then
  echo "==> ad-hoc signing for macOS…"
  for f in "$BUILD_DIR"/leoflow "$BUILD_DIR"/leoflow-server "$BUILD_DIR"/leoflow-agent; do
    xattr -c "$f" 2>/dev/null || true
    codesign --force -s - "$f" >/dev/null 2>&1 || true
  done
fi

echo "==> stopping any running lite…"
if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
  kill "$(cat "$PID_FILE")" || true
  sleep 2
fi
# Also catch a manually-started one, under either name.
pkill -f "(dexaflow|leoflow) lite" 2>/dev/null || true
sleep 1

echo "==> swapping binaries…"
# Both locations lite resolves from. Keep them in lockstep so the dev loop
# is unambiguous regardless of which dir was picked.
for dst in bin "$HOME/.dexaflow/bin"; do
  mkdir -p "$dst"
  cp "$BUILD_DIR/leoflow"         "$dst/leoflow"
  cp "$BUILD_DIR/leoflow-server"  "$dst/leoflow-server"
  cp "$BUILD_DIR/leoflow-agent"   "$dst/leoflow-agent"
  # `cp` on macOS resurrects com.apple.provenance from the source path's
  # attrs and the binary loses its signature, so re-strip + re-sign at the
  # destination too. (Same SIGKILL/exit-137 failure mode as above.)
  if [ "$(uname -s)" = "Darwin" ]; then
    for f in "$dst"/leoflow "$dst"/leoflow-server "$dst"/leoflow-agent; do
      xattr -c "$f" 2>/dev/null || true
      codesign --force -s - "$f" >/dev/null 2>&1 || true
    done
  fi
done

echo "==> starting lite (port $PORT, log-level $LOG_LEVEL)…"
nohup ./bin/dexaflow lite --postgres managed --port "$PORT" --log-level "$LOG_LEVEL" \
  >"$LOG_FILE" 2>&1 &
echo $! >"$PID_FILE"
LITE_PID="$(cat "$PID_FILE")"

echo "==> polling /readyz…"
ready=0
for i in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:$PORT/readyz" >/dev/null 2>&1; then
    ready=1
    echo "✓ /readyz responded after ${i}s"
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo "::error::/readyz never responded after 60s — boot log tail:"
  tail -50 "$LOG_FILE"
  exit 1
fi

cat <<EOF

============================================================
  dexaflow lite is up.
  URL:    http://localhost:$PORT
  PID:    $LITE_PID  (cat $PID_FILE)
  log:    tail -f $LOG_FILE
  stop:   kill \$(cat $PID_FILE)
============================================================
EOF
