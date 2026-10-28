# Contributing to Dexaflow

Thank you for your interest in contributing to Dexaflow! This document explains how to get involved.

## Before You Start

1. Read [`README.md`](README.md) to understand what Dexaflow is.
2. Read the [Architecture Decision Records](website/content/project/adrs/) under `website/content/project/adrs/`. These document non-negotiable design choices. Contributions that contradict an ADR will be rejected unless the ADR is first amended via a separate PR.
3. Read [`AGENTS.md`](AGENTS.md): the standing rules for every change, and a map to the rules of each area (directories such as `migrations/` carry their own `AGENTS.md`) and to the step by step recipes.
4. Read [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).
5. See what is planned: issues are the backlog, each in the milestone of the release it is planned for. The [Dexaflow roadmap](https://github.com/orgs/dexadata/projects/4) project is a read-only view of them; change the issue, not the project.

## How to Contribute

### Reporting Bugs

1. Search [existing issues](https://github.com/dexadata/dexaflow/issues) to confirm the bug has not been reported.
2. If not, open a new issue using the **Bug Report** template.
3. Include reproduction steps, expected behavior, actual behavior, and environment details (OS, Go version, K8s version if applicable).

### Suggesting Features

1. Open an issue using the **Feature Request** template.
2. Describe the use case and the proposed solution.
3. **For significant features, propose an ADR.** Open a PR adding a draft ADR under `website/content/project/adrs/` with status "Proposed." Discussion happens on the PR.

### Submitting Code

We welcome pull requests. To make the review process fast and pleasant:

#### 1. Discuss First (for non-trivial changes)

Open an issue or comment on an existing one before starting work on anything beyond a small bug fix. This avoids duplicate effort and misaligned designs.

#### 2. Follow the Engineering Standards

Dexaflow has strict engineering standards documented in the ADRs:

- **[ADR 0011 — TDD Strict](website/content/project/adrs/0011-tdd-strict.md):** every production change is preceded by a failing test. Two-commit pattern preferred (`test:` followed by `feat:`).
- **Bug fixes carry a regression test:** a test that reproduces the bug, fails without the fix and passes with it, committed before the fix (`test:` followed by `fix:`). See the [Fix a bug](website/content/contribute/recipes/fix-a-bug.md) recipe.
- **[ADR 0012 — Code Quality Standards](website/content/project/adrs/0012-code-quality-standards.md):** Go Report Card A+ as floor. GoDocs mandatory on every exported identifier. Cyclomatic complexity ≤ 15.
- **Go files stay at or under 1,500 lines** outside tests and generated code (`scripts/check-go-file-length.sh`). The files over it today are pinned in `scripts/go-file-length-allowlist.txt` and may only shrink.
- **[ADR 0014 — Supply Chain Security](website/content/project/adrs/0014-supply-chain-security.md):** vulnerability scans, signed commits encouraged, no introduction of unsafe patterns.

CI enforces all of the above automatically. PRs that fail CI cannot be merged.

#### 3. Branch and PR Conventions

- Branch from `main`. Name: `feat/<short-description>`, `fix/<short-description>`, `docs/<short-description>`, `test/<short-description>`.
- One logical change per PR. If you find yourself describing the PR with "and also...", split it.
- Text is in English, and new text has no em dashes: use a comma, a colon,
  parentheses or two sentences. Commit messages carry no `Co-Authored-By`
  trailer, no session link and no generated-by footer. The "Text rules" check
  on every pull request names the file and line, or the commit, that breaks
  either rule, and `make setup` installs hooks that check the same before a
  commit (`scripts/lint-new-text.sh`). Text that was there before keeps its
  em dashes until a cleanup of its own.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):
  - `feat: add XCom schema validation`
  - `fix: handle pod OOMKilled in K8s executor`
  - `test: failing test for retry backoff`
  - `docs: clarify executor configuration`
  - `chore: bump dependencies`
  - `refactor: extract scheduler decision logic`

#### Sign Off Your Commits (DCO)

Every commit in a pull request carries a sign-off from its author under the
[Developer Certificate of Origin](https://developercertificate.org): a line
at the end of the message,

```text
Signed-off-by: Your Name <you@example.com>
```

with the name and email of the commit's author. It certifies that you wrote
the change, or otherwise have the right to submit it under the project's
license. `git commit -s` adds it, and the `prepare-commit-msg` hook that
`make setup` installs adds it to every commit for you. To sign off the
commits already on your branch, run `git rebase --signoff main` and push
again.

The "DCO" check on every pull request names each commit without a sign-off
from its author. Merge commits and Dependabot's commits are exempt.

#### 4. Record the Change

Every pull request that changes shipped behavior records what changed. It does
**not** do that by editing `CHANGELOG.md`. It writes one small file instead:

```bash
make changelog          # or: changie new
```

`changie` asks for a kind (Added / Changed / Deprecated / Removed / Fixed /
Security) and a one-line body, then writes `.changes/unreleased/<slug>.yaml`.
Commit that file with your change. The release cut assembles every pending
fragment into `CHANGELOG.md` under `## [Unreleased]`, so the entry reaches the
published changelog without anyone hand-merging it.

Write the body the way the existing CHANGELOG entries are written: what an
operator or DAG author will notice, in the imperative, with the issue or PR
number. `- **Pods no longer restart on an OOM kill.** ... (#1216)` rather than
`- fix reconcile`.

The point of the fragment is the conflict it does not cause. `CHANGELOG.md` has
exactly one `## [Unreleased]` section, so every open PR edits the same handful
of lines: each merge conflicts the others, each conflict costs a rebase, and
each rebase re-runs a full CI matrix. Resolving those conflicts by keeping both
sides also corrupted the file in practice, leaving five headings for three
kinds. Two fragments are two different files and cannot conflict at all.

Editing `CHANGELOG.md` by hand still passes the gate, because a change that is
recorded is recorded. Prefer the fragment, and never edit a dated
(`## [X.Y.Z] - <date>`) section: those are published release notes, and the
release tooling copies them between branches verbatim.

Once your fragment is merged, leave it alone. The release cut folds it into
`CHANGELOG.md` and deletes it; when a patch release ships it from
`release-X.Y`, a `changelog: sync vX.Y.Z to main` pull request deletes it from
`main` too. A cherry-pick to a release branch carries the fragment unchanged,
with the same file name: renaming or rewording it on one side is how a shipped
entry gets published twice. The **CHANGELOG structure** check validates every
fragment (it parses, has a declared `kind` and a non-empty `body`) and fails a
pull request that adds one whose text already shipped. The whole lifecycle, and
what to do when it drifts, is in
[RELEASING.md, How the changelog flows](RELEASING.md#how-the-changelog-flows).

If the PR has no user-facing change at all (release prep, a chore, a
dependency bump, a docs-only edit), add a line with the reason to the PR
description instead, or apply the **`skip-changelog`** label if you can:

```
Skip-changelog: test-only change, nothing a user can notice
```

Dependabot is exempt automatically.

The **docs guard** works the same way for the docs site. A PR that changes a
chart value, the authoring schema, a CLI command, a server setting, a
migration, the OpenAPI document, or adds a changelog fragment of kind Added,
Changed, Deprecated or Removed updates `website/content/` in the same PR. When
nothing in it is user-discoverable, add a line with the reason to the PR
description (the **`skip-docs`** label is optional):

```
Skip-docs: internal refactor, no setting or behaviour an operator can see
```

The label alone, without the line, fails the guard. The release cut reads the
reason back (`scripts/docs-gap.sh`), so write it for the person cutting the
release.

The **API compatibility** check (`scripts/api-compat.sh`) compares the three
public APIs with the base branch: the agent protocol in `proto/` (`buf
breaking`, with the rules in `buf.yaml`), the REST API in
`docs/api/openapi.yaml` (`oasdiff breaking`), and the Go client in `pkg/client`
(`apidiff`). Removing or renaming a proto field, an OpenAPI path or parameter,
or an exported `pkg/client` symbol fails it. When the break is intended, the
PR needs both:

- the **`breaking-change`** label, which a maintainer applies after agreeing
  to the break, and
- a changelog fragment of kind `Changed` or `Removed` that names what broke and
  what users do about it.

Run it locally against `main` before pushing:

```bash
git worktree add /tmp/base origin/main
scripts/api-compat.sh /tmp/base    # needs buf, oasdiff and apidiff on PATH
```

#### 5. Pull Request Process

1. Fork the repository and create your branch.
2. Make your changes following TDD discipline.
3. Run `make lint test` locally before pushing.
4. Push and open a PR using the template.
5. Fill in the PR description completely. Linked issue, what changed, what was tested, screenshots if UI is affected.
6. Wait for CI. If any check fails, fix and push again.
7. Address review feedback. We aim for a first response within 7 days; ping on the PR if it is quiet.
8. Maintainers will squash-merge or rebase-merge based on the change.

**Pull requests from a fork** get the same checks, and none of them needs an
action only a maintainer can take:

- Labels: you cannot apply `skip-changelog` or `skip-docs` on a fork, so use
  the `Skip-changelog: <reason>` and `Skip-docs: <reason>` lines above. Both
  guards re-run when you edit the description.
- Milestone: only maintainers can set one. The milestone guard passes a fork's
  pull request with a notice instead of failing it.
- Secrets: no workflow that runs on pull requests uses a repository secret, and
  none runs on `pull_request_target` (`scripts/check-fork-safe-workflows.sh`
  enforces both), so nothing waits on a secret your fork cannot have.
- First run: the workflows of a first-time contributor's pull request wait
  until a maintainer approves them (the repository requires approval for
  first-time contributors), so a pending run on your first pull request is
  expected and needs nothing from you.

Before merging a pull request from a fork, the maintainer:

1. sets the release milestone (`vX.Y.Z`), which re-runs the milestone guard;
2. reads the `Skip-changelog:` and `Skip-docs:` reasons, if any, and agrees
   with them (the release cut reads the docs reason back).

## Security-Sensitive Changes

Changes to the following areas require extra review and are not accepted from first-time contributors:

- `internal/auth/` (JWT, RBAC)
- `internal/executor/` (pod creation, K8s API)
- `internal/storage/` (SQL queries, database access)
- `migrations/` (schema changes)
- `proto/` (gRPC contract between core and agent)
- Anything affecting how credentials or secrets are handled

If you have a contribution in these areas, please open a discussion issue first.

### Writing a Migration

Migrations live in `migrations/` as `NNN_name.up.sql` and `NNN_name.down.sql`,
numbered one past the highest file on `main`. They are embedded in the binary
and applied by golang-migrate on start and by the chart's pre-upgrade Job.

- **Every up has a down.** When a change cannot be undone (a data fix, an enum
  value), the down file is a comment that says why it is a no-op, and the up
  must be safe to apply again.
- **Built-in roles change in every tenant.** A tenant created through the
  service API copies the built-in roles and their grants from `default` once,
  when it is created. A migration that adds, changes or revokes a built-in role
  or one of its grants must therefore apply to every tenant's built-in roles
  (join on `roles.is_system` across all tenants), never filter on
  `t.name = 'default'`, and must not touch custom roles (`is_system = false`)
  or `user_roles`. `migrations/tenant_roles_test.go` fails an up migration that
  writes `roles` or `role_permissions` and names the default tenant
  (#1305).
- **Renumbering is the author's job.** Numbers must be contiguous
  (`migrations/sequence_test.go`), so when another pull request merges a
  migration with your number first, merge `main` into your branch and rename
  yours to the next free number before the pull request enters the merge
  queue. Once the merge queue is on for `main`, it tests each pull request on
  top of the ones ahead of it, so two pull requests with the same number
  cannot both merge: the second fails `Test + Coverage` in the queue and is
  removed from it.

## Development Environment

```bash
# Clone the repo
git clone https://github.com/dexadata/dexaflow.git
cd dexaflow
```

### See it run first (one command)

```bash
docker compose --profile demo up --build
# open http://localhost:8080 and log in as admin@dexaflow.local / admin
# (admin@leoflow.local on a volume created before the rename)
# stop with: docker compose --profile demo down   (add -v to wipe data)
```

### Set up for development

```bash
# Read AGENTS.md first: the standing rules for PRs, releases and CI.

make setup        # Go tools, Python parser/runtime, pre-commit hook
make build        # build bin/dexaflow, bin/dexaflow-server, bin/dexaflow-agent (plus leoflow* links)
make dev-up       # start Postgres + Redis (Docker) and apply migrations
make lint test    # the quality gates you must pass before pushing
```

For an end-to-end author→run loop without Kubernetes, use `dexaflow lite`
(see [Editions & operating modes](website/content/concepts/editions.md)).

## Project Layout

See [`README.md`](README.md) for the high-level layout. Detailed module documentation lives in package-level `doc.go` files (mandatory per ADR 0012).

## Communication

See [`SUPPORT.md`](SUPPORT.md) for where to ask questions, report bugs, and report security issues.

- GitHub Issues for bugs and feature requests
- GitHub Discussions for design questions and general help

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).

## Recognition

All contributors are shown on the repository's [contributors page](https://github.com/dexadata/dexaflow/graphs/contributors). Significant contributions are also highlighted in release notes.

Thank you for helping make Dexaflow better!
