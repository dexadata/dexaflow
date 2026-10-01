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

// TestRegisterVersionDuplicateReturns409 drives the full handler + real
// Repository against Postgres: pushing the same version string with different
// content collides on dag_versions_unique. The response must be 409, and the
// body must not leak the raw pg SQLSTATE or the constraint name. Before #746 the
// repository returned the raw 23505, which handleRepoError mapped to 500 with the
// constraint name in the detail.
func TestRegisterVersionDuplicateReturns409(t *testing.T) {
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

	dagID := fmt.Sprintf("api_dup_%d", time.Now().UnixNano())
	path := "/api/v2/dags/" + dagID + "/versions"
	first := fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":"dev","image":"img:dev","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`, dagID)
	// Same dag_version "dev", different task -> different spec hash -> bypasses dedup.
	second := fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":"dev","image":"img:dev","tasks":[{"task_id":"b","type":"python","entrypoint":"dag:b"}]}`, dagID)

	if rec := authGet(srv, http.MethodPost, path, first); rec.Code != http.StatusCreated {
		t.Fatalf("first push = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}

	rec := authGet(srv, http.MethodPost, path, second)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate version push = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	// Scan what the server composed, not the dag id it echoes back — see
	// leakScanTarget.
	body := leakScanTarget(rec.Body.String(), dagID)
	for _, leak := range pgLeaks {
		if strings.Contains(body, leak) {
			t.Errorf("409 body leaks raw pg internals (%q): %s", leak, body)
		}
	}
}
