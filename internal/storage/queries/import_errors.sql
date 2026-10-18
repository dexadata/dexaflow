-- name: UpsertImportError :exec
INSERT INTO import_errors (tenant_id, filename, stacktrace, bundle_name)
SELECT t.id, sqlc.arg(filename), sqlc.arg(stacktrace), sqlc.narg(bundle_name)
FROM tenants t
WHERE t.name = sqlc.arg(tenant)
ON CONFLICT (tenant_id, filename)
DO UPDATE SET stacktrace = EXCLUDED.stacktrace,
              bundle_name = EXCLUDED.bundle_name,
              created_at = now();

-- name: DeleteImportError :exec
DELETE FROM import_errors
WHERE tenant_id = (SELECT id FROM tenants WHERE name = sqlc.arg(tenant))
  AND filename = sqlc.arg(filename);

-- name: ListImportErrors :many
SELECT e.id, e.filename, e.stacktrace, e.bundle_name, e.created_at
FROM import_errors e
JOIN tenants t ON t.id = e.tenant_id
WHERE t.name = sqlc.arg(tenant)
ORDER BY e.created_at DESC;

-- name: ListImportErrorsFiltered :many
SELECT e.id, e.filename, e.stacktrace, e.bundle_name, e.created_at
FROM import_errors e
JOIN tenants t ON t.id = e.tenant_id
WHERE t.name = sqlc.arg(tenant)
  AND (sqlc.narg('filename')::text IS NULL OR e.filename = sqlc.narg('filename') OR e.filename ILIKE '%' || sqlc.narg('filename') || '%')
  AND (sqlc.narg('bundle_name')::text IS NULL OR e.bundle_name = sqlc.narg('bundle_name'))
ORDER BY e.created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountImportErrorsFiltered :one
SELECT count(*)
FROM import_errors e
JOIN tenants t ON t.id = e.tenant_id
WHERE t.name = sqlc.arg(tenant)
  AND (sqlc.narg('filename')::text IS NULL OR e.filename = sqlc.narg('filename') OR e.filename ILIKE '%' || sqlc.narg('filename') || '%')
  AND (sqlc.narg('bundle_name')::text IS NULL OR e.bundle_name = sqlc.narg('bundle_name'));
