//go:build integration

package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestRegisterVersionUnmappedSQLSTATEIsOpaqueIntegration is the other half of
// TestRegisterVersionDuplicateReturns409. That test proves the ONE SQLSTATE
// mapConflict translates (23505) comes back clean; this one proves an
// untranslated SQLSTATE does too, driven by a real Postgres against the real
// Repository so the error value is the driver's, not a fixture's.
//
// A NUL byte in the DAG description is the cheapest way to make the server
// itself raise an error mapConflict has never heard of: Postgres refuses it
// with 22021 (invalid byte sequence for encoding "UTF8") on the UpsertDag
// write. Before #961 that reached the tenant as
// "upserting dag: ERROR: ... (SQLSTATE 22021)".
func TestRegisterVersionUnmappedSQLSTATEIsOpaqueIntegration(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)

	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		Versions:      storage.NewRepository(pg),
	})

	dagID := fmt.Sprintf("api_leak_%d", time.Now().UnixNano())
	path := "/api/v2/dags/" + dagID + "/versions"
	// The \u0000 escape survives JSON decoding into the Go string and is
	// rejected by Postgres, not by our own validation - exactly the shape
	// needed: a driver error raised inside a repository write.
	spec := fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":"dev","image":"img:dev",`+
		`"description":"nul\u0000byte","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`, dagID)

	rec := authGet(srv, http.MethodPost, path, spec)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("NUL in description = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
	// Scan what the server composed, not the dag id it echoes back — see
	// leakScanTarget.
	body := leakScanTarget(rec.Body.String(), dagID)
	for _, leak := range append(pgLeaks, "22021", "ERROR:", "encoding") {
		if strings.Contains(body, leak) {
			t.Errorf("500 body leaks raw pg internals (%q): %s", leak, body)
		}
	}
}
