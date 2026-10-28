---
title: "ADR 0069: Contributor context is public and tool-agnostic; tool files stay local"
linkTitle: "0069 · Contributor context"
weight: 690
description: "ADR 0069: what a contributor needs to change Dexaflow correctly lives in the repository as plain Markdown (AGENTS.md, nested AGENTS.md, recipes, specs); files that configure one editor or assistant stay out of it."
---

**Status:** Accepted
**Date:** 2026-10-09 (proposed and accepted the same day by the project owner)
**Relates:** ADR 0011 (strict TDD), ADR 0062 (release branches), ADR 0068 (patch content).

> **Numbering.** 0063 (#1365) and 0067 (#1517) are open PRs, and 0068 is
> #1582, so this record takes 0069.

## Context

Most changes to Dexaflow are made with the help of coding assistants, and each
assistant reads its own file: one reads `CLAUDE.md`, others read `AGENTS.md`,
`GEMINI.md`, `.cursor/rules` or `.github/copilot-instructions.md`. Until #1550
the repository carried one of these tool files, holding project rules next to
instructions for one tool, while the same knowledge was also spread across
CONTRIBUTING.md, ADRs, script headers and test comments.

Two problems follow. Knowledge a contributor needs (migration numbers must be
contiguous, a cherry-pick needs `-x`, a behavior change needs a `Changed`
fragment for the docs gate) is rediscovered by every new session and every new
person, because it lives in a test comment or in someone's notes. And a file
written for one tool is noise for every other contributor, and ties the project
to that tool.

## Decision

### 1. One test decides where a piece of context lives

If a contributor working without any assistant would need it to make a correct
change, it is **public**: committed, in English, written for people. If it is
about one tool, one person's preferences or work in progress, it is **local**:
ignored by git and never committed.

### 2. The public layer

| What | Where | Purpose |
|---|---|---|
| Standing rules and the map | `AGENTS.md` at the root | Rules every change follows, and links to everything below. Short: it points, it does not explain. |
| Area rules | `AGENTS.md` in a directory (`migrations/`, `helm/`, `website/`, `scripts/`, `internal/api/`) | Rules that apply only when changing that directory. The nearest file to the changed code applies, on top of the root one. |
| Recipes | `website/content/contribute/recipes/` | Step by step for a recurring change: add a migration, backport to a release branch, record a change. Each recipe names the script or `make` target that does the work. |
| Specs | `website/content/project/specs/` | What a large change will build, written before the code: scope, interfaces, migrations, the ADR 0068 safety bar, tests, rollout. |
| Decisions | `website/content/project/adrs/` | Why a choice was made. Unchanged by this record. |
| Package overviews | the `// Package` comment of each Go package | What the package owns and what it must not do. Already present in all 31 `internal/` packages. |

`AGENTS.md` is the entry point because most assistants read it natively and the
rest can be pointed at it from a local file (`@AGENTS.md`). Nothing in the
public layer addresses a specific tool.

A procedure lives in a script or a `make` target, not in prose: the recipe says
when to run it and what to check, and the script stays the single source of
the steps.

### 3. The local layer

Ignored by `.gitignore` and checked by `scripts/check-no-tool-context.sh` in CI:

- tool files: `CLAUDE.md`, `CLAUDE.local.md`, `GEMINI.md`, `COPILOT.md`,
  `.cursorrules`, `.claude/`, `.cursor/`, `.windsurf/`, `.continue/`, `.aider*`,
  `.github/copilot-instructions.md`;
- `.local/`, a free directory for personal notes, plans and drafts inside a
  clone.

A local tool file should load `AGENTS.md` and add only what is specific to the
tool or the person.

### 4. Keeping it true

- A change that alters a rule updates the `AGENTS.md` that states it in the same
  PR, as code and docs already move together under the docs guard.
- When a review or an incident teaches something a contributor would need next
  time, it becomes a line in the nearest `AGENTS.md` or a recipe step, not only
  a comment in the PR.
- Rules that can be checked by a script are checked by a script; the text then
  names the check.

## Alternatives considered

- **Commit one file per tool.** Rejected: duplicated rules drift, and the
  repository would endorse specific tools.
- **Everything in CONTRIBUTING.md.** Rejected: one long file is read once and
  then skipped, and area rules are needed exactly when that area is touched.
- **Keep it all private.** Rejected: outside contributors would hit the same
  traps with no way to learn them before review.

## Consequences

- New contributors and new sessions read the same rules.
- The root `AGENTS.md` grows only by links; detail goes to area files and recipes.
- A CI check blocks committing a tool file by accident.
