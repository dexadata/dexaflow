-- name: RecordStagingVolume :exec
INSERT INTO staging_volumes (tenant_id, dag_id, run_id, pvc_name, size, state)
VALUES (sqlc.arg(tenant_id), sqlc.arg(dag_id), sqlc.arg(run_id), sqlc.arg(pvc_name), sqlc.arg(size), 'active')
ON CONFLICT (pvc_name) DO NOTHING;

-- name: MarkStagingDeleted :exec
UPDATE staging_volumes
SET state = 'deleted', deleted_at = now(), delete_reason = sqlc.arg(reason)
WHERE pvc_name = sqlc.arg(pvc_name) AND state <> 'deleted';

-- name: ListActiveStagingVolumes :many
-- run_id is the dag_run's UUID (StagingClaimName uses it), so join on dag_runs.id,
-- which is globally unique. run_state is NULL only when the run row is truly gone.
-- The join compares uuids so it probes dag_runs_pkey; casting dag_runs.id to text
-- instead read every run. run_id is TEXT and older rows may hold something else,
-- so the CASE casts only the canonical lower case form, the one id::text produces,
-- and leaves anything else unmatched rather than failing the cast.
SELECT s.pvc_name, s.created_at, r.state AS run_state, r.ended_at AS run_ended_at
FROM staging_volumes s
LEFT JOIN dag_runs r ON r.id = CASE
    WHEN s.run_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN s.run_id::uuid
END
WHERE s.state = 'active';
