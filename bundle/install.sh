#!/usr/bin/env bash
# Leoflow Lite bundle installer.
#
# One-shot installer for the Leoflow Lite hands-on validation flow. Downloads
# the latest pre-release binary (via the canonical install.sh), runs `leoflow setup`
# `leoflow setup` (which generates the admin password — printed in CYAN on
# the terminal, save it), drops the curated DAG bundle into the workspace,
# and prints the next-step commands.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dexadata/leoflow/main/bundle/install.sh | bash
# or, if you cloned the repo:
#   bash bundle/install.sh

set -euo pipefail

# ─── Colors (with TTY detection) ─────────────────────────────────────────────
if [[ -t 1 ]]; then
  CYAN=$'\e[36m'; YELLOW=$'\e[33m'; GREEN=$'\e[32m'; BOLD=$'\e[1m'; RESET=$'\e[0m'
else
  CYAN=''; YELLOW=''; GREEN=''; BOLD=''; RESET=''
fi

echo "${BOLD}Leoflow Lite bundle installer${RESET}"
echo

# ─── 1. Detect OS / arch ────────────────────────────────────────────────────
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
if [[ "$OS" != "linux" && "$OS" != "darwin" ]]; then
  echo "unsupported OS: $OS (Lite targets linux; macOS works for spot-checks)" >&2
  exit 1
fi

# ─── 2. Resolve the source dir for the DAGs ─────────────────────────────────
# When this script is run via `bash bundle/install.sh` from a checkout,
# the DAGs sit next to it. When piped from curl, we re-fetch them from the
# same repo+commit the script came from (BUNDLE_REPO + BUNDLE_REF defaults
# below; override via env if you're testing a branch).
BUNDLE_REPO="${BUNDLE_REPO:-dexadata/leoflow}"
BUNDLE_REF="${BUNDLE_REF:-main}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo "")"
if [[ -n "$SCRIPT_DIR" && -d "$SCRIPT_DIR/dags" ]]; then
  DAG_SOURCE="$SCRIPT_DIR/dags"
  echo "DAG source: ${DAG_SOURCE} (local checkout)"
else
  TMPDIR="$(mktemp -d)"
  trap 'rm -rf "$TMPDIR"' EXIT
  echo "Fetching DAG bundle from github.com/${BUNDLE_REPO}@${BUNDLE_REF}..."
  curl -fsSL "https://codeload.github.com/${BUNDLE_REPO}/tar.gz/${BUNDLE_REF}" \
    | tar -xz -C "$TMPDIR" --strip-components=1 \
    "$(basename "$BUNDLE_REPO")-${BUNDLE_REF}/bundle/dags" \
    "$(basename "$BUNDLE_REPO")-${BUNDLE_REF}/examples" 2>/dev/null \
    || { echo "could not fetch DAG bundle"; exit 1; }
  DAG_SOURCE="$TMPDIR/bundle/dags"
  EXAMPLES_SOURCE="$TMPDIR/examples"
fi

# ─── 3. Install the leoflow binary ───────────────────────────────────────────
# Delegate to the top-level install.sh — the canonical, tested, hostile-locale-
# smoked installer. This wrapper used to re-implement the release resolution
# inline and shipped the bug "404 on /releases/latest when every release is a
# pre-release" (2026-06-01). Calling install.sh keeps one source of truth so a
# future fix benefits both `curl … install.sh | sh` and this bundle path.
echo
echo "${BOLD}1. Installing leoflow binary${RESET}"
INSTALL_SH_URL="https://raw.githubusercontent.com/${BUNDLE_REPO}/${BUNDLE_REF}/install.sh"
# Pass-through env vars: LEOFLOW_VERSION pins the release tag (the upstream
# script's name for what we used to call LEOFLOW_RELEASE_TAG).
LEOFLOW_VERSION_PASS="${LEOFLOW_RELEASE_TAG:-${LEOFLOW_VERSION:-}}"
if [[ -n "$LEOFLOW_VERSION_PASS" ]]; then
  curl -fsSL "$INSTALL_SH_URL" | LEOFLOW_VERSION="$LEOFLOW_VERSION_PASS" sh
else
  curl -fsSL "$INSTALL_SH_URL" | sh
fi
# install.sh drops the binary into ~/.local/bin (or /usr/local/bin via PATH
# probe); make it discoverable in this shell session so the setup step below
# finds it.
export PATH="${HOME}/.local/bin:/usr/local/bin:$PATH"
echo "  installed: $(leoflow version 2>&1 | head -1)"

# ─── 4. Run setup (generates admin password — captured in setup's output) ───
echo
echo "${BOLD}2. Running leoflow setup (generates the admin password)${RESET}"
SETUP_LOG="$(mktemp)"
trap 'rm -f "$SETUP_LOG"' EXIT
if ! leoflow setup 2>&1 | tee "$SETUP_LOG"; then
  echo "${YELLOW}setup did not complete cleanly; check the output above${RESET}" >&2
  exit 1
fi

# Try to extract the printed credentials block from the log. setup prints
# `user:` / `password:` / `open:` lines on first install only (a re-run
# preserves the existing config and prints "already configured").
PASSWORD_LINE="$(grep -E '^\s+password:' "$SETUP_LOG" | head -1 || true)"

# ─── 5. Copy the DAG bundle into the workspace ──────────────────────────────
WORKSPACE="${HOME}/leoflow"
mkdir -p "$WORKSPACE"
echo
echo "${BOLD}3. Copying the DAG bundle into ${WORKSPACE}${RESET}"
cp -R "$DAG_SOURCE/." "$WORKSPACE/"
echo "  bundled DAGs ($(ls "$DAG_SOURCE" | wc -l | tr -d ' ')) copied"

# Curate a useful subset of /examples too. Skip the connector DAGs that
# need external services pre-configured (the user can copy those by hand
# after creating Connections in the UI).
if [[ -n "${EXAMPLES_SOURCE:-}" ]] || [[ -d "$SCRIPT_DIR/../examples" ]]; then
  EX_SRC="${EXAMPLES_SOURCE:-$SCRIPT_DIR/../examples}"
  for dag in lifecycle montecarlo_pi fan_out_aggregate ml_hparam_search taskflow_sales; do
    if [[ -d "$EX_SRC/$dag" ]]; then
      cp -R "$EX_SRC/$dag" "$WORKSPACE/"
      echo "  example DAG copied: $dag"
    fi
  done
fi

# ─── 6. Print the next-step block ───────────────────────────────────────────
echo
echo "${BOLD}${GREEN}═══ All set ═══${RESET}"
echo
if [[ -n "$PASSWORD_LINE" ]]; then
  echo "${BOLD}Save these credentials (the password is shown ONCE):${RESET}"
  echo
  echo "  user:     admin@leoflow.local"
  echo "  ${YELLOW}${PASSWORD_LINE}${RESET}"
  echo "  open:     http://localhost:8088"
  echo
  echo "  Lost the password? Run: ${CYAN}sudo leoflow lite reset-password${RESET}"
else
  echo "${YELLOW}(setup re-ran on an existing install; the password above was preserved.)${RESET}"
  echo "  Lost it? Run: ${CYAN}sudo leoflow lite reset-password${RESET}"
fi
echo
echo "${BOLD}DAGs bundled in ${WORKSPACE}:${RESET}"
ls -1 "$WORKSPACE" | sed 's/^/  - /'
echo
echo "${BOLD}Start Leoflow Lite:${RESET}"
echo "  ${CYAN}leoflow lite${RESET}"
echo
echo "  (then open http://localhost:8088 — or from your Mac host:"
echo "   http://<host-ip>:8088 (use `leoflow lite --host 0.0.0.0`))"
