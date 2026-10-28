//go:build integration

package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// #1677: one connection whose extra no configured key opens (written under a
// key the server no longer has) must not fail the tenant's connection list.
// The list returns every connection, the unreadable one marked with its extra
// withheld; reading it alone is a recognizable error that names it.
func TestConnectionListSurvivesAnUnreadableExtra(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	ctx := context.Background()

	stranger := keyMigrationRepo(t, dsn)
	stranger.Configure(storage.WithCipher(varCipher(t, kmStranger)))
	if err := stranger.SetConnection(ctx, "default", domain.Connection{
		ConnID: "lost", ConnType: "http", Host: "api.example.com", Extra: `{"token":"s3cret"}`,
	}); err != nil {
		t.Fatal(err)
	}

	repo := keyMigrationRepo(t, dsn)
	repo.Configure(storage.WithCipher(varCipher(t, kmNew)))
	if err := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: "ok", ConnType: "postgres", Extra: `{"sslmode":"require"}`,
	}); err != nil {
		t.Fatal(err)
	}

	listed, total, err := repo.ListConnections(ctx, "default", 100, 0)
	if err != nil || total != 2 || len(listed) != 2 {
		t.Fatalf("ListConnections = %+v, %d, %v; want both connections", listed, total, err)
	}
	for _, c := range listed {
		switch c.ConnID {
		case "lost":
			if !c.ExtraUnreadable || c.Extra != "" || c.Host != "api.example.com" {
				t.Errorf("lost = %+v, want its fields, marked unreadable, no extra", c)
			}
		case "ok":
			if c.ExtraUnreadable || c.Extra != `{"sslmode":"require"}` {
				t.Errorf("ok = %+v", c)
			}
		}
	}

	_, err = repo.GetConnection(ctx, "default", "lost")
	var safe *domain.SafeError
	if !errors.Is(err, domain.ErrValueUnreadable) || !errors.As(err, &safe) || !strings.Contains(safe.ClientMessage(), `"lost"`) {
		t.Errorf("GetConnection(lost) = %v, want a SafeError of class ErrValueUnreadable naming the connection", err)
	}
	// Deleting it, the documented way out, does not need the key.
	if err := repo.DeleteConnection(ctx, "default", "lost"); err != nil {
		t.Errorf("deleting the unreadable connection: %v", err)
	}
}

// A write that leaves extra out (a nil patch field) keeps the ciphertext of an
// extra this server cannot decrypt (#1677): it is the only copy of that
// credential. The API refuses writes to such a connection before they get
// here; this locks the store underneath, so the old key, once added back,
// still opens the original extra.
func TestConnectionPatchKeepsAnUnreadableExtra(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	ctx := context.Background()

	stranger := keyMigrationRepo(t, dsn)
	stranger.Configure(storage.WithCipher(varCipher(t, kmStranger)))
	if err := stranger.SetConnection(ctx, "default", domain.Connection{
		ConnID: "lost", ConnType: "http", Host: "api.example.com", Extra: `{"token":"s3cret"}`,
	}); err != nil {
		t.Fatal(err)
	}

	repo := keyMigrationRepo(t, dsn)
	repo.Configure(storage.WithCipher(varCipher(t, kmNew)))
	host := "new.example.com"
	if err := repo.SetConnectionPatch(ctx, "default", domain.ConnectionPatch{
		ConnID: "lost", ConnType: "http", Host: &host,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := stranger.GetConnection(ctx, "default", "lost")
	if err != nil {
		t.Fatalf("GetConnection with the original key: %v", err)
	}
	if got.Extra != `{"token":"s3cret"}` || got.Host != host {
		t.Errorf("after a host-only patch = %+v, want the original extra kept and the new host", got)
	}
}
