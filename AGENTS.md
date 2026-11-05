# AGENTS.md

Standing rules for everyone who changes this repository: maintainers, contributors
and automated agents alike. Read this file before opening a pull request or cutting
a release. The project principles, code conventions and testing discipline are in
section 3, and section 4 maps where everything else is documented. When this
file and an ADR disagree, the ADR wins and this file gets fixed.

Directories with rules of their own carry their own `AGENTS.md`; when you change
files there, that file applies on top of this one.

## 1. Release candidate reviews

No release candidate (`vX.Y.Z-rc.N`) is cut until every review below is recorded,
one line each, in the release's tracking issue for the `vX.Y.Z` milestone. A line
names the review, who did it, the result, and a link to the evidence (a run, a
report, a PR comment). A review that does not apply to this candidate still gets
its line, saying why it does not apply. The cut script does not check these lines
yet: whoever runs `scripts/cut-release.sh` for the rc checks them by hand first.

1. **Docs.** `scripts/docs-gap.sh` is clean for the version being cut (#1478),
   `scripts/check-stale-versions.sh --candidate X.Y.Z` is clean on the
   candidate's commit (#1934), and the pages for every user-facing change read
   correctly on the docs site. The Docs gap workflow runs both checks on every
   rc tag, and on every push to a mapped `release-X.Y` (#1938): link its green
   run on the candidate's commit as the evidence.
2. **Algorithm.** Every change to scheduling, dispatch, state transitions,
   retries, pools or any other decision logic was read against its ADR and its
   tests, edge cases included.
3. **Regression.** The changes the candidate carries were checked for behavior
   they break elsewhere: the full Go and Python suites, the integration suite
   (`make test-integration`), and the fixes of the previous patches still holding.
4. **End to end.** The E2E suites ran green on the candidate's commit: E2E Lite,
   the k3d e2es (`make rc-smoke`), the performance gates workflow
   (`.github/workflows/e2e-gates.yaml`) and the daemonless deploy workflow
   (`.github/workflows/e2e-daemonless-deploy.yaml`, #1888). The performance
   gates workflow and the soak smoke run nightly on `main` and on rc tags, the
   daemonless deploy workflow on rc tags only, so start all three by hand
   (`workflow_dispatch`) on the candidate's commit, on its base branch, before
   the cut.
5. **UI with Playwright.** When the candidate changes anything the browser shows
   (embedded UI assets, `/api/v2/` responses the UI reads, sign-in, login or SSO),
   the Playwright flows ran against it, with screenshots of the changed screens.
   CI runs them on every PR: the `ui-smoke` job runs `test/e2e/ui-smoke.js` and
   the `test/ui-contract/` sweep and clear dialog check, and `e2e-sso-login`
   covers SSO; a green run of both jobs on the candidate's commit is the check.
6. **Security.** Tenant isolation (no read or write across tenants, roles
   included), every new external input validated, new or bumped dependencies
   clean under `make vuln`, and the image scan (`image-scan.yaml`, Trivy) clean
   or every finding justified in `.trivyignore.yaml`.
   **Every open security advisory** has its fix in the release, and its
   publication is planned for after that release ships (the only exception is
   active exploitation, see [`SECURITY.md`](SECURITY.md)). Track it without exposing details: a neutral
   item in the release milestone (no vulnerability description), the fix
   developed in the advisory's private fork and merged to `main`, and, for a
   patch cut from a mapped `release-X.Y`, `scripts/release-gap.sh` confirming
   the fix is there. The line
   for this review lists the open advisories by identifier only.
7. **Upgrade and rollback.** The new migrations were applied over a database of
   realistic size, upgrading from the previous release, and rolled back to the
   previous release's schema (`make migrate-down` undoes one migration, so run it
   once per new migration, or follow the rollback steps in the upgrade guide),
   with the time each took and the server healthy after both.
8. **Performance and reliability.** Every section of
   [`test/release/perf-reliability-checklist.md`](test/release/perf-reliability-checklist.md)
   that applies to this candidate (rc, GA, or a patch touching the area) gets its
   own line: gates on the tagged commit, benchmarks against the previous release,
   the database at scale, dispatch backpressure, failure injection, warm pools,
   soak and leaks, and the code-only deploy time and warm-up. At minimum, when the candidate touches the scheduler, dispatch
   or storage: the benchmarks of the area compared with the previous release, and
   a soak run (`make soak`) with no recorded violations.
9. **Release checklist.**
   - `scripts/release-gap.sh X.Y.Z` is empty (patches of a minor mapped to
     `release-X.Y` in `.github/patch-branches.txt`; a patch cut from `main`
     has nothing to pick).
   - The changelog fragments and release notes describe what ships.
   - `scripts/changelog-sync.sh --check-all --fetch` is clean: `main` carries
     the changelog of every patch already tagged (a minor cut refuses
     otherwise), and after a patch GA its `changelog: sync vX.Y.Z to main`
     pull request is merged
     ([RELEASING.md](RELEASING.md#how-the-changelog-flows)). The Changelog
     sync workflow runs the same check on `main` every day; a green run
     without a notice is the evidence.
   - `leoflow.yaml`, `LEOFLOW_*` variables and `~/.leoflow` still work next to
     `dexaflow.yaml`, `DEXAFLOW_*` and `~/.dexaflow`.
   - No commit in the range carries a `Co-Authored-By` trailer, a session link
     or an attribution footer (see section 2).

## 2. Working rules

### Branches and releases

- **`main` only changes through pull requests.** Every PR to `main` names its
  release as a milestone (`v0.5.2`, `v0.6.0`, ...); the milestone guard fails a
  PR without one.
- **Releases are cut from `main`, patches included, until `main` diverges**
  ([ADR 0102](https://dexaflow.dexadata.ai/project/adrs/0102-patches-from-main-until-main-diverges/)).
  `scripts/release-base.sh <version>` names the base. `main` must stay
  shippable as the next patch: a change that may not ship in it opens
  `release-X.Y` from the minor's newest GA tag and maps it in
  `.github/patch-branches.txt`, in the same pull request.
- **Fixes reach a mapped `release-X.Y` only as cherry-picks.** After the change
  merges to `main`, it goes in a `[release-X.Y]` pull request built with
  `git cherry-pick -x <sha>`, carrying its changelog fragment.
  `scripts/release-gap.sh` matches the `(cherry picked from commit ...)` line
  that `-x` writes, so a pick without it counts as missing. See
  [`RELEASING.md`](RELEASING.md).
- **Patch content is decided by the safety bar of
  [ADR 0068](https://dexaflow.dexadata.ai/project/adrs/0068-patch-content-gated-by-safety/),
  not by kind.** While the project is 0.x, features and migrations ship in
  patches when they meet it. A review does not ask to move a change to a minor
  for being a feature or adding a migration; it names the point of the bar the
  change fails, with evidence.
- **Nothing merged to `main` is left out of a release branch by accident.**
  What will never ship in a mapped minor is listed with its reason in
  `.github/release-skip.txt` on its release branch.
- **Every performance PR is reviewed by a specialist before it merges**, for
  regressions, security and reliability (ADR 0062).
- **No release from a red branch.** A candidate or GA is never tagged on a
  commit of `main` or `release-X.Y` with a red or unfinished required check;
  `cut-release.sh` and the Release workflow refuse it, with no override
  ([RELEASING.md](RELEASING.md#red-ci-blocks-a-release)). A red check the
  branch does not require stops the cut too, unless `--allow-red=<reason>`
  records why it does not matter for that release.
- **A red `main` is fixed first.** An open `ci-red` issue ("CI is red on
  main", opened by the Branch red watch) comes before new work: read the
  failed job, rerun it only if its log is a known transient, otherwise open
  the fix. A transient message the watch did not recognize goes into
  `scripts/flake-patterns.sh` with a self-test case in the same fix.

### Migrations

- **Migration numbers are contiguous** (`migrations/sequence_test.go`). When
  another PR took your number first, renumber yours to the next free number while
  merging `main` into your branch.

### CI hygiene

The account runs at most 20 Actions jobs at once, and one push to a pull
request starts about 50 (CI, Security and the guards). A change to `main`
runs CI again in the merge queue, and a pick to `release-X.Y` twice more.
Every wasted push delays everyone, the release cut included.

- Run the local checks before pushing: `make lint test`, or `make ci-local` for
  every gate. For a change to one area, the tests of the packages it touches
  and the integration suite run before the push, not after a red CI.
- Push once, with all the fixes together, not one push per fix. A pull request
  is opened when its local checks pass, not to see what CI says.
- Before pushing to a PR branch, cancel the runs of commits that push supersedes.
- Before opening a PR, check that no open PR already covers the change.
- A red job is read before anything is rerun. A flake is rerun with
  `gh run rerun <id> --failed`, which repeats only the failed jobs, never the
  whole run. A failure that comes back on a second push is reproduced locally
  before a third one.
- A new check that runs in seconds becomes a step of the `Repository gates`
  job in `ci.yaml`, not a job of its own: a job costs a runner slot and half a
  minute of setup. A new job that needs a cluster or a long build declares
  `needs: [lint, repo-gates]`, like the k3d jobs, and says in its pull request
  why it cannot run nightly or on rc tags instead.

### Batching pull requests

Each pull request costs at least two full CI runs, so the count of pull
requests is the main cost the queue pays.

- **One logical change, complete.** The docs, changelog fragment, comments
  and tests a change makes stale go in the same pull request, not in a
  follow-up opened after the merge.
- **Small fixes of one kind travel together.** Typos, stale links or comment
  fixes found in one review go in one pull request, not one each.
- **Picks are batched.** For a minor mapped to a release branch, cherry-picks
  to `release-X.Y` go in one
  `[release-X.Y] picks` pull request per day or per candidate, one
  `git cherry-pick -x` commit per original change (as #2140 did with sixteen),
  not one pull request per pick. A pick needed to unblock a failed cut may
  ride in the next prepare pull request.

### Changes that only run on a tag

`release.yaml` and the other tag-only workflows run nowhere before a tag is
pushed, so a defect in them costs a failed candidate and a new one (each cycle
about ninety minutes: fix, pick, prepare, tag, release run).

- A pull request that changes a tag-only step shows that step working before
  it merges: the same command run locally against a real input (an image
  digest, a lock, a chart), or the step run in a `workflow_dispatch` workflow,
  with the output in "How it was tested".
- Anything the release workflow refuses to publish has a cut-time gate that
  asks the same question first. `scripts/check-runtime-data-lock.sh` scans
  the data image lock with the release's Trivy criterion; `run_gates()` in
  `cut-release.sh` runs it with every other `scripts/check-*.sh` right before
  the tag.

### Commits, pull requests and text

- **No attribution trailers or footers.** Commits, PR descriptions and PR
  comments carry no `Co-Authored-By` lines, no session links and no generated-by
  footers. Commit messages describe the change and nothing else.
- **Everything in the repository is in English**: code, comments, commit
  messages, PR descriptions, docs and changelog entries.
- **No em dashes** in new text written to the repository or to its PRs and
  issues. Use a comma, a colon, parentheses or two sentences instead.
- `scripts/lint-new-text.sh` checks the two rules above that a script can
  check, an em dash in an added line and an attribution trailer in a commit
  message, on every pull request ("Text rules") and in the hooks `make setup`
  installs.
- Follow the PR template (`.github/PULL_REQUEST_TEMPLATE.md`): one logical change
  per PR, a changelog fragment (`make changelog`) or the `skip-changelog` label,
  docs updated or `skip-docs` with its reason.

## 3. Principles and conventions

### Project principles

1. **English everywhere.** Code, comments, commit messages, documentation and
   identifiers are in English.
2. **Strict TDD.** Production code is written in response to a failing test:
   write the test, run it, see it fail, implement, see it pass, refactor. See
   [ADR 0011](https://dexaflow.dexadata.ai/project/adrs/0011-tdd-strict/).
3. **Go Report Card A+ is the quality floor.** Every commit keeps gofmt, govet,
   gocyclo (max 15), GoDocs on all exports, ineffassign, misspell and license at
   100%, plus the golangci-lint stack in `.golangci.yaml` (ADR 0012).
4. **GoDocs on every exported identifier,** starting with the identifier name and
   ending with a period.
5. **No Python in the hot path.** Python runs only in the DAG parser sidecar and
   inside user task containers.
6. **DAGs are immutable artifacts:** a `dag.json` and a container image, versioned
   together, never mutated after compilation.
7. **Each DAG has its own container image.** No shared `/dags` filesystem and no
   monolithic worker image.
8. **The Airflow UI is a hard compatibility target.** The HTTP API at `/api/v2/`
   matches Airflow 3.2.x semantics; internal models can be richer, but the public
   API speaks Airflow's vocabulary. Never break that surface.
9. **Enterprise architecture, simple implementations.** Schemas, interfaces and
   middleware are built for production from day one; implementations start simple.
10. **Observability is not optional.** Prometheus metrics, OpenTelemetry tracing
    and structured logs ship with every feature.
11. **Supply chain security is built in.** govulncheck, gosec, Trivy and CodeQL run
    on every PR (ADR 0014). A new dependency is a new supply chain surface: it must
    be clean under `make vuln` and tracked by Dependabot.
12. **`dexaflow.yaml` is the authoring standard.** The compiler generates the
    Dockerfile. A hand-written Dockerfile is a supported escape hatch (ADR 0003),
    never the default in examples, docs or tests.
13. **ADRs are immutable once accepted.** Read the ADRs of an area before changing
    it; a new decision gets a new ADR.

### Code conventions

- **Package names:** lowercase, one word, no underscores.
- **Errors:** wrap with context (`fmt.Errorf("doing X: %w", err)`); never swallow
  an error.
- **Logging:** `log/slog` with structured fields; no `fmt.Println` in production
  code.
- **Context:** every function that does I/O takes `context.Context` first.
- **Tests:** integration tests carry the `//go:build integration` tag; coverage
  floors are enforced in CI (ADR 0011).
- **No global state** except metrics registries and configuration
  (`internal/config`).
- **Interfaces live near their consumers,** in the package that uses them.
- **`pkg/` is public API (ADR 0099).** A patch never breaks an exported
  identifier of a package under `pkg/`. A minor may, once the identifier was
  marked `Deprecated:` in the previous minor, with a `Changed` or `Removed`
  changelog fragment that says what to use instead and the `breaking-change`
  label (`scripts/api-compat.sh` checks every `pkg/` package with apidiff).
  Adding a function, an option, a struct field or a method to a struct is not a
  break; adding a method to an exported interface is, so an interface that may
  grow gets an optional second interface the server type-asserts. Contract
  types live in `pkg/`, and the `internal/` packages alias them; implementations
  stay internal.

### Testing discipline

- **Show the result.** `make lint` and `go test ./...` run before a change is
  called done, and the output is what proves it.
- **Test the documented path.** A test that bypasses the path users take (a
  hand-written Dockerfile, an overridden environment variable, a fixture that
  never imports `dag.py`) proves nothing about that path.
- **Every bug fix ships a regression test.** The test reproduces the reported
  behavior, fails on the code without the fix and passes with it. Commit it
  before the fix (`test:` then `fix:`) and name it in the PR under "How it was
  tested". A fix without such a test is not done; see the
  [Fix a bug](https://dexaflow.dexadata.ai/contribute/recipes/fix-a-bug/) recipe.
- **Mutation-test every new assertion.** Revert the fix, watch the test fail,
  restore it, and check that the mutation actually applied. For a tier 1
  package, `scripts/mutation.sh diff origin/main` mutates every line the branch
  changed; kill or accept each survivor it lists
  ([Mutation testing](https://dexaflow.dexadata.ai/contribute/mutation-testing/)).
- **Lock the wiring, not the leaf.** Drive the real entry point and assert on its
  output; bugs cluster where a correct helper gets a wrong input.
- **A comment that describes behavior the code does not have is a bug.** When
  behavior changes, search for the sentences that described the old behavior.
- **Read the exit code you think you read.** `cmd | head` reports the status of
  `head`; an empty filter output is not a pass.

## 4. Where things are documented

This file stays short and points; detail lives next to what it describes
([ADR 0069](https://dexaflow.dexadata.ai/project/adrs/0069-contributor-context/)).

| You need | Read |
|---|---|
| What Dexaflow is and how it is built | [Architecture](https://dexaflow.dexadata.ai/concepts/architecture/), [core concepts](https://dexaflow.dexadata.ai/concepts/core-concepts/), [glossary](https://dexaflow.dexadata.ai/reference/glossary/) |
| Why a design is the way it is | [ADRs](https://dexaflow.dexadata.ai/project/adrs/), then the `// Package` comment of the Go package |
| Rules for one area | [`migrations/AGENTS.md`](migrations/AGENTS.md), [`internal/api/AGENTS.md`](internal/api/AGENTS.md), [`internal/auth/AGENTS.md`](internal/auth/AGENTS.md) (tenant isolation too), [`internal/config/AGENTS.md`](internal/config/AGENTS.md), [`internal/dispatch/AGENTS.md`](internal/dispatch/AGENTS.md), [`internal/executor/AGENTS.md`](internal/executor/AGENTS.md), [`internal/scheduler/AGENTS.md`](internal/scheduler/AGENTS.md), [`internal/storage/AGENTS.md`](internal/storage/AGENTS.md), [`proto/AGENTS.md`](proto/AGENTS.md), [`parser/AGENTS.md`](parser/AGENTS.md), [`runtime/AGENTS.md`](runtime/AGENTS.md), [`helm/AGENTS.md`](helm/AGENTS.md), [`scripts/AGENTS.md`](scripts/AGENTS.md), [`website/AGENTS.md`](website/AGENTS.md) |
| Steps for a recurring change | [Recipes](https://dexaflow.dexadata.ai/contribute/recipes/): add a migration, a setting, an `/api/v2/` endpoint, a Helm value, a CLI command, a connector; record a change; backport; cut a release candidate; fix a flaky end-to-end job |
| What to check in a review | [Review checklists](https://dexaflow.dexadata.ai/contribute/review-checklists/): Airflow compatibility, Kubernetes behavior, docs, developer experience |
| A large change before coding it | [Specs](https://dexaflow.dexadata.ai/project/specs/) and their template; a new decision starts from the [ADR template](https://dexaflow.dexadata.ai/project/adrs/#template) |
| Setting up and running locally | [`CONTRIBUTING.md`](CONTRIBUTING.md), [local dev loop](https://dexaflow.dexadata.ai/contribute/local-dev-loop/) |
| Cutting a release | [`RELEASING.md`](RELEASING.md) and section 1 above |
| What a release may contain | [ADR 0068](https://dexaflow.dexadata.ai/project/adrs/0068-patch-content-gated-by-safety/) |
| Reporting or fixing a vulnerability | [`SECURITY.md`](SECURITY.md) |

### Public and local context

- Everything a contributor needs to make a correct change is committed, in
  English, and written for people, in the places above.
- Files that configure one editor or assistant (`CLAUDE.md`, `GEMINI.md`,
  `.cursor/`, `.claude/` and the like) and personal notes (`.local/`) stay out
  of the repository; `.gitignore` lists them and
  `scripts/check-no-tool-context.sh` fails CI if one is tracked. A local tool
  file loads this one and adds only what is specific to the tool or the person.
- When a review or an incident teaches something the next contributor would
  need, add it to the nearest `AGENTS.md` or recipe in the same PR.
