package migrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/golang-migrate/migrate/v4"

	"github.com/dexadata/dexaflow/migrations"
)

// Row level security as a second tenant barrier (ADR 0104): migration 059
// creates the policies, and `rls on` turns them on. They are separate steps
// so that an upgrade alone changes nothing, and the operator chooses when the
// tables start enforcing them.

// rlsState is one tenant table's row level security, as pg_class and
// pg_policy record it.
type rlsState struct {
	table            string
	enabled, forced  bool
	policy, existing bool
}

// runRLS runs `rls on|off|status`. on and off need the schema at
// migrations.RowSecurityVersion or later and clean, so every policy exists.
func runRLS(ctx context.Context, op *opened, dsn, action string, stdout io.Writer) error {
	if action != "status" {
		v, dirty, err := op.m.Version()
		if errors.Is(err, migrate.ErrNilVersion) {
			return &Failure{Kind: KindUsage, Code: ExitUsage, Summary: "rls needs a migrated database: run up first"}
		}
		if err != nil {
			return Classify(err, op.target)
		}
		if dirty || v < migrations.RowSecurityVersion {
			return &Failure{Kind: KindUsage, Code: ExitUsage,
				Summary: fmt.Sprintf("rls %s needs the schema at version %d or later and clean, the database is at %d (dirty: %v): run up first", action, migrations.RowSecurityVersion, v, dirty)}
		}
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return Classify(err, op.target)
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // best-effort close of a handle we own
	if action != "status" {
		if err := setRLS(ctx, db, action == "on"); err != nil {
			return Classify(err, op.target)
		}
	}
	states, err := readRLS(ctx, db)
	if err != nil {
		return Classify(err, op.target)
	}
	printRLS(stdout, states)
	return nil
}

// setRLS turns row level security on (enabled and forced, so the table's
// owner, usually the server's own role, is held to it too) or off for every
// tenant table, in one transaction with a bounded lock wait: either every
// table changes or none does.
func setRLS(ctx context.Context, db *sql.DB, on bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning the rls transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		return fmt.Errorf("setting lock_timeout: %w", err)
	}
	for _, t := range migrations.RowSecurityTables {
		stmts := []string{"ENABLE ROW LEVEL SECURITY", "FORCE ROW LEVEL SECURITY"}
		if !on {
			stmts = []string{"NO FORCE ROW LEVEL SECURITY", "DISABLE ROW LEVEL SECURITY"}
		}
		for _, s := range stmts {
			// The table names are the fixed list above, never input.
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+t+" "+s); err != nil {
				return fmt.Errorf("ALTER TABLE %s %s: %w", t, s, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing the rls change: %w", err)
	}
	return nil
}

func readRLS(ctx context.Context, db *sql.DB) ([]rlsState, error) {
	byTable := make(map[string]*rlsState, len(migrations.RowSecurityTables))
	out := make([]rlsState, len(migrations.RowSecurityTables))
	for i, t := range migrations.RowSecurityTables {
		out[i].table = t
		byTable[t] = &out[i]
	}
	rows, err := db.QueryContext(ctx, `
SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
       EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid AND p.polname = $1)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relkind = 'r'`, migrations.RowSecurityPolicy)
	if err != nil {
		return nil, fmt.Errorf("reading row level security state: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // rows.Err below reports what matters
	for rows.Next() {
		var s rlsState
		if err := rows.Scan(&s.table, &s.enabled, &s.forced, &s.policy); err != nil {
			return nil, fmt.Errorf("reading row level security state: %w", err)
		}
		if p, ok := byTable[s.table]; ok {
			s.existing = true
			*p = s
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading row level security state: %w", err)
	}
	return out, nil
}

// printRLS prints one line per table and a verdict the upgrade guide quotes:
// "row level security: on", "off" or "partial".
func printRLS(w io.Writer, states []rlsState) {
	on, off := 0, 0
	for _, s := range states {
		var parts []string
		switch {
		case !s.existing:
			parts = append(parts, "missing")
		case s.enabled && s.forced && s.policy:
			parts = append(parts, "on")
			on++
		case !s.enabled && !s.forced:
			parts = append(parts, "off")
			off++
		default:
			parts = append(parts, fmt.Sprintf("enabled=%v forced=%v", s.enabled, s.forced))
		}
		if s.existing && !s.policy {
			parts = append(parts, "no policy")
		}
		say(w, "%-26s %s\n", s.table, strings.Join(parts, ", "))
	}
	verdict := "partial"
	switch len(states) {
	case on:
		verdict = "on"
	case off:
		verdict = "off"
	}
	say(w, "row level security: %s\n", verdict)
}
