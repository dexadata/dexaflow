---
title: "ADR 0098: new runs and task instances get time-ordered UUIDv7 keys"
linkTitle: "0098 · UUIDv7 run and task keys"
weight: 980
description: "ADR 0098: dag_runs and task_instances draw new primary keys from dexaflow_uuidv7(), a SQL function that builds an RFC 9562 version 7 UUID on every supported Postgres, instead of the random gen_random_uuid(). Only the column defaults change: the type, existing ids and foreign keys stay as they are. Decided on the numbers of Experiment 9 (#525)."
---

**Status:** Accepted
**Date:** 2026-10-29
**Relates:** ADR 0068 (patch safety bar), ADR 0090 (expand/contract migrations)
**Issues:** #525

## Context

Every table keys its rows with `gen_random_uuid()`, a random UUIDv4. A random
key lands anywhere in the primary key index, so once the index no longer fits
the cache each insert reads a leaf back in, dirties it and, after a
checkpoint, writes a full page image of it to the WAL. Leaves also split half
full, so the index is larger than it needs to be.

Experiment 9 (`test/load/uuid_keys`, results under "Primary key order" in
`test/load/BASELINE.md`) measured it on 5 million rows the width of a task
instance. Against a UUIDv7, the random key inserted about a third fewer rows
per second over the last tenth of the rows, wrote a third more WAL per row,
built a key index 27% larger and served point lookups about a sixth slower. The heap was the same size.

The control plane inserts far below those rates, so the key is not a
throughput limit today. It shows as WAL volume, index bloat and cache pressure
on a large install, and `dag_runs` and `task_instances` are the tables that
grow with every run.

## Decision

1. **New rows of `dag_runs` and `task_instances` get a UUIDv7 key.**
   Migration 057 sets the default of both `id` columns to
   `dexaflow_uuidv7()`. Nothing else changes: the type stays `uuid`, existing
   ids keep their value, and no foreign key or index is rebuilt.

2. **The generator is a SQL function, not Postgres 18's `uuidv7()`.**
   `dexaflow_uuidv7()` takes a UUIDv4 from `gen_random_uuid()`, writes the Unix
   time in milliseconds over its first 48 bits and sets the version nibble to
   7, which is RFC 9562's layout with 74 random bits. It runs on every
   supported Postgres, so 16, 17 and 18 make the same kind of key. Ids made in
   the same millisecond are not ordered among themselves; the index does not
   need them to be.

3. **The database keeps generating the ids.** No insert of either table names
   an id, so the change needs no code, and a previous release running against
   the new schema makes UUIDv7 keys too.

4. **Other tables keep UUIDv4 for now.** They grow far more slowly; any of
   them can follow with its own `SET DEFAULT` when a measurement asks for it.

## Consequences

- A UUIDv7 carries the time its row was created, to the millisecond, and run
  and task instance ids are served by the API. The same time is already
  served next to them (`queued_at`, `start_date`, a run's logical date), so
  this exposes nothing new; it does mean an id alone tells when it was made.
- The migration only changes two defaults, so it is additive in the sense of
  ADR 0068 and ADR 0090: a previous binary boots and runs on it, and its down
  restores `gen_random_uuid()` and drops the function. Ids made as UUIDv7
  stay valid after a down.
- Existing rows keep their random keys, so the index gains its benefit as new
  rows come in and retention removes old ones. Nothing rewrites the table.
- Ordering by id is not a contract. Code that needs creation order keeps
  ordering by its timestamp columns.
