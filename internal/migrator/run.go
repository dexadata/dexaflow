package migrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" migrate scheme
	"github.com/golang-migrate/migrate/v4/source"
	_ "github.com/golang-migrate/migrate/v4/source/file" // registers file:// for -path and -source
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for the preflight ping

	"github.com/dexadata/dexaflow/migrations"
)

// options are what the command line sets.
type options struct {
	databaseURL    string
	path           string
	source         string
	verbose        bool
	lockTimeout    time.Duration
	prefetch       uint
	connectTimeout time.Duration
	// installConfigMap and installNamespace name the installation ConfigMap;
	// installID is the chart's installation.id. Empty installConfigMap runs
	// without the installation id.
	installConfigMap string
	installNamespace string
	installID        string
}

// tracksInstallation reports whether this run checks and records the
// installation id: a plain `up` with the ConfigMap named.
func (o options) tracksInstallation(c command) bool {
	return o.installConfigMap != "" && c.name == "up" && !c.hasN
}

// opened is a migrator ready to run a command, with the target it points at.
type opened struct {
	m      *migrate.Migrate
	target Target
}

// open parses the DSN, checks the CA file, reaches the server and builds the
// migrator. Every error it returns is a *Failure.
func open(ctx context.Context, o options, log io.Writer) (*opened, error) {
	t, err := ParseTarget(o.databaseURL)
	if err != nil {
		return nil, err
	}
	if caErr := PreflightCA(t); caErr != nil {
		return nil, caErr
	}
	if pingErr := ping(ctx, connURL(o.databaseURL), o.connectTimeout); pingErr != nil {
		return nil, Classify(pingErr, t)
	}
	srcName, src, err := openSource(o)
	if err != nil {
		return nil, &Failure{Kind: KindUsage, Code: ExitUsage, Cause: err,
			Summary: fmt.Sprintf("cannot read the migrations: %v", err)}
	}
	m, err := migrate.NewWithSourceInstance(srcName, src, DriverURL(o.databaseURL))
	if err != nil {
		_ = src.Close() //nolint:errcheck // best-effort close on a failed open
		return nil, Classify(err, t)
	}
	m.Log = &logger{w: log, verbose: o.verbose}
	if o.lockTimeout > 0 {
		m.LockTimeout = o.lockTimeout
	}
	if o.prefetch > 0 {
		m.PrefetchMigrations = o.prefetch
	}
	return &opened{m: m, target: t}, nil
}

// ping opens one connection with a deadline. golang-migrate's own open pings
// with no deadline, so a host that drops packets would hang the Job until
// Helm's hook timeout instead of failing with a reason.
func ping(ctx context.Context, dsn string, timeout time.Duration) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("opening the database: %w", err)
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // best-effort close of a probe handle
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connecting to the database: %w", err)
	}
	return nil
}

// openSource picks the migrations: a -source URL, a -path directory, or the
// copy embedded in this binary.
func openSource(o options) (string, source.Driver, error) {
	switch {
	case o.source != "":
		d, err := source.Open(o.source)
		return "source", d, err
	case o.path != "":
		d, err := source.Open("file://" + o.path)
		return "file", d, err
	}
	d, err := iofs.New(migrations.Files, ".")
	return "iofs", d, err
}

// command is one parsed subcommand.
type command struct {
	name string
	n    int  // the version of force and goto, the step count of up
	hasN bool // up was given a step count
}

// execute runs the command on an opened migrator and prints its outcome.
func execute(op *opened, c command, stdout io.Writer) error {
	var err error
	switch c.name {
	case "up":
		if c.hasN {
			err = op.m.Steps(c.n)
		} else {
			err = op.m.Up()
		}
	case "goto":
		err = op.m.Migrate(uint(c.n)) //nolint:gosec // parseCommand rejects negative versions
	case "force":
		err = op.m.Force(c.n)
	case "version":
		return printVersion(op, stdout)
	}
	if errors.Is(err, migrate.ErrNoChange) {
		say(stdout, "no change\n")
		return printVersion(op, stdout)
	}
	if err != nil {
		return Classify(err, op.target)
	}
	return printVersion(op, stdout)
}

// printVersion prints the schema version in the CLI's form: "27" or
// "27 (dirty)", which the upgrades page quotes.
func printVersion(op *opened, stdout io.Writer) error {
	v, dirty, err := op.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		say(stdout, "no migration has been applied\n")
		return nil
	}
	if err != nil {
		return Classify(err, op.target)
	}
	if dirty {
		say(stdout, "%d (dirty)\n", v)
		return nil
	}
	say(stdout, "%v\n", v)
	return nil
}

// logger is golang-migrate's Logger, printing each applied file the way the
// upstream CLI does ("27/u dag_runs_version_index (1.2s)").
type logger struct {
	w       io.Writer
	verbose bool
}

// Printf prints one line of golang-migrate's progress.
func (l *logger) Printf(format string, v ...any) { say(l.w, format, v...) }

// Verbose reports whether golang-migrate should print its verbose lines.
func (l *logger) Verbose() bool { return l.verbose }

// say prints to an output stream. A write error to stdout or stderr has
// nowhere better to go, and the exit code still carries the outcome.
func say(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...) //nolint:errcheck // see the comment above
}
