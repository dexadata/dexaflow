-- Leave 15% of each task_instances heap page free (performance item D3), so the
-- state transitions and heartbeats every row receives can put the new row
-- version on the same page and stay HOT. Applies to pages written from now on;
-- existing pages are only repacked by a table rewrite (VACUUM FULL, pg_repack),
-- which this migration deliberately does not do. Takes a brief SHARE UPDATE
-- EXCLUSIVE lock, which does not block reads or writes.
ALTER TABLE task_instances SET (fillfactor = 85);
