# website/

Rules for changing the documentation site (Hugo and Docsy). The root
[AGENTS.md](../AGENTS.md) applies too; building the site locally is described
in [build the docs](content/contribute/build-docs.md).

- **Docs move with the code.** A PR that changes behavior, flags,
  configuration, migrations or the API updates the matching pages; the docs
  guard and `scripts/docs-gap.sh` check it at PR and at release cut. A PR with
  nothing user-facing uses the `skip-docs` label with a `Skip-docs: <reason>`
  line in its description.
- **Accepted ADRs are immutable.** A new decision gets a new ADR that names the
  one it replaces. Pick the next free number, checking open PRs as well.
- **A dated note may be appended to an accepted ADR** when a statement in it no
  longer matches the system as built: a blockquote starting
  `**Note (YYYY-MM, #issue).**` placed next to the passage it corrects, saying
  what is true now and linking the page or ADR that has the detail. The
  decision text itself is never edited, and an earlier note is never rewritten:
  a later change gets a note of its own. A note records facts; a changed
  decision still needs a new ADR. Examples: ADR 0003, 0014 and 0041.
- **The configuration reference tables are generated.** In
  `content/reference/configuration.md`, the rows between `BEGIN GENERATED` and
  `END GENERATED` markers come from `internal/config`: change a purpose or an
  edition in `internal/tools/configref/settings.yaml` (or the field's doc
  comment), then run `make gen-config-reference`. The prose around the markers
  is edited by hand. `go test ./internal/tools/configref/` fails on a stale page
  and on a `DEXAFLOW_*` variable the code reads but the page does not list.
- **New names first.** Examples use `dexaflow`, `DEXAFLOW_*` and
  `dexaflow.yaml`; the names from before the rename appear only where the page
  explains that they still work.
- **Pages are written for the reader's task.** Recipes and how-to pages give
  steps; reference pages list facts; concepts explain. Keep the three apart.
- **Specs** for large changes go in `content/project/specs/` before the code
  ([ADR 0069](https://dexaflow.dexadata.ai/project/adrs/0069-contributor-context/)).
