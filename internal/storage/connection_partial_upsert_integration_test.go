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

// TestConnectionPartialUpsertPreservesOmittedFields locks the safe-by-default
// upsert semantics (#881): a write changes only the fields it carries and leaves
// every omitted field intact. The load-bearing case is the password — it is
// write-only (never read back), so a partial `connections set --host newhost`
// that forgot to re-supply it must NOT wipe it, or the next DAG run fails auth
// with no visible cause. Before the COALESCE upsert this test's second write
// nulled the password, extra, login and port.
func TestConnectionPartialUpsertPreservesOmittedFields(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	connID := fmt.Sprintf("uiq_partial_%d", time.Now().UnixNano())
	port := 5432
	if err := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "postgres", Host: "db", Login: "u",
		Password: "s3cr3t", Schema: "public", Port: &port, Extra: `{"sslmode":"require"}`,
		Description: "primary warehouse",
	}); err != nil {
		t.Fatalf("initial SetConnection: %v", err)
	}

	// Partial write: change ONLY the host. Every other field is omitted (zero
	// value), which maps to a NULL param and must be preserved by the upsert.
	if err := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "postgres", Host: "db2",
	}); err != nil {
		t.Fatalf("partial SetConnection: %v", err)
	}

	got, err := repo.GetConnection(ctx, "default", connID)
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	if got.Host != "db2" {
		t.Errorf("host not updated: got %q, want db2", got.Host)
	}
	if got.Login != "u" {
		t.Errorf("login was clobbered: got %q, want u (preserved)", got.Login)
	}
	if got.Port == nil || *got.Port != 5432 {
		t.Errorf("port was clobbered: got %v, want 5432 (preserved)", got.Port)
	}
	if got.Extra != `{"sslmode":"require"}` {
		t.Errorf("extra was clobbered: got %q, want the original (preserved)", got.Extra)
	}
	if got.Description != "primary warehouse" {
		t.Errorf("description was clobbered: got %q (preserved)", got.Description)
	}

	// The password is write-only, so prove its survival through the delivery path
	// (the decrypted AIRFLOW_CONN_<id> URI a task receives), not GetConnection.
	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, ok := uris[connID]
	if !ok {
		t.Fatalf("connection %s missing from delivered URIs", connID)
	}
	if !strings.Contains(uri, "s3cr3t") {
		t.Errorf("password was wiped by the partial upsert: delivered URI %q no longer carries it", uri)
	}
	if !strings.Contains(uri, "db2") {
		t.Errorf("delivered URI did not reflect the updated host: %q", uri)
	}

	// A write that DOES carry a new password replaces it (not a no-op merge).
	if err := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "postgres", Password: "rotated",
	}); err != nil {
		t.Fatalf("password-rotating SetConnection: %v", err)
	}
	uris, err = repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs (after rotate): %v", err)
	}
	if u := uris[connID]; strings.Contains(u, "s3cr3t") || !strings.Contains(u, "rotated") {
		t.Errorf("password was not rotated: %q", u)
	}

	_ = repo.DeleteConnection(ctx, "default", connID)
}
