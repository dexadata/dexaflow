//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// bearerRequest sends one request carrying token as its bearer.
func bearerRequest(srv *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequestWithContext(context.Background(), method, path, http.NoBody)
	} else {
		r = httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

// TestDeactivatedUserLosesRenewalAndAccessIntegration is the end to end check
// #960 asked for, over the real store and the real JWT authenticator: a user
// signs in, renews, is deactivated by an admin through PATCH
// /api/v2/users/{id}, and from then on both the renewal and the token the user
// already holds are refused; the audit log records who deactivated whom; and
// reactivating restores access with a fresh sign-in.
func TestDeactivatedUserLosesRenewalAndAccessIntegration(t *testing.T) {
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
	repo := storage.NewRepository(pg)
	tenant := fmt.Sprintf("deact_%d", time.Now().UnixNano())
	if _, err = pg.Pool.Exec(ctx, `INSERT INTO tenants (name, display_name) VALUES ($1, $1)`, tenant); err != nil {
		t.Fatalf("inserting tenant: %v", err)
	}
	if _, err = pg.Pool.Exec(ctx, `INSERT INTO roles (tenant_id, name) SELECT id, 'viewer' FROM tenants WHERE name = $1`, tenant); err != nil {
		t.Fatalf("inserting role: %v", err)
	}
	admin, err := repo.CreateUser(ctx, tenant, "admin@"+tenant+".test", "admin-password", nil)
	if err != nil {
		t.Fatalf("creating admin: %v", err)
	}
	user, err := repo.CreateUser(ctx, tenant, "bob@"+tenant+".test", "bob-password", []string{"viewer"})
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}

	jwt := auth.NewJWTAuthenticator(repo, "integration-secret-0123456789abcdef", time.Hour)
	real := newTestServer(Dependencies{
		Observability: ObservabilityDeps{Logger: discardLogger()},
		Server:        ServerDeps{CORSOrigins: []string{"*"}},
		Auth: AuthDeps{
			Authenticator:        jwt,
			TokenRenewer:         jwt,
			RateLimiter:          auth.NewRateLimiter(1000, time.Minute),
			TokenTTLSecs:         3600,
			TokenMaxLifetimeSecs: 86400,
		},
		Stores: StoreDeps{Users: repo, AuditLog: repo},
	})
	adminSrv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{Logger: discardLogger()},
		Server:        ServerDeps{CORSOrigins: []string{"*"}},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: admin.ID, TenantID: tenant, Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(1000, time.Minute),
		},
		Stores: StoreDeps{Users: repo, AuditLog: repo},
	})

	token, err := jwt.IssueToken(ctx, auth.Credentials{Tenant: tenant, Username: user.Email, Password: "bob-password"})
	if err != nil {
		t.Fatalf("signing in: %v", err)
	}
	if rec := bearerRequest(real, http.MethodGet, "/api/v2/plugins", token, ""); rec.Code != http.StatusOK {
		t.Fatalf("an active user's request = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := bearerRequest(real, http.MethodPost, "/api/v2/auth/token/renew", token, ""); rec.Code != http.StatusOK {
		t.Fatalf("an active user's renewal = %d (%s)", rec.Code, rec.Body.String())
	}

	rec := bearerRequest(adminSrv, http.MethodPatch, "/api/v2/users/"+user.ID, "admin", `{"is_active":false}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"is_active":false`) {
		t.Fatalf("deactivating = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := bearerRequest(real, http.MethodPost, "/api/v2/auth/token/renew", token, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("a deactivated user's renewal = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
	if rec := bearerRequest(real, http.MethodGet, "/api/v2/plugins", token, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("a deactivated user's existing token = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
	if _, err := jwt.IssueToken(ctx, auth.Credentials{Tenant: tenant, Username: user.Email, Password: "bob-password"}); err == nil {
		t.Error("a deactivated user signed in again")
	}

	var action, actor, resource string
	if err := pg.Pool.QueryRow(ctx, `
		SELECT a.action, a.user_id::text, a.resource_id
		FROM audit_log a JOIN tenants t ON t.id = a.tenant_id
		WHERE t.name = $1 AND a.resource_type = 'user' AND a.action LIKE 'user.%activate'`, tenant).Scan(&action, &actor, &resource); err != nil {
		t.Fatalf("reading the audit entry: %v", err)
	}
	if action != "user.deactivate" || actor != admin.ID || resource != user.ID {
		t.Errorf("audit entry = %s by %s on %s, want user.deactivate by %s on %s", action, actor, resource, admin.ID, user.ID)
	}

	// Another tenant's admin cannot reach the user at all.
	other := newTestServer(Dependencies{
		Observability: ObservabilityDeps{Logger: discardLogger()},
		Server:        ServerDeps{CORSOrigins: []string{"*"}},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: admin.ID, TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(1000, time.Minute),
		},
		Stores: StoreDeps{Users: repo, AuditLog: repo},
	})
	if rec := bearerRequest(other, http.MethodPatch, "/api/v2/users/"+user.ID, "admin", `{"is_active":true}`); rec.Code != http.StatusNotFound {
		t.Errorf("another tenant's admin reactivating = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}

	rec = bearerRequest(adminSrv, http.MethodPatch, "/api/v2/users/"+user.ID, "admin", `{"is_active":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivating = %d (%s)", rec.Code, rec.Body.String())
	}
	var dto userDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil || !dto.IsActive || len(dto.Roles) != 1 || dto.Roles[0] != "viewer" {
		t.Errorf("reactivated user = %+v (%v), want active with its viewer role", dto, err)
	}
	fresh, err := jwt.IssueToken(ctx, auth.Credentials{Tenant: tenant, Username: user.Email, Password: "bob-password"})
	if err != nil {
		t.Fatalf("signing in after reactivation: %v", err)
	}
	if rec := bearerRequest(real, http.MethodGet, "/api/v2/plugins", fresh, ""); rec.Code != http.StatusOK {
		t.Errorf("a reactivated user's request = %d (%s)", rec.Code, rec.Body.String())
	}
}
