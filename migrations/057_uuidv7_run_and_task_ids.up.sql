-- 057_uuidv7_run_and_task_ids.up.sql
-- New dag_runs and task_instances rows get a time-ordered UUIDv7 key instead
-- of a random UUIDv4 (#525, ADR 0098). A random key inserts all over the
-- primary key index, so once the index outgrows the cache every insert reads
-- a leaf back in and dirties it; Experiment 9 (test/load/BASELINE.md) measured
-- a third of the insert rate, a third more WAL per row and a 27% larger index
-- at 5 million rows. A UUIDv7 appends to the right edge of the index.
--
-- dexaflow_uuidv7() builds an RFC 9562 version 7 UUID from the clock and a
-- UUIDv4: the first 48 bits are the Unix time in milliseconds, the version
-- nibble becomes 7, and the variant and the remaining 74 random bits are the
-- UUIDv4's. It runs on every supported Postgres; Postgres 18's built-in
-- uuidv7() is not used so that 16 and 17 get the same keys. Ids made in the
-- same millisecond are not ordered among themselves, which the index does not
-- need.
--
-- Only the defaults change: the type stays uuid, existing ids keep their
-- value and every foreign key is untouched, so nothing is rewritten. Each
-- ALTER takes a brief ACCESS EXCLUSIVE lock on its table; lock_timeout bounds
-- the wait for it: past 5 seconds the transaction rolls back, golang-migrate
-- marks version 57 dirty and nothing changed; force 56 and retry when the
-- tables are quiet. Every statement can be repeated.
--
-- The application never writes these ids itself, and 0.5.8 and earlier never
-- read the default, so a previous release keeps running against this schema
-- and its rows get UUIDv7 keys too.

BEGIN;

SET LOCAL lock_timeout = '5s';

CREATE OR REPLACE FUNCTION dexaflow_uuidv7() RETURNS uuid
LANGUAGE sql VOLATILE PARALLEL SAFE AS $$
    SELECT encode(
        set_bit(
            set_bit(
                overlay(uuid_send(gen_random_uuid())
                        PLACING substring(int8send(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
                        FROM 1 FOR 6),
                52, 1),
            53, 1),
        'hex')::uuid
$$;

ALTER TABLE dag_runs ALTER COLUMN id SET DEFAULT dexaflow_uuidv7();
ALTER TABLE task_instances ALTER COLUMN id SET DEFAULT dexaflow_uuidv7();

COMMIT;
