package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// liteDatabaseURLVars are the variables a control plane reads its database
// from. Lite does not read them: it owns its database (the managed or the
// Docker Postgres it starts) and hands the server that one's URL (#1185).
var liteDatabaseURLVars = []string{"DEXAFLOW_DATABASE_URL", "LEOFLOW_DATABASE_URL"}

// dbTarget is where a database URL points: the host (or socket directory),
// the port and the database name. Credentials are left out, so a target can be
// printed.
type dbTarget struct {
	host     string
	port     uint16
	database string
}

// String names the target the way the CLI prints it.
func (t dbTarget) String() string {
	return fmt.Sprintf("host %s, port %d, database %s", t.host, t.port, t.database)
}

// sameAs reports whether t and o are the same database. The loopback names
// are one host: Lite's URL says localhost, a hand-written one often 127.0.0.1.
func (t dbTarget) sameAs(o dbTarget) bool {
	return loopbackHost(t.host) == loopbackHost(o.host) && t.port == o.port && t.database == o.database
}

// loopbackHost returns "localhost" for any loopback spelling, and host
// otherwise.
func loopbackHost(host string) string {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return "localhost"
	}
	return host
}

// parseDBTarget reads the target of a Postgres URL or keyword/value string.
func parseDBTarget(dsn string) (dbTarget, error) {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return dbTarget{}, fmt.Errorf("parsing the database URL: %w", err)
	}
	return dbTarget{host: cfg.Host, port: cfg.Port, database: cfg.Database}, nil
}

// liteDatabaseTarget is the database the Lite commands act on: the managed
// Postgres over its socket when it is up, else the Docker Postgres on the
// port this install recorded.
func liteDatabaseTarget() (dbTarget, error) {
	return parseDBTarget(devDSNs().database)
}

// foreignDatabaseURL returns an error naming the first of liteDatabaseURLVars
// that getenv reports set to a database other than lite, or one that does not
// parse, and nil when neither is set or both name lite. The error never
// carries the variable's value, which may hold a password.
func foreignDatabaseURL(getenv func(string) string, lite dbTarget) error {
	for _, name := range liteDatabaseURLVars {
		value := getenv(name)
		if value == "" {
			continue
		}
		set, err := parseDBTarget(value)
		if err != nil {
			return fmt.Errorf("%s is set but does not parse as a Postgres URL, so it cannot be told apart from Lite's own database (%s)", name, lite)
		}
		if !set.sameAs(lite) {
			return fmt.Errorf("%s is set and points at %s, but Lite only uses its own database (%s)", name, set, lite)
		}
	}
	return nil
}

// noteForeignDatabaseURL writes a note to w when DEXAFLOW_DATABASE_URL or
// LEOFLOW_DATABASE_URL names a database other than Lite's, ending with what,
// which says what the command does instead. Commands that only read Lite's
// database (backup, the Lite boot) go ahead after it; db reset refuses.
func noteForeignDatabaseURL(w io.Writer, what string) {
	target, err := liteDatabaseTarget()
	if err != nil {
		return
	}
	if ferr := foreignDatabaseURL(os.Getenv, target); ferr != nil {
		devPrintf(w, "note: %v; %s\n", ferr, what)
	}
}
