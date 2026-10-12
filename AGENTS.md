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
   and the pages for every user-facing change read correctly on the docs site.
2. **Algorithm.** Every change to scheduling, dispatch, state transitions,
   retries, pools or any other decision logic was read against its ADR and its
   tests, edge cases included.
3. **Regression.** The changes the candidate carries were checked for behavior
   they break elsewhere: the full Go and Python suites, the integration suite
   (`make test-integration`), and the fixes of the previous patches still holding.
4. **End to end.** The E2E suites ran green on the candidate's commit: E2E Lite,
   the k3d e2es (`make rc-smoke`), and the performance gates workflow
   (`.github/workflows/e2e-gates.yaml`). That workflow and the soak smoke run
   nightly on `main` and on rc tags only, so start both by hand
   (`workflow_dispatch`) on the `release-X.Y` commit before the cut.
5. **UI with Playwright.** When the candidate changes anything the browser shows
   (embedded UI assets, `/api/v2/` responses the UI reads, sign-in, login or SSO),
   the Playwright flows (`test/e2e/ui-smoke.js`, `make e2e-sso`,
   `test/ui-contract/`) ran against it, with screenshots of the changed screens.
6. **Security.** Tenant isolation (no read or write across tenants, roles
   included), every new external input validated, new or bumped dependencies
   clean under `make vuln`, and the image scan (`image-scan.yaml`, Trivy) clean
   or every finding justified in `.trivyignore.yaml`.
   **Every open security advisory** has its fix in the release, and its
   publication is planned for after that release ships (the only exception is
   active exploitation, see [`SECURITY.md`](SECURITY.md)). Track it without exposing details: a neutral
   item in the release milestone (no vulnerability description), the fix
   developed in the advisory's private fork and merged to `main`, and
   `scripts/release-gap.sh` confirming the fix is on `release-X.Y`. The line
   for this review lists the open advisories by identifier only.
7. **Upgrade and rollback.** The new migrations were applied over a database of
   realistic size, upgrading from the previous release, and rolled back to the
   previous release's schema (`make migrate-down` undoes one migration, so run it
   once per new migration, or follow the rollback steps in the upgrade guide),
   with the time each took and the server healthy after both.
8. **Performance and soak.** When the candidate touches the scheduler, dispatch
   or storage: the benchmarks of the area compared with the previous release, and
   a soak run (`make soak`) with no recorded violations.
9. **Release checklist.**
   - `scripts/release-gap.sh X.Y.Z` is empty (patches cut from `release-X.Y`).
   - The changelog fragments and release notes describe what ships.
   - `leoflow.yaml`, `LEOFLOW_*` variables and `~/.leoflow` still work next to
     `dexaflow.yaml`, `DEXAFLOW_*` and `~/.dexaflow`.
   - No commit in the range carries a `Co-Authored-By` trailer, a session link
     or an attribution footer (see section 2).

## 2. Working rules

### Branches and releases

- **`main` only changes through pull requests.** Every PR to `main` names its
  release as a milestone (`v0.5.2`, `v0.6.0`, ...); the milestone guard fails a
  PR without one.
- **Fixes reach `release-X.Y` only as cherry-picks.** After the change merges to
  `main`, open a PR against `release-X.Y` built with `git cherry-pick -x <sha>`,
  titled `[release-X.Y] ...`, linking the original PR and carrying its changelog
  fragment. `scripts/release-gap.sh` matches the `(cherry picked from commit ...)`
  line that `-x` writes, so a pick without it counts as missing. See
  [ADR 0062](https://dexaflow.dexadata.ai/project/adrs/0062-release-branches-and-open-main/)
  and [`RELEASING.md`](RELEASING.md).
- **Patch content is decided by the safety bar of
  [ADR 0068](https://dexaflow.dexadata.ai/project/adrs/0068-patch-content-gated-by-safety/),
  not by kind.** While the project is 0.x, features and migrations ship in
  patches when they meet it. A review does not ask to move a change to a minor
  for being a feature or adding a migration; it names the point of the bar the
  change fails, with evidence.
- **Nothing merged to `main` is left out of the release branch by accident.**
  What will never ship in the minor is listed with its reason in
  `.github/release-skip.txt` on the release branch.
- **Every performance PR is reviewed by a specialist before it merges**, for
  regressions, security and reliability (ADR 0062).

### Migrations

- **Migration numbers are contiguous** (`migrations/sequence_test.go`). When
  another PR took your number first, renumber yours to the next free number while
  merging `main` into your branch.

### CI hygiene

The Actions queue has few runners, so every wasted run delays everyone.

- Run the local checks before pushing: `make lint test`, or `make ci-local` for
  every gate.
- Push once, with all the fixes together, not one push per fix.
- Before pushing to a PR branch, cancel the runs of commits that push supersedes.
- Before opening a PR, check that no open PR already covers the change.

### Commits, pull requests and text

- **No attribution trailers or footers.** Commits, PR descriptions and PR
  comments carry no `Co-Authored-By` lines, no session links and no generated-by
  footers. Commit messages describe the change and nothing else.
- **Everything in the repository is in English**: code, comments, commit
  messages, PR descriptions, docs and changelog entries.
- **No em dashes** in new text written to the repository or to its PRs and
  issues. Use a comma, a colon, parentheses or two sentences instead.
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

### Testing discipline

- **Show the result.** `make lint` and `go test ./...` run before a change is
  called done, and the output is what proves it.
- **Test the documented path.** A test that bypasses the path users take (a
  hand-written Dockerfile, an overridden environment variable, a fixture that
  never imports `dag.py`) proves nothing about that path.
- **Mutation-test every new assertion.** Revert the fix, watch the test fail,
  restore it, and check that the mutation actually applied.
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
| Rules for one area | [`migrations/AGENTS.md`](migrations/AGENTS.md), [`internal/api/AGENTS.md`](internal/api/AGENTS.md), [`helm/AGENTS.md`](helm/AGENTS.md), [`scripts/AGENTS.md`](scripts/AGENTS.md), [`website/AGENTS.md`](website/AGENTS.md) |
| Steps for a recurring change | [Recipes](https://dexaflow.dexadata.ai/contribute/recipes/): add a migration, record a change, backport to a release branch |
| A large change before coding it | [Specs](https://dexaflow.dexadata.ai/project/specs/) and their template |
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
