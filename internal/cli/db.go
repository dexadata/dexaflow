package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"
)

// newDBCommand groups Lite-database management commands, mirroring Airflow's
// `airflow db ...`. They operate ONLY on the isolated local Lite database (the
// schema name is still `leoflow_dev` for upgrade-in-place safety with installs
// from before the Dev→Lite product rename); the production database is managed
// via migrations + Helm.
func newDBCommand() *cobra.Command {
	db := &cobra.Command{
		Use:   "db",
		Short: "Manage the local Lite database (schema name leoflow_dev for upgrade safety).",
	}
	db.AddCommand(newDBMigrateCommand(), newDBResetCommand())
	return db
}

// newDBMigrateCommand creates the Lite database if needed and applies the
// embedded migrations (like `airflow db migrate`).
func newDBMigrateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Create (if needed) and migrate the Lite database to the latest schema.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ensureDevDatabase(cmdContext(cmd), cmd); err != nil {
				return err
			}
			return devMigrate(cmd)
		},
	}
}

// newDBResetCommand drops, recreates, and migrates the Lite database (like
// `airflow db reset`). Destructive, so it requires --yes, and it names the
// database it drops.
//
// Lite owns its database and never reads DEXAFLOW_DATABASE_URL (or
// LEOFLOW_DATABASE_URL). An operator who set one to another database and ran
// a reset believed they acted on the database they named, while Lite's was
// dropped (#1185). So the reset refuses when either names a database other
// than Lite's, before it connects to anything.
func newDBResetCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Drop, recreate, and migrate the Lite database (DESTRUCTIVE).",
		Long: "reset drops Lite's own database, the managed or the Docker Postgres `dexaflow lite` " +
			"starts, then recreates and migrates it. It never acts on another database: Lite does not " +
			"read DEXAFLOW_DATABASE_URL (or LEOFLOW_DATABASE_URL), and reset refuses, dropping nothing, " +
			"when either is set to a different host, port or database.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, terr := liteDatabaseTarget()
			if terr != nil {
				return fmt.Errorf("resolving the Lite database: %w", terr)
			}
			if ferr := foreignDatabaseURL(os.Getenv, target); ferr != nil {
				return fmt.Errorf("refusing to reset, nothing was dropped: %w; unset the variable to reset Lite's database", ferr)
			}
			if !yes {
				return fmt.Errorf("refusing to reset without --yes (this drops the Lite database: %s)", target)
			}
			ctx := cmdContext(cmd)
			devPrintln(cmd.OutOrStdout(), "▸ resetting the Lite database ("+target.String()+")")
			if err := dropDevDatabase(ctx, cmd); err != nil {
				return err
			}
			if err := ensureDevDatabase(ctx, cmd); err != nil {
				return err
			}
			return devMigrate(cmd)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the destructive reset")
	return cmd
}

// dropDevDatabase drops the isolated dev database, terminating any open
// connections (Postgres 13+ WITH FORCE), via the maintenance database.
func dropDevDatabase(ctx context.Context, cmd *cobra.Command) error {
	conn, err := pgx.Connect(ctx, devDSNs().maintenance)
	if err != nil {
		return fmt.Errorf("connecting to Postgres (is it up?): %w", err)
	}
	defer func() { _ = conn.Close(ctx) }() //nolint:errcheck // best-effort close of a short-lived maintenance connection
	devPrintln(cmd.OutOrStdout(), "▸ dropping "+devDBName+" …")
	//nolint:gosec // G201: the database name is a fixed constant, sanitized as an identifier.
	if _, eerr := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{devDBName}.Sanitize()+" WITH (FORCE)"); eerr != nil {
		return fmt.Errorf("dropping database %s: %w", devDBName, eerr)
	}
	return nil
}
