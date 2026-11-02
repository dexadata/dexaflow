---
title: "ADR 0101: mutation testing measures the tests, and coverage is gated per core package"
linkTitle: "0101 · Mutation testing and coverage policy"
weight: 1010
description: "ADR 0101: the test suite is measured by mutation testing with gremlins, on two tiers of packages with efficacy floors that start at the measured baseline and only rise. A pull request gets an advisory run on its changed lines, the full run is weekly and sharded so it never competes with pull requests for runners, and a release candidate is cut only when no core survivor is left untriaged. The 80% statement floor of ADR 0011 stays, and a core package may not lose more than one point of coverage in a pull request."
---

**Status:** Accepted
**Date:** 2026-11-01
**Accepted:** 2026-11-01
**Relates:** ADR 0011 (strict TDD and the coverage floor), ADR 0062 (specialist review), ADR 0071 (stage, verify, promote)

## Context

ADR 0011 makes every change start from a failing test and holds the unit
suite to an 80% statement floor (`scripts/coverage-gate.sh`). Statement
coverage says which lines ran under a test. It does not say whether any test
would notice if a line were wrong: a test that calls a function and asserts
nothing covers every line it reaches.

AGENTS.md already asks for the missing half by hand: "Mutation-test every new
assertion. Revert the fix, watch the test fail, restore it." The v0.5.4
release review did this on five changes (M1 to M5): on one of them a single
test out of twelve failed on the broken code, and the other eleven passed. That practice depends on the reviewer choosing the right
mutation, covers only the lines a change touches, and leaves no number to
compare across releases.

Three constraints shape any tool we add:

- **Runners are scarce.** The Actions plan runs at most 20 jobs at once, and
  a pull request already fans out to most of them. A check that runs the
  whole suite once per mutant cannot run on every push.
- **Some core code is tested only against a database.** The storage
  repositories, the leader election and the lock bindings are excluded from
  the unit floor because only the integration suite exercises them honestly.
  One integration run of `internal/storage` takes about ten minutes, so
  mutating that code one mutant at a time is out of reach in CI.
- **Suites differ by two orders of magnitude.** `internal/retention` runs in
  under a second; `internal/scheduler` takes about two minutes. A per-mutant
  budget that suits one does not suit the other.

## Decision

1. **The tool is gremlins**, pinned (`v0.6.0`) and configured in
   `.gremlins.yaml`, run through `scripts/mutation.sh`. It mutates
   conditionals, boundaries, arithmetic, increments and negations on the lines
   the test suite covers, runs the package's tests once per mutant, and
   reports each mutant as killed, lived, timed out, not covered or not viable.
   It runs on a package path, on the lines a diff changed (`--diff`), and with
   build tags.

   Every run starts with a **canary**: a throwaway module with one test that
   asserts nothing and one that asserts the boundary. The first must leave its
   mutants alive and the second must kill them, or the run stops. A tool or
   configuration that fails every test run otherwise reads as 100% efficacy
   everywhere; the first baseline of this ADR measured exactly that, because
   gremlins passes its `test-cpu` setting to `go test` as one malformed
   argument.

2. **Two numbers are tracked per package.** Test efficacy is killed over
   killed plus lived: of the mutants a test reaches, the share some test
   notices. Mutant coverage is killed plus lived over all mutants: the share
   any test reaches. Efficacy is the gated number; mutant coverage is reported
   next to statement coverage. A timed-out mutant is reported on its own and
   counts in neither number.

3. **Packages are in two tiers**, listed in `.github/mutation/targets.txt`
   with one floor each:

   - **Tier 1, the core**: the code where a wrong branch loses state, runs a
     task twice or leaks a credential. `internal/scheduler`,
     `internal/executor`, `internal/dispatch`, `internal/agent`,
     `internal/storage` (its unit-tested files), `internal/retention`,
     `internal/taskoutcome`, `internal/gatedtick`, `internal/auth`,
     `internal/oidc`, `internal/secrets`, `internal/admissionwebhook`.
     Target efficacy: 80%.
   - **Tier 2, everything else** that the coverage floor counts. Target
     efficacy: 65%.
   - **Out of scope**: what `scripts/coverage-gate.sh` excludes (generated
     code, `cmd/`, `test/`, `internal/version`, and the database and process
     bindings only integration can exercise).

   The floor of a package is its measured efficacy rounded down to a whole
   number, never higher than measured. A change may raise a floor and never
   lower one, except in the pull request that also explains why in the
   targets file (a package split, a mutator added). Floors move toward the
   targets as survivors are killed.

4. **A survivor is killed or accepted, starting with new code.** The
   survivors the baseline found stay below the floor and are worked down
   package by package. A survivor on a line a change adds is handled in that
   change: killed with an assertion, or accepted. An accepted survivor is an
   equivalent mutant (the mutated code behaves the same, such as `<` and `<=`
   on a value that is never equal) or one not worth a test, and it is listed
   in `.github/mutation/accepted.txt` as `<file>:<line>:<column> <MUTATOR>
   <reason>`. The reason is required and reviewed like code. Accepted mutants
   do not count against the floor.

5. **Where it runs**, so it never competes with pull requests for runners:

   - **On a pull request**: one job, `Mutation (changed lines)`, only when the
     change touches non-test Go files of a tier 1 package. It mutates only the
     changed lines of those packages and lists every survivor in the job
     summary. It is advisory: it is not a required check, and a reviewer
     treats each survivor like a review comment (kill it or accept it in
     `accepted.txt`). It is cancelled when a newer push supersedes it.
   - **Weekly** (Sunday 05:00 UTC, after the nightly fuzz) and by hand: the
     full run, one matrix job per package, at most two at a time. A job fails
     when its package falls below its floor, and its summary lists the
     survivors and says when the floor can be raised. The summaries are the
     report: efficacy and mutant coverage per package.
   - **Before a release candidate**: the release thread starts the full run on
     the release branch by hand (`workflow_dispatch`) and links it from the
     release checklist. A tier 1 package below its floor blocks the cut until
     its survivors are killed or accepted.

6. **Integration-only code keeps the manual rule.** The files excluded from
   the unit floor are not mutated by the tool. For them, the AGENTS.md rule
   stays the control: the pull request that changes them shows the test
   failing on the reverted code. The weekly run also measures the statement
   coverage the integration suite gives those files, so a file no test
   reaches at all is visible.

7. **Coverage stays gated, and gains a per-package rule.** The 80% floor of
   ADR 0011 stays as the one global gate. In addition, a tier 1 package may
   not lose more than one percentage point of statement coverage against the
   base branch in a pull request (the same tolerance ADR 0011 gives the
   pre-commit hook). The comparison uses the base profile the Test + Coverage
   job already fetches; without one, the rule is skipped and the summary says
   so.

## Baseline

The first run (2026-11-01, gremlins v0.6.0, unit tests, the default mutators)
measured these tier 1 packages; their floors in `targets.txt` are these
numbers rounded down.

| Package | Efficacy | Mutant coverage | Killed | Lived |
|---|---:|---:|---:|---:|
| `internal/oidc` | 98.0% | 96.2% | 49 | 1 |
| `internal/auth` | 95.3% | 97.7% | 81 | 4 |
| `internal/storage` | 88.7% | 16.7% | 125 | 16 |
| `internal/dispatch` | 87.0% | 91.8% | 107 | 16 |
| `internal/admissionwebhook` | 85.7% | 100.0% | 36 | 6 |
| `internal/secrets` | 84.6% | 68.4% | 22 | 4 |
| `internal/taskoutcome` | 84.2% | 100.0% | 16 | 3 |
| `internal/retention` | 80.6% | 93.1% | 54 | 13 |
| `internal/gatedtick` | 55.6% | 100.0% | 5 | 4 |

`internal/storage` reaches few mutants with unit tests because most of its
code is tested against a database (decision 6). `internal/executor`,
`internal/agent` and `internal/scheduler` start without a floor: their first
full runs take hours on one machine, and the first weekly run sets them, as it
does for tier 2.

## Consequences

- A test that runs code without checking it shows up as a survivor, with the
  file and line, instead of passing review because the line is green.
- The numbers are comparable across releases, so "the tests got weaker" is a
  fact in the weekly summary rather than an impression.
- Pull requests get no new required check and at most one extra job, so the
  runner budget of a pull request is unchanged in practice.
- The weekly run costs a few runner hours, at a time nothing else runs; the
  scheduler package alone is most of it, because its suite takes about two
  minutes and runs once per mutant.
- Floors at the baseline mean the tool starts green and only blocks a
  regression. Reaching the 80% and 65% targets is planned work, one package
  at a time, tracked in issues.
- Accepted survivors are a list that can grow without anyone looking. It is
  reviewed with the targets file at each minor release.

## Alternatives considered

- **go-mutesting (and its forks).** More mutators, including statement
  removal, but no diff mode, so a pull request run would mutate whole
  packages. Statement removal is worth revisiting if gremlins gains it.
- **Mutation testing as a required pull request check.** It would hold every
  pull request for the slowest package (scheduler: about two minutes per
  mutant) and spend the runner budget the rest of CI needs. The advisory job
  gives the same signal where the change is.
- **A higher statement floor instead.** It rewards more lines run, not more
  checks made, and pushes toward tests that assert nothing.
- **Mutating the integration suite.** About ten minutes per storage mutant;
  hundreds of mutants. Kept manual, as above.

## Plan

1. This ADR and the contributor page
   [Mutation testing and coverage](/contribute/mutation-testing/).
2. `.gremlins.yaml`, `.github/mutation/targets.txt` with the measured
   baseline, `.github/mutation/accepted.txt`, `scripts/mutation.sh`, the
   `Mutation` workflow and the per-package coverage rule in
   `scripts/coverage-gate.sh`.
3. One issue per tier 1 package below its target, with its survivors.
