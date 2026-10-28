<!-- Thanks for contributing! Keep PRs to one logical change. -->

## What & why

<!-- What does this change do, and why? -->

Closes #

## How it was tested

<!-- Commands you ran and the tests you added. Dexaflow is strict TDD (ADR 0011):
     the test came first and failed before the implementation existed. -->

## Checklist

- [ ] **TDD** — a failing test preceded the production code (red → green → refactor) — [ADR 0011](https://dexaflow.dexadata.ai/project/adrs/0011-tdd-strict/)
- [ ] **Bug fix?** A regression test reproduces the bug: it fails without the fix and passes with it, and it is named under "How it was tested" ([Fix a bug](https://dexaflow.dexadata.ai/contribute/recipes/fix-a-bug/))
- [ ] `make lint test` passes locally
- [ ] GoDocs on every new exported identifier; cyclomatic complexity ≤ 15 — [ADR 0012](https://dexaflow.dexadata.ai/project/adrs/0012-code-quality-standards/)
- [ ] No new dependency without justification; `make vuln` clean — [ADR 0014](https://dexaflow.dexadata.ai/project/adrs/0014-supply-chain-security/)
- [ ] Public `/api/v2/` surface unchanged, or Airflow 3.2.x compatibility preserved
- [ ] Docs updated under `website/content/` if behavior, flags, config, migrations or the API changed; a new migration also adds its row to the recovery table in `operate/upgrades.md` ("An interrupted migration"). Nothing user-discoverable? Add a line `Skip-docs: <reason>` to this description (the `skip-docs` label is optional).
- [ ] Changelog recorded: `make changelog` writes `.changes/unreleased/<slug>.yaml` (one file per PR, so it never conflicts). No user-facing change? Add a line `Skip-changelog: <reason>` to this description, or apply the `skip-changelog` label.
- [ ] All code, comments, and commit messages are in English
- [ ] One logical change (no "and also…")

<!-- UI change? Add before/after screenshots.
     Touching internal/auth, internal/executor, internal/storage, migrations/, or
     proto/? Note it here — those areas get extra review (see CONTRIBUTING.md) and
     are not accepted from first-time contributors. -->
