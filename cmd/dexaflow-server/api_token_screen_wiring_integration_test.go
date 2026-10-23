//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestAPITokenScreenStaysInItsTenantAgainstPostgres drives the token screen's
// browser path (#1886) through the server the boot path builds: the session
// cookie, the screen, its menu entry, and the list and revoke it calls. An
// account manager of one tenant neither lists nor revokes another tenant's
// token, the list carries metadata and never the token or its hash, and a
// revoke from the screen is audited and stops the token.
func TestAPITokenScreenStaysInItsTenantAgainstPostgres(t *testing.T) {
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
	stamp := time.Now().UnixNano()
	other := fmt.Sprintf("screen-%d", stamp)
	if _, err := repo.EnsureTenant(ctx, other, other, 0, domain.TenantLimitsUpdate{}); err != nil {
		t.Fatalf("EnsureTenant: %v", err)
	}
	const password = "screen-password-1"
	emailA := fmt.Sprintf("a_%d@example.com", stamp)
	emailB := fmt.Sprintf("b_%d@example.com", stamp)
	if _, err := repo.CreateUser(ctx, "default", emailA, password, []string{"admin"}); err != nil {
		t.Fatalf("CreateUser A: %v", err)
	}
	if _, err := repo.CreateUser(ctx, other, emailB, password, []string{"admin"}); err != nil {
		t.Fatalf("CreateUser B: %v", err)
	}

	cfg := &config.ServerConfig{}
	cfg.Auth.JWT.Secret = "screen-wiring-secret"
	cfg.Auth.JWT.TokenTTLSeconds = 3600
	reg := prometheus.NewRegistry()
	tel := &observability.Telemetry{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  observability.NewMetrics(reg),
		Registry: reg,
		Tracer:   noop.NewTracerProvider().Tracer("test"),
	}
	authn := auth.NewJWTAuthenticator(repo, cfg.Auth.JWT.Secret, time.Hour)
	apiSrv, buildErr := buildAPIServer(cfg, tel, authn, pg, repo, nil, nil, nil, nil, api.ExecutorInfo{}, nil, nil, nil)
	if buildErr != nil {
		t.Fatalf("buildAPIServer: %v", buildErr)
	}
	handler := apiSrv.Handler

	// call sends a request as the browser does: the session in the cookie.
	call := func(method, path, cookie, bearer, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "_token", Value: cookie})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	login := func(tenant, email string) string {
		rec := call(http.MethodPost, "/auth/token", "", "", fmt.Sprintf(`{"username":%q,"password":%q,"tenant":%q}`, email, password, tenant))
		var s struct {
			AccessToken string `json:"access_token"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &s) != nil || s.AccessToken == "" {
			t.Fatalf("login %s/%s = %d %s", tenant, email, rec.Code, rec.Body.String())
		}
		return s.AccessToken
	}
	sessionA, sessionB := login("default", emailA), login(other, emailB)

	created := call(http.MethodPost, "/api/v2/apiTokens", "", sessionA, `{"name":"ci-screen","scope":"deploy","expires_in_seconds":86400}`)
	var tok struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &tok) != nil || !auth.IsAPIToken(tok.Token) {
		t.Fatalf("create token = %d %s", created.Code, created.Body.String())
	}

	if rec := call(http.MethodGet, "/dexaflow/api-tokens", sessionA, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "API tokens") {
		t.Errorf("token screen with a session = %d, want 200", rec.Code)
	}
	if rec := call(http.MethodGet, "/api/v2/plugins", sessionA, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"href":"/dexaflow/api-tokens"`) {
		t.Errorf("plugins = %d %s, want the token screen's external view", rec.Code, rec.Body.String())
	}

	listA := call(http.MethodGet, "/api/v2/apiTokens", sessionA, "", "")
	if listA.Code != http.StatusOK || !strings.Contains(listA.Body.String(), tok.ID) {
		t.Fatalf("own tenant list = %d %s, want the token", listA.Code, listA.Body.String())
	}
	var hash string
	if err := pg.Pool.QueryRow(ctx, `SELECT token_hash FROM api_tokens WHERE id = $1`, tok.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{tok.Token, hash, `"token"`, "token_hash"} {
		if strings.Contains(listA.Body.String(), secret) {
			t.Errorf("the list carries %q: %s", secret, listA.Body.String())
		}
	}

	listB := call(http.MethodGet, "/api/v2/apiTokens", sessionB, "", "")
	if listB.Code != http.StatusOK || strings.Contains(listB.Body.String(), tok.ID) || strings.Contains(listB.Body.String(), emailA) ||
		!strings.Contains(listB.Body.String(), `"total_entries":0`) {
		t.Errorf("another tenant's admin lists = %d %s, want no token of default", listB.Code, listB.Body.String())
	}
	if rec := call(http.MethodDelete, "/api/v2/apiTokens/"+tok.ID, sessionB, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("another tenant's admin revokes = %d %s, want 404", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/api/v2/dags", "", tok.Token, ""); rec.Code != http.StatusOK {
		t.Fatalf("the token after a cross-tenant revoke = %d %s, want it still working", rec.Code, rec.Body.String())
	}

	if rec := call(http.MethodDelete, "/api/v2/apiTokens/"+tok.ID, sessionA, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke from the screen = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/api/v2/dags", "", tok.Token, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token = %d, want 401", rec.Code)
	}
	var audits int
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log a JOIN tenants t ON t.id = a.tenant_id
		WHERE t.name = 'default' AND a.action = 'api_token.revoke' AND a.metadata->>'token_id' = $1`, tok.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Errorf("api_token.revoke audit rows for the token = %d, want 1", audits)
	}
}
