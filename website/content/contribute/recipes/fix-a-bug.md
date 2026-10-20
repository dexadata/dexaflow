---
title: Fix a bug
linkTitle: Fix a bug
weight: 95
description: Reproduce the bug in a test that fails, fix it, and prove the test would catch it coming back.
---

Every bug fix ships a regression test: a test that reproduces the reported
behavior, fails on the code without the fix, and passes with it. This is
[ADR 0011](/project/adrs/0011-tdd-strict/) applied to a defect: the failing
test comes first. A fix without that test is not done, because nothing stops
the same bug from returning in the next refactor.

1. **Reproduce it in a test.** Write the test against the path the user took
   (the CLI command, the `/api/v2/` endpoint, the scheduler tick), not only
   the helper you suspect, so the test catches the bug wherever the wiring
   breaks. Pick the lightest test that still reproduces it: a unit test when
   the logic is local, an integration test (`//go:build integration`) when it
   needs Postgres or another process, an end-to-end script under `test/e2e/`
   when only a cluster shows it.
2. **Watch it fail for the right reason.** Run it on the code without the fix
   and read the failure: it must fail on the assertion that describes the bug,
   not on a compile error, a missing fixture or a timeout.
3. **Commit the test first.** `test: failing test for <bug> (#<issue>)`, then
   the fix as `fix: <what changed> (#<issue>)`. Keeping them as two commits
   lets a reviewer check out the first one and see the failure.
4. **Fix the cause** and run the test again, then `make lint test`.
5. **Prove the test guards the fix.** Revert only the fix, watch the test fail,
   restore it (root `AGENTS.md`, "Mutation-test every new assertion").
6. **Open the PR** with the issue linked, the cause in "What & why", and the
   regression test named in "How it was tested", with the command that runs it.
   A user-facing fix gets a changelog fragment of kind `Fixed`
   ([Record a change](/contribute/recipes/record-a-change/)).

When a fix must also reach a release branch, the regression test travels with
it in the same cherry-pick ([Backport to a release branch](/contribute/recipes/backport-to-a-release-branch/)).

A flaky end-to-end job that turns out to be a product race is a bug like any
other; [Fix a flaky end-to-end job](/contribute/recipes/fix-a-flaky-end-to-end-job/) covers
telling it apart from an infrastructure transient.
