---
title: Add a CLI command or flag
linkTitle: Add a CLI command
weight: 70
description: Add a dexaflow command or flag, regenerate the CLI reference, and keep every command written in the docs runnable.
---

The CLI lives in `internal/cli/`; `NewRootCommand` in `root.go` builds the
command tree.

1. **Write the test first** in `internal/cli/`, driving the command the way a
   user runs it and asserting on its output and exit status.
2. **Add the command or flag.** A new command goes in its group in
   `NewRootCommand` (authoring, runtime, inspection, operations or lifecycle) so
   `dexaflow --help` lists it where a user looks. Flags are kebab-case; a flag
   that overrides a persisted setting is mapped in `flagToKey`
   (`internal/config/config.go`).
3. **Keep old spellings working.** Flags are a public surface: in a patch they
   only gain things
   ([ADR 0068](/project/adrs/0068-patch-content-gated-by-safety/), safety bar
   point 4).
4. **Regenerate the CLI reference**, which is generated from Cobra and
   committed:

   ```sh
   ./website/scripts/gen-cli.sh   # writes website/content/reference/cli/
   ```

   The site build reruns it, so a stale page is overwritten, but commit the
   result so the PR shows the change.
5. **Update the pages that use it.** `internal/cli/*.go` is user-facing surface
   for the docs guard. `TestDocumentedFlagsExist` reads every `dexaflow ...`
   command in a code block or code span of the docs site and of `examples/`
   (ADRs and specs excluded), and fails when one names a subcommand or flag
   the binary does not have, so a rename updates those pages in the same PR.
   Record a changelog fragment ([record a change](/contribute/recipes/record-a-change/)).
6. **Check:** `go test ./internal/cli/` and `make lint test`.
