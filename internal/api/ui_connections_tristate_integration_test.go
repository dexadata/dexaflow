//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
)

// tristateServer wires the real Repository (over the test DB, with a cipher) as
// the connection/variable store behind the actual HTTP handlers, plus a handle
// to the repo for out-of-band assertions on the delivered secret.
func tristateServer(t *testing.T) (srv *gin.Engine, repo *storage.Repository, ctx context.Context) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx = context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	repo = storage.NewRepository(pg)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 23)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(1000, time.Minute),
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		Connections:   repo,
		Variables:     repo,
	}), repo, ctx
}

// TestConnectionMaskedRoundTripFullStack is the #874 end-to-end regression lock:
// it drives the real HTTP handlers against a real Repository and proves that
// GETting a connection (secrets shown as "***") and PATCHing it straight back —
// exactly what the embedded Admin UI does — does NOT overwrite the stored
// password or the secret inside `extra` with the mask. The password survives is
// proven through the delivered AIRFLOW_CONN_<id> URI; the extra secret through
// GetConnection (which returns the decrypted, unredacted extra).
func TestConnectionMaskedRoundTripFullStack(t *testing.T) {
	srv, repo, ctx := tristateServer(t)
	connID := fmt.Sprintf("api_tristate_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	// Create with a real password and a secret token inside extra.
	create := fmt.Sprintf(
		`{"connection_id":%q,"conn_type":"databricks","host":"h","password":"PW-REAL","extra":"{\"token\":\"TOK-REAL\",\"http_path\":\"/sql/1.0/x\"}"}`,
		connID)
	if rec := authGet(srv, http.MethodPost, "/api/v2/connections", create); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}

	// GET as the UI would: the password is absent, the extra token is masked.
	rec := authGet(srv, http.MethodGet, "/api/v2/connections/"+connID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d (%s)", rec.Code, rec.Body.String())
	}
	var dto connectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Extra == nil || !strings.Contains(*dto.Extra, secretMask) {
		t.Fatalf("GET should mask the extra token: %+v", dto.Extra)
	}

	// The UI edits a plain field (http_path) and re-submits the masked extra plus
	// a masked password — the round-trip that #874 is about.
	maskedExtra := *dto.Extra
	maskedExtra = strings.Replace(maskedExtra, "/sql/1.0/x", "/sql/2.0/y", 1)
	patch := map[string]any{"conn_type": "databricks", "password": secretMask, "extra": maskedExtra}
	pb, _ := json.Marshal(patch)
	if rec := authGet(srv, http.MethodPatch, "/api/v2/connections/"+connID, string(pb)); rec.Code != http.StatusOK {
		t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
	}

	// Password must still be the real one (proven via the delivered URI).
	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	if uri := uris[connID]; !strings.Contains(uri, "PW-REAL") {
		t.Errorf("masked password round-trip overwrote the real secret: %q", uri)
	}

	// Extra token must still be the real one, and the plain edit must have landed.
	got, err := repo.GetConnection(ctx, "default", connID)
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	var ex map[string]any
	if err := json.Unmarshal([]byte(got.Extra), &ex); err != nil {
		t.Fatalf("stored extra not json: %v (%q)", err, got.Extra)
	}
	if ex["token"] != "TOK-REAL" {
		t.Errorf("masked extra token overwrote the real secret: %v", ex["token"])
	}
	if ex["http_path"] != "/sql/2.0/y" {
		t.Errorf("the plain extra edit did not land: %v", ex["http_path"])
	}
}

// TestConnectionExplicitClearFullStack proves the other half of the tri-state
// contract end to end: an explicit empty value clears a non-secret field, while
// the omitted (unreadable) password is preserved.
func TestConnectionExplicitClearFullStack(t *testing.T) {
	srv, repo, ctx := tristateServer(t)
	connID := fmt.Sprintf("api_clear_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	create := fmt.Sprintf(
		`{"connection_id":%q,"conn_type":"postgres","host":"h","login":"analytics","password":"PW-REAL"}`,
		connID)
	if rec := authGet(srv, http.MethodPost, "/api/v2/connections", create); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}

	// Blank the login and save (Airflow-UI clear semantics), password omitted.
	if rec := authGet(srv, http.MethodPatch, "/api/v2/connections/"+connID,
		`{"conn_type":"postgres","login":""}`); rec.Code != http.StatusOK {
		t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
	}

	got, err := repo.GetConnection(ctx, "default", connID)
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	if got.Login != "" {
		t.Errorf("explicit empty login did not clear: %q", got.Login)
	}
	tenantUUID, _ := repo.TenantUUID(ctx, "default")
	uris, _ := repo.SecretConnectionURIs(ctx, tenantUUID)
	if uri := uris[connID]; !strings.Contains(uri, "PW-REAL") {
		t.Errorf("clearing login must preserve the omitted password: %q", uri)
	}
}

// TestVariableMaskedRoundTripFullStack is the variable analog of the #874
// lock: a GET of a sensitive-keyed variable masks the value, and writing it
// straight back must not persist the mask over the real value.
func TestVariableMaskedRoundTripFullStack(t *testing.T) {
	srv, repo, ctx := tristateServer(t)
	key := fmt.Sprintf("api_token_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = repo.DeleteVariable(ctx, "default", key) })

	create := fmt.Sprintf(`{"key":%q,"value":"TOK-REAL"}`, key)
	if rec := authGet(srv, http.MethodPost, "/api/v2/variables", create); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	// Write back the masked value the UI GET returns.
	if rec := authGet(srv, http.MethodPatch, "/api/v2/variables/"+key,
		fmt.Sprintf(`{"value":%q}`, secretMask)); rec.Code != http.StatusOK {
		t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
	}
	got, err := repo.GetVariable(ctx, "default", key)
	if err != nil {
		t.Fatalf("GetVariable: %v", err)
	}
	if got.Value != "TOK-REAL" {
		t.Errorf("masked variable round-trip overwrote the real value: %q", got.Value)
	}
}
