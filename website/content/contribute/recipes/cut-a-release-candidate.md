---
title: Cut a release candidate
linkTitle: Cut a release candidate
weight: 90
description: The short path through an rc cut. RELEASING.md has the full flow and the recovery steps; scripts/cut-release.sh does the work.
---

This is the maintainer's checklist. The full flow, what each step of the
script does and how to recover from a failed cut are in
[RELEASING.md](https://github.com/dexadata/dexaflow/blob/main/RELEASING.md).
`scripts/cut-release.sh` is the single source of the steps; never repeat them
by hand.

1. **Know the base.** Every release is cut from `main` unless
   `.github/patch-branches.txt` maps a patch's minor to `release-X.Y`.
   `scripts/release-base.sh X.Y.Z` prints it, and the script picks the base
   the same way, never from your checkout
   ([ADR 0102](/project/adrs/0102-patches-from-main-until-main-diverges/)).
2. **Check the base is green.** No release is cut from a red `main` or
   `release-X.Y`. The cut refuses while a check the base requires is red on
   its tip or on the merge commit, and so does the Release workflow on the
   tagged commit; there is no override for a required check. An open
   `ci-red` issue ("CI is red on main") is fixed first:

   ```sh
   scripts/release-ci-gate.sh "$(git rev-parse origin/main)" main
   ```

   A red check the base does not require stops the cut at the merge commit
   too, unless `--allow-red="<reason>"` records why it does not matter; the
   reason goes into the tag annotation
   ([RELEASING.md](https://github.com/dexadata/dexaflow/blob/main/RELEASING.md#red-ci-blocks-a-release)).
3. **Close the gaps.** The cut refuses a patch while the first list is not
   empty, and any candidate while the second is not:

   ```sh
   scripts/release-gap.sh X.Y.Z --fetch   # mapped minors: main commits the patch would miss
   scripts/docs-gap.sh X.Y.Z --fetch      # user-facing changes without docs
   ```

   A change that will never ship in the minor goes in
   `.github/release-skip.txt` on the release branch, with its reason.
4. **Run the end-to-end gates on the candidate's commit.** `make rc-smoke` runs
   the gates and the k3d suites. The performance gates workflow
   (`.github/workflows/e2e-gates.yaml`) and the soak smoke run nightly on `main`
   and on rc tags, and the daemonless deploy workflow
   (`.github/workflows/e2e-daemonless-deploy.yaml`: `dexaflow deploy` of
   `examples/taskflow_sales` from the `dexaflow-cli` image with no container
   daemon, then a run on k3d) on rc tags only, so start all three with
   `workflow_dispatch` on the candidate's commit (on its base branch) before
   the cut.
   On a machine where another project owns port 5432, run
   `DEXAFLOW_DEV_DB_PORT=55432 make rc-smoke`: the dev Postgres, the
   migrations, the integration tests, the k3d suites and the UI smoke all use
   that port, and the battery never resets a database it did not start.
   `make soak` already runs on its own Postgres on port 55433 (`SOAK_PG_PORT`).
5. **Record every review** of
   [AGENTS.md section 1](https://github.com/dexadata/dexaflow/blob/main/AGENTS.md#1-release-candidate-reviews)
   in the tracking issue of the `vX.Y.Z` milestone: one line each, with who did
   it, the result and a link to the evidence, including the reviews that do not
   apply and why. The script does not check these lines; whoever runs it does.
6. **Preview, then cut:**

   ```sh
   scripts/cut-release.sh vX.Y.Z-rc.N --dry-run
   scripts/cut-release.sh vX.Y.Z-rc.N
   ```

   The script prepares the release PR, waits for it to go green, merges it,
   and asks you to type the tag back before it pushes. It refuses while the
   Actions queue is busy: wait for it to drain and resume, or pass
   `--force-queue` when the release cannot wait.
7. **Freeze pull request pushes** from the go-ahead until the release run is
   green, so release jobs do not wait behind PR runs.
8. **If the cut stops after the prepare PR merged**, fix the cause and resume:
   `scripts/cut-release.sh vX.Y.Z-rc.N --resume --dry-run`, then without
   `--dry-run`.

Before promoting the candidate to GA, run the GA rows of
`test/release/perf-reliability-checklist.md` and, on a real cluster,
`test/release/rc-cluster-validation.md`.
