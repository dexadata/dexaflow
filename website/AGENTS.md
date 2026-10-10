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
- **New names first.** Examples use `dexaflow`, `DEXAFLOW_*` and
  `dexaflow.yaml`; the names from before the rename appear only where the page
  explains that they still work.
- **Pages are written for the reader's task.** Recipes and how-to pages give
  steps; reference pages list facts; concepts explain. Keep the three apart.
- **Specs** for large changes go in `content/project/specs/` before the code
  ([ADR 0069](https://dexaflow.dexadata.ai/project/adrs/0069-contributor-context/)).
