#!/usr/bin/env sh
# Upgrade-in-place smoke for the release gate (#150 / ADR 0033).
#
# Installs the PREVIOUS non-draft prerelease, runs `leoflow version` to prove
# the old binary is healthy, then installs the CURRENT tag and asserts the new
# version is in place + the binary still runs. Catches the class of bug where
# install.sh handles a fresh box but breaks on top of an existing ~/.leoflow/
# (stale config, stale pysrc, leftover PATH entries, etc.) — the failure mode
# the user would otherwise hit on `leoflow upgrade`.
#
# Required env:
#   LEOFLOW_REPO     — "owner/name"     (typically ${GITHUB_REPOSITORY})
#   LEOFLOW_VERSION  — the just-pushed tag we are gating
#   GH_TOKEN         — used by `gh release list` (auto in GitHub Actions)
#
# Run in a clean container (the release workflow does this; locally use
# `docker run --rm -it -e LEOFLOW_REPO=... ubuntu:24.04 bash`).
set -eu

: "${LEOFLOW_REPO:?LEOFLOW_REPO must be set (e.g. dexadata/leoflow)}"
: "${LEOFLOW_VERSION:?LEOFLOW_VERSION must be set to the just-pushed tag}"

fail() {
  printf '  \033[31mFAIL\033[0m %s\n' "$1" >&2
  exit 1
}
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }

echo "==> resolving previous non-draft prerelease (excluding ${LEOFLOW_VERSION})"
# Page through enough releases to skip the just-published one and any drafts.
# `gh release list` orders newest-first, so the first non-draft, non-self entry
# is the previous prealpha.
PREVIOUS="$(gh release list \
  --repo "${LEOFLOW_REPO}" \
  --limit 20 \
  --json tagName,isDraft \
  --jq 'map(select(.isDraft == false and .tagName != "'"${LEOFLOW_VERSION}"'")) | .[0].tagName')"

if [ -z "${PREVIOUS}" ] || [ "${PREVIOUS}" = "null" ]; then
  echo "==> no previous non-draft prerelease available — skipping upgrade smoke"
  echo "    (first release on a fresh project; the next tag will exercise this gate)"
  pass "skipped (nothing to upgrade from)"
  exit 0
fi
echo "==> upgrading from ${PREVIOUS} → ${LEOFLOW_VERSION}"

echo "==> installing previous version (${PREVIOUS})"
# `VAR=value cmd1 | cmd2` only exports VAR to cmd1 — the piped sh runs
# without LEOFLOW_VERSION and install.sh falls back to "latest", which is
# the just-cut tag we are testing AGAINST, not the previous one. Export
# explicitly so install.sh inside the `| sh` pipeline sees it. (Previous
# silent breakage: prealpha.24 coincidentally matched because "latest"
# resolved to the previous tag at that moment.)
INSTALL_URL="https://raw.githubusercontent.com/${LEOFLOW_REPO}/${LEOFLOW_VERSION}/install.sh"
export LEOFLOW_VERSION_FOR_INSTALL="${PREVIOUS}"
LEOFLOW_VERSION="${LEOFLOW_VERSION_FOR_INSTALL}" \
  sh -c "curl -fsSL \"${INSTALL_URL}\" | LEOFLOW_VERSION=\"${LEOFLOW_VERSION_FOR_INSTALL}\" sh"

# Make the binary discoverable on this shell — install.sh writes a hint to
# rc files but we cannot rely on those in a sh-only smoke runner.
export PATH="${HOME}/.local/bin:${HOME}/.dexaflow/bin:${HOME}/.leoflow/bin:${PATH}"

# `leoflow version` (the name every release answers to, before and after the
# rename) prints a free-form line like:
#   leoflow v0.0.1-prealpha.23 (commit abc, built ..., go1.26.3)
# Earlier pipelines using `--output json | grep | head` silently returned
# empty because `head -1` exits 0 even on empty input, so the `||` fallback
# never fired (gate falsely failed against valid binaries). Capture the
# whole line and let the case-match below assert the tag.
INSTALLED="$(command -v leoflow >/dev/null 2>&1 && leoflow version 2>&1 | head -1 || true)"
if [ -z "${INSTALLED}" ]; then
  echo "==> debug: PATH=${PATH}; which leoflow=$(command -v leoflow || echo missing)" >&2
  fail "leoflow version reported nothing after installing ${PREVIOUS}"
fi
# `gh release list` returns tag names with a leading `v` (e.g. v0.0.1-prealpha.23),
# but `leoflow version` prints the tag without the v (e.g. "leoflow 0.0.1-prealpha.23").
# Strip the v so the substring match against the version line actually fires.
PREVIOUS_NUM="${PREVIOUS#v}"
case "${INSTALLED}" in
  *${PREVIOUS_NUM}*) pass "previous version installed (${INSTALLED})" ;;
  *) fail "expected ${PREVIOUS} after first install, got '${INSTALLED}'" ;;
esac

echo "==> installing current version (${LEOFLOW_VERSION}) ON TOP of ${PREVIOUS}"
# Same env-propagation guard as the previous install (see above).
sh -c "curl -fsSL \"${INSTALL_URL}\" | LEOFLOW_VERSION=\"${LEOFLOW_VERSION}\" sh"

UPGRADED="$(command -v leoflow >/dev/null 2>&1 && leoflow version 2>&1 | head -1 || true)"
LEOFLOW_VERSION_NUM="${LEOFLOW_VERSION#v}"
case "${UPGRADED}" in
  *${LEOFLOW_VERSION_NUM}*) pass "upgrade landed (${UPGRADED})" ;;
  *) fail "after upgrading, expected ${LEOFLOW_VERSION}, got '${UPGRADED}'" ;;
esac

echo "==> verifying the managed CPython still launches after the upgrade"
if [ -x "${HOME}/.leoflow/python/bin/python3.11" ]; then
  "${HOME}/.leoflow/python/bin/python3.11" --version >/dev/null
  pass "managed CPython runs after upgrade"
else
  echo "    (managed CPython not extracted on this distro — skip)"
fi

echo
echo "  \033[32m✅ upgrade-in-place ${PREVIOUS} → ${LEOFLOW_VERSION} verified.\033[0m"
