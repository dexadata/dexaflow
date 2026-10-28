-- 055_usage_events.down.sql
-- Drop the usage events and the trigger that writes them. Every recorded
-- event is lost; task_instances and its history are untouched.

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS task_instances_usage_event ON task_instances;
DROP FUNCTION IF EXISTS record_usage_event();
DROP TABLE IF EXISTS usage_events;
DROP TABLE IF EXISTS usage_events_switch;

COMMIT;
