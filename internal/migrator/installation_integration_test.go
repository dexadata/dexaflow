//go:build integration

package migrator

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/migrations"
)

// memStore is the installation ConfigMap kept in memory.
type memStore struct{ stored storedID }

func (m *memStore) Get(context.Context) (storedID, error) { return m.stored, nil }
func (m *memStore) Create(_ context.Context, id string) error {
	if m.stored.found {
		return errStoreRace
	}
	m.stored = storedID{found: true, id: id}
	return nil
}
func (m *memStore) Update(_ context.Context, id string) error {
	m.stored = storedID{found: true, id: id}
	return nil
}
func (m *memStore) String() string { return "test/installation" }

// upTracked runs `up` with the installation id, as the chart's Job does.
func upTracked(t *testing.T, dsn string, store idStore, pinned, fresh string) error {
	t.Helper()
	op, err := open(t.Context(), options{databaseURL: dsn, connectTimeout: 10 * time.Second}, io.Discard)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _, _ = op.m.Close() }()
	db, err := sql.Open("pgx", connURL(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	return upWithInstallation(t.Context(), op, db, store, pinned, func() string { return fresh }, io.Discard)
}

func dbState(t *testing.T, dsn string) databaseID {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	got, err := readDatabaseID(t.Context(), db)
	if err != nil {
		t.Fatalf("readDatabaseID: %v", err)
	}
	return got
}

func wantReplaced(t *testing.T, err error) {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) || f.Code != ExitReplaced {
		t.Fatalf("err = %v, want a replaced-database failure", err)
	}
}

func emptyDatabase(t *testing.T, dsn string) {
	t.Helper()
	exec(t, dsn, "DROP SCHEMA public CASCADE")
	exec(t, dsn, "CREATE SCHEMA public")
}

// A first install records the id in both places; a database emptied under
// the release is refused without migrating; changing installation.id to a
// new id accepts it once.
func TestIntegrationInstallationFirstInstallEmptiedAcknowledged(t *testing.T) {
	dsn := createScratch(t, adminURL(t))
	store := &memStore{}

	if err := upTracked(t, dsn, store, "", "id-1"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if store.stored != (storedID{found: true, id: "id-1"}) {
		t.Fatalf("ConfigMap = %+v, want id-1", store.stored)
	}
	if got := dbState(t, dsn); got != (databaseID{schema: true, id: "id-1"}) {
		t.Fatalf("database = %+v, want id-1", got)
	}
	if err := upTracked(t, dsn, store, "", "unused"); err != nil {
		t.Fatalf("second up on the same database: %v", err)
	}

	emptyDatabase(t, dsn)
	wantReplaced(t, upTracked(t, dsn, store, "", "unused"))
	if got := dbState(t, dsn); got.schema {
		t.Fatalf("a replaced database was migrated: %+v", got)
	}

	if err := upTracked(t, dsn, store, "id-2", "unused"); err != nil {
		t.Fatalf("acknowledged with a new id: %v", err)
	}
	if got := dbState(t, dsn); got != (databaseID{schema: true, id: "id-2"}) {
		t.Fatalf("database = %+v, want id-2", got)
	}
	if store.stored.id != "id-2" {
		t.Fatalf("ConfigMap = %+v, want id-2", store.stored)
	}
	// The acknowledgement applies once: the next emptying is refused again,
	// with installation.id still set to id-2.
	emptyDatabase(t, dsn)
	wantReplaced(t, upTracked(t, dsn, store, "id-2", "unused"))
}

// An installation from before the installation table is adopted once, and a
// database carrying another installation's id is refused until adopted.
func TestIntegrationInstallationAdoptAndOtherInstallation(t *testing.T) {
	admin := adminURL(t)
	dsn := createScratch(t, admin)
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, 0, "-database="+dsn, "goto", strconv.FormatUint(uint64(latest-1), 10))
	if got := dbState(t, dsn); got != (databaseID{schema: true}) {
		t.Fatalf("pre-table database = %+v, want a schema without an id", got)
	}

	store := &memStore{}
	if err := upTracked(t, dsn, store, "", "adopted"); err != nil {
		t.Fatalf("upgrading an existing installation: %v", err)
	}
	if got := dbState(t, dsn); got.id != "adopted" || store.stored.id != "adopted" {
		t.Fatalf("database %+v, ConfigMap %+v, want both adopted", got, store.stored)
	}

	exec(t, dsn, "UPDATE installation SET id = 'other'")
	wantReplaced(t, upTracked(t, dsn, store, "", "unused"))
	wantReplaced(t, upTracked(t, dsn, store, "brand-new", "unused"))
	if err := upTracked(t, dsn, store, "other", "unused"); err != nil {
		t.Fatalf("adopting the other installation's database: %v", err)
	}
	if store.stored.id != "other" {
		t.Fatalf("ConfigMap = %+v, want other", store.stored)
	}
}

// Up is what the server runs in-process for migrations.onBoot (ADR 0090
// decision 4): the embedded migrations, golang-migrate's lock, the same
// classified failures as the image.
func TestIntegrationUpInProcess(t *testing.T) {
	dsn := createScratch(t, adminURL(t))
	if err := Up(t.Context(), dsn, io.Discard); err != nil {
		t.Fatalf("Up on an empty database: %v", err)
	}
	if err := Up(t.Context(), dsn, io.Discard); err != nil {
		t.Fatalf("a second Up (no change): %v", err)
	}
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}
	stdout, _ := mustRun(t, 0, "-database="+dsn, "version")
	if want := strconv.FormatUint(uint64(latest), 10); stdout != want+"\n" {
		t.Fatalf("version after Up = %q, want %s", stdout, want)
	}
	exec(t, dsn, "UPDATE schema_migrations SET dirty = true")
	var f *Failure
	if err := Up(t.Context(), dsn, io.Discard); !errors.As(err, &f) || f.Code != ExitDirty {
		t.Fatalf("Up on a dirty schema = %v, want the dirty failure", err)
	}
}
