//go:build integration

package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestRegisterVersionDryRunAgainstPostgres drives the handler and the real
// repository: a dry run of a new DAG answers 200 and leaves no DAG behind, a
// dry run of a conflicting label gets the register's 409, and a dry run of
// an undeclared connection the register's 400, with the same bodies (#1705).
func TestRegisterVersionDryRunAgainstPostgres(t *testing.T) {
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
	repo := storage.NewRepository(pg)
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
			TokenTTLSecs:  3600,
		},
		Stores: StoreDeps{
			Versions: repo,
		},
	})
	dagID := fmt.Sprintf("api_dry_%d", time.Now().UnixNano())
	path := "/api/v2/dags/" + dagID + "/versions"
	spec := func(task, conn string) string {
		conns := ""
		if conn != "" {
			conns = fmt.Sprintf(`,"connections":[%q]`, conn)
		}
		return fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":"dev","image":"img:dev","tasks":[{"task_id":%q,"type":"python","entrypoint":"dag:a"}]%s}`, dagID, task, conns)
	}

	if rec := authGet(srv, http.MethodPost, path+"/dry_run", spec("a", "")); rec.Code != http.StatusOK {
		t.Fatalf("dry run of a new DAG = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if _, gerr := repo.GetDag(ctx, "default", dagID); gerr == nil {
		t.Fatal("a dry run left the DAG behind")
	}
	if rec := authGet(srv, http.MethodPost, path, spec("a", "")); rec.Code != http.StatusCreated {
		t.Fatalf("register = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	for name, body := range map[string]string{
		"conflict":           spec("b", ""),
		"unknown connection": spec("a", fmt.Sprintf("missing_%d", time.Now().UnixNano())),
	} {
		dry := authGet(srv, http.MethodPost, path+"/dry_run", body)
		real := authGet(srv, http.MethodPost, path, body)
		if real.Code < http.StatusBadRequest {
			t.Fatalf("%s: register = %d, want a refusal", name, real.Code)
		}
		dryBody, realBody := problemWithoutInstance(t, dry.Body.String()), problemWithoutInstance(t, real.Body.String())
		if dry.Code != real.Code || dryBody != realBody {
			t.Errorf("%s: dry run = %d %s, want the register's %d %s", name, dry.Code, dryBody, real.Code, realBody)
		}
	}
	versions, err := repo.ListDagVersions(ctx, "default", dagID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Errorf("versions = %d, want the one register", len(versions))
	}
}
