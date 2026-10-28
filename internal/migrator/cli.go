package migrator

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const usageText = `Usage: dexaflow-migrate [options] <command>

Applies the Dexaflow schema migrations to the database. The subcommands and
flags are the golang-migrate CLI's, so the recovery commands the upgrade guide
names work unchanged.

Commands:
  up [N]       apply every pending migration, or the next N
  goto V       migrate up or down to version V
  force V      set version V without running any SQL, and clear the dirty flag
  version      print the current version ("27" or "27 (dirty)")

Options:
  -database URL        the Postgres DSN (postgres://user:password@host:5432/db?sslmode=...)
  -path DIR            read the migrations from DIR instead of the embedded copy
  -source URL          read the migrations from a golang-migrate source URL
  -lock-timeout N      seconds to wait for the migration lock (default 15)
  -connect-timeout N   seconds to wait for the first connection (default 30)
  -prefetch N          migrations to load ahead (default 10)
  -verbose             print verbose progress
  -installation-configmap NAME
                       check and record the installation id (ADR 0090) in this
                       ConfigMap; up without N only, needs the Kubernetes API
  -installation-namespace NS
                       the namespace of that ConfigMap
  -installation-id ID  the chart's installation.id: the id of a first install,
                       or, changed away from the ConfigMap's, the acceptance of
                       a replaced database
  -version             print this binary's version and exit
  -help                print this text

Exit codes: 0 done; 2 usage; 3 database.url is not a Postgres URL; 4 the
database does not exist; 5 user or password refused; 6 the sslrootcert file is
missing; 7 the schema is dirty; 8 the database is not this release's own
(replaced or emptied); 75 the server could not be reached yet or the
lock is held (retry); 1 anything else.
`

// Main runs the command line the migrate image takes and returns the process
// exit code. Failures are printed to stderr in two lines: what happened and
// what to do, then the driver's own error, which the docs quote.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	o, c, code, done := parseArgs(args, stdout, stderr, version)
	if done {
		return code
	}
	op, err := run(ctx, o, c, stdout)
	if err == nil {
		return 0
	}
	f := Classify(err, Target{})
	say(stderr, "dexaflow-migrate: error: %s\n", f.Summary)
	if f.Cause != nil {
		t := Target{}
		if op != nil {
			t = op.target
		} else if pt, perr := ParseTarget(o.databaseURL); perr == nil {
			t = pt
		}
		say(stderr, "dexaflow-migrate: driver error: %s\n", t.redact(f.Cause.Error()))
	}
	return f.Code
}

// run opens the migrator and runs the command, through upWithInstallation
// when the run tracks the installation id. It returns the opened migrator,
// closed, for the target that redacts the error.
func run(ctx context.Context, o options, c command, stdout io.Writer) (*opened, error) {
	var store idStore
	if o.tracksInstallation(c) {
		s, err := newKubeStore(o.installNamespace, o.installConfigMap)
		if err != nil {
			return nil, err
		}
		store = s
	}
	op, err := open(ctx, o, stdout)
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = op.m.Close() }() //nolint:errcheck // best-effort close of source and database handles
	if store == nil {
		return op, execute(op, c, stdout)
	}
	db, err := sql.Open("pgx", connURL(o.databaseURL))
	if err != nil {
		return op, Classify(err, op.target)
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // best-effort close of a handle we own
	return op, upWithInstallation(ctx, op, db, store, o.installID, uuid.NewString, stdout)
}

// parseArgs reads the flags and the command. done is true when Main should
// return code without running anything (help, -version, a usage error).
func parseArgs(args []string, stdout, stderr io.Writer, version string) (o options, c command, code int, done bool) {
	fs := flag.NewFlagSet("dexaflow-migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.databaseURL, "database", "", "")
	fs.StringVar(&o.path, "path", "", "")
	fs.StringVar(&o.source, "source", "", "")
	fs.BoolVar(&o.verbose, "verbose", false, "")
	lock := fs.Uint("lock-timeout", 15, "")
	connect := fs.Uint("connect-timeout", 30, "")
	fs.UintVar(&o.prefetch, "prefetch", 10, "")
	fs.StringVar(&o.installConfigMap, "installation-configmap", "", "")
	fs.StringVar(&o.installNamespace, "installation-namespace", "", "")
	fs.StringVar(&o.installID, "installation-id", "", "")
	showVersion := fs.Bool("version", false, "")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			say(stdout, "%s", usageText)
			return o, c, 0, true
		}
		return o, c, usage(stderr, err.Error()), true
	}
	if *showVersion {
		say(stdout, "%v\n", version)
		return o, c, 0, true
	}
	o.lockTimeout = time.Duration(*lock) * time.Second       //nolint:gosec // a flag value in seconds
	o.connectTimeout = time.Duration(*connect) * time.Second //nolint:gosec // a flag value in seconds
	c, err := parseCommand(fs.Args())
	if err == nil {
		err = checkInstallationFlags(o)
	}
	if err != nil {
		return o, c, usage(stderr, err.Error()), true
	}
	return o, c, 0, false
}

// checkInstallationFlags refuses -installation-* flags that cannot work
// together.
func checkInstallationFlags(o options) error {
	switch {
	case o.installID != "" && o.installConfigMap == "":
		return errors.New("-installation-id needs -installation-configmap")
	case o.installConfigMap != "" && o.installNamespace == "":
		return errors.New("-installation-configmap needs -installation-namespace")
	case o.installID != "" && !validInstallationID.MatchString(o.installID):
		return fmt.Errorf("installation id %q is not 1 to 128 letters, digits, '.', '_' or '-', starting with a letter or digit", o.installID)
	}
	return nil
}

func usage(stderr io.Writer, problem string) int {
	say(stderr, "dexaflow-migrate: %s\n\n%s", problem, usageText)
	return ExitUsage
}

// parseCommand reads the subcommand and its argument.
func parseCommand(rest []string) (command, error) {
	if len(rest) == 0 {
		return command{}, errors.New("no command given")
	}
	c := command{name: rest[0]}
	arg := ""
	if len(rest) > 1 {
		arg = rest[1]
	}
	if len(rest) > 2 {
		return c, fmt.Errorf("%s takes at most one argument, got %d", c.name, len(rest)-1)
	}
	switch c.name {
	case "version":
		if arg != "" {
			return c, errors.New("version takes no argument")
		}
	case "up":
		if arg == "" {
			return c, nil
		}
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 {
			return c, fmt.Errorf("up takes a step count of 1 or more, got %q", arg)
		}
		c.n, c.hasN = n, true
	case "force", "goto":
		if arg == "" {
			return c, fmt.Errorf("%s needs a version", c.name)
		}
		n, err := strconv.Atoi(arg)
		low := 0
		if c.name == "force" {
			low = -1 // golang-migrate's "no version"
		}
		if err != nil || n < low {
			return c, fmt.Errorf("%s takes a version of %d or more, got %q", c.name, low, arg)
		}
		c.n = n
	default:
		return c, fmt.Errorf("unknown command %q (this image runs up, goto, force and version; "+
			"use the golang-migrate CLI for the others)", c.name)
	}
	return c, nil
}
