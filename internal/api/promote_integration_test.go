//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// promoteServerAs is the API over a real repository for a caller of tenant.
func promoteServerAs(t *testing.T, repo *storage.Repository, tenant string) *gin.Engine {
	t.Helper()
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: tenant, Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
			TokenTTLSecs:  3600,
		},
		Stores: StoreDeps{
			Dags:        repo,
			Versions:    repo,
			Specs:       repo,
			DagVersions: repo,
			DagSources:  repo,
			Audit:       repo,
		},
	})
}

// TestPromoteRegistersTheSameVersionElsewhereIntegration does over HTTP what
// `dexaflow promote` does: it reads an older version's spec from one tenant
// (standing in for the source control plane) and registers it in another
// with promoted_from. The destination's spec hash is the source's, so the
// version is the same, and its audit entry names the source (#1708).
func TestPromoteRegistersTheSameVersionElsewhereIntegration(t *testing.T) {
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
	dest := fmt.Sprintf("promote_dst_%d", time.Now().UnixNano())
	if _, err := repo.EnsureTenant(ctx, dest, dest, 16, domain.TenantLimitsUpdate{}); err != nil {
		t.Fatalf("creating the destination tenant: %v", err)
	}
	src, dst := promoteServerAs(t, repo, "default"), promoteServerAs(t, repo, dest)
	dagID := fmt.Sprintf("api_promote_%d", time.Now().UnixNano())
	hashes := map[string]string{}
	for _, v := range []string{"v1", "v2"} {
		spec := fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":%q,"image":"reg.example/etl@sha256:%064d",`+
			`"source":"# source of %s\n","schedule":"@daily","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`, dagID, v, len(v), v)
		rec := authGet(src, http.MethodPost, "/api/v2/dags/"+dagID+"/versions", spec)
		if rec.Code != http.StatusCreated {
			t.Fatalf("register %s = %d (%s)", v, rec.Code, rec.Body.String())
		}
		var reg versionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &reg); err != nil {
			t.Fatal(err)
		}
		hashes[v] = reg.SpecHash
	}

	read := authGet(src, http.MethodGet, "/api/v2/dags/"+dagID+"/dagVersions/1/spec", "")
	if read.Code != http.StatusOK {
		t.Fatalf("reading version 1's spec = %d (%s)", read.Code, read.Body.String())
	}
	if rec := authGet(dst, http.MethodGet, "/api/v2/dags/"+dagID+"/dagVersions/1/spec", ""); rec.Code != http.StatusNotFound {
		t.Errorf("another tenant read the spec: %d (%s)", rec.Code, rec.Body.String())
	}
	rec := authGet(dst, http.MethodPost, "/api/v2/dags/"+dagID+"/versions?promoted_from=staging&promoted_from_server=https%3A%2F%2Fstaging.example", read.Body.String())
	if rec.Code != http.StatusCreated {
		t.Fatalf("promoted register = %d (%s)", rec.Code, rec.Body.String())
	}
	var got versionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SpecHash != hashes["v1"] || got.Version != "v1" {
		t.Errorf("promoted register = %+v, want version v1 with the source's hash %s", got, hashes["v1"])
	}
	entries, _, err := repo.ListAuditLogs(ctx, dest, dagID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		PromotedFrom map[string]string `json:"promoted_from"`
	}
	for _, e := range entries {
		if e.Action == "dag.version.register" {
			_ = json.Unmarshal([]byte(e.Extra), &meta)
		}
	}
	if meta.PromotedFrom["target"] != "staging" || meta.PromotedFrom["server"] != "https://staging.example" {
		t.Errorf("destination audit promoted_from = %v, want staging at https://staging.example", meta.PromotedFrom)
	}
}
