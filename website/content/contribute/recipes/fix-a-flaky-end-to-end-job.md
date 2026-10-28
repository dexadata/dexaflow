---
title: Fix a flaky end-to-end job
linkTitle: Fix a flaky E2E job
weight: 100
description: Tell an infrastructure transient from a race in the test or the product, reproduce it, and fix the cause instead of retrying past it.
---

A flaky job costs every contributor a rerun on a queue with few runners, and a
flake during a release cut has cost a tag before (#1017). Fix the cause; a
retry that hides it only moves the cost.

1. **Find the script behind the job.** `test/e2e/README.md` lists each
   end-to-end script, what it guards, and which workflow job and `make` target
   run it. The heavy k3d jobs decide whether to run with
   `scripts/ci-heavy-gate.sh`; `scripts/ci-heavy-gate.sh --decide` reads a file
   list on stdin and prints `run` or `skip`.
2. **Classify the failure from its log.**
   - **Infrastructure:** a registry rate limit, a module proxy reset, a runner
     that never started its service containers. `FLAKE_RE` in
     `scripts/cut-release.sh` lists the messages the release cut already treats
     as transient. A new infrastructure message belongs there, with a
     `--self-test` case; a product or test failure never does.
   - **The test:** it asserts before the state it checks is written. Two real
     examples: a test re-ticked the scheduler before the asynchronous delivery
     stamp landed (#815), and a helper returned a file the moment it existed,
     before anything was written into it (#1120).
   - **The product:** the same race a user would hit. Treat it as a bug, with
     its own issue and a failing test.
3. **Reproduce it locally** with the job's `make` target (`make e2e-lite`,
   `make e2e`, `make e2e-sso`, ...), under load or in a loop until it fails.
   A failure you never saw is a failure you cannot say you fixed.
4. **Fix the cause.** Wait for the condition the assertion needs (the stamp,
   the file's content, the pod's state), not for a fixed time. When a tool
   reports success it did not achieve, check its output and fail loudly, as
   `k3d_import` in `test/e2e/lib.sh` does for `k3d image import`.
5. **Prove the fix.** Run the job repeatedly with the fix, then revert the fix
   and see the failure come back (root AGENTS.md, "Mutation-test every new
   assertion").
6. **Open the PR** with the failing run linked, the cause, and how you
   reproduced it. A fix that changes only tests or CI ships nothing to users,
   so it carries the `skip-changelog` label.
