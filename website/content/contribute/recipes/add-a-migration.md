---
title: Add a migration
linkTitle: Add a migration
weight: 10
description: Add a schema change that upgrades and rolls back cleanly, with the checks the migration tests enforce.
---

The rules are in [`migrations/AGENTS.md`](https://github.com/dexadata/dexaflow/blob/main/migrations/AGENTS.md).

1. **Pick the number.** Take the highest number in `migrations/` on the latest
   `main` and add one. Check open PRs that add a migration too; whoever merges
   second renumbers.
2. **Write both files** with the same name:
   `NNN_short_name.up.sql` and `NNN_short_name.down.sql`. Start each with a
   comment saying what it changes and why, and the issue or ADR.
3. **Write every migration as expand/contract (ADR 0090).** A release only
   adds (the expand phase): new tables, new indexes, and columns that are
   nullable or carry a constant default. The previous release's binary must
   continue to run on the newer schema so that `helm rollback` is always safe.
   Removals, renames, and NOT NULL columns without defaults happen in a
   contract phase at least one minor release later, once no supported release
   binary expects the old shape. When targeting a patch, keep it additive and
   within the safety bar of
   [ADR 0068](/project/adrs/0068-patch-content-gated-by-safety/).
4. **Regenerate and test:**

   ```sh
   make sqlc                 # queries, when the schema they read changed
   go test ./migrations/     # numbering, pairs, index and role rules
   make test-integration     # the storage layer against a real Postgres
   ```

5. **Prove upgrade and rollback** on a database with data from the previous
   release: `make migrate-up`, check the server, then `make migrate-down` once
   per new migration and check again. Put the timings in the PR; the release
   candidate review needs them (AGENTS.md, review 7).
6. **Document it.** A migration is user-facing: add a section to
   [upgrades](/operate/upgrades/) when it takes noticeable time or needs an
   action, and a changelog fragment. Every migration also gets its row in
   the recovery table of
   [an interrupted migration](/operate/upgrades/#an-interrupted-migration):
   the version left dirty, what to do before `force`, and the version to
   force (the one whose schema the database holds once the file was cut
   short).
7. **Before merging**, merge the latest `main` into your branch; if the number
   is taken, rename both files to the next free number and rerun step 4.
