package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The pool tuning keys are off by default: an install that sets none of them
// keeps one shared pool, no statement timeout and no lifetime jitter.
func TestDatabasePoolTuningDefaultsKeepTodaysBehavior(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	db := c.Database
	if db.SchedulerMaxConns != 0 || db.StatementTimeoutMS != 0 || db.ConnMaxLifetimeJitterMS != 0 {
		t.Errorf("defaults = scheduler_max_conns %d, statement_timeout_ms %d, conn_max_lifetime_jitter_ms %d; want all 0",
			db.SchedulerMaxConns, db.StatementTimeoutMS, db.ConnMaxLifetimeJitterMS)
	}
}

// Each key binds its DEXAFLOW_* variable and, for installs that predate the
// rename, its LEOFLOW_* one.
func TestDatabasePoolTuningReadsBothPrefixes(t *testing.T) {
	t.Setenv("DEXAFLOW_DATABASE_STATEMENT_TIMEOUT_MS", "30000")
	t.Setenv("LEOFLOW_DATABASE_SCHEDULER_MAX_CONNS", "4")
	t.Setenv("LEOFLOW_DATABASE_CONN_MAX_LIFETIME_JITTER_MS", "60000")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	db := c.Database
	if db.StatementTimeoutMS != 30000 || db.SchedulerMaxConns != 4 || db.ConnMaxLifetimeJitterMS != 60000 {
		t.Errorf("got scheduler_max_conns %d, statement_timeout_ms %d, conn_max_lifetime_jitter_ms %d; want 4, 30000, 60000",
			db.SchedulerMaxConns, db.StatementTimeoutMS, db.ConnMaxLifetimeJitterMS)
	}
}

// The same keys read from the config file, whichever name it has.
func TestDatabasePoolTuningFromConfigFile(t *testing.T) {
	for _, name := range []string{"dexaflow.yaml", "leoflow.yaml"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			body := "database:\n  scheduler_max_conns: 3\n  statement_timeout_ms: 15000\n  conn_max_lifetime_jitter_ms: 120000\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadServer(path, nil)
			if err != nil {
				t.Fatalf("LoadServer() error = %v", err)
			}
			db := c.Database
			if db.SchedulerMaxConns != 3 || db.StatementTimeoutMS != 15000 || db.ConnMaxLifetimeJitterMS != 120000 {
				t.Errorf("got scheduler_max_conns %d, statement_timeout_ms %d, conn_max_lifetime_jitter_ms %d; want 3, 15000, 120000",
					db.SchedulerMaxConns, db.StatementTimeoutMS, db.ConnMaxLifetimeJitterMS)
			}
		})
	}
}
