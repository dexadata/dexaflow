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
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestAPITokenWiringAgainstPostgres locks the wiring, not the leaf (ADR
// 0074): through the API server the boot path builds, an admin signs in and
// creates a deploy token; whatever its owner's roles, the token then reads
// DAGs, is refused every other read, deleting a DAG and minting another
// token, and stops working once revoked.
func TestAPITokenWiringAgainstPostgres(t *testing.T) {
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
	email := fmt.Sprintf("ci_%d@example.com", time.Now().UnixNano())
	const password = "deployer-password-1"
	if _, err := repo.CreateUser(ctx, "default", email, password, []string{"admin"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cfg := &config.ServerConfig{}
	cfg.Auth.JWT.Secret = "wiring-secret"
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

	call := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	login := call(http.MethodPost, "/auth/token", "", fmt.Sprintf(`{"username":%q,"password":%q}`, email, password))
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &session) != nil {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	created := call(http.MethodPost, "/api/v2/apiTokens", session.AccessToken, `{"name":"ci-wiring","scope":"deploy","expires_in_seconds":86400}`)
	var tok struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &tok) != nil || !auth.IsAPIToken(tok.Token) {
		t.Fatalf("create token = %d %s", created.Code, created.Body.String())
	}

	if rec := call(http.MethodGet, "/api/v2/dags", tok.Token, ""); rec.Code != http.StatusOK {
		t.Errorf("list dags with the token = %d %s, want 200", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/v2/variables", "/api/v2/connections", "/api/v2/users", "/api/v2/eventLogs"} {
		if rec := call(http.MethodGet, path, tok.Token, ""); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "missing scope") {
			t.Errorf("GET %s with a deploy token = %d %s, want 403 missing scope", path, rec.Code, rec.Body.String())
		}
	}
	if rec := call(http.MethodDelete, "/api/v2/dags/anything", tok.Token, ""); rec.Code != http.StatusForbidden {
		t.Errorf("delete dag with the token = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodPost, "/api/v2/apiTokens", tok.Token, `{"name":"again","scope":"deploy"}`); rec.Code != http.StatusForbidden {
		t.Errorf("mint a token with a token = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodDelete, "/api/v2/apiTokens/"+tok.ID, session.AccessToken, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/api/v2/dags", tok.Token, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token = %d %s, want 401", rec.Code, rec.Body.String())
	}
}
