//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
)

// The point of a key rotation is to stop needing the old key. That only happens
// if something rewrites the rows, and until this existed nothing did: the
// fallback made old rows readable forever and the published Lite key could
// never be deleted (#486).
func TestReencryptMovesRowsOntoThePrimaryKey(t *testing.T) {
	repo, _, ctx := openRepo(t)
	clearConnections(t, repo, ctx)

	oldCipher := mustCipher(t, "the-old-published-key-32bytes!!!")
	newCipher := mustCipher(t, "a-fresh-per-install-key-32byte!!")

	// A connection written by the previous install, under the old key only.
	repo.SetCipher(oldCipher)
	if err := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: "prod_db", ConnType: "postgres", Host: "db.internal",
		Login: "svc", Password: "hunter2", Extra: `{"sslmode":"require"}`,
	}); err != nil {
		t.Fatalf("seeding a connection under the old key: %v", err)
	}

	// Rotation: the new key encrypts, the old one only decrypts.
	rotating := secrets.WithFallback(newCipher, oldCipher)
	repo.SetCipher(rotating)

	res, err := repo.ReencryptSecrets(ctx)
	if err != nil {
		t.Fatalf("ReencryptSecrets: %v", err)
	}
	if res.Migrated != 1 || !res.Clean() {
		t.Errorf("result %+v, want 1 row re-encrypted and a clean pass", res)
	}

	// The decisive assertion: the row now opens under the NEW key ALONE. If it
	// does not, the old key can never be retired and the rotation is cosmetic.
	repo.SetCipher(newCipher)
	got, gerr := repo.GetConnection(ctx, "default", "prod_db")
	if gerr != nil {
		t.Fatalf("reading with the new key alone: %v; the rotation did not move the row", gerr)
	}
	if got.Extra == "" {
		t.Errorf("extra did not survive the re-encryption: %+v", got)
	}

	// And a second pass has nothing to do: re-encrypting on every boot would
	// rewrite every row forever.
	repo.SetCipher(rotating)
	again, aerr := repo.ReencryptSecrets(ctx)
	if aerr != nil {
		t.Fatalf("second pass: %v", aerr)
	}
	if again.Migrated != 0 {
		t.Errorf("second pass re-encrypted %d rows, want 0", again.Migrated)
	}
}

// A row the configured keys cannot open must be left alone and reported, never
// silently dropped or overwritten: its ciphertext is the only copy of a
// credential, and the operator may still find the key.
func TestReencryptLeavesUnreadableRowsUntouched(t *testing.T) {
	repo, _, ctx := openRepo(t)
	clearConnections(t, repo, ctx)

	strangerCipher := mustCipher(t, "a-key-nobody-configured-32bytes!")
	repo.SetCipher(strangerCipher)
	if err := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: "orphan", ConnType: "postgres", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}

	repo.SetCipher(secrets.WithFallback(
		mustCipher(t, "a-fresh-per-install-key-32byte!!"),
		mustCipher(t, "the-old-published-key-32bytes!!!"),
	))
	res, err := repo.ReencryptSecrets(ctx)
	if err == nil {
		t.Error("a row no configured key can open must be reported, not passed over in silence")
	}
	if res.Migrated != 0 || res.Unreadable != 1 || res.Clean() {
		t.Errorf("result %+v, want nothing re-encrypted and 1 unreadable column", res)
	}

	// Still readable with its own key: nothing was destroyed.
	repo.SetCipher(strangerCipher)
	if _, gerr := repo.GetConnection(ctx, "default", "orphan"); gerr != nil {
		t.Errorf("the untouched row is no longer readable with its own key: %v", gerr)
	}
}

// clearConnections removes the rows these tests create, before and after. The
// re-encryption pass is global by design, so a row left by a previous test, or
// a previous run of this suite, changes what the next one counts.
func clearConnections(t *testing.T, repo *storage.Repository, ctx context.Context) {
	t.Helper()
	drop := func() {
		for _, id := range []string{"prod_db", "orphan"} {
			_ = repo.DeleteConnection(ctx, "default", id) //nolint:errcheck // absent is the desired state
		}
	}
	drop()
	t.Cleanup(drop)
}

func mustCipher(t *testing.T, key string) secrets.Cipher {
	t.Helper()
	k, err := secrets.ParseKey(key)
	if err != nil {
		t.Fatalf("parsing key: %v", err)
	}
	c, cerr := secrets.NewAESGCM(k)
	if cerr != nil {
		t.Fatalf("building cipher: %v", cerr)
	}
	return c
}

// racingReader rewrites the row under the primary key the first time it is
// asked to open it, the way a user saving the connection between the sweep's
// read and its write would.
type racingReader struct {
	secrets.StaleReader
	race func()
	done bool
}

func (r *racingReader) DecryptStale(ct string) (string, bool, error) {
	if !r.done {
		r.done = true
		r.race()
	}
	return r.StaleReader.DecryptStale(ct)
}

// A row the optimistic guard skipped was not moved by this pass, so the pass
// is not complete and its result must say so (ADR 0065 gap 1). It used to be
// logged and dropped, and the caller printed "rotation complete".
func TestReencryptReportsSkippedRows(t *testing.T) {
	repo, _, ctx := openRepo(t)
	clearConnections(t, repo, ctx)
	writer, _, _ := openRepo(t)

	oldCipher := mustCipher(t, "the-old-published-key-32bytes!!!")
	newCipher := mustCipher(t, "a-fresh-per-install-key-32byte!!")
	repo.SetCipher(oldCipher)
	if err := repo.SetConnection(ctx, "default", domain.Connection{ConnID: "prod_db", ConnType: "postgres", Password: "a"}); err != nil {
		t.Fatal(err)
	}
	writer.SetCipher(newCipher)
	repo.SetCipher(&racingReader{
		StaleReader: secrets.WithFallback(newCipher, oldCipher),
		race: func() {
			if err := writer.SetConnection(ctx, "default", domain.Connection{ConnID: "prod_db", ConnType: "postgres", Password: "b"}); err != nil {
				t.Errorf("racing write: %v", err)
			}
		},
	})
	res, err := repo.ReencryptSecrets(ctx)
	if err != nil {
		t.Fatalf("ReencryptSecrets: %v", err)
	}
	if res.Skipped != 1 || res.Clean() {
		t.Errorf("result %+v, want 1 skipped row and a pass that is not clean", res)
	}
}
