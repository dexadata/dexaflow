package storage

import (
	"context"
	"fmt"
	"strconv"

	"github.com/golang-migrate/migrate/v4/database"
)

// migrateVersionTable is the table golang-migrate keeps the schema version
// in. Its name is part of the migrator's advisory-lock key.
const migrateVersionTable = "schema_migrations"

// migrationInFlight reports whether a migration run holds golang-migrate's
// advisory lock on this database. The migrator takes that lock for a whole
// run and marks each version dirty while it applies it, so a schema dirty at
// this binary's latest under a held lock is the last migration finishing,
// not one that stopped half applied.
func (p *Postgres) migrationInFlight(ctx context.Context) (bool, error) {
	var dbName string
	var schema *string
	if err := p.probePool().QueryRow(ctx, "SELECT current_database(), current_schema()").Scan(&dbName, &schema); err != nil {
		return false, fmt.Errorf("reading the database and schema names: %w", err)
	}
	if schema == nil {
		// No schema on the search path: the migrator cannot run here either.
		return false, nil
	}
	// The key the migrator's postgres drivers derive in Lock. Opened from a
	// URL, the driver names the database by the URL's path, "/name"; opened
	// from a URL without one, by current_database(). Either may be the
	// migrator's, so both count.
	keys := make([]int64, 0, 2)
	for _, name := range []string{"/" + dbName, dbName} {
		raw, err := database.GenerateAdvisoryLockId(name, *schema, migrateVersionTable)
		if err != nil {
			return false, fmt.Errorf("deriving the migration lock key: %w", err)
		}
		key, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return false, fmt.Errorf("parsing the migration lock key %q: %w", raw, err)
		}
		keys = append(keys, key)
	}
	// pg_locks stores a 64-bit advisory key as two oids, high half in classid
	// and low half in objid, with objsubid = 1. Advisory locks are per
	// database, so match this one: the same key in another database on the
	// cluster is another migration.
	var held bool
	err := p.probePool().QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM pg_locks
		   WHERE locktype = 'advisory' AND objsubid = 1 AND granted
		     AND ((classid::bigint = $1 AND objid::bigint = $2) OR (classid::bigint = $3 AND objid::bigint = $4))
		     AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		 )`, keys[0]>>32, keys[0]&0xFFFFFFFF, keys[1]>>32, keys[1]&0xFFFFFFFF).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("checking for a migration in flight: %w", err)
	}
	return held, nil
}
