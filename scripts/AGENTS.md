# scripts/

Rules for changing the scripts that gate CI and cut releases. The root
[AGENTS.md](../AGENTS.md) applies too.

- **Every `check-*.sh` is a release gate.** `cut-release.sh` runs every
  `scripts/check-*.sh` before a cut, so a new check must pass on a clean
  checkout with no network and no secrets, and a renamed one must keep the
  `check-` prefix.
- **Logic has a self-test.** A script with decision logic defines `self_test()`
  behind `--self-test`; `check-script-selftests.sh` finds and runs it by that
  definition.
- **A pinned tool has one source of truth.** The `check-*-pin.sh` scripts keep
  the version used locally and in CI equal; bump the pin and every place it
  names in the same PR.
- **Release scripts** (`cut-release.sh`, `release-gap.sh`, `docs-gap.sh`,
  `release-notes.sh`) follow [RELEASING.md](../RELEASING.md); a change to
  `cut-release.sh` shows a `--dry-run` of it in the PR.
- Shell style: `set -euo pipefail`, and a header comment that says what the
  script guards and which incident made it necessary.
