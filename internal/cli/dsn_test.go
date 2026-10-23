package cli

import (
	"strings"
	"testing"
)

// TestTCPDSNsTrackThePort: the Docker DSNs are built on the given host port (Lite
// picks a free one when 5432 is taken), targeting the dedicated dev database and
// the maintenance "postgres" db, with the migrate DSN on the pgx5 scheme.
func TestTCPDSNsTrackThePort(t *testing.T) {
	t.Parallel()
	d := tcpDSNs(5433)
	for label, dsn := range map[string]string{"database": d.database, "maintenance": d.maintenance, "migrate": d.migrate} {
		if !strings.Contains(dsn, "localhost:5433") {
			t.Errorf("%s DSN %q must use the resolved host port 5433", label, dsn)
		}
	}
	if !strings.HasPrefix(d.migrate, "pgx5://") {
		t.Errorf("migrate DSN must keep the pgx5 scheme, got %q", d.migrate)
	}
	if !strings.Contains(d.database, "/leoflow_dev") || !strings.Contains(d.maintenance, "/postgres") {
		t.Errorf("DSNs target the wrong databases: %+v", d)
	}
}

// TestTCPDSNsDefaultPortMatchesConsts pins the invariant that the default-port
// (5432) DSNs equal the documented consts — so nothing regressed when the port
// became dynamic.
func TestTCPDSNsDefaultPortMatchesConsts(t *testing.T) {
	t.Parallel()
	d := tcpDSNs(defaultDevDBPort)
	if d.database != devDatabaseURL || d.maintenance != devMaintenanceURL || d.migrate != devMigrateURL {
		t.Fatalf("tcpDSNs(%d) must equal the 5432 consts, got %+v", defaultDevDBPort, d)
	}
}

// TestSocketDSNsUseTheSocket: with a managed socket dir, every DSN connects over
// that Unix socket (host=<dir>) and never over TCP localhost — so a foreign
// Postgres bound to 5432 can never be reached or mistaken for ours.
func TestSocketDSNsUseTheSocket(t *testing.T) {
	t.Parallel()
	dir := "/home/u/.leoflow/pgdata"
	d := socketDSNs(dir)
	for label, dsn := range map[string]string{"database": d.database, "maintenance": d.maintenance, "migrate": d.migrate} {
		if !strings.Contains(dsn, "host="+dir) {
			t.Errorf("%s DSN %q must select the unix socket via host=%s", label, dsn, dir)
		}
		if strings.Contains(dsn, "localhost:5432") || strings.Contains(dsn, "@localhost") {
			t.Errorf("%s DSN %q must not use TCP localhost", label, dsn)
		}
	}
	if !strings.HasPrefix(d.migrate, "pgx5://") {
		t.Errorf("migrate DSN must keep the pgx5 scheme, got %q", d.migrate)
	}
}
