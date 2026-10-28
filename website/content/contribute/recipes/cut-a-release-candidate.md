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

1. **Know the base.** `X.Y.0` and its candidates are cut from `main`; a later
   patch (`X.Y.Z`, Z > 0) is cut from `release-X.Y`. The script picks the base
   from the version, not from your checkout
   ([ADR 0062](/project/adrs/0062-release-branches-and-open-main/)).
2. **Close the gaps.** The cut refuses a patch while the first list is not
   empty, and any candidate while the second is not:

   ```sh
   scripts/release-gap.sh X.Y.Z --fetch   # main commits the patch would miss
   scripts/docs-gap.sh X.Y.Z --fetch      # user-facing changes without docs
   ```

   A change that will never ship in the minor goes in
   `.github/release-skip.txt` on the release branch, with its reason.
3. **Run the end-to-end gates on the candidate's commit.** `make rc-smoke` runs
   the gates and the k3d suites. The performance gates workflow
   (`.github/workflows/e2e-gates.yaml`) and the soak smoke run nightly on `main`
   and on rc tags, and the daemonless deploy workflow
   (`.github/workflows/e2e-daemonless-deploy.yaml`: `dexaflow deploy` of
   `examples/taskflow_sales` from the `dexaflow-cli` image with no container
   daemon, then a run on k3d) on rc tags only, so start all three with
   `workflow_dispatch` on the `release-X.Y` commit before the cut.
   On a machine where another project owns port 5432, run
   `DEXAFLOW_DEV_DB_PORT=55432 make rc-smoke`: the dev Postgres, the
   migrations, the integration tests, the k3d suites and the UI smoke all use
   that port, and the battery never resets a database it did not start.
   `make soak` already runs on its own Postgres on port 55433 (`SOAK_PG_PORT`).
4. **Record every review** of
   [AGENTS.md section 1](https://github.com/dexadata/dexaflow/blob/main/AGENTS.md#1-release-candidate-reviews)
   in the tracking issue of the `vX.Y.Z` milestone: one line each, with who did
   it, the result and a link to the evidence, including the reviews that do not
   apply and why. The script does not check these lines; whoever runs it does.
5. **Preview, then cut:**

   ```sh
   scripts/cut-release.sh vX.Y.Z-rc.N --dry-run
   scripts/cut-release.sh vX.Y.Z-rc.N
   ```

   The script prepares the release PR, waits for it to go green, merges it,
   and asks you to type the tag back before it pushes. It refuses while the
   Actions queue is busy: wait for it to drain and resume, or pass
   `--force-queue` when the release cannot wait.
6. **Freeze pull request pushes** from the go-ahead until the release run is
   green, so release jobs do not wait behind PR runs.
7. **If the cut stops after the prepare PR merged**, fix the cause and resume:
   `scripts/cut-release.sh vX.Y.Z-rc.N --resume --dry-run`, then without
   `--dry-run`.

Before promoting the candidate to GA, run the GA rows of
`test/release/perf-reliability-checklist.md` and, on a real cluster,
`test/release/rc-cluster-validation.md`.
