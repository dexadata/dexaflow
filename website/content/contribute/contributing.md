---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /contributing.html
# --- end AUTO redirect aliases ---
title: Contributing
weight: 10
description: "How to contribute to Dexaflow — workflow, standards, and the TDD gate."
---

This page is the **functional path** from zero to a merged pull request. Every
command below is verified against the current repo. The exhaustive policy lives in
[`CONTRIBUTING.md`](https://github.com/dexadata/leoflow/blob/main/CONTRIBUTING.md);
the design *why* lives in the [ADRs](/project/adrs/).

## The path at a glance

1. [Clone and see it run](#1-clone-and-see-it-run) — the demo, one command.
2. [Set up for development](#2-set-up-for-development) — tools, build, gates.
3. [Know the quality bar](#3-the-quality-bar-non-negotiable) — TDD, A+, GoDocs.
4. [Pick or open an issue](#4-pick-or-open-an-issue).
5. [Fork → branch → TDD → PR](#5-fork--branch--tdd--pr).
6. [Pass the CI gates](#6-the-ci-gates).

---

## 1. Clone and see it run

The full stack — Postgres, Redis, and the control plane with the **embedded
Airflow 3.2.1 UI** — runs from a single Compose profile. No Go or Python toolchain
needed for this step, just Docker.

```bash
git clone https://github.com/dexadata/leoflow.git
cd leoflow
docker compose --profile demo up --build
```

Then open **<http://localhost:8080>** and log in as **`admin@leoflow.local`** /
**`admin`**. Stop with `docker compose --profile demo down` (add `-v` to wipe data).

{{% alert title="What you're looking at" color="success" %}}
One process serves the API (`/api/v2`), the internal UI API (`/ui/*`), and the
React UI (ADR 0017). It auto-applies migrations and seeds the admin user on
first boot. The three operating modes (Lite · Pro-soon · Demo) are
described in [Operating modes](/concepts/editions/).
{{% /alert %}}

## 2. Set up for development

```bash
cp .github/CLAUDE.md.template ./CLAUDE.md  # optional (Claude Code; gitignored)

make setup        # Go tools, Python parser/runtime, and the pre-commit hook
make build        # bin/leoflow, bin/leoflow-server, bin/leoflow-agent
make dev-up       # start Postgres + Redis (Docker) and apply migrations
make lint test    # the gates you must pass before pushing
```

For a full author→run loop without Kubernetes, `dexaflow lite` runs an isolated,
hot-reloading stack with the silver **Lite** edition badge (see
[Operating modes](/concepts/editions/)):

```bash
make dev-install            # put leoflow + server + agent on your PATH
dexaflow lite provision           # check/provision dev dependencies
dexaflow init dags/my_dag    # scaffold a project
dexaflow lite dags/my_dag     # hot-reload at http://localhost:8088 (Lite edition)
```

## 3. The quality bar (non-negotiable)

These are enforced by the pre-commit hook **and** CI — a PR that misses them cannot
merge. They are not bureaucracy; they are why the codebase stays an A+.

- **Strict TDD** — every line of production code is written against a *failing*
  test (red → green → refactor). The preferred shape is two commits: a `test:`
  commit that fails, then the `feat:`/`fix:` that makes it pass. ([ADR 0011](/project/adrs/))
- **Go Report Card A+** — gofmt, govet, gocyclo ≤ 15, golint, ineffassign, misspell,
  license, plus the extended golangci-lint stack. `make lint` clean every commit.
  ([ADR 0012](/project/adrs/))
- **GoDocs on every exported identifier** — starting with the name, ending with a period.
- **English** for all code, comments, commit messages, and docs.
- **No new dependency** without justification; the supply-chain scans
  (govulncheck, gosec, Trivy, CodeQL) run on every PR. ([ADR 0014](/project/adrs/))

{{% alert title="ADRs win" color="warning" %}}
A change that contradicts an [ADR](/project/adrs/) is rejected unless the ADR is first
amended in a separate PR. When in doubt, open an issue and ask before coding.
{{% /alert %}}

## 4. Pick or open an issue

{{< tabpane text=true >}}
{{% tab header="Find something to work on" %}}

Browse [open issues](https://github.com/dexadata/leoflow/issues). Good entry
points are labelled
[`good first issue`](https://github.com/dexadata/leoflow/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)
and [`help wanted`](https://github.com/dexadata/leoflow/issues?q=is%3Aissue+is%3Aopen+label%3A%22help+wanted%22).
Comment on the issue to claim it before starting, so effort isn't duplicated.
{{% /tab %}}
{{% tab header="Report a bug" %}}

Open a [new issue](https://github.com/dexadata/leoflow/issues/new/choose) and
pick **Bug report**. The form asks for repro steps, how you're running Dexaflow
(Lite / Pro / Demo), and environment — fill it in fully so we can
reproduce.
{{% /tab %}}
{{% tab header="Propose a feature" %}}

Open a [new issue](https://github.com/dexadata/leoflow/issues/new/choose) and
pick **Feature request**. For anything architectural or cross-cutting, also open
a PR adding a draft ADR under `website/content/project/adrs/` with status **Proposed** — the design
discussion happens there.
{{% /tab %}}
{{< /tabpane >}}

{{% alert title="Discuss non-trivial work first" color="info" %}}
For anything beyond a small bug fix, open or comment on an issue before writing
code. This avoids misaligned designs and wasted effort.
{{% /alert %}}

## 5. Fork → branch → TDD → PR

```bash
# 1. Fork on GitHub, then clone YOUR fork and add the upstream remote
git clone https://github.com/<you>/leoflow.git
cd leoflow
git remote add upstream https://github.com/dexadata/leoflow.git

# 2. Branch from an up-to-date main
git fetch upstream && git switch -c fix/clear-error-message upstream/main

# 3. Write the FAILING test first, watch it fail, then implement
#    (commit the test, then the code — red → green → refactor)
make lint test

# 4. Record what changed, as a fragment that cannot conflict with anyone else's
make changelog          # or: changie new

# 5. Push to your fork and open the PR
git push -u origin fix/clear-error-message
```

- **Branch names:** `feat/…`, `fix/…`, `docs/…`, `test/…`, `refactor/…`, `chore/…`.
- **Commits:** [Conventional Commits](https://www.conventionalcommits.org/) —
  `feat: add XCom schema validation`, `fix: handle pod OOMKilled`, `test: failing
  test for retry backoff`.
- **One logical change per PR.** If you write "and also…" in the description, split it.
- Opening the PR loads a **template** — fill in what changed, how you tested, and
  tick the checklist (TDD, lint, GoDocs, ADR compliance).
- **The changelog entry is a file, not an edit.** `make changelog` (a wrapper
  around `changie new`) asks for a kind and a one-line body and writes
  `.changes/unreleased/<slug>.yaml`. Commit it with your change; the release cut
  assembles every pending fragment into `CHANGELOG.md`. Write the body for the
  person who will read the release notes: what an operator or DAG author will
  notice, with the issue number.

  This exists because `CHANGELOG.md` has one `## [Unreleased]` section, so every
  open PR edits the same few lines: each merge conflicts the rest, each conflict
  costs a rebase, and each rebase re-runs the full CI matrix. Two fragments are
  two different files and cannot conflict. Hand-editing `CHANGELOG.md` still
  passes the gate; the fragment is the cheaper way to do the same thing.

  A PR with no user-facing change (release prep, chore, dependency bump,
  docs-only) takes the **`skip-changelog`** label instead. Dependabot PRs are
  exempt without one.

{{% alert title="Security-sensitive areas need extra review" color="danger" %}}
Changes to `internal/auth/`, `internal/executor/`, `internal/storage/`,
`migrations/`, `proto/`, or anything touching credentials/secrets are **not
accepted from first-time contributors** — open a discussion issue first.
{{% /alert %}}

## 6. The CI gates

Every PR runs, and must pass: the **build + unit/integration tests** with the
per-package coverage floor, **golangci-lint** (the A+ stack), and the
**security** suite (govulncheck, gosec, Trivy, CodeQL, gitleaks), and the
**CHANGELOG guard** (a fragment, a hand-written entry, or the `skip-changelog`
label). The same
`make lint test` you run locally is the fast feedback loop; CI is the source of
truth. Push fixes until everything is green, then a maintainer reviews — we aim
for three business days.

---

By contributing you agree your work is licensed under
[Apache 2.0](https://github.com/dexadata/leoflow/blob/main/LICENSE). Thank you for
helping make Dexaflow better.

## Editing the docs

This site is Hugo + Docsy, and its source is `website/`. Install [Hugo
extended](https://gohugo.io/installation/) (0.110.0 or newer; CI pins 0.165.0) and
run `npm ci` once inside `website/` for the theme's PostCSS toolchain, then preview
with live reload:

```bash
cd website
hugo server     # serves at http://localhost:1313/leoflow/ (the subpath matters)
```

The CLI and Go references are **generated, not written**: `website/scripts/gen-cli.sh`
renders `content/reference/cli/` from Cobra, and `gen-go.sh` renders
`content/reference/go/` from gomarkdoc. Both trees are committed so a fresh clone can
preview without those toolchains, but CI reruns every generator before it builds, so
**a hand edit to either tree never reaches the published site**. Change the command
definition or the GoDoc instead.

[Build the docs](/contribute/build-docs/) covers the rest: the other two generators
(OpenAPI and the Python runtime API), the redirect aliases, and the production build.
