//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

// crossTenantFixture is two tenants on one database, each behind its own
// server whose principal belongs to it: the shared-engine shape, where every
// tenant reaches the same handlers and only the session tells them apart.
type crossTenantFixture struct {
	repo    *storage.Repository
	ctx     context.Context
	a, b    *gin.Engine
	tenantA string
	tenantB string
	suffix  string
}

func newCrossTenantFixture(t *testing.T) crossTenantFixture {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: dbURL})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 41)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo := storage.NewRepository(pg, storage.WithCipher(cipher))

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	f := crossTenantFixture{repo: repo, ctx: ctx, tenantA: "xt_a_" + suffix, tenantB: "xt_b_" + suffix, suffix: suffix}
	for _, name := range []string{f.tenantA, f.tenantB} {
		if _, err = pg.Pool.Exec(ctx, `INSERT INTO tenants (name, display_name) VALUES ($1, $1)`, name); err != nil {
			t.Fatalf("inserting tenant %s: %v", name, err)
		}
	}
	server := func(tenant string) *gin.Engine {
		return newTestServer(Dependencies{
			Observability: ObservabilityDeps{
				Logger: discardLogger(),
			},
			Server: ServerDeps{
				CORSOrigins: []string{"*"},
				Pools:       true, // pools are mounted on the cluster edition only
			},
			Auth: AuthDeps{
				Authenticator: &fakeAuthn{user: &auth.User{ID: "u-" + tenant, TenantID: tenant, Roles: []string{"admin"}}},
				RateLimiter:   auth.NewRateLimiter(1000, time.Minute),
				TokenTTLSecs:  3600,
			},
			Stores: StoreDeps{
				Dags:         repo,
				DagRuns:      repo,
				Tasks:        repo,
				Versions:     repo,
				Specs:        repo,
				DagSources:   repo,
				AuditLog:     repo,
				Variables:    repo,
				Connections:  repo,
				Pools:        repo,
				ImportErrors: repo,
			},
		})
	}
	f.a = server(f.tenantA)
	f.b = server(f.tenantB)
	return f
}

// xtExpect sends one request and fails the test unless it answers want.
func xtExpect(t *testing.T, srv *gin.Engine, method, path, body string, want int) string {
	t.Helper()
	rec := authGet(srv, method, path, body)
	if rec.Code != want {
		t.Fatalf("%s %s = %d, want %d (%s)", method, path, rec.Code, want, rec.Body.String())
	}
	return rec.Body.String()
}

// xtField reads one top-level string field of a JSON object body.
func xtField(t *testing.T, body, name string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	s, _ := m[name].(string)
	return s
}

// crossTenantCRUD is one keyed resource of the Admin API: B owns key, and A
// tries every verb on it.
type crossTenantCRUD struct {
	name       string
	collection string // e.g. /api/v2/variables
	create     func(key, marker string) string
	patch      string // a PATCH body that would change the marker
	marker     func(t *testing.T, body string) string
}

// TestCrossTenantAdminCRUDIntegration drives the real handlers over the real
// Repository with two tenants. For variables, connections and pools, tenant A
// holding B's key can neither read, list, change nor delete B's object, and a
// POST of the same key creates A's own object, leaving B's untouched.
func TestCrossTenantAdminCRUDIntegration(t *testing.T) {
	f := newCrossTenantFixture(t)
	cases := []crossTenantCRUD{
		{
			name:       "variable",
			collection: "/api/v2/variables",
			create:     func(key, v string) string { return fmt.Sprintf(`{"key":%q,"value":%q}`, key, v) },
			patch:      `{"value":"changed-by-a"}`,
			marker:     func(t *testing.T, body string) string { return xtField(t, body, "value") },
		},
		{
			name:       "connection",
			collection: "/api/v2/connections",
			create: func(key, v string) string {
				return fmt.Sprintf(`{"connection_id":%q,"conn_type":"http","host":%q}`, key, v)
			},
			patch:  `{"conn_type":"http","host":"changed-by-a"}`,
			marker: func(t *testing.T, body string) string { return xtField(t, body, "host") },
		},
		{
			name:       "pool",
			collection: "/api/v2/pools",
			create:     func(key, v string) string { return fmt.Sprintf(`{"name":%q,"slots":3,"description":%q}`, key, v) },
			patch:      `{"slots":99,"description":"changed-by-a"}`,
			marker:     func(t *testing.T, body string) string { return xtField(t, body, "description") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange: B owns the key.
			key := "xt_" + tc.name + "_" + f.suffix
			item := tc.collection + "/" + url.PathEscape(key)
			xtExpect(t, f.b, http.MethodPost, tc.collection, tc.create(key, "owned-by-b"), http.StatusCreated)

			// Act and Assert: every verb A sends on B's key misses.
			xtExpect(t, f.a, http.MethodGet, item, "", http.StatusNotFound)
			if list := xtExpect(t, f.a, http.MethodGet, tc.collection+"?limit=1000", "", http.StatusOK); strings.Contains(list, key) {
				t.Errorf("A's %s list names B's key %s: %s", tc.name, key, list)
			}
			xtExpect(t, f.a, http.MethodPatch, item, tc.patch, http.StatusNotFound)
			xtExpect(t, f.a, http.MethodDelete, item, "", http.StatusNotFound)
			if got := tc.marker(t, xtExpect(t, f.b, http.MethodGet, item, "", http.StatusOK)); got != "owned-by-b" {
				t.Fatalf("B's %s after A's writes = %q, want owned-by-b", tc.name, got)
			}

			// The same key posted by A is A's own object.
			xtExpect(t, f.a, http.MethodPost, tc.collection, tc.create(key, "owned-by-a"), http.StatusCreated)
			if got := tc.marker(t, xtExpect(t, f.a, http.MethodGet, item, "", http.StatusOK)); got != "owned-by-a" {
				t.Errorf("A's own %s = %q, want owned-by-a", tc.name, got)
			}
			if got := tc.marker(t, xtExpect(t, f.b, http.MethodGet, item, "", http.StatusOK)); got != "owned-by-b" {
				t.Errorf("B's %s after A's POST of the same key = %q, want owned-by-b", tc.name, got)
			}
		})
	}
}

// TestCrossTenantDagSourceImportErrorsAndEventLogsIntegration covers the reads
// keyed by a DAG or a file name: B's DAG source, import errors and event log
// stay out of A's answers, and A's clear of B's import error leaves it.
func TestCrossTenantDagSourceImportErrorsAndEventLogsIntegration(t *testing.T) {
	// Arrange: B registers a DAG, reports an import error and has an audit entry.
	f := newCrossTenantFixture(t)
	dagID := "xt_dag_" + f.suffix
	spec := fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":"v1","image":"img:v1",`+
		`"tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`, dagID)
	xtExpect(t, f.b, http.MethodPost, "/api/v2/dags/"+dagID+"/versions", spec, http.StatusCreated)
	file := "dags/xt_broken_" + f.suffix + ".py"
	xtExpect(t, f.b, http.MethodPut, "/api/v2/importErrors",
		fmt.Sprintf(`{"filename":%q,"stack_trace":"boom-in-b"}`, file), http.StatusNoContent)
	if err := f.repo.RecordTaskActionAudit(f.ctx, f.tenantB, "u-b", "clear", dagID, "r1", "a", 1); err != nil {
		t.Fatalf("recording B's audit entry: %v", err)
	}
	xtExpect(t, f.b, http.MethodGet, "/api/v2/dagSources/"+dagID, "", http.StatusOK)
	if body := xtExpect(t, f.b, http.MethodGet, "/api/v2/eventLogs?limit=1000", "", http.StatusOK); !strings.Contains(body, dagID) {
		t.Fatalf("B's own event log misses its entry for %s: %s", dagID, body)
	}

	// Act and Assert: A reads none of it, and A's clear changes nothing for B.
	xtExpect(t, f.a, http.MethodGet, "/api/v2/dagSources/"+dagID, "", http.StatusNotFound)
	xtExpect(t, f.a, http.MethodGet, "/api/v2/dags/"+dagID, "", http.StatusNotFound)
	if body := xtExpect(t, f.a, http.MethodGet, "/api/v2/importErrors", "", http.StatusOK); strings.Contains(body, file) {
		t.Errorf("A's import errors name B's file: %s", body)
	}
	if body := xtExpect(t, f.a, http.MethodGet, "/api/v2/eventLogs?limit=1000", "", http.StatusOK); strings.Contains(body, dagID) {
		t.Errorf("A's event log names B's DAG: %s", body)
	}
	xtExpect(t, f.a, http.MethodDelete, "/api/v2/importErrors?filename="+url.QueryEscape(file), "", http.StatusNoContent)
	if body := xtExpect(t, f.b, http.MethodGet, "/api/v2/importErrors", "", http.StatusOK); !strings.Contains(body, file) {
		t.Errorf("B's import error is gone after A's clear: %s", body)
	}
}
