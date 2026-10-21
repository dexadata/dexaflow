package migrator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"syscall"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kind names the class of a migration failure.
type Kind int

// The failure kinds. Each maps to one exit code.
const (
	// KindOther is an error the wrapper does not recognize; it is retried.
	KindOther Kind = iota
	// KindUsage is a command line the wrapper cannot run.
	KindUsage
	// KindBadDSN is a database.url that is not a Postgres URL.
	KindBadDSN
	// KindMissingDatabase is a server without the database the DSN names.
	KindMissingDatabase
	// KindAuth is a server that refused the DSN's user or password.
	KindAuth
	// KindMissingCA is an sslrootcert path with no file behind it.
	KindMissingCA
	// KindDirty is a schema_migrations row left dirty by an interrupted file.
	KindDirty
	// KindUnreachable is a server that could not be reached or is not
	// accepting connections yet.
	KindUnreachable
	// KindLocked is another migration holding golang-migrate's lock.
	KindLocked
	// KindReplaced is a database that is not the one this release was
	// installed on (ADR 0090 decision 3): emptied, restored from another
	// installation, or repointed.
	KindReplaced
)

// String is the kind's name, for logs and test output.
func (k Kind) String() string {
	switch k {
	case KindOther:
		return "other"
	case KindUsage:
		return "usage"
	case KindBadDSN:
		return "bad-dsn"
	case KindMissingDatabase:
		return "missing-database"
	case KindAuth:
		return "auth"
	case KindMissingCA:
		return "missing-ca"
	case KindDirty:
		return "dirty"
	case KindUnreachable:
		return "unreachable"
	case KindLocked:
		return "locked"
	case KindReplaced:
		return "replaced"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// Exit codes. The deterministic ones (see DeterministicExitCodes) are the
// failures a retry cannot fix; the chart fails the Job on them at once.
const (
	// ExitFailed is an unrecognized failure, retried by the Job.
	ExitFailed = 1
	// ExitUsage is a command line the wrapper cannot run.
	ExitUsage = 2
	// ExitBadDSN is a database.url that is not a Postgres URL.
	ExitBadDSN = 3
	// ExitMissingDatabase is a database that does not exist on the server.
	ExitMissingDatabase = 4
	// ExitAuth is a refused user or password.
	ExitAuth = 5
	// ExitMissingCA is a missing sslrootcert file.
	ExitMissingCA = 6
	// ExitDirty is a dirty schema, which needs a person.
	ExitDirty = 7
	// ExitReplaced is a database that is not this release's own, which needs
	// a person to restore the right one or accept this one.
	ExitReplaced = 8
	// ExitTransient is a failure worth retrying: a server not reachable yet,
	// not accepting connections, or another migration holding the lock. It is
	// EX_TEMPFAIL from sysexits.h.
	ExitTransient = 75
)

// DeterministicExitCodes are the exit codes a retry cannot change. The chart's
// migration Job lists the same set in its podFailurePolicy (a test keeps the
// two equal).
func DeterministicExitCodes() []int {
	return []int{ExitUsage, ExitBadDSN, ExitMissingDatabase, ExitAuth, ExitMissingCA, ExitDirty, ExitReplaced}
}

// Documentation the messages point at.
const (
	docsBase       = "https://dexaflow.dexadata.ai"
	docsDirty      = docsBase + "/operate/upgrades/#an-interrupted-migration"
	docsJob        = docsBase + "/operate/troubleshooting/#the-migration-job"
	docsReplaced   = docsBase + "/operate/troubleshooting/#a-replaced-database"
	readmeBase     = "https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md"
	readmeDatabase = readmeBase + "#datastore-compatibility"
	readmeTLS      = readmeBase + "#verified-tls-to-managed-postgres-315"
)

// Failure is a classified migration error: its kind, the process exit code,
// and a message in the chart's terms. Cause is the driver error it came from.
type Failure struct {
	// Kind is the class of the failure.
	Kind Kind
	// Code is the exit code the process ends with.
	Code int
	// Summary names the cause and the remedy.
	Summary string
	// Cause is the underlying error, nil when the wrapper produced it.
	Cause error
}

// Error is the summary; the cause is printed on its own line by Main.
func (f *Failure) Error() string { return f.Summary }

// Unwrap returns the driver error, so callers can errors.Is it.
func (f *Failure) Unwrap() error { return f.Cause }

// SQLSTATE codes the classifier reads.
const (
	sqlstateInvalidCatalogName  = "3D000" // database does not exist
	sqlstateInvalidPassword     = "28P01"
	sqlstateInvalidAuthSpec     = "28000" // no pg_hba entry, role does not exist
	sqlstateCannotConnectNow    = "57P03" // starting up, shutting down, in recovery
	sqlstateTooManyConnections  = "53300"
	sqlstateAdminShutdown       = "57P01"
	sqlstateCrashShutdown       = "57P02"
	sqlstateConnectionException = "08" // class 08: connection exceptions
)

// Classify maps an error from opening the database or running a command to a
// Failure. A Failure already in the chain is returned as is. Classify(nil)
// is nil.
func Classify(err error, t Target) *Failure {
	if err == nil {
		return nil
	}
	var f *Failure
	if errors.As(err, &f) {
		return f
	}
	if f := classifyServer(err, t); f != nil {
		return f
	}
	if f := classifyMigrate(err); f != nil {
		return f
	}
	var perr *pgconn.ParseConfigError
	if errors.As(err, &perr) {
		reason := "pgx could not parse it"
		if inner := errors.Unwrap(perr); inner != nil {
			reason = t.redact(inner.Error())
		}
		return badDSN("database.url is not a usable Postgres URL (" + reason + ")")
	}
	if isUnreachable(err) {
		return unreachable(err, t)
	}
	return &Failure{
		Kind: KindOther, Code: ExitFailed, Cause: err,
		Summary: fmt.Sprintf("migrating %s at %s failed: %s. The Job retries this; see %s.",
			t.Database, t.Address(), t.redact(err.Error()), docsJob),
	}
}

// classifyServer handles an error the Postgres server reported.
func classifyServer(err error, t Target) *Failure {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return nil
	}
	switch {
	case pg.Code == sqlstateInvalidCatalogName:
		return &Failure{
			Kind: KindMissingDatabase, Code: ExitMissingDatabase, Cause: err,
			Summary: fmt.Sprintf("database %q does not exist on %s. Dexaflow does not create the database: "+
				"create it (CREATE DATABASE %s;) as a role that may, or point database.url (a chart value) "+
				"at an existing one. See the chart README, %q (%s).",
				t.Database, t.Address(), quoteIdent(t.Database), "Datastore compatibility", readmeDatabase),
		}
	case pg.Code == sqlstateInvalidPassword || pg.Code == sqlstateInvalidAuthSpec:
		return &Failure{
			Kind: KindAuth, Code: ExitAuth, Cause: err,
			Summary: fmt.Sprintf("Postgres at %s rejected user %q: %s. Check the user and password in database.url "+
				"(the databaseUrl key of the chart Secret, or of database.existingSecret) and the server's pg_hba rules; "+
				"a retry sends the same credentials, so the Job is not retried. See %s.",
				t.Address(), t.User, pg.Message, docsJob),
		}
	case pg.Code == sqlstateCannotConnectNow || pg.Code == sqlstateTooManyConnections ||
		pg.Code == sqlstateAdminShutdown || pg.Code == sqlstateCrashShutdown ||
		strings.HasPrefix(pg.Code, sqlstateConnectionException):
		return unreachable(err, t)
	}
	return nil
}

// classifyMigrate handles golang-migrate's own errors.
func classifyMigrate(err error) *Failure {
	var dirty migrate.ErrDirty
	if errors.As(err, &dirty) {
		return &Failure{
			Kind: KindDirty, Code: ExitDirty, Cause: err,
			Summary: fmt.Sprintf("schema_migrations is dirty at version %d: a migration was interrupted, and every "+
				"run stops here until a person repairs it. Find what the file left behind and the version to "+
				"force with %s, then run this image with `force N` and upgrade again.",
				dirty.Version, docsDirty),
		}
	}
	if errors.Is(err, migrate.ErrLockTimeout) || errors.Is(err, migrate.ErrLocked) {
		return &Failure{
			Kind: KindLocked, Code: ExitTransient, Cause: err,
			Summary: "another migration holds the schema_migrations lock (a previous Job pod, or a second " +
				"release on the same database); this run is retried.",
		}
	}
	return nil
}

func isUnreachable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) || pgconn.Timeout(err) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		return true
	}
	var operr *net.OpError
	return errors.As(err, &operr)
}

func unreachable(err error, t Target) *Failure {
	return &Failure{
		Kind: KindUnreachable, Code: ExitTransient, Cause: err,
		Summary: fmt.Sprintf("cannot reach Postgres at %s yet; this is retried. Check that the server is running "+
			"and accepting connections, and that database.url's host and port are reachable from this namespace "+
			"(a NetworkPolicy, a firewall rule, or a managed instance's authorized networks). See %s.",
			t.Address(), docsJob),
	}
}

var plainIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// quoteIdent quotes a database name for the CREATE DATABASE hint only when
// Postgres would fold or reject it unquoted.
func quoteIdent(name string) string {
	if plainIdent.MatchString(name) {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
