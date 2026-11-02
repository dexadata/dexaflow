-- 056_usage_events_pruned.up.sql
-- The newest usage event the retention janitor has deleted (ADR 0096 point 5,
-- #2174). The janitor deletes events in the order the service API serves
-- them, (xid, id), and stops at the first one inside the window, so what it
-- deleted is always a prefix and this one row marks its end. A reader whose
-- cursor is below the mark has lost events: the route answers 410 with the
-- mark as the oldest cursor still complete.
--
-- A new table that nothing else reads: no lock on a table in use, and every
-- statement can be repeated.

BEGIN;

CREATE TABLE IF NOT EXISTS usage_events_pruned (
    id        BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    xid       XID8,
    event_id  BIGINT,
    pruned_at TIMESTAMPTZ,
    CHECK ((xid IS NULL) = (event_id IS NULL))
);

INSERT INTO usage_events_pruned (id) VALUES (TRUE) ON CONFLICT (id) DO NOTHING;

COMMIT;
