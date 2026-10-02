-- Leave 15% of each task_instances heap page free (performance item D3), so the
-- state transitions and heartbeats every row receives can put the new row
-- version on the same page and stay HOT. Applies to pages written from now on;
-- existing pages are only repacked by a table rewrite (VACUUM FULL, pg_repack),
-- which this migration deliberately does not do.
--
-- Takes a brief SHARE UPDATE EXCLUSIVE lock, which does not block reads or
-- writes but does wait behind a running (auto)vacuum or index build, and while
-- it waits every later conflicting lock request queues behind it. lock_timeout
-- bounds that wait. If it fires, the transaction rolls back, golang-migrate
-- marks version 31 dirty and nothing changed: run `migrate force 30` and retry
-- when the table is quiet.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instances SET (fillfactor = 85);
COMMIT;
