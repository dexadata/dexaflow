---
title: "Review checklist: docs"
linkTitle: Docs
weight: 30
description: Check that a page sits in the right kind of section, starts from the reader's task, uses the current names, and passes the docs gates.
---

The rules are in [`website/AGENTS.md`](https://github.com/dexadata/dexaflow/blob/main/website/AGENTS.md).
This checklist is what a reviewer reads a docs change against.

## Placement

The site follows the four kinds of documentation of the
[Diátaxis](https://diataxis.fr/) framework, one kind per section:

| Kind | Reader | Where it lives |
|---|---|---|
| Tutorial | learning by doing, start to finish | `get-started/` |
| How-to | doing one task they already chose | `author-dags/`, `operate/`, `connections/`, `contribute/recipes/` |
| Reference | looking up a fact | `reference/` (much of it generated) |
| Explanation | understanding why | `concepts/`, `project/adrs/` |

- [ ] **The page is in the section of its kind.** A how-to does not stop to
  explain the design (link to the concept instead); a reference page lists
  facts without steps; a concept page has no procedure.
- [ ] **It is not a second copy.** A fact that already has a home (a setting in
  the [configuration reference](/reference/configuration/), a flag in the
  [CLI reference](/reference/cli/)) is linked, not repeated.

## Reader task first

- [ ] **The first lines say what the reader can do** with the page and when
  they need it, before any background.
- [ ] **Steps are runnable as written:** commands in order, with the values the
  reader has to replace marked. `TestDocumentedFlagsExist` (in
  `internal/cli`) fails a `dexaflow` command in a code block or code span that
  names a flag or subcommand the binary does not have.

## New names first

- [ ] Examples use `dexaflow`, `DEXAFLOW_*` and `dexaflow.yaml`. `leoflow`,
  `LEOFLOW_*` and `leoflow.yaml` appear only where the page explains that they
  still work.
- [ ] No page pins a release to install by hand: use the
  `{{</* latest-version */>}}` shortcode. `scripts/check-stale-versions.sh`
  fails a page that tells readers to install a release older than the newest
  one (ADRs and other records under `project/` are exempt). At a release cut
  it runs as `--candidate <tag>` and judges against the release being cut,
  since `versions.json` still names the GA it replaces.

## The gates

- [ ] **Docs moved with the code.** A PR that changes a chart value, the
  authoring schema, a CLI command, a server setting, a migration or the
  OpenAPI document changes `website/content/` too, or carries `skip-docs` with
  a `Skip-docs: <reason>` line (`scripts/check-docs-updated.sh`, run by the
  docs guard). `scripts/docs-gap.sh` reads the same rule back at release cut.
- [ ] **Generated pages were regenerated, not edited.** `reference/cli/` comes
  from `website/scripts/gen-cli.sh`, `reference/go/` from `gen-go.sh`, and the
  chart README from `helm-docs`
  ([build the docs](/contribute/build-docs/#generate-the-reference-do-this-before-building)).
- [ ] **Accepted ADRs were not edited.** A changed decision is a new ADR.
- [ ] **The site builds:** `hugo --gc --minify` in `website/`, also run by
  `make ci-local` when Hugo is installed and by the website build workflow.
