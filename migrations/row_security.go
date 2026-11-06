package migrations

// RowSecurityVersion is the migration that creates the tenant policies of
// row level security (ADR 0104).
const RowSecurityVersion = 59

// RowSecurityPolicy is the name of the tenant policy on each of
// RowSecurityTables.
const RowSecurityPolicy = "dexaflow_tenant_barrier"

// RowSecurityTables are the tables with a tenant_id column, each of which
// migration 059 gives a tenant policy. `dexaflow-migrate rls on` turns row
// level security on for exactly these, and a server with
// database.row_level_security checks them at boot.
var RowSecurityTables = []string{
	"api_tokens", "audit_log", "cli_device_authorizations", "connections",
	"dag_favorites", "dag_runs", "dags", "import_errors", "pools", "roles",
	"staging_volumes", "task_instances", "usage_events", "users", "variables",
	"xcom_index",
}
