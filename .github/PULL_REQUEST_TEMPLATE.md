<!-- Thanks for contributing! Keep PRs to one logical change. -->

## What & why

<!-- What does this change do, and why? -->

Closes #

## How it was tested

<!-- Commands you ran and the tests you added. Dexaflow is strict TDD (ADR 0011):
     the test came first and failed before the implementation existed. -->

## Checklist

- [ ] **TDD** — a failing test preceded the production code (red → green → refactor) — [ADR 0011](https://dexaflow.dexadata.ai/project/adrs/0011-tdd-strict/)
- [ ] `make lint test` passes locally
- [ ] GoDocs on every new exported identifier; cyclomatic complexity ≤ 15 — [ADR 0012](https://dexaflow.dexadata.ai/project/adrs/0012-code-quality-standards/)
- [ ] No new dependency without justification; `make vuln` clean — [ADR 0014](https://dexaflow.dexadata.ai/project/adrs/0014-supply-chain-security/)
- [ ] Public `/api/v2/` surface unchanged, or Airflow 3.2.x compatibility preserved
- [ ] Docs updated if behavior, flags, or config changed
- [ ] Changelog recorded: `make changelog` writes `.changes/unreleased/<slug>.yaml` (one file per PR, so it never conflicts). No user-facing change? Apply the `skip-changelog` label instead.
- [ ] All code, comments, and commit messages are in English
- [ ] One logical change (no "and also…")

<!-- UI change? Add before/after screenshots.
     Touching internal/auth, internal/executor, internal/storage, migrations/, or
     proto/? Note it here — those areas get extra review (see CONTRIBUTING.md) and
     are not accepted from first-time contributors. -->
