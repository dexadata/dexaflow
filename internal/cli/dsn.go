package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// dsnSet holds the three Postgres DSNs Lite needs: the dedicated dev database,
// the maintenance database used only to CREATE it, and the golang-migrate
// (pgx5) variant of the dev database.
type dsnSet struct {
	database    string
	maintenance string
	migrate     string
}

// devDSNs resolves the Lite DSNs for the current run. A live managed cluster
// (detected by its socket file under ~/.dexaflow/pgdata) is reached over its Unix
// socket; otherwise the Docker datastore is reached over TCP on the persisted
// host port (ADR 0030 auto-selects which backend). Every entry point (the lite
// runner, reset-password, db reset) calls this, so they connect consistently
// without threading the choice through each.
func devDSNs() dsnSet {
	if sock := managedSocketDir(); sock != "" {
		return socketDSNs(sock)
	}
	return tcpDSNs(devDBPort(liteDevDir()))
}

// tcpDSNs builds the Docker datastore DSNs for the given host port. The host port
// is dynamic (Lite picks a free one when 5432 is taken — see resolveDevDBPort), so
// the DSNs must track it rather than hard-code 5432.
func tcpDSNs(port int) dsnSet {
	url := func(db string) string {
		return fmt.Sprintf("postgres://leoflow:leoflow@localhost:%d/%s?sslmode=disable", port, db)
	}
	return dsnSet{
		database:    url(devDBName),
		maintenance: url("postgres"),
		migrate:     fmt.Sprintf("pgx5://leoflow:leoflow@localhost:%d/%s?sslmode=disable", port, devDBName),
	}
}

// socketDSNs builds the managed-cluster DSNs over its per-user Unix socket
// (host=<socketDir>, trust auth, no TCP) — so the managed cluster is reached only
// through its own socket and a foreign Postgres bound to 5432 is never connected
// to or mistaken for ours.
func socketDSNs(socketDir string) dsnSet {
	q := "?host=" + socketDir + "&sslmode=disable"
	return dsnSet{
		database:    "postgres://leoflow@/" + devDBName + q,
		maintenance: "postgres://leoflow@/postgres" + q,
		migrate:     "pgx5://leoflow@/" + devDBName + q,
	}
}

// managedSocketDir returns the managed Postgres data dir when its Unix socket is
// live there (cluster up), or "" otherwise (Docker datastore, or not started).
func managedSocketDir() string {
	_, dataDir, err := managedPGPaths()
	if err != nil {
		return ""
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, ".s.PGSQL.5432")); statErr == nil {
		return dataDir
	}
	return ""
}
