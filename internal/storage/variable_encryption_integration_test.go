//go:build integration

package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
)

func varCipher(t *testing.T, key string) secrets.StaleReader {
	t.Helper()
	c, err := secrets.NewAESGCM([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return secrets.WithFallback(c)
}

// storedVariable reads the value column exactly as Postgres holds it.
func storedVariable(t *testing.T, conn *pgx.Conn, key string) string {
	t.Helper()
	var v string
	if err := conn.QueryRow(context.Background(),
		"SELECT value FROM variables v JOIN tenants t ON t.id = v.tenant_id WHERE t.name = 'default' AND v.key = $1", key).Scan(&v); err != nil {
		t.Fatalf("reading %s: %v", key, err)
	}
	return v
}

// #507: with variable_encryption on, the value is sealed with the secret key
// at rest, and every reader (API, delivery to task pods) sees the plaintext.
func TestVariableEncryptionOnSealsAtRest(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	ctx := context.Background()
	repo.Configure(storage.WithCipher(varCipher(t, kmNew)))
	repo.Configure(storage.WithVariableEncryption(storage.VariableEncryptionOn))

	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: "stripe_sk", Value: "sk_live_1"}); err != nil {
		t.Fatal(err)
	}
	v := "postgres://u:p@db/x"
	if err := repo.SetVariablePatch(ctx, "default", domain.VariablePatch{Key: "db_dsn", Value: &v}); err != nil {
		t.Fatal(err)
	}
	for key, plain := range map[string]string{"stripe_sk": "sk_live_1", "db_dsn": v} {
		raw := storedVariable(t, conn, key)
		if !strings.HasPrefix(raw, storage.VariableCiphertextPrefix) || strings.Contains(raw, plain) {
			t.Errorf("%s is not sealed at rest: %q", key, raw)
		}
		got, err := repo.GetVariable(ctx, "default", key)
		if err != nil || got.Value != plain || !got.Encrypted {
			t.Errorf("GetVariable(%s) = %+v, %v", key, got, err)
		}
	}
	list, _, err := repo.ListVariables(ctx, "default", 10, 0)
	if err != nil || len(list) != 2 || list[0].Value != v || list[1].Value != "sk_live_1" {
		t.Fatalf("ListVariables = %+v, %v", list, err)
	}
	tid, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	all, err := repo.SecretVariables(ctx, tid)
	if err != nil || all["stripe_sk"] != "sk_live_1" || all["db_dsn"] != v {
		t.Fatalf("SecretVariables = %v, %v", all, err)
	}
	scoped, err := repo.SecretVariablesScoped(ctx, tid, []string{"stripe_sk"})
	if err != nil || len(scoped) != 1 || scoped["stripe_sk"] != "sk_live_1" {
		t.Fatalf("SecretVariablesScoped = %v, %v", scoped, err)
	}
}

// The upgrade path: rows a v0.5.3 server wrote stay readable as they are, and
// the "on" sweep seals them in place. The "decrypt" sweep is the way back, the
// step before a rollback to a release that cannot read ciphertext.
func TestVariableEncryptionSweepBothWays(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	ctx := context.Background()
	repo.Configure(storage.WithCipher(varCipher(t, kmNew)))

	// Off (the default): the stored form is what v0.5.3 writes.
	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: "legacy", Value: "plain"}); err != nil {
		t.Fatal(err)
	}
	if raw := storedVariable(t, conn, "legacy"); raw != "plain" {
		t.Fatalf("off mode changed the stored form: %q", raw)
	}

	repo.Configure(storage.WithVariableEncryption(storage.VariableEncryptionOn))
	if got, err := repo.GetVariable(ctx, "default", "legacy"); err != nil || got.Value != "plain" || got.Encrypted {
		t.Fatalf("a legacy row must read as is: %+v, %v", got, err)
	}
	res, err := repo.ConvergeVariableEncryption(ctx)
	if err != nil || res.Migrated != 1 || !res.Clean() {
		t.Fatalf("on sweep = %+v, %v", res, err)
	}
	if raw := storedVariable(t, conn, "legacy"); !strings.HasPrefix(raw, storage.VariableCiphertextPrefix) {
		t.Fatalf("the on sweep did not seal the legacy row: %q", raw)
	}
	if res, err = repo.ConvergeVariableEncryption(ctx); err != nil || res.Migrated != 0 {
		t.Fatalf("a second on sweep must be a no-op: %+v, %v", res, err)
	}

	repo.Configure(storage.WithVariableEncryption(storage.VariableEncryptionDecrypt))
	if res, err = repo.ConvergeVariableEncryption(ctx); err != nil || res.Migrated != 1 || !res.Clean() {
		t.Fatalf("decrypt sweep = %+v, %v", res, err)
	}
	if raw := storedVariable(t, conn, "legacy"); raw != "plain" {
		t.Fatalf("the decrypt sweep must restore the v0.5.3 form, got %q", raw)
	}
	// Writes in decrypt mode stay plaintext, so nothing new needs a key.
	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: "later", Value: "x"}); err != nil {
		t.Fatal(err)
	}
	if raw := storedVariable(t, conn, "later"); raw != "x" {
		t.Fatalf("decrypt mode wrote %q", raw)
	}
}

// Off mode never touches existing ciphertext: switching back to the default
// leaves sealed rows sealed and readable.
func TestVariableEncryptionOffSweepIsANoop(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	ctx := context.Background()
	repo.Configure(storage.WithCipher(varCipher(t, kmNew)))
	repo.Configure(storage.WithVariableEncryption(storage.VariableEncryptionOn))
	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: "k", Value: "v"}); err != nil {
		t.Fatal(err)
	}
	sealed := storedVariable(t, conn, "k")
	repo.Configure(storage.WithVariableEncryption(storage.VariableEncryptionOff))
	if res, err := repo.ConvergeVariableEncryption(ctx); err != nil || res.Migrated != 0 {
		t.Fatalf("off sweep = %+v, %v", res, err)
	}
	if raw := storedVariable(t, conn, "k"); raw != sealed {
		t.Fatal("off mode rewrote a sealed row")
	}
	if got, err := repo.GetVariable(ctx, "default", "k"); err != nil || got.Value != "v" {
		t.Fatalf("GetVariable = %+v, %v", got, err)
	}
}

// One value no configured key opens must not blind the tenant's delivery, the
// same rule connections follow; the sweeps leave it untouched and report it.
func TestVariableEncryptionUnreadableValue(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	ctx := context.Background()

	stranger := keyMigrationRepo(t, dsn)
	stranger.Configure(storage.WithCipher(varCipher(t, kmStranger)))
	stranger.Configure(storage.WithVariableEncryption(storage.VariableEncryptionOn))
	if err := stranger.SetVariable(ctx, "default", domain.Variable{Key: "lost", Value: "x"}); err != nil {
		t.Fatal(err)
	}
	lost := storedVariable(t, conn, "lost")

	repo.Configure(storage.WithCipher(varCipher(t, kmNew)))
	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: "ok", Value: "y"}); err != nil {
		t.Fatal(err)
	}
	tid, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	all, err := repo.SecretVariables(ctx, tid)
	if err != nil || all["ok"] != "y" {
		t.Fatalf("SecretVariables = %v, %v", all, err)
	}
	if _, present := all["lost"]; present {
		t.Error("an undecryptable variable must be skipped, never delivered as ciphertext")
	}
	// The API list keeps the tenant's other variables and marks the bad one,
	// instead of failing the whole page.
	listed, total, err := repo.ListVariables(ctx, "default", 100, 0)
	if err != nil || total != 2 || len(listed) != 2 {
		t.Fatalf("ListVariables = %+v, %d, %v; want both variables", listed, total, err)
	}
	for _, v := range listed {
		switch v.Key {
		case "lost":
			if !v.Unreadable || !v.Encrypted || v.Value != "" {
				t.Errorf("lost = %+v, want unreadable, encrypted, no value", v)
			}
		case "ok":
			if v.Unreadable || v.Value != "y" {
				t.Errorf("ok = %+v", v)
			}
		}
	}
	// Reading it alone is an error a caller can recognize, with a message that
	// says what happened.
	_, err = repo.GetVariable(ctx, "default", "lost")
	var safe *domain.SafeError
	if !errors.Is(err, domain.ErrValueUnreadable) || !errors.As(err, &safe) || !strings.Contains(safe.ClientMessage(), `"lost"`) {
		t.Errorf("GetVariable(lost) = %v, want a SafeError of class ErrValueUnreadable naming the key", err)
	}
	repo.Configure(storage.WithVariableEncryption(storage.VariableEncryptionDecrypt))
	res, err := repo.ConvergeVariableEncryption(ctx)
	if err == nil || res.Unreadable != 1 {
		t.Fatalf("decrypt sweep must report the unreadable row: %+v, %v", res, err)
	}
	if raw := storedVariable(t, conn, "lost"); raw != lost {
		t.Fatal("the sweep rewrote a value it could not open")
	}
}

// Sealed variables are part of the encrypted-column registry, so the Pro boot
// sweep and the Lite key migration move them with the connections. A
// plaintext variable is not a secret value of the registry.
func TestVariableCiphertextFollowsKeyRotation(t *testing.T) {
	dsn := freshKeyMigrationDB(t)
	repo := keyMigrationRepo(t, dsn)
	conn := keyMigrationConn(t, dsn)
	ctx := context.Background()
	old, fresh := varCipher(t, kmPublished), varCipher(t, kmNew)

	repo.Configure(storage.WithCipher(old))
	repo.Configure(storage.WithVariableEncryption(storage.VariableEncryptionOn))
	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: "a", Value: "1"}); err != nil {
		t.Fatal(err)
	}
	repo.Configure(storage.WithVariableEncryption(storage.VariableEncryptionOff))
	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: "plain", Value: "2"}); err != nil {
		t.Fatal(err)
	}
	vals, err := storage.ReadSecretValues(ctx, conn)
	if err != nil || len(vals) != 1 || vals[0].Label != "a" {
		t.Fatalf("ReadSecretValues = %+v, %v", vals, err)
	}

	// Pro: the boot sweep moves it onto the first key.
	oldKey, _ := secrets.NewAESGCM([]byte(kmPublished))
	freshKey, _ := secrets.NewAESGCM([]byte(kmNew))
	repo.Configure(storage.WithCipher(secrets.WithFallback(freshKey, oldKey)))
	rres, err := repo.ReencryptSecrets(ctx)
	if err != nil || rres.Migrated != 1 {
		t.Fatalf("ReencryptSecrets = %+v, %v", rres, err)
	}
	repo.Configure(storage.WithCipher(fresh))
	if got, gerr := repo.GetVariable(ctx, "default", "a"); gerr != nil || got.Value != "1" {
		t.Fatalf("after the boot sweep: %+v, %v", got, gerr)
	}

	// Lite: migrate-key moves it back onto another key, verified.
	mres, err := storage.MigrateSecretValues(ctx, conn, oldKey, []secrets.Cipher{freshKey}, storage.KeyMigrationHooks{})
	if err != nil || mres.Migrated != 1 || mres.Verified != 1 {
		t.Fatalf("MigrateSecretValues = %+v, %v", mres, err)
	}
	repo.Configure(storage.WithCipher(old))
	if got, gerr := repo.GetVariable(ctx, "default", "a"); gerr != nil || got.Value != "1" {
		t.Fatalf("after migrate-key: %+v, %v", got, gerr)
	}
	if raw := storedVariable(t, conn, "plain"); raw != "2" {
		t.Fatalf("a key migration touched a plaintext variable: %q", raw)
	}
}
