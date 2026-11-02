---
title: "Mutation testing and coverage"
linkTitle: "Mutation testing"
weight: 45
description: "How Dexaflow measures its tests: the coverage floor, the per-package coverage rule for the core, and mutation testing with gremlins. What runs on a pull request, what runs weekly, and what to do with a surviving mutant."
---

Coverage says which lines ran under a test. Mutation testing says whether a test
would notice if one of those lines were wrong. Dexaflow gates both, on the
rules of [ADR 0011](/project/adrs/0011-tdd-strict/) and
[ADR 0101](/project/adrs/0101-mutation-testing-and-coverage-policy/). This page
is the practical side.

## The three checks

| Check | Where | Blocks |
|---|---|---|
| Unit statement coverage of at least 80% | Test + Coverage job, every pull request; the pre-commit hook | Yes |
| A tier 1 package loses at most 1 point of coverage against the base | Test + Coverage job, every pull request | Yes |
| Mutation testing of the changed lines in tier 1 packages | `Mutation (changed lines)` job, pull requests that change Go code | No: its survivors are review comments |
| Each package at or above its mutation floor | `Mutation` workflow, weekly and before a release candidate | The weekly run, and the release cut |

The floor, the exclude list and the per-package rule live in
`scripts/coverage-gate.sh`. The mutation tiers and floors live in
`.github/mutation/targets.txt`.

## Tiers

**Tier 1** is the core: the packages where a wrong branch loses state, runs a
task twice or leaks a credential. They are listed in
`.github/mutation/targets.txt` and aim for 80% mutation efficacy.
**Tier 2** is every other package the coverage floor counts, aiming for 65%.
`scripts/mutation.sh packages 1` and `scripts/mutation.sh packages 2` list
them.

What the coverage floor excludes is out of scope: generated code, `cmd/`,
`test/`, `internal/version`, and the database and process bindings only the
integration suite can exercise. For those bindings the manual rule of
`AGENTS.md` still applies: revert the fix, watch the test fail, restore it.
The weekly run reports the coverage the integration suite gives them.

## Reading the numbers

- **Efficacy**: killed over killed plus lived. Of the mutants some test
  reaches, the share some test notices. This is the gated number.
- **Mutant coverage**: killed plus lived over all mutants. The share any test
  reaches at all; the mutation-side twin of statement coverage.
- **Lived** (a survivor): the code was changed, for example `>` became `>=`,
  and every test still passed.
- **Not covered**: no test runs that line. Statement coverage already shows
  it.
- **Timed out** and **not viable** (the mutant did not compile) count in
  neither number.

## Run it locally

```sh
go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0

# The whole package, checked against its floor.
scripts/mutation.sh run internal/dispatch

# Only the lines your branch changed, in the tier 1 packages it touches.
scripts/mutation.sh diff origin/main
```

Both print a Markdown report and keep the gremlins JSON under `.mutation/`.
Both start with a canary: a throwaway module whose weak test must leave its
mutants alive and whose strong test must kill them. If the canary fails, the
tool or `.gremlins.yaml` is broken and no number of that run means anything.

A full run of a large package takes a while: the package's tests run once per
mutant. `internal/scheduler`, whose suite takes about two minutes, is the
slowest.

## A survivor on your change

The pull request job lists each survivor as `<file>:<line>:<column>
<MUTATOR>`. For each one:

1. **Kill it.** Write the assertion that tells the mutated code from the real
   one. A boundary survivor (`CONDITIONALS_BOUNDARY`) usually means no test
   uses the value at the edge; a negation survivor (`CONDITIONALS_NEGATION`)
   usually means no test takes the other branch and checks its result.
2. **Or accept it**, when the mutant is equivalent (the mutated code behaves
   the same for every input the program can reach) or a test would cost more
   than the line is worth. Add a line to `.github/mutation/accepted.txt`:

   ```
   internal/retention/janitor.go:352:8 CONDITIONALS_BOUNDARY the cutoff is a whole day and never equals a row's timestamp
   ```

   The reason is required and is reviewed like code. A line that moves
   leaves its entry stale; the run reports survivors by position, so update
   the entry in the change that moves the line.

## Floors

A floor is a package's measured efficacy rounded down to a whole number. The
weekly summary says "raise it to N" when a package is a point or more above its
floor: raise it in `targets.txt` in any pull request. Lowering a floor needs a
reason written on the line above it in the same change (a package split, a
mutator enabled).

## Before a release candidate

Start the `Mutation` workflow by hand on the release branch (Actions, Mutation,
Run workflow) and link the run from the release checklist. A tier 1 package
below its floor blocks the cut until its survivors are killed or accepted.
