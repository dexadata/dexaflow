//go:build integration

package storage_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
)

const (
	kmPublished = "dev-insecure-secret-key-32bytes!"
	kmNew       = "a-fresh-per-install-key-32byte!!"
	kmHandSet   = "an-operator-hand-set-key-32byte!"
	kmStranger  = "a-key-nobody-configured-32bytes!"
)

// snapshotCiphertexts returns every encrypted column as stored, keyed by
// column and row, so a test can assert "byte-identical afterwards".
func snapshotCiphertexts(t *testing.T, conn *pgx.Conn) map[string]string {
	t.Helper()
	vals, err := storage.ReadSecretValues(context.Background(), conn)
	if err != nil {
		t.Fatalf("reading secrets: %v", err)
	}
	out := make(map[string]string, len(vals))
	for _, v := range vals {
		out[v.Column.String()+"/"+v.Label] = v.Ciphertext
	}
	return out
}

func seedConn(t *testing.T, repo *storage.Repository, c secrets.Cipher, conn domain.Connection) {
	t.Helper()
	repo.SetCipher(c)
	if err := repo.SetConnection(context.Background(), "default", conn); err != nil {
		t.Fatalf("seeding %s: %v", conn.ConnID, err)
	}
}

// assertAllOpenUnder fails unless every non-empty encrypted column opens under
// c ALONE and carries the seeded plaintext.
func assertAllOpenUnder(t *testing.T, conn *pgx.Conn, c secrets.Cipher, want map[string]string) {
	t.Helper()
	vals, err := storage.ReadSecretValues(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != len(want) {
		t.Fatalf("found %d encrypted values, want %d", len(vals), len(want))
	}
	for _, v := range vals {
		got, derr := c.Decrypt(v.Ciphertext)
		if derr != nil {
			t.Errorf("%s of %s does not open under the new key alone: %v", v.Column, v.Label, derr)
			continue
		}
		if k := v.Column.String() + "/" + v.Label; got != want[k] {
			t.Errorf("%s: plaintext %q, want %q", k, got, want[k])
		}
	}
}

// The core of ADR 0065 step 4: everything a predecessor opened is moved onto
// the new key in ONE transaction, the two columns of a row independently, nil
// and empty columns untouched, and the pass is verified under the new key
// alone before it commits.
func TestMigrateSecretValuesMovesEveryColumnOntoTheNewKey(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	ctx := context.Background()
	published, fresh := mustCipher(t, kmPublished), mustCipher(t, kmNew)

	seedConn(t, repo, published, domain.Connection{ConnID: "both_old", ConnType: "postgres", Password: "pw-a", Extra: `{"a":1}`})
	seedConn(t, repo, published, domain.Connection{ConnID: "mixed", ConnType: "postgres", Password: "pw-b", Extra: `{"b":2}`})
	// Password re-entered under the new key, extra still under the old one.
	repo.SetCipher(fresh)
	pw := "pw-b"
	if err := repo.SetConnectionPatch(ctx, "default", domain.ConnectionPatch{ConnID: "mixed", ConnType: "postgres", Password: &pw}); err != nil {
		t.Fatal(err)
	}
	seedConn(t, repo, published, domain.Connection{ConnID: "no_secrets", ConnType: "http"})
	empty := ""
	repo.SetCipher(published)
	if err := repo.SetConnectionPatch(ctx, "default", domain.ConnectionPatch{ConnID: "cleared", ConnType: "http", Password: &empty, Extra: &empty}); err != nil {
		t.Fatal(err)
	}

	var calls []string
	res, err := storage.MigrateSecretValues(ctx, conn, fresh, []secrets.Cipher{published}, storage.KeyMigrationHooks{
		AfterFirstUpdate: func() { calls = append(calls, "first-update") },
		AfterUpdates:     func() { calls = append(calls, "updates") },
		AfterVerify:      func() { calls = append(calls, "verify") },
	})
	if err != nil {
		t.Fatalf("MigrateSecretValues: %v", err)
	}
	if res.Migrated != 3 || res.Verified != 4 || res.Skipped != 0 || res.Unreadable != 0 || !res.Clean() {
		t.Errorf("result %+v, want 3 migrated, 4 verified, nothing skipped or unreadable", res)
	}
	if fmt.Sprint(calls) != "[first-update updates verify]" {
		t.Errorf("hooks ran as %v, want first-update, updates, verify", calls)
	}
	assertAllOpenUnder(t, conn, fresh, map[string]string{
		"connections.password/both_old": "pw-a", "connections.extra/both_old": `{"a":1}`,
		"connections.password/mixed": "pw-b", "connections.extra/mixed": `{"b":2}`,
	})

	// A second pass has nothing to move and still verifies.
	again, aerr := storage.MigrateSecretValues(ctx, conn, fresh, []secrets.Cipher{published}, storage.KeyMigrationHooks{})
	if aerr != nil || again.Migrated != 0 || again.Verified != 4 {
		t.Errorf("second pass: %+v, %v; want nothing migrated, 4 verified", again, aerr)
	}
}

// A column no recorded key opens rolls the whole pass back: the mixed case
// the issue requires leaves the datastore byte-identical.
func TestMigrateSecretValuesRollsBackOnAnUnreadableColumn(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	ctx := context.Background()
	published, fresh, stranger := mustCipher(t, kmPublished), mustCipher(t, kmNew), mustCipher(t, kmStranger)

	seedConn(t, repo, published, domain.Connection{ConnID: "old", ConnType: "postgres", Password: "pw"})
	seedConn(t, repo, fresh, domain.Connection{ConnID: "new", ConnType: "postgres", Password: "pw2"})
	seedConn(t, repo, stranger, domain.Connection{ConnID: "orphan", ConnType: "postgres", Password: "lost"})
	before := snapshotCiphertexts(t, conn)

	res, err := storage.MigrateSecretValues(ctx, conn, fresh, []secrets.Cipher{published}, storage.KeyMigrationHooks{})
	var unreadable *storage.UnreadableSecretsError
	if !errors.As(err, &unreadable) {
		t.Fatalf("want an UnreadableSecretsError, got %v", err)
	}
	if len(unreadable.Values) != 1 || unreadable.Values[0].Label != "orphan" {
		t.Errorf("unreadable values %+v, want only orphan", unreadable.Values)
	}
	if res.Clean() || res.Unreadable != 1 {
		t.Errorf("result %+v must report the unreadable column and not be clean", res)
	}
	if after := snapshotCiphertexts(t, conn); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("datastore changed by a pass that must have rolled back:\nbefore %v\nafter  %v", before, after)
	}
}

// garbageSealer encrypts to bytes no key opens: a stand-in for a broken cipher
// or a bug in the re-encryption, which the in-transaction verification exists
// to catch.
type garbageSealer struct{ secrets.Cipher }

func (garbageSealer) Encrypt(string) (string, error) {
	return base64.StdEncoding.EncodeToString(make([]byte, 40)), nil
}

func TestMigrateSecretValuesRollsBackWhenVerificationFails(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	published, fresh := mustCipher(t, kmPublished), mustCipher(t, kmNew)
	seedConn(t, repo, published, domain.Connection{ConnID: "old", ConnType: "postgres", Password: "pw", Extra: "{}"})
	before := snapshotCiphertexts(t, conn)

	_, err := storage.MigrateSecretValues(context.Background(), conn, garbageSealer{fresh}, []secrets.Cipher{published}, storage.KeyMigrationHooks{})
	if err == nil {
		t.Fatal("a pass whose output does not open under the new key must fail")
	}
	if after := snapshotCiphertexts(t, conn); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("a failed verification committed: before %v after %v", before, after)
	}
}

// Rows under three keys: the per-install key, a hand-set predecessor and the
// published constant all end under the first.
func TestMigrateSecretValuesThreeKeys(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	published, fresh, hand := mustCipher(t, kmPublished), mustCipher(t, kmNew), mustCipher(t, kmHandSet)
	seedConn(t, repo, published, domain.Connection{ConnID: "a", ConnType: "x", Password: "1"})
	seedConn(t, repo, fresh, domain.Connection{ConnID: "b", ConnType: "x", Password: "2"})
	seedConn(t, repo, hand, domain.Connection{ConnID: "c", ConnType: "x", Password: "3"})

	res, err := storage.MigrateSecretValues(context.Background(), conn, fresh, []secrets.Cipher{hand, published}, storage.KeyMigrationHooks{})
	if err != nil || res.Migrated != 2 || res.Verified != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	assertAllOpenUnder(t, conn, fresh, map[string]string{
		"connections.password/a": "1", "connections.password/b": "2", "connections.password/c": "3",
	})
}

// A few thousand rows still go in one verified transaction.
func TestMigrateSecretValuesAFewThousandRows(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	conn := keyMigrationConn(t, dsn)
	ctx := context.Background()
	published, fresh := mustCipher(t, kmPublished), mustCipher(t, kmNew)
	const n = 3000
	want := make(map[string]string, n)
	batch := &pgx.Batch{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("conn_%04d", i)
		ct, err := published.Encrypt("pw-" + id)
		if err != nil {
			t.Fatal(err)
		}
		batch.Queue(`INSERT INTO connections (tenant_id, conn_id, conn_type, password)
		             SELECT id, $1, 'x', $2 FROM tenants WHERE name = 'default'`, id, ct)
		want["connections.password/"+id] = "pw-" + id
	}
	if err := conn.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	res, err := storage.MigrateSecretValues(ctx, conn, fresh, []secrets.Cipher{published}, storage.KeyMigrationHooks{})
	if err != nil || res.Migrated != n || res.Verified != n {
		t.Fatalf("%+v %v", res, err)
	}
	assertAllOpenUnder(t, conn, fresh, want)
}

// The session dying mid-transaction (pg_terminate_backend from another
// session) rolls everything back.
func TestMigrateSecretValuesSessionKilledMidTransaction(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	killer := keyMigrationConn(t, dsn)
	ctx := context.Background()
	published, fresh := mustCipher(t, kmPublished), mustCipher(t, kmNew)
	seedConn(t, repo, published, domain.Connection{ConnID: "a", ConnType: "x", Password: "1", Extra: "{}"})
	seedConn(t, repo, published, domain.Connection{ConnID: "b", ConnType: "x", Password: "2"})
	before := snapshotCiphertexts(t, killer)

	pid := conn.PgConn().PID()
	_, err := storage.MigrateSecretValues(ctx, conn, fresh, []secrets.Cipher{published}, storage.KeyMigrationHooks{
		AfterFirstUpdate: func() {
			if _, kerr := killer.Exec(ctx, "SELECT pg_terminate_backend($1)", pid); kerr != nil {
				t.Errorf("terminating: %v", kerr)
			}
		},
	})
	if err == nil {
		t.Fatal("a pass whose session was terminated cannot report success")
	}
	if after := snapshotCiphertexts(t, killer); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("a terminated transaction left changes behind: before %v after %v", before, after)
	}
}

// The re-check of step 5 lists every column the new key alone cannot open.
func TestSecretValuesNotUnder(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	published, fresh := mustCipher(t, kmPublished), mustCipher(t, kmNew)
	seedConn(t, repo, fresh, domain.Connection{ConnID: "ok", ConnType: "x", Password: "1"})
	seedConn(t, repo, published, domain.Connection{ConnID: "late", ConnType: "x", Extra: "{}"})
	left, err := storage.SecretValuesNotUnder(context.Background(), conn, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Label != "late" || left[0].Column.Column != "extra" {
		t.Errorf("got %+v, want only late's extra", left)
	}
}

// The datastore lock of ADR 0065 section 3: a shared holder (a Lite server or
// its supervisor) blocks the exclusive migration, and an exclusive holder (a
// migration) blocks a server from starting.
func TestKeyMigrationLockExcludesServerAndMigration(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	ctx := context.Background()
	server, migration := keyMigrationConn(t, dsn), keyMigrationConn(t, dsn)

	ok, err := storage.TryKeyMigrationLockShared(ctx, server)
	if err != nil || !ok {
		t.Fatalf("shared lock on an idle datastore: %v %v", ok, err)
	}
	if held, herr := storage.HoldsKeyMigrationLock(ctx, server); herr != nil || !held {
		t.Errorf("the holder does not see its own lock: %v %v", held, herr)
	}
	if ok, err = storage.TryKeyMigrationLockExclusive(ctx, migration); err != nil || ok {
		t.Fatalf("a migration took the lock while a server held it: %v %v", ok, err)
	}
	if _, err = server.Exec(ctx, "SELECT pg_advisory_unlock_shared($1)", storage.KeyMigrationLockID); err != nil {
		t.Fatal(err)
	}
	if ok, err = storage.TryKeyMigrationLockExclusive(ctx, migration); err != nil || !ok {
		t.Fatalf("the migration could not lock an idle datastore: %v %v", ok, err)
	}
	if ok, err = storage.TryKeyMigrationLockShared(ctx, server); err != nil || ok {
		t.Fatalf("a server started while a migration held the lock: %v %v", ok, err)
	}
}

// A terminated lock session is reported as a lost lock, which the server
// treats as fatal.
func TestHoldsKeyMigrationLockAfterTermination(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	ctx := context.Background()
	holder, killer := keyMigrationConn(t, dsn), keyMigrationConn(t, dsn)
	if ok, err := storage.TryKeyMigrationLockShared(ctx, holder); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := killer.Exec(ctx, "SELECT pg_terminate_backend($1)", holder.PgConn().PID()); err != nil {
		t.Fatal(err)
	}
	held, err := storage.HoldsKeyMigrationLock(ctx, holder)
	if err == nil && held {
		t.Error("a terminated session still reports holding the lock")
	}
}

// KeyMigrationLockHeld lets a command that is not a lock party see whether a
// Lite server is running against the datastore (#413), without taking the lock
// itself: taking it even briefly could make a server starting at that moment
// refuse to boot.
func TestKeyMigrationLockHeldSeesAnotherSession(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	ctx := context.Background()
	server, observer := keyMigrationConn(t, dsn), keyMigrationConn(t, dsn)

	if held, err := storage.KeyMigrationLockHeld(ctx, observer); err != nil || held {
		t.Fatalf("idle datastore reported as locked: %v %v", held, err)
	}
	if ok, err := storage.TryKeyMigrationLockShared(ctx, server); err != nil || !ok {
		t.Fatalf("shared lock on an idle datastore: %v %v", ok, err)
	}
	if held, err := storage.KeyMigrationLockHeld(ctx, observer); err != nil || !held {
		t.Fatalf("a server's shared lock was not seen: %v %v", held, err)
	}
	if mine, err := storage.HoldsKeyMigrationLock(ctx, observer); err != nil || mine {
		t.Errorf("the observer took the lock while looking at it: %v %v", mine, err)
	}
	if _, err := server.Exec(ctx, "SELECT pg_advisory_unlock_shared($1)", storage.KeyMigrationLockID); err != nil {
		t.Fatal(err)
	}
	if held, err := storage.KeyMigrationLockHeld(ctx, observer); err != nil || held {
		t.Errorf("a released lock is still reported: %v %v", held, err)
	}
}
