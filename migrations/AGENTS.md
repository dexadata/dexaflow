# migrations/

Rules for changing the database schema. The root [AGENTS.md](../AGENTS.md)
applies too; the step by step is in the
[add a migration recipe](https://dexaflow.dexadata.ai/contribute/recipes/add-a-migration/).

- **Numbers are contiguous.** Up versions run 1..N with no gap and no
  duplicate, and every `NNN_name.up.sql` has an `NNN_name.down.sql` with the
  same name (`sequence_test.go`). When another PR took your number, renumber
  yours to the next free one when you merge `main`. Never renumber a migration
  that already shipped in a release: an upgraded database would skip or repeat
  it.
- **Every up has a down.** When a change cannot be undone (a data fix, an enum
  value), the down file is a comment saying why it is a no-op, and the up must
  be safe to apply again. CONTRIBUTING.md, "Writing a Migration", has the
  longer form of these rules.
- **Concurrent index builds do not use `IF NOT EXISTS`.** An interrupted
  `CREATE INDEX CONCURRENTLY` leaves an INVALID index with the final name; with
  `IF NOT EXISTS` the retry would skip it and record the migration as applied.
- **Bound lock waits.** A statement that takes a strong lock on a large table
  runs in a transaction with `SET LOCAL lock_timeout`, so it fails fast and can
  be retried instead of queuing every other query behind it.
- **Built-in roles change for every tenant.** A migration that adds, changes or
  revokes a built-in role or grant joins on `roles.is_system`; it never names
  the `default` tenant alone (`tenant_roles_test.go`). Custom roles are never
  touched.
- **Only `default` is seeded.** No migration creates another tenant
  (`tenant_seed_test.go`).
- **Patch releases** may carry a migration only when it meets the safety bar of
  [ADR 0068](https://dexaflow.dexadata.ai/project/adrs/0068-patch-content-gated-by-safety/):
  additive, compatible with the previous patch's binary, down file tested.
- After a schema change, regenerate the queries with `make sqlc` and test with
  `make test-integration`.
