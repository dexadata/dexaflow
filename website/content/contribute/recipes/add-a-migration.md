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
3. **Keep it additive** when it targets a patch: a new table, a nullable column
   or a column with a constant default, or an index built with
   `CREATE INDEX CONCURRENTLY` (without `IF NOT EXISTS`). Anything that rewrites
   data or locks a large table waits for a minor
   ([ADR 0068](/project/adrs/0068-patch-content-gated-by-safety/)).
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
   action, and a changelog fragment.
7. **Before merging**, merge the latest `main` into your branch; if the number
   is taken, rename both files to the next free number and rerun step 4.
