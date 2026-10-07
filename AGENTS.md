# AGENTS.md

Standing rules for everyone who changes this repository: maintainers, contributors
and automated agents alike. Read this file before opening a pull request or cutting
a release. Codebase context (architecture, tech stack, code conventions, testing
discipline) lives in [`.github/CLAUDE.md.template`](.github/CLAUDE.md.template);
the release mechanics live in [`RELEASING.md`](RELEASING.md). When this file and
an ADR disagree, the ADR wins and this file gets fixed.

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
   (`.github/workflows/e2e-gates.yaml`).
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
