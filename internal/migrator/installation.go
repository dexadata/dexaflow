package migrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
)

// The installation id (ADR 0090 decision 3) tells a replaced or emptied
// database from the one a release was installed on. It lives in two places:
// the one-row installation table, which the wrapper writes after `up` (never
// the SQL), and a ConfigMap the wrapper creates and updates itself, from which
// the server reads it. A database whose id is not the ConfigMap's is not
// migrated: it is reported as replaced, and an operator accepts it by setting
// the chart's installation.id.

// storedID is the installation id outside the database, in the ConfigMap.
type storedID struct {
	found bool
	id    string
}

// databaseID is what the database says about its installation: whether it
// has a schema at all, and the id it carries ("" for none).
type databaseID struct {
	schema bool
	id     string
}

// installationPlan is what a run does with the id once `up` has succeeded.
type installationPlan struct {
	// id is the installation id after the run.
	id string
	// writeDB writes id into the database.
	writeDB bool
	// createCM creates the ConfigMap with id; updateCM sets it to id.
	createCM, updateCM bool
}

// planInstallation decides, before anything is migrated, whether this
// database belongs to this release, and what to record. It refuses with a
// KindReplaced Failure when the database is not this release's own and the
// operator has not accepted it.
//
// The id is written into a database without one in two cases only: the
// namespace has no ConfigMap yet (a first install, or an installation from
// before the table, adopted once), or the operator changed installation.id
// away from the ConfigMap's, which accepts a replacement once.
func planInstallation(cm storedID, pinned string, db databaseID, newID func() string) (installationPlan, *Failure) {
	switch {
	case !cm.found:
		return planFirst(pinned, db, newID)
	case pinned == "" || pinned == cm.id:
		if db.id == cm.id {
			return installationPlan{id: cm.id}, nil
		}
		return installationPlan{}, replaced(fmt.Sprintf(
			"database replaced: this release's installation id is %q, and the database %s. It was emptied, "+
				"restored from another installation, or database.url points at another database, so nothing was "+
				"migrated. Restore the right database or point database.url back at it, or accept this one by "+
				"setting installation.id: to the id it carries to adopt it, or to a new id for an emptied one. See %s.",
			cm.id, describe(db), docsReplaced))
	default:
		return planAcknowledged(pinned, db)
	}
}

// planFirst is a namespace without the ConfigMap: nothing records this
// release yet, so the database's own id, if any, is adopted.
func planFirst(pinned string, db databaseID, newID func() string) (installationPlan, *Failure) {
	if db.id == "" {
		id := pinned
		if id == "" {
			id = newID()
		}
		return installationPlan{id: id, writeDB: true, createCM: true}, nil
	}
	if pinned != "" && pinned != db.id {
		return installationPlan{}, replaced(fmt.Sprintf(
			"installation.id is %q, but the database carries installation id %q. Set installation.id to %q to "+
				"adopt this database, or unset it; nothing was migrated. See %s.",
			pinned, db.id, db.id, docsReplaced))
	}
	return installationPlan{id: db.id, createCM: true}, nil
}

// planAcknowledged is an installation.id changed away from the ConfigMap's:
// the operator accepts the database, by adopting the id it carries or by
// giving an emptied one a new id. A new id is never written over another id,
// so a database that belongs to an installation is never re-keyed.
func planAcknowledged(pinned string, db databaseID) (installationPlan, *Failure) {
	if db.id != "" && db.id != pinned {
		return installationPlan{}, replaced(fmt.Sprintf(
			"installation.id was changed to %q, but the database carries installation id %q. A new id is "+
				"written only into a database without one: set installation.id to %q to adopt this database, "+
				"or point database.url at the database you meant; nothing was migrated. See %s.",
			pinned, db.id, db.id, docsReplaced))
	}
	return installationPlan{id: pinned, writeDB: db.id == "", updateCM: true}, nil
}

func describe(db databaseID) string {
	switch {
	case !db.schema:
		return "has no schema"
	case db.id == "":
		return "carries no installation id"
	default:
		return fmt.Sprintf("carries installation id %q", db.id)
	}
}

func replaced(summary string) *Failure {
	return &Failure{Kind: KindReplaced, Code: ExitReplaced, Summary: summary}
}

// validInstallationID is what installation.id may hold: a UUID, or any short
// name an operator chooses.
var validInstallationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

const sqlstateUndefinedTable = "42P01"

// readDatabaseID reads whether the database has a schema and the id it
// carries. A schema_migrations table with no row is no schema, as
// golang-migrate reads it.
func readDatabaseID(ctx context.Context, db *sql.DB) (databaseID, error) {
	var rows int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&rows)
	if isUndefinedTable(err) {
		return databaseID{}, nil
	}
	if err != nil {
		return databaseID{}, fmt.Errorf("reading schema_migrations: %w", err)
	}
	if rows == 0 {
		return databaseID{}, nil
	}
	var id string
	err = db.QueryRowContext(ctx, "SELECT id FROM installation").Scan(&id)
	switch {
	case isUndefinedTable(err), errors.Is(err, sql.ErrNoRows):
		return databaseID{schema: true}, nil
	case err != nil:
		return databaseID{}, fmt.Errorf("reading the installation id: %w", err)
	}
	return databaseID{schema: true, id: id}, nil
}

// writeDatabaseID records id in the one-row installation table, replacing
// the row if there is one.
func writeDatabaseID(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO installation (id) VALUES ($1)
ON CONFLICT (singleton) DO UPDATE SET id = EXCLUDED.id, recorded_at = now()`, id)
	if err != nil {
		return fmt.Errorf("writing the installation id: %w", err)
	}
	return nil
}

func isUndefinedTable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == sqlstateUndefinedTable
}

// idStore is where the installation id lives outside the database: the
// ConfigMap, in the image.
type idStore interface {
	// Get reads the id; found is false when there is no ConfigMap.
	Get(ctx context.Context) (storedID, error)
	// Create creates the ConfigMap holding id.
	Create(ctx context.Context, id string) error
	// Update sets the ConfigMap's id.
	Update(ctx context.Context, id string) error
	// String names the ConfigMap for messages.
	String() string
}

// upWithInstallation is `up` with the installation id: it refuses a database
// that is not this release's own before migrating anything, and records the
// id in the database and the ConfigMap after `up` succeeds. The database is
// written first, so a run cut short between the two is completed by the next
// one (planFirst adopts the database's id, an acknowledgement finds the
// database already carrying it).
func upWithInstallation(ctx context.Context, op *opened, db *sql.DB, store idStore, pinned string, newID func() string, stdout io.Writer) error {
	cm, err := store.Get(ctx)
	if err != nil {
		return storeFailure("reading", store, err)
	}
	dbID, err := readDatabaseID(ctx, db)
	if err != nil {
		return Classify(err, op.target)
	}
	plan, f := planInstallation(cm, pinned, dbID, newID)
	if f != nil {
		return f
	}
	if upErr := execute(op, command{name: "up"}, stdout); upErr != nil {
		return upErr
	}
	if plan.writeDB {
		if werr := writeDatabaseID(ctx, db, plan.id); werr != nil {
			return Classify(werr, op.target)
		}
	}
	switch {
	case plan.createCM:
		err = store.Create(ctx, plan.id)
	case plan.updateCM:
		err = store.Update(ctx, plan.id)
	}
	if err != nil {
		return storeFailure("writing", store, err)
	}
	say(stdout, "installation id: %s\n", plan.id)
	return nil
}

// errStoreRace is a ConfigMap another run created or changed between this
// run's read and its write.
var errStoreRace = errors.New("the ConfigMap changed while this run held it")

func storeFailure(verb string, store idStore, err error) *Failure {
	code, kind := ExitFailed, KindOther
	if errors.Is(err, errStoreRace) {
		code, kind = ExitTransient, KindLocked
	}
	return &Failure{
		Kind: kind, Code: code, Cause: err,
		Summary: fmt.Sprintf("%s the installation ConfigMap %s failed. The migrate Job's ServiceAccount needs get, "+
			"create and update on it (the chart's migrate Role grants them when rbac.create is true); "+
			"the Job retries this. See %s.", verb, store, docsJob),
	}
}
