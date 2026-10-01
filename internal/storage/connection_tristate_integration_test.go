//go:build integration

package storage_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
)

// ptr is a tiny helper for building tri-state patch fields in these tests.
func ptr(s string) *string { return &s }

// tristateCipher installs a deterministic AES-GCM cipher on the repo so the
// connection write/read paths encrypt and decrypt the password/extra at rest.
func tristateCipher(t *testing.T) secrets.Cipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 11)
	}
	c, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestConnectionPatchTriState locks the #887 storage primitive that the API
// write path rests on: SetConnectionPatch treats a nil field as "preserve"
// (NULL param → COALESCE), a non-nil "" as "clear", and a value as "set". The
// password is write-only, so its preservation is proven through the delivered
// AIRFLOW_CONN_<id> URI (the credential a task actually receives), not through
// GetConnection. This is the mechanism a masked ("***") password relies on — the
// handler maps the mask to a nil Password, exercised here as the nil case.
func TestConnectionPatchTriState(t *testing.T) {
	repo, _, ctx := openRepo(t)
	repo.SetCipher(tristateCipher(t))

	connID := fmt.Sprintf("tristate_%d", time.Now().UnixNano())
	port := 5432
	// Seed a full connection through the patch path (every field present).
	if err := repo.SetConnectionPatch(ctx, "default", domain.ConnectionPatch{
		ConnID: connID, ConnType: "postgres",
		Host: ptr("db"), Login: ptr("analytics"), Password: ptr("s3cr3t"),
		Schema: ptr("public"), Port: &port, Extra: ptr(`{"sslmode":"require"}`),
		Description: ptr("primary warehouse"),
	}); err != nil {
		t.Fatalf("seed SetConnectionPatch: %v", err)
	}

	tenantUUID, terr := repo.TenantUUID(ctx, "default")
	if terr != nil {
		t.Fatalf("TenantUUID: %v", terr)
	}
	uriHas := func(want string) {
		t.Helper()
		uris, uerr := repo.SecretConnectionURIs(ctx, tenantUUID)
		if uerr != nil {
			t.Fatalf("SecretConnectionURIs: %v", uerr)
		}
		if uri := uris[connID]; !strings.Contains(uri, want) {
			t.Errorf("delivered URI %q does not carry %q", uri, want)
		}
	}

	// (1) Absent field preserves — including the write-only password. This is the
	// #881 lock re-expressed on the patch path: only the host is present.
	if err := repo.SetConnectionPatch(ctx, "default", domain.ConnectionPatch{
		ConnID: connID, ConnType: "postgres", Host: ptr("db2"),
	}); err != nil {
		t.Fatalf("partial patch: %v", err)
	}
	got, err := repo.GetConnection(ctx, "default", connID)
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	if got.Host != "db2" {
		t.Errorf("host not updated: %q", got.Host)
	}
	if got.Login != "analytics" || got.Extra != `{"sslmode":"require"}` ||
		got.Port == nil || *got.Port != 5432 || got.Description != "primary warehouse" {
		t.Errorf("omitted fields were clobbered: %+v", got)
	}
	uriHas("s3cr3t") // password preserved across the partial write
	uriHas("db2")    // host edit reflected

	// (2) A masked password (nil Password, as the handler maps "***") preserves
	// the stored secret, while a co-submitted host edit still lands (#874).
	if err := repo.SetConnectionPatch(ctx, "default", domain.ConnectionPatch{
		ConnID: connID, ConnType: "postgres", Host: ptr("db3"), Password: nil,
	}); err != nil {
		t.Fatalf("masked-password patch: %v", err)
	}
	uriHas("s3cr3t")
	uriHas("db3")

	// (3) Explicit "" clears a non-secret field (login), leaving the password.
	if err := repo.SetConnectionPatch(ctx, "default", domain.ConnectionPatch{
		ConnID: connID, ConnType: "postgres", Login: ptr(""),
	}); err != nil {
		t.Fatalf("clear-login patch: %v", err)
	}
	if got, _ := repo.GetConnection(ctx, "default", connID); got.Login != "" {
		t.Errorf("explicit empty login did not clear: %q", got.Login)
	}
	uriHas("s3cr3t") // clearing login must not disturb the password

	// (4) A write that DOES carry a new password rotates it.
	if err := repo.SetConnectionPatch(ctx, "default", domain.ConnectionPatch{
		ConnID: connID, ConnType: "postgres", Password: ptr("rotated"),
	}); err != nil {
		t.Fatalf("rotate-password patch: %v", err)
	}
	uris, _ := repo.SecretConnectionURIs(ctx, tenantUUID)
	if u := uris[connID]; strings.Contains(u, "s3cr3t") || !strings.Contains(u, "rotated") {
		t.Errorf("password not rotated: %q", u)
	}

	_ = repo.DeleteConnection(ctx, "default", connID)
}

// TestVariablePatchTriState locks the #887 storage primitives for variables that
// live at the SQL layer: SetVariablePatch overwrites value with the resolved
// value (the API handler resolves an omitted/masked value to the stored value,
// since the NOT NULL `value` column cannot preserve via COALESCE), an explicit
// "" clears it, and a nil Description preserves the stored description. The
// masked-value round-trip (handler-resolved preserve) is proven end to end in
// the api package's TestVariableMaskedRoundTripFullStack.
func TestVariablePatchTriState(t *testing.T) {
	repo, _, ctx := openRepo(t)

	key := fmt.Sprintf("tristate_var_%d", time.Now().UnixNano())
	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: key, Value: "real", Description: "prod"}); err != nil {
		t.Fatalf("seed SetVariable: %v", err)
	}

	// A resolved value overwrites; a nil Description preserves the stored one.
	if err := repo.SetVariablePatch(ctx, "default", domain.VariablePatch{Key: key, Value: ptr("real"), Description: nil}); err != nil {
		t.Fatalf("preserve-description patch: %v", err)
	}
	got, err := repo.GetVariable(ctx, "default", key)
	if err != nil {
		t.Fatalf("GetVariable: %v", err)
	}
	if got.Value != "real" {
		t.Errorf("value not written: %q", got.Value)
	}
	if got.Description != "prod" {
		t.Errorf("nil description overwrote the stored description: %q", got.Description)
	}

	// Explicit "" clears the value.
	if err := repo.SetVariablePatch(ctx, "default", domain.VariablePatch{Key: key, Value: ptr("")}); err != nil {
		t.Fatalf("clear-value patch: %v", err)
	}
	if got, _ := repo.GetVariable(ctx, "default", key); got.Value != "" {
		t.Errorf("explicit empty value did not clear: %q", got.Value)
	}

	_ = repo.DeleteVariable(ctx, "default", key)
}
