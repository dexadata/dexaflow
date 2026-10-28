// Package migrator applies the Dexaflow SQL migrations with golang-migrate over
// pgx v5 and turns the driver's errors into messages that name the cause, the
// remedy and the chart value involved (#1020).
//
// It is what the migrate image runs (cmd/dexaflow-migrate) for the Helm
// chart's pre-install and pre-upgrade hook Job. Before it, the image shipped
// golang-migrate's own CLI, which linked lib/pq while the control plane and
// Lite use pgx, and printed the raw driver error: `pq: database "x" does not
// exist` on a fresh install, with nothing saying that database.url is a chart
// value or that Dexaflow does not create the database. ADR 0090 builds the
// installation id and the opt-in self-migration on top of this package.
//
// Exit codes split deterministic failures from transient ones, so the chart's
// podFailurePolicy can fail the Job at once on a missing database or a wrong
// password instead of retrying it three times (see DeterministicExitCodes).
package migrator

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// Target is what the messages say about the database the DSN points at. It is
// parsed from the DSN once, before any connection, and never carries the
// password into a message.
type Target struct {
	// Host is the host name or address of the Postgres server.
	Host string
	// Port is the TCP port, 5432 when the DSN names none.
	Port string
	// Database is the database name from the DSN's path.
	Database string
	// User is the role the DSN connects as.
	User string
	// SSLRootCert is the sslrootcert parameter, empty when absent.
	SSLRootCert string

	// password is kept only so redact can remove it from driver errors.
	password string
}

// Address is host:port in the form a message prints it.
func (t Target) Address() string {
	if t.Host == "" {
		return "the database"
	}
	return net.JoinHostPort(t.Host, t.Port)
}

// redact removes the DSN's password from s, which quotes a driver error that
// pgx may have built from the connection string.
func (t Target) redact(s string) string {
	if t.password == "" {
		return s
	}
	// Only the places a DSN carries a password are replaced: a bare
	// replacement would also blank a user or database name that happens to
	// equal it (leoflow:leoflow is the dev default).
	for _, pw := range []string{t.password, url.QueryEscape(t.password), url.PathEscape(t.password)} {
		s = strings.ReplaceAll(s, ":"+pw+"@", ":xxxxx@")
		s = strings.ReplaceAll(s, "password="+pw, "password=xxxxx")
	}
	return s
}

// schemes are the URL schemes the wrapper accepts. postgres:// and
// postgresql:// are what database.url documents; pgx5:// is golang-migrate's
// name for the same driver, which a DSN written for the old CLI may carry.
var schemes = map[string]bool{"postgres": true, "postgresql": true, "pgx5": true}

// ParseTarget reads the parts of a DSN the messages need. A DSN that is not a
// Postgres URL with a database name is a KindBadDSN failure, whose text never
// quotes the DSN, since it holds the password.
func ParseTarget(dsn string) (Target, error) {
	if strings.TrimSpace(dsn) == "" {
		return Target{}, badDSN("database.url is empty")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		var uerr *url.Error
		reason := "it does not parse as a URL"
		if errors.As(err, &uerr) {
			reason = uerr.Err.Error()
		}
		return Target{}, badDSN("database.url is not a valid postgres:// URL (" + reason + ")")
	}
	if u.Scheme == "" {
		return Target{}, badDSN("database.url is not a postgres:// URL; the keyword=value form is not accepted")
	}
	if !schemes[u.Scheme] {
		return Target{}, badDSN(fmt.Sprintf("database.url has scheme %q; it must be postgres:// or postgresql://", u.Scheme))
	}
	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		return Target{}, badDSN("database.url has no database name after the host (postgres://user:password@host:5432/<database>)")
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	pw, _ := u.User.Password()
	return Target{
		Host:        u.Hostname(),
		Port:        port,
		Database:    db,
		User:        u.User.Username(),
		SSLRootCert: u.Query().Get("sslrootcert"),
		password:    pw,
	}, nil
}

// DriverURL rewrites a postgres:// or postgresql:// DSN to golang-migrate's
// pgx5:// scheme, so the migration runs on pgx like the control plane does
// and a DSN that starts the server also migrates (sslrootcert=system is
// pgx-only, and the lib/pq CLI failed on it).
func DriverURL(dsn string) string {
	for _, s := range []string{"postgresql://", "postgres://"} {
		if strings.HasPrefix(dsn, s) {
			return "pgx5://" + strings.TrimPrefix(dsn, s)
		}
	}
	return dsn
}

// connURL is the DSN as pgx's database/sql driver reads it: golang-migrate's
// x- parameters removed, which pgx would send to the server as settings.
func connURL(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Scheme = "postgres"
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(k, "x-") {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// caMountPath is where the chart mounts database.caConfigMap, in the Job and
// in the control plane.
const caMountPath = "/etc/leoflow/db-ca/ca.crt"

// PreflightCA checks, before any dial, that the sslrootcert file the DSN names
// is present. Without it pgx fails with `unable to read CA file`, which says
// nothing about the ConfigMap the chart mounts there. A Helm-side check cannot
// do this: lookup returns nothing under helm template, --dry-run and Argo CD.
func PreflightCA(t Target) error {
	if t.SSLRootCert == "" || t.SSLRootCert == "system" {
		return nil
	}
	_, err := os.Stat(t.SSLRootCert)
	if err == nil {
		return nil
	}
	what := "is not present in this container"
	if !errors.Is(err, os.ErrNotExist) {
		what = "cannot be read (" + err.Error() + ")"
	}
	return &Failure{
		Kind: KindMissingCA,
		Code: ExitMissingCA,
		Summary: fmt.Sprintf("database.url sets sslrootcert=%s and that file %s. "+
			"The chart mounts the ConfigMap named by database.caConfigMap at %s: "+
			"set database.caConfigMap to a ConfigMap that holds the CA bundle under the key ca.crt, "+
			"or point sslrootcert at a file the image has (sslrootcert=system uses the system roots). "+
			"See the chart README, %q (%s).",
			t.SSLRootCert, what, caMountPath, "Verified TLS to managed Postgres", readmeTLS),
		Cause: err,
	}
}

func badDSN(summary string) *Failure {
	return &Failure{
		Kind: KindBadDSN,
		Code: ExitBadDSN,
		Summary: summary + ". Set database.url (the databaseUrl key of the chart Secret, or of database.existingSecret) " +
			"to postgres://user:password@host:5432/dbname?sslmode=verify-full.",
	}
}
