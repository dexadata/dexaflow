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

// TestRequireDigestWiringAgainstPostgres locks the wiring (#1703): the
// deploy.require_digest the server loads from its environment, under either
// prefix, reaches the register the boot path builds. Under warn a tag
// registers with a warning in the answer, a count and an audit entry that
// records it; under enforce it is refused and nothing is registered.
func TestRequireDigestWiringAgainstPostgres(t *testing.T) {
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
	email := fmt.Sprintf("rd_%d@example.com", time.Now().UnixNano())
	const password = "require-digest-pw-1"
	if _, cerr := repo.CreateUser(ctx, "default", email, password, []string{"deployer"}); cerr != nil {
		t.Fatalf("CreateUser: %v", cerr)
	}
	dagID := fmt.Sprintf("rd_%d", time.Now().UnixNano())
	spec := func(version, image string) string {
		return fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":%q,"image":%q,"tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`,
			dagID, version, image)
	}

	for _, tc := range []struct {
		env, mode  string
		wantStatus int
	}{
		{"DEXAFLOW_DEPLOY_REQUIRE_DIGEST", "warn", http.StatusCreated},
		{"LEOFLOW_DEPLOY_REQUIRE_DIGEST", "enforce", http.StatusBadRequest},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Setenv(tc.env, tc.mode)
			cfg, err := config.LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			cfg.Auth.JWT.Secret = "wiring-secret"
			cfg.Auth.JWT.TokenTTLSeconds = 3600
			if verr := cfg.Validate(); verr != nil {
				t.Fatalf("Validate: %v", verr)
			}
			reg := prometheus.NewRegistry()
			metrics := observability.NewMetrics(reg)
			tel := &observability.Telemetry{
				Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
				Metrics:  metrics,
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

			version := "v-" + tc.mode
			rec := call(http.MethodPost, "/api/v2/dags/"+dagID+"/versions", session.AccessToken, spec(version, "ghcr.io/acme/etl:"+version))
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), "deploy.require_digest="+tc.mode) {
				t.Fatalf("register of a tag under %s = %d %s, want %d naming the setting", tc.mode, rec.Code, rec.Body.String(), tc.wantStatus)
			}
			outcome := map[string]string{"warn": "warned", "enforce": "refused"}[tc.mode]
			if got := unpinnedCount(t, reg, "register", outcome); got != 1 {
				t.Errorf("dexaflow_unpinned_image_total{stage=register,outcome=%s} = %v, want 1", outcome, got)
			}
			entries, _, err := repo.ListAuditLogs(ctx, "default", dagID, 100, 0)
			if err != nil {
				t.Fatal(err)
			}
			var audited []map[string]any
			for _, e := range entries {
				var meta map[string]any
				if e.Action == "dag.version.register" && json.Unmarshal([]byte(e.Extra), &meta) == nil && meta["version"] == version {
					audited = append(audited, meta)
				}
			}
			switch {
			case tc.mode == "enforce" && len(audited) != 0:
				t.Errorf("a refused register was audited: %v", audited)
			case tc.mode == "warn" && (len(audited) != 1 || audited[0]["require_digest"] != "warn" || audited[0]["image_pinned"] != false):
				t.Errorf("register audit = %v, want one with require_digest warn and image_pinned false", audited)
			}
		})
	}
}

// unpinnedCount is dexaflow_unpinned_image_total for stage and outcome in reg.
func unpinnedCount(t *testing.T, reg *prometheus.Registry, stage, outcome string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range families {
		if fam.GetName() != "dexaflow_unpinned_image_total" {
			continue
		}
		for _, m := range fam.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["stage"] == stage && labels["outcome"] == outcome {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}
