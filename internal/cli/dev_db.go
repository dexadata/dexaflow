package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" migrate scheme
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/migrations"
)

// bringUpDependencies starts Lite's datastore and returns a cleanup func the
// caller defers. It resolves the "auto" --postgres for the host first (Docker
// Postgres when Docker is present, else a managed relocatable PG), then: the
// Docker path is a no-op cleanup (its container is left up across runs); the
// managed path returns a stop only when this run started the cluster.
func bringUpDependencies(ctx context.Context, cmd *cobra.Command, o *devOptions) (func(), error) {
	noop := func() {}
	if o.noUp {
		return noop, nil
	}
	o.postgres = autoDatastore(cmd, o.postgres)
	if o.postgres == datastoreManaged {
		// Managed relocatable Postgres, no Docker at all: Lite is Redis-free (XCom
		// on Postgres, in-process log tailer, ADR 0026), so nothing comes up via
		// docker compose.
		started, perr := startManagedPostgres(ctx, cmd)
		if perr != nil {
			return noop, perr
		}
		// Stop it on the way out only if this run started it (ADR 0065
		// section 3): a cluster found running may be a migration's.
		//nolint:contextcheck // stop runs at shutdown with a fresh context; the run's ctx is already canceled
		return managedCleanup(started, func() { stopManagedPostgres(cmd) }), nil
	}
	// Docker datastore: only Postgres (Lite needs no Redis). Pick a host port that
	// is free (so a foreign Postgres on 5432 (system or another project) never
	// conflicts; Lite stays isolated rather than adopting it). The port is
	// persisted so reset-password / db reset agree with the running server.
	port, perr := resolveDevDBPort(liteDevDir(), func() int { return firstFreePort(defaultDevDBPort) })
	if perr != nil {
		return noop, perr
	}
	cf, err := resolveComposeFile(cmd.OutOrStdout(), o.composeFile)
	if err != nil {
		return noop, err
	}
	o.composeFile = cf
	devPrintf(cmd.OutOrStdout(), "▸ Postgres (Docker) on localhost:%d  [project %s]\n", port, devProjectName())
	return noop, devComposeUp(ctx, cmd, *o, "postgres")
}

// resolveComposeFile returns the docker-compose file Lite uses for its local
// Postgres (Lite is Redis-free, ADR 0026). An explicit --compose wins; else a docker-compose.dev.yaml in
// the working dir (a source checkout) is used; else the compose embedded in the
// binary is materialized under ~/.dexaflow, so a binary-only install runs with
// `dexaflow lite` alone, and refreshed there on a binary upgrade unless the
// operator edited it (ensureManagedComposeIn, #981). Notices go to out.
func resolveComposeFile(out io.Writer, flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if _, err := os.Stat("docker-compose.dev.yaml"); err == nil {
		return "docker-compose.dev.yaml", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	dir := stateDirIn(home)
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return "", fmt.Errorf("creating %s: %w", dir, mkErr)
	}
	return ensureManagedComposeIn(dir, func(format string, args ...any) { devPrintf(out, format+"\n", args...) })
}

// devComposeUp starts the named local datastore services via docker compose. Lite
// brings up only Postgres (it is Redis-free, ADR 0026); the dev's own database
// lives inside it, isolated by name. The compose runs under a per-install project
// name (so two users/installs never share or clobber a container/volume) and on
// the resolved host port (LEOFLOW_DB_PORT, which the compose interpolates).
func devComposeUp(ctx context.Context, cmd *cobra.Command, o devOptions, services ...string) error {
	// Without the plugin, `docker compose -f ... up` fails with the docker CLI's
	// usage text (#759); say what to do instead.
	if err := requireComposePlugin(); err != nil {
		return err
	}
	devPrintln(cmd.OutOrStdout(), "▸ starting dependencies (docker compose) …")
	var captured bytes.Buffer
	args := append([]string{"compose", "-f", o.composeFile, "up", "-d", "--wait"}, services...)
	up := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // operator-supplied compose file on the dev CLI
	up.Env = composeEnv()
	up.Stdout = cmd.OutOrStdout()
	// Tee stderr so the user still sees compose progress while we inspect it to
	// translate a port-allocation failure into an actionable message.
	up.Stderr = io.MultiWriter(cmd.ErrOrStderr(), &captured)
	if err := up.Run(); err != nil {
		return composeUpError(err, captured.String())
	}
	return nil
}

// composeUpError turns a `docker compose up` failure into an actionable message.
// A port-allocation failure (a foreign Postgres already bound to 5432, the
// common real-world conflict) produces a cryptic raw Docker error, so it is
// translated; any other failure keeps the generic "is Docker running?" hint.
func composeUpError(err error, output string) error {
	low := strings.ToLower(output)
	if strings.Contains(low, "already allocated") || strings.Contains(low, "address already in use") || strings.Contains(low, "port is already") {
		return fmt.Errorf("the Postgres port 5432 is already in use: another Postgres is bound to it. Lite owns its database and does not run on another Postgres, so stop that one, or run `dexaflow lite --postgres managed` (a private, socket-only Postgres that needs no port): %w", err)
	}
	if strings.Contains(low, "unknown command") || strings.Contains(low, "is not a docker command") || strings.Contains(low, "compose") && strings.Contains(low, "not found") {
		return fmt.Errorf("the Docker Compose v2 plugin is not installed (the `docker compose` subcommand is missing). Install it, or run `dexaflow lite --postgres managed` for a Docker-free Postgres: %w", err)
	}
	return fmt.Errorf("docker compose up (is Docker running, with the Compose v2 plugin?): %w", err)
}

// devMigrate applies the embedded SQL migrations to the isolated dev database.
// The migrations are compiled into the binary (no source tree or migrate CLI),
// a step toward a binaries-only dev install (#60).
//
// Before applying anything, devMigrate refuses to start when the database's
// schema_migrations.version is HIGHER than the highest version embedded in
// this binary (#136). That shape means an older binary is being run against
// a database that a newer binary already upgraded: proceeding silently would
// let the older binary read/write rows under a schema it does not understand.
// The user is told to upgrade the binary, or to run `dexaflow uninstall --purge`
// to start over.
func devMigrate(cmd *cobra.Command) error {
	devPrintln(cmd.OutOrStdout(), "▸ migrating "+devDBName+" (embedded) …")
	src, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("loading embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, devDSNs().migrate)
	if err != nil {
		return fmt.Errorf("initializing migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }() //nolint:errcheck // best-effort close of source + db handles
	if derr := checkDevSchemaDrift(m); derr != nil {
		return derr
	}
	if uerr := m.Up(); uerr != nil && !errors.Is(uerr, migrate.ErrNoChange) {
		return fmt.Errorf("applying migrations: %w", uerr)
	}
	return nil
}

// checkDevSchemaDrift compares the DB's current schema version against the
// highest migration the binary embeds and refuses to start when the DB is
// ahead. A fresh DB (no rows in schema_migrations, surfaced as
// migrate.ErrNilVersion) is the expected first-run state and proceeds.
func checkDevSchemaDrift(m *migrate.Migrate) error {
	dbVersion, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading current schema version: %w", err)
	}
	embedded, lerr := migrations.Latest()
	if lerr != nil {
		return fmt.Errorf("checking embedded migrations: %w", lerr)
	}
	return decideSchemaDrift(dbVersion, dirty, embedded)
}

// decideSchemaDrift is the pure decision the drift check applies, split out
// so it can be unit-tested without a real Postgres. dbVersion is the value in
// the database's schema_migrations table; embedded is the highest version
// the binary embeds (migrations.Latest()). A DB ahead of the binary is the
// drift case (#136); a dirty marker is a separate operational failure.
func decideSchemaDrift(dbVersion uint, dirty bool, embedded uint) error {
	if dirty {
		return fmt.Errorf(
			"database schema is marked dirty at version %d (a prior migration was interrupted); "+
				"run `dexaflow uninstall --purge` to reset, or fix manually with `migrate force` if you know what you are doing",
			dbVersion,
		)
	}
	if dbVersion > embedded {
		return fmt.Errorf(
			"database is at schema version %d but this binary only knows up to %d; "+
				"an older `dexaflow` is being run against a newer database. "+
				"Upgrade the binary, or run `dexaflow uninstall --purge` to start over (this WIPES your data)",
			dbVersion, embedded,
		)
	}
	return nil
}

// ensureDevDatabase creates the isolated leoflow_dev database if it does not yet
// exist, so the dev experience never shares the product's "leoflow" database.
func ensureDevDatabase(ctx context.Context, cmd *cobra.Command) error {
	conn, err := pgx.Connect(ctx, devDSNs().maintenance)
	if err != nil {
		return fmt.Errorf("connecting to Postgres (is it up?): %w", err)
	}
	defer func() { _ = conn.Close(ctx) }() //nolint:errcheck // best-effort close of a short-lived maintenance connection
	var exists bool
	if qerr := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", devDBName).Scan(&exists); qerr != nil {
		return fmt.Errorf("checking for %s: %w", devDBName, qerr)
	}
	if exists {
		return nil
	}
	devPrintln(cmd.OutOrStdout(), "▸ creating isolated dev database "+devDBName+" …")
	//nolint:gosec // G201: the database name is a fixed constant, sanitized as an identifier.
	if _, eerr := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{devDBName}.Sanitize()); eerr != nil {
		return fmt.Errorf("creating database %s: %w", devDBName, eerr)
	}
	return nil
}

// liteAdminState reports the auth.AdminState of an email in the Lite
// database's default tenant. Like countRegisteredDags it reads Postgres
// directly, bounded, because it runs at banner time.
func liteAdminState(ctx context.Context, email string) (auth.AdminState, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, devDSNs().database)
	if err != nil {
		return auth.UserAbsent, fmt.Errorf("connecting to the Lite database: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }() //nolint:errcheck // best-effort close of a short-lived read connection
	var found, admin bool
	if qerr := conn.QueryRow(ctx, `
SELECT count(*) > 0,
       coalesce(bool_or(r.name = 'admin'), false)
FROM users u
JOIN tenants t ON t.id = u.tenant_id
LEFT JOIN user_roles ur ON ur.user_id = u.id
LEFT JOIN roles r ON r.id = ur.role_id
WHERE t.name = 'default' AND u.email = $1`, email).Scan(&found, &admin); qerr != nil {
		return auth.UserAbsent, fmt.Errorf("looking up the Lite admin: %w", qerr)
	}
	switch {
	case admin:
		return auth.UserIsAdmin, nil
	case found:
		return auth.UserWithoutAdminRole, nil
	default:
		return auth.UserAbsent, nil
	}
}

// countRegisteredDags reports how many DAGs the local database already holds.
// Called before this session registers anything, so the number is exactly what
// earlier runs left behind (#1104).
//
// It reads Postgres directly rather than going through the API: at banner time
// the admin token has not been minted yet, and a read-only count has no business
// depending on the auth surface. A failure returns -1 ("unknown", which the
// banner reports as nothing) because a state line that guesses is worse than no
// state line: it would assert a clean slate on a lookup that never ran.
func countRegisteredDags(ctx context.Context) int {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, devDSNs().database)
	if err != nil {
		return -1
	}
	defer func() { _ = conn.Close(ctx) }() //nolint:errcheck // best-effort close of a short-lived read connection
	var n int
	if qerr := conn.QueryRow(ctx, "SELECT count(*) FROM dags").Scan(&n); qerr != nil {
		return -1
	}
	return n
}

// provisionDevDatabase brings the Lite dev database up, dropping it first when
// fresh is set.
//
// State under ~/.dexaflow/dev otherwise outlives every session: a DAG registered
// during an old spike stays registered, keeps being scheduled, and fails inside
// a run that has nothing to do with it (#1104). The drop is the same one
// `dexaflow db reset` performs, and it is announced before it happens: it takes
// registered DAGs, runs and history with it.
func provisionDevDatabase(ctx context.Context, cmd *cobra.Command, out io.Writer, fresh bool) error {
	if fresh {
		devPrintln(out, "▸ --fresh: dropping the local dev database (registered DAGs, runs and history go with it)")
		if err := dropDevDatabase(ctx, cmd); err != nil {
			return err
		}
	}
	return ensureDevDatabase(ctx, cmd)
}
