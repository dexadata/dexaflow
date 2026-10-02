#!/usr/bin/env bash
#
# install.sh: schedule the soak battery to run periodically, LOCALLY.
#
# The long runs must not go on a hosted CI runner. A 48 h soak is 2880
# runner-minutes per occurrence; on a private repository's 2000-minute monthly
# allowance that is one weekend over budget, and a weekly cadence bills roughly
# 9520 minutes per month, about USD 76 at the published Linux rate, on a personal
# card, for a job whose entire premise is hardware we already own. It also gives a
# worse measurement: a hosted runner is destroyed per job, so nothing about
# long-lived process behavior survives. See test/soak/README.md section 6.4.
#
# This writes a launchd agent (macOS) or a systemd user timer (Linux) into the
# OPERATOR'S OWN HOME. Nothing it generates is committed, and nothing it
# generates or that lives in the repository names an account, a hostname, a path
# outside this checkout, or a credential.
#
#   bash test/soak/schedule/install.sh --duration 12h --at 22:00
#   bash test/soak/schedule/install.sh --uninstall
#
# Inspect what it would write first:
#   bash test/soak/schedule/install.sh --print
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
DURATION="12h"
AT="22:00"
FAULTS="standard"
LABEL="scheduled"
ACTION="install"
NAME="dev.leoflow.soak"

while [ $# -gt 0 ]; do
  case "$1" in
    --duration) DURATION="$2"; shift 2 ;;
    --at)       AT="$2"; shift 2 ;;
    --faults)   FAULTS="$2"; shift 2 ;;
    --label)    LABEL="$2"; shift 2 ;;
    --uninstall) ACTION="uninstall"; shift ;;
    --print)    ACTION="print"; shift ;;
    -h|--help)  sed -n '2,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

HOUR="${AT%%:*}"
MIN="${AT##*:}"
CMD="cd $ROOT && bash test/soak/soak.sh --duration $DURATION --faults $FAULTS --label $LABEL"

launchd_plist() {
  cat <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>${NAME}</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>-lc</string>
    <string>${CMD}</string>
  </array>
  <key>StartCalendarInterval</key>
  <dict>
    <key>Hour</key><integer>${HOUR}</integer>
    <key>Minute</key><integer>${MIN#0}</integer>
  </dict>
  <key>StandardOutPath</key><string>${ROOT}/.soak/scheduled.out.log</string>
  <key>StandardErrorPath</key><string>${ROOT}/.soak/scheduled.err.log</string>
  <key>RunAtLoad</key><false/>
</dict>
</plist>
PLIST
}

systemd_service() {
  cat <<UNIT
[Unit]
Description=Leoflow soak battery (bounded, local)

[Service]
Type=oneshot
WorkingDirectory=${ROOT}
ExecStart=/bin/bash -lc '${CMD}'
# The harness has its own wall-clock ceiling and watchdog; this is the third,
# independent stop, so a hung systemd job cannot become a resident process.
TimeoutStartSec=${DURATION}
UNIT
}

systemd_timer() {
  cat <<UNIT
[Unit]
Description=Leoflow soak battery schedule

[Timer]
OnCalendar=*-*-* ${HOUR}:${MIN}:00
Persistent=false

[Install]
WantedBy=timers.target
UNIT
}

case "$(uname -s)" in
  Darwin) TARGET="$HOME/Library/LaunchAgents/${NAME}.plist" ;;
  Linux)  TARGET="$HOME/.config/systemd/user/${NAME}.timer" ;;
  *) echo "unsupported platform $(uname -s); run soak.sh from your own scheduler" >&2; exit 2 ;;
esac

if [ "$ACTION" = "print" ]; then
  echo "# would write: $TARGET"
  case "$(uname -s)" in
    Darwin) launchd_plist ;;
    Linux)  echo "# --- ${NAME}.service ---"; systemd_service; echo "# --- ${NAME}.timer ---"; systemd_timer ;;
  esac
  exit 0
fi

if [ "$ACTION" = "uninstall" ]; then
  case "$(uname -s)" in
    Darwin)
      launchctl unload "$TARGET" 2>/dev/null || true
      rm -f "$TARGET"
      ;;
    Linux)
      systemctl --user disable --now "${NAME}.timer" 2>/dev/null || true
      rm -f "$HOME/.config/systemd/user/${NAME}.timer" "$HOME/.config/systemd/user/${NAME}.service"
      systemctl --user daemon-reload 2>/dev/null || true
      ;;
  esac
  echo "removed the ${NAME} schedule"
  exit 0
fi

mkdir -p "$(dirname "$TARGET")" "$ROOT/.soak"
case "$(uname -s)" in
  Darwin)
    launchd_plist > "$TARGET"
    launchctl unload "$TARGET" 2>/dev/null || true
    launchctl load "$TARGET"
    echo "installed launchd agent $TARGET (daily at ${AT}, ${DURATION}, faults=${FAULTS})"
    ;;
  Linux)
    systemd_service > "$HOME/.config/systemd/user/${NAME}.service"
    systemd_timer   > "$HOME/.config/systemd/user/${NAME}.timer"
    systemctl --user daemon-reload
    systemctl --user enable --now "${NAME}.timer"
    echo "installed systemd user timer ${NAME}.timer (daily at ${AT}, ${DURATION}, faults=${FAULTS})"
    ;;
esac
echo "evidence will land in ${ROOT}/.soak/"
echo "uninstall with: bash test/soak/schedule/install.sh --uninstall"
