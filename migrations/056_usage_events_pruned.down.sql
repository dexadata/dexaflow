-- 056_usage_events_pruned.down.sql
-- Drop the pruned mark. The events are untouched; a reader below the mark is
-- no longer told that it lost events.

BEGIN;

DROP TABLE IF EXISTS usage_events_pruned;

COMMIT;
