-- Seed for the skip-version upgrade test (#2311): a production-sized history
-- written with the columns of the oldest supported release's schema only, so
-- the same file seeds any release from it on. Every run is finished and every
-- DAG paused, so the control plane that boots on the upgraded database has an
-- empty active set and the rows only weigh on the migrations.
--
-- Variables (psql -v): dags, runs_per_dag, tasks_per_run, history_every
-- (every Nth task instance gets one archived earlier try), audit_rows.
-- Defaults of test/upgrade/skip-version.sh: 1,000 x 1,000 runs (1 M) x 5
-- tasks (5 M task instances), history on every 10th, 1 M audit rows.
\set ON_ERROR_STOP on
SET synchronous_commit = off;

CREATE TEMP TABLE seed_dags AS
SELECT gen_random_uuid() AS id, gen_random_uuid() AS version_id, n
FROM generate_series(1, :dags) AS n;

INSERT INTO dags (id, tenant_id, dag_id, is_paused, created_at, updated_at)
SELECT d.id, t.id, 'upgrade_seed_' || d.n, true, now() - interval '400 days', now() - interval '400 days'
FROM seed_dags d, (SELECT id FROM tenants WHERE name = 'default') t;

INSERT INTO dag_versions (id, dag_id, version, image_reference, spec, spec_hash, created_at)
SELECT d.version_id, d.id, '1', 'ghcr.io/dexadata/upgrade-seed:1',
       jsonb_build_object('dag_id', 'upgrade_seed_' || d.n, 'schedule', '@daily', 'tasks',
         (SELECT jsonb_agg(jsonb_build_object('task_id', 't' || k, 'operator', 'PythonOperator'))
          FROM generate_series(1, :tasks_per_run) AS k)),
       md5('upgrade_seed_' || d.n), now() - interval '400 days'
FROM seed_dags d;

-- Runs one day apart going back from yesterday; one in ten failed.
INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date,
                      data_interval_start, data_interval_end, state, trigger,
                      queued_at, started_at, ended_at)
SELECT t.id, d.id, d.version_id,
       'scheduled__' || to_char(ld, 'YYYY-MM-DD"T"HH24:MI:SS'),
       ld, ld, ld + interval '1 day',
       CASE WHEN r % 10 = 0 THEN 'failed'::dag_run_state ELSE 'success'::dag_run_state END,
       'scheduled', ld + interval '1 day', ld + interval '1 day 5 seconds', ld + interval '1 day 2 minutes'
FROM seed_dags d, (SELECT id FROM tenants WHERE name = 'default') t,
     generate_series(1, :runs_per_dag) AS r,
     LATERAL (SELECT date_trunc('day', now()) - (r + 1) * interval '1 day' AS ld) l;

INSERT INTO task_instances (tenant_id, dag_run_id, task_id, try_number, max_tries,
                            state, operator, queued_at, scheduled_at, started_at,
                            ended_at, duration_seconds, hostname)
SELECT r.tenant_id, r.id, 't' || k, 1, 2,
       CASE WHEN r.state = 'failed' AND k = :tasks_per_run THEN 'failed'::task_state ELSE 'success'::task_state END,
       'PythonOperator', r.queued_at, r.queued_at, r.started_at + k * interval '1 second',
       r.started_at + k * interval '20 seconds', 19, 'upgrade-seed'
FROM dag_runs r, generate_series(1, :tasks_per_run) AS k
WHERE r.run_id LIKE 'scheduled__%' AND r.dag_version_id IN (SELECT version_id FROM seed_dags);

INSERT INTO task_instance_history (task_instance_id, try_number, state, queued_at,
                                   started_at, ended_at, duration_seconds, exit_code,
                                   error_message, hostname, archived_at)
SELECT ti.id, 0, 'up_for_retry', ti.queued_at, ti.started_at - interval '1 minute',
       ti.started_at - interval '50 seconds', 10, 1, 'seeded earlier try', 'upgrade-seed',
       ti.started_at - interval '50 seconds'
FROM task_instances ti
WHERE ti.hostname = 'upgrade-seed' AND abs(hashtext(ti.id::text)) % :history_every = 0;

INSERT INTO audit_log (tenant_id, action, resource_type, resource_id, metadata, occurred_at)
SELECT t.id, 'dag_run.trigger', 'dag', 'upgrade_seed_' || (n % :dags + 1),
       jsonb_build_object('seed', n), now() - (n % 400) * interval '1 day'
FROM generate_series(1, :audit_rows) AS n, (SELECT id FROM tenants WHERE name = 'default') t;

ANALYZE;
