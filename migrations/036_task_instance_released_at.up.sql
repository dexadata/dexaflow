-- Release stamp for the orphan-run reaper. The retry release, the reschedule
-- re-dispatch, the infra re-place and an operator clear all send a task instance
-- back to 'none' and clear its per-attempt timestamps (queued_at, scheduled_at,
-- started_at, ended_at), and the planner only moves it on to 'scheduled' on the
-- next tick. The orphan reaper measures a run's last activity from those
-- timestamps, so for that one tick a healthy run whose other activity is older
-- than the orphan threshold looked abandoned and could be failed as orphaned.
--
-- released_at records the last time the task instance was sent back to 'none'
-- for another attempt, and the reaper counts it as activity. It is a dedicated
-- column because none of the existing ones fits: queued_at and scheduled_at are
-- per-attempt stamps that must stay NULL until the next attempt reaches those
-- states (the dispatch-lost reaper keys on a fresh queued_at).
--
-- Nullable and never cleared: a task instance that was never released keeps it
-- NULL, and GREATEST ignores NULLs, so existing rows behave exactly as before.
ALTER TABLE task_instances
    ADD COLUMN IF NOT EXISTS released_at TIMESTAMPTZ;
