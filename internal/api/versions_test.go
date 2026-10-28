package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeVersionRepo struct {
	created    bool
	dagCreated bool
	paused     bool // is_paused the fake reports when no override is asked
	seen       []string
	actors     []string
	asked      []*bool                // the is_paused override of each register
	dryRuns    []bool                 // the dry_run of each register
	promoted   []*domain.DagPromotion // the promoted_from of each register
	builds     []*domain.BuildInfo    // the build_info of each register
	// requireDigest is the deploy.require_digest of each register.
	requireDigest []domain.DigestRequirement
	err           error

	// The set-current side: the specs by label the fake holds, the change it
	// answers, and what the handler asked for.
	specs      map[string]domain.DAGSpec
	change     domain.CurrentVersionChange
	setErr     error
	selections []domain.DagVersionSelector
	setTenants []string
	admitErr   error
	// setRequireDigest is the deploy.require_digest of each set-current.
	setRequireDigest []domain.DigestRequirement
}

func (f *fakeVersionRepo) RegisterDagVersionAs(_ context.Context, _ string, reg domain.DagRegistration) (domain.DagRegistered, error) {
	f.seen = append(f.seen, reg.SpecHash)
	f.actors = append(f.actors, reg.ActorUserID)
	f.asked = append(f.asked, reg.IsPaused)
	f.dryRuns = append(f.dryRuns, reg.DryRun)
	f.promoted = append(f.promoted, reg.PromotedFrom)
	f.builds = append(f.builds, reg.BuildInfo)
	f.requireDigest = append(f.requireDigest, reg.RequireDigest)
	if f.err != nil {
		return domain.DagRegistered{}, f.err
	}
	paused := f.paused
	if reg.IsPaused != nil {
		paused = *reg.IsPaused
	}
	return domain.DagRegistered{Created: f.created, DagCreated: f.dagCreated, IsPaused: paused}, nil
}

func (f *fakeVersionRepo) SetCurrentDagVersionAs(_ context.Context, tenant string, req domain.DagSetCurrent) (domain.CurrentVersionChange, error) {
	sel, actor, admit := req.Selector, req.ActorUserID, req.Admit
	f.setRequireDigest = append(f.setRequireDigest, req.RequireDigest)
	f.selections = append(f.selections, sel)
	f.setTenants = append(f.setTenants, tenant)
	f.actors = append(f.actors, actor)
	if f.setErr != nil {
		return domain.CurrentVersionChange{}, f.setErr
	}
	if spec, ok := f.specs[f.change.To.Version]; ok {
		if err := admit(spec); err != nil {
			f.admitErr = err
			return domain.CurrentVersionChange{}, fmt.Errorf("admitting: %w", err)
		}
	}
	return f.change, nil
}

func versionServer(repo DagVersionRepository) *gin.Engine {
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			Versions: repo,
		},
	})
}

const validSpecJSON = `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`

func TestRegisterVersionCreated(t *testing.T) {
	repo := &fakeVersionRepo{created: true}
	rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var resp versionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DagID != "etl" || !resp.Created || len(resp.SpecHash) != 64 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestRegisterVersionIdempotent(t *testing.T) {
	rec := authGet(versionServer(&fakeVersionRepo{created: false}), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
	if rec.Code != http.StatusOK {
		t.Errorf("idempotent register = %d, want 200", rec.Code)
	}
}

func TestRegisterVersionRejectsMismatchedDagID(t *testing.T) {
	rec := authGet(versionServer(&fakeVersionRepo{}), http.MethodPost, "/api/v2/dags/other/versions", validSpecJSON)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("mismatched dag_id = %d, want 400", rec.Code)
	}
}

// A hand-written spec declaring the removed http_api task type is rejected at
// registration (ADR 0047/0048, #512): the structural guard is that no executor
// exists, and Validate refuses the type before it can be stored.
func TestRegisterVersionRejectsRemovedHTTPAPIType(t *testing.T) {
	spec := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"hook","type":"http_api"}]}`
	rec := authGet(versionServer(&fakeVersionRepo{}), http.MethodPost, "/api/v2/dags/etl/versions", spec)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("removed http_api type = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// A spec that declares a connection (or variable) the tenant has not defined is
// rejected by the repository as domain.ErrValidation (ADR 0055 D6). That is a
// client-fixable input error — the author must run `dexaflow connections set` or
// drop the declaration — so the handler must surface it as 400, not 500. Before
// the handleRepoError ErrValidation branch it fell through to 500, which sent
// users to server logs instead of to their own DAG (#724).
func TestRegisterVersionUnknownConnectionReturns400(t *testing.T) {
	repo := &fakeVersionRepo{err: domain.Safef(domain.ErrValidation,
		"dag %q declares unknown connection(s) %s; define them (dexaflow connections set) or remove them from the DAG's connections: declaration",
		"etl", "warehouse")}
	rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown connection = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	// The phrase names what to fix. Redacting storage errors (#961) must not
	// take it away, or the 400 sends the author to the logs after all.
	if !strings.Contains(rec.Body.String(), "warehouse") {
		t.Errorf("400 body no longer names the undeclared connection: %s", rec.Body.String())
	}
}

func TestRegisterVersionRejectsInvalidSpec(t *testing.T) {
	bad := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img","tasks":[]}`
	rec := authGet(versionServer(&fakeVersionRepo{}), http.MethodPost, "/api/v2/dags/etl/versions", bad)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid spec = %d, want 400", rec.Code)
	}
}

// With an operator resource unit (ADR 0066 §3), a task whose declared
// resources exceed pool_slots x unit is refused at registration, naming the
// size it would need, and a task that fits is accepted.
func TestRegisterVersionRejectsATaskLargerThanItsSize(t *testing.T) {
	unit, err := domain.ParseResourceUnit(domain.ResourceUnitConfig{CPU: "250m", Memory: "512Mi"})
	if err != nil {
		t.Fatal(err)
	}
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
		},
		Execution: ExecutionDeps{
			ResourceUnit: unit,
		},
		Stores: StoreDeps{
			Versions: &fakeVersionRepo{created: true},
		},
	})
	big := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"train","type":"python","entrypoint":"dag:a","resources":{"limits":{"cpu":"2"}}}]}`
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions", big)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "size: 8") {
		t.Errorf("oversized task = %d %s, want 400 naming size: 8", rec.Code, rec.Body.String())
	}
	fits := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"train","type":"python","entrypoint":"dag:a","pool_slots":8,"resources":{"limits":{"cpu":"2"}}}]}`
	if rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions", fits); rec.Code != http.StatusCreated {
		t.Errorf("fitting task = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// fakeUnitMisfits records the stages a tolerated misfit was counted at.
type fakeUnitMisfits struct{ stages []string }

func (f *fakeUnitMisfits) RecordUnitMisfit(stage string) { f.stages = append(f.stages, stage) }

// Under executor.unit.enforce=warn a task larger than its size registers, and
// the misfit is counted; a size above max_size is still refused (ADR 0066 §3).
func TestRegisterVersionWarnAcceptsAMisfitAndCountsIt(t *testing.T) {
	unit, err := domain.ParseResourceUnit(domain.ResourceUnitConfig{
		CPU: "250m", Memory: "512Mi", Enforce: domain.UnitEnforceWarn, MaxSize: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	misfits := &fakeUnitMisfits{}
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
		},
		Execution: ExecutionDeps{
			ResourceUnit: unit,
			UnitMisfits:  misfits,
		},
		Stores: StoreDeps{
			Versions: &fakeVersionRepo{created: true},
		},
	})
	big := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"train","type":"python","entrypoint":"dag:a","resources":{"limits":{"cpu":"2"}}}]}`
	if rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions", big); rec.Code != http.StatusCreated {
		t.Errorf("misfit under warn = %d %s, want 201", rec.Code, rec.Body.String())
	}
	if len(misfits.stages) != 1 || misfits.stages[0] != "register" {
		t.Errorf("misfits recorded = %v, want one at register", misfits.stages)
	}
	huge := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v2","image":"img:v1","tasks":[` +
		`{"task_id":"train","type":"python","entrypoint":"dag:a","pool_slots":9}]}`
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions", huge)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_size") {
		t.Errorf("size above max_size = %d %s, want 400 naming max_size", rec.Code, rec.Body.String())
	}
}

// With source mode on (ADR 0067 §3), a version on the runtime image must carry
// a source within the cap; a version on its own image registers as before.
func TestRegisterVersionChecksSourceMode(t *testing.T) {
	const rt = "ghcr.io/dexadata/runtime@sha256:abc"
	spec := func(image, source string) string {
		b, err := json.Marshal(domain.DAGSpec{SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: image, Source: source,
			Tasks: []domain.TaskSpec{{TaskID: "a", Type: "python", Entrypoint: "dag:a"}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	server := func(mode string) *gin.Engine {
		return newTestServer(Dependencies{
			Observability: ObservabilityDeps{
				Logger: discardLogger(),
			},
			Server: ServerDeps{
				CORSOrigins: []string{"*"},
			},
			Auth: AuthDeps{
				Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
				RateLimiter:   auth.NewRateLimiter(100, time.Minute),
			},
			Execution: ExecutionDeps{
				SourceModeImage: mode,
			},
			Stores: StoreDeps{
				Versions: &fakeVersionRepo{created: true},
			},
		})
	}
	oversize := strings.Repeat("x", domain.MaxSourceModeBytes+1)
	cases := map[string]struct {
		mode, image, source string
		want                int
		wantBody            string
	}{
		"on, runtime image with a source": {rt, rt, "print(1)\n", http.StatusCreated, ""},
		"on, runtime image, no source":    {rt, rt, "", http.StatusBadRequest, "has no source"},
		"on, runtime image, oversize":     {rt, rt, oversize, http.StatusBadRequest, "source mode"},
		"on, own image, no source":        {rt, "img:v1", "", http.StatusCreated, ""},
		"off, runtime image, no source":   {"", rt, "", http.StatusCreated, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := authGet(server(tc.mode), http.MethodPost, "/api/v2/dags/etl/versions", spec(tc.image, tc.source))
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("register = %d %s, want %d mentioning %q", rec.Code, rec.Body.String(), tc.want, tc.wantBody)
			}
		})
	}
}

// The operator execution allowlist (issue #629) is enforced at registration: a
// DAG whose task sets an execution field outside it answers 400 naming the
// task and the field, and a DAG within it registers.
func TestRegisterVersionRejectsAnExecutionFieldOutsideTheAllowlist(t *testing.T) {
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{
		Fields: []string{"tolerations", "priority_class_name"}, PriorityClasses: []string{"etl"},
	})
	if err != nil {
		t.Fatal(err)
	}
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
		},
		Execution: ExecutionDeps{
			ExecutionPolicy: policy,
		},
		Stores: StoreDeps{
			Versions: &fakeVersionRepo{created: true},
		},
	})
	for want, execution := range map[string]string{
		"execution.affinity":      `{"affinity":{"nodeAffinity":{}}}`,
		"system-cluster-critical": `{"priority_class_name":"system-cluster-critical"}`,
	} {
		body := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
			`{"task_id":"train","type":"python","entrypoint":"dag:a","execution":` + execution + `}]}`
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) || !strings.Contains(rec.Body.String(), "train") {
			t.Errorf("%s = %d %s, want 400 naming it and the task", want, rec.Code, rec.Body.String())
		}
	}
	ok := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"train","type":"python","entrypoint":"dag:a","execution":{"priority_class_name":"etl","tolerations":[{"key":"etl"}]}}]}`
	if rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions", ok); rec.Code != http.StatusCreated {
		t.Errorf("allowed execution = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// The service account and image allowlists (issue #537 C2) are enforced at
// registration: an unlisted image or service account answers 400 naming it.
func TestRegisterVersionRejectsAnImageOrServiceAccountOutsideTheAllowlist(t *testing.T) {
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{
		ServiceAccounts: []string{"etl"}, Images: []string{"registry.example.com/team-a/*"},
	})
	if err != nil {
		t.Fatal(err)
	}
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
		},
		Execution: ExecutionDeps{
			ExecutionPolicy: policy,
		},
		Stores: StoreDeps{
			Versions: &fakeVersionRepo{created: true},
		},
	})
	cases := map[string]string{
		"img:v1": `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
			`{"task_id":"train","type":"python","entrypoint":"dag:a"}]}`,
		"cluster-admin": `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"registry.example.com/team-a/etl:v1","tasks":[` +
			`{"task_id":"train","type":"python","entrypoint":"dag:a","execution":{"service_account":"cluster-admin"}}]}`,
	}
	for want, body := range cases {
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s = %d %s, want 400 naming it", want, rec.Code, rec.Body.String())
		}
	}
	ok := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"registry.example.com/team-a/etl:v1","tasks":[` +
		`{"task_id":"train","type":"python","entrypoint":"dag:a","execution":{"service_account":"etl"}}]}`
	if rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions", ok); rec.Code != http.StatusCreated {
		t.Errorf("allowed image and service account = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// TestRegisterVersionPassesTheUser: the register runs on behalf of the
// authenticated user, so the version's created_by and its audit entry name
// them (#1702).
func TestRegisterVersionPassesTheUser(t *testing.T) {
	repo := &fakeVersionRepo{created: true}
	if rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON); rec.Code != http.StatusCreated {
		t.Fatalf("register = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(repo.actors) != 1 || repo.actors[0] != "u1" {
		t.Errorf("register actors = %v, want [u1]", repo.actors)
	}
}

// TestRegisterVersionIsPausedQuery: ?is_paused=true|false reaches the
// repository as the override and the answer reports the resulting state;
// without it no override is asked and the state is still reported; any other
// value is refused before anything is registered (#1705).
func TestRegisterVersionIsPausedQuery(t *testing.T) {
	for _, tc := range []struct {
		query      string
		wantCode   int
		wantAsked  string // "nil", "true" or "false"
		wantPaused bool
	}{
		{"", http.StatusCreated, "nil", true},
		{"?is_paused=false", http.StatusCreated, "false", false},
		{"?is_paused=true", http.StatusCreated, "true", true},
		{"?is_paused=yes", http.StatusBadRequest, "", false},
		{"?is_paused=", http.StatusBadRequest, "", false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			repo := &fakeVersionRepo{created: true, paused: true}
			rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl/versions"+tc.query, validSpecJSON)
			if rec.Code != tc.wantCode {
				t.Fatalf("register = %d, want %d (%s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode != http.StatusCreated {
				if len(repo.asked) != 0 {
					t.Errorf("a refused query still registered: %v", repo.asked)
				}
				if !strings.Contains(rec.Body.String(), "is_paused") {
					t.Errorf("refusal %s does not name is_paused", rec.Body.String())
				}
				return
			}
			got := "nil"
			if len(repo.asked) == 1 && repo.asked[0] != nil {
				got = fmt.Sprint(*repo.asked[0])
			}
			if got != tc.wantAsked {
				t.Errorf("override asked = %s, want %s", got, tc.wantAsked)
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp["is_paused"] != tc.wantPaused {
				t.Errorf("response is_paused = %v, want %v (%s)", resp["is_paused"], tc.wantPaused, rec.Body.String())
			}
		})
	}
}

// TestRegisterVersionReportsDagCreated: the answer tells a register that
// created the DAG from a new version of an existing one.
func TestRegisterVersionReportsDagCreated(t *testing.T) {
	for _, want := range []bool{true, false} {
		rec := authGet(versionServer(&fakeVersionRepo{created: true, dagCreated: want}), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp["dag_created"] != want {
			t.Errorf("dag_created = %v, want %v (%s)", resp["dag_created"], want, rec.Body.String())
		}
	}
}

// TestRegisterVersionIsPausedNeedsTheRunScope: pausing is a run-state write,
// granted by dexaflow:run on PATCH /dags/{dag_id}; a deploy-only token must
// not get it through the register's is_paused (#1705).
func TestRegisterVersionIsPausedNeedsTheRunScope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scopes   []string
		query    string
		wantCode int
	}{
		{"deploy only, no override", []string{auth.ScopeDeploy}, "", http.StatusCreated},
		{"deploy only, override", []string{auth.ScopeDeploy}, "?is_paused=false", http.StatusForbidden},
		{"deploy and run, override", []string{auth.ScopeDeploy, auth.ScopeRun}, "?is_paused=true", http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeVersionRepo{created: true}
			srv := newTestServer(Dependencies{
				Observability: ObservabilityDeps{
					Logger: discardLogger(),
				},
				Server: ServerDeps{
					CORSOrigins: []string{"*"},
				},
				Auth: AuthDeps{
					Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"},
						Scoped: true, Scopes: tc.scopes}},
					RateLimiter: auth.NewRateLimiter(100, time.Minute),
				},
				Stores: StoreDeps{
					Versions: repo,
				},
			})
			rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions"+tc.query, validSpecJSON)
			if rec.Code != tc.wantCode {
				t.Fatalf("register = %d, want %d (%s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusForbidden {
				if !strings.Contains(rec.Body.String(), "missing scope "+auth.ScopeRun) {
					t.Errorf("refusal %s does not name %s", rec.Body.String(), auth.ScopeRun)
				}
				if len(repo.asked) != 0 {
					t.Errorf("a refused override still registered: %v", repo.asked)
				}
			}
		})
	}
}

// rollbackChange is v2 -> v1 of etl, as the repository reports it.
func rollbackChange() domain.CurrentVersionChange {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return domain.CurrentVersionChange{
		Changed: true,
		From:    domain.DagVersion{ID: "id-2", VersionNumber: 2, Version: "v2", CreatedAt: at, ImageReference: "img@sha256:bb", SpecHash: "h2", CreatedBy: "7f3c2a10-5b1e-4c3d-9a8e-2f6b1c0d4e5a"},
		To:      domain.DagVersion{ID: "id-1", VersionNumber: 1, Version: "v1", CreatedAt: at, ImageReference: "img@sha256:aa", SpecHash: "h1", IsCurrent: true},
	}
}

// TestSetCurrentVersionByLabel: PUT currentVersion with a label answers 200
// with the from and to versions, scoped to the caller's tenant and run on
// behalf of the caller.
func TestSetCurrentVersionByLabel(t *testing.T) {
	repo := &fakeVersionRepo{change: rollbackChange()}
	rec := authGet(versionServer(repo), http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set current = %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		DagID   string         `json:"dag_id"`
		Changed bool           `json:"changed"`
		From    map[string]any `json:"from"`
		To      map[string]any `json:"to"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DagID != "etl" || !resp.Changed || resp.From["bundle_version"] != "v2" || resp.To["bundle_version"] != "v1" {
		t.Errorf("response = %s, want etl v2 -> v1 changed", rec.Body.String())
	}
	if resp.To["version_number"] != float64(1) || resp.To["is_current"] != true || resp.To["image"] != "img@sha256:aa" {
		t.Errorf("to = %v, want version 1, current, its image", resp.To)
	}
	if len(repo.selections) != 1 || repo.selections[0] != (domain.DagVersionSelector{Version: "v1"}) {
		t.Errorf("selections = %+v, want label v1", repo.selections)
	}
	if repo.setTenants[0] != "default" || repo.actors[0] != "u1" {
		t.Errorf("tenant %q actor %q, want default and u1", repo.setTenants[0], repo.actors[0])
	}
}

// TestSetCurrentVersionByNumber: a version_number selects by number.
func TestSetCurrentVersionByNumber(t *testing.T) {
	repo := &fakeVersionRepo{change: rollbackChange()}
	rec := authGet(versionServer(repo), http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version_number":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set current = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(repo.selections) != 1 || repo.selections[0] != (domain.DagVersionSelector{Number: 1}) {
		t.Errorf("selections = %+v, want number 1", repo.selections)
	}
}

// TestSetCurrentVersionValidatesTheBody: exactly one of version and
// version_number, a label within the spec's limits and a positive number.
func TestSetCurrentVersionValidatesTheBody(t *testing.T) {
	for name, body := range map[string]string{
		"empty":       `{}`,
		"both":        `{"version":"v1","version_number":1}`,
		"blank label": `{"version":""}`,
		"zero":        `{"version_number":0}`,
		"negative":    `{"version_number":-3}`,
		"long label":  `{"version":"` + strings.Repeat("a", 300) + `"}`,
		"not json":    `v1`,
	} {
		repo := &fakeVersionRepo{change: rollbackChange()}
		rec := authGet(versionServer(repo), http.MethodPut, "/api/v2/dags/etl/currentVersion", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: = %d (%s), want 400", name, rec.Code, rec.Body.String())
		}
		if len(repo.selections) != 0 {
			t.Errorf("%s: reached the repository with %+v", name, repo.selections)
		}
	}
}

// TestSetCurrentVersionNotFound: an unknown DAG or version answers 404 with
// the repository's phrase.
func TestSetCurrentVersionNotFound(t *testing.T) {
	repo := &fakeVersionRepo{setErr: domain.Safef(domain.ErrNotFound, "dag %q has no version %q", "etl", "v9")}
	rec := authGet(versionServer(repo), http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v9"}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "v9") {
		t.Errorf("unknown version = %d %s, want 404 naming v9", rec.Code, rec.Body.String())
	}
}

// TestSetCurrentVersionAppliesTheRegisterChecks: the version made current
// must pass the checks a register of it would pass today, so a policy
// tightened since it registered is not bypassed by a rollback.
func TestSetCurrentVersionAppliesTheRegisterChecks(t *testing.T) {
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{Images: []string{"registry.example.com/team-a/*"}})
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeVersionRepo{change: rollbackChange(), specs: map[string]domain.DAGSpec{"v1": {
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img@sha256:aa",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: "python", Entrypoint: "dag:a"}},
	}}}
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
		},
		Execution: ExecutionDeps{
			ExecutionPolicy: policy,
		},
		Stores: StoreDeps{
			Versions: repo,
		},
	})
	rec := authGet(srv, http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v1"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "img@sha256:aa") {
		t.Errorf("rollback to a refused image = %d %s, want 400 naming it", rec.Code, rec.Body.String())
	}
	if repo.admitErr == nil {
		t.Error("the admission check did not run")
	}
}

// TestSetCurrentVersionNeedsWriteOnDags: a user who may only read DAGs cannot
// change which version is live.
func TestSetCurrentVersionNeedsWriteOnDags(t *testing.T) {
	repo := &fakeVersionRepo{change: rollbackChange()}
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u2", TenantID: "default",
				Permissions: []auth.Permission{{Action: "read", Resource: "dag"}}}},
			RateLimiter: auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			Versions: repo,
		},
	})
	rec := authGet(srv, http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v1"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("reader = %d, want 403", rec.Code)
	}
	if len(repo.selections) != 0 {
		t.Errorf("a reader reached the repository: %+v", repo.selections)
	}
}

// TestSetCurrentVersionCountsAMisfitAtItsOwnStage: a tolerated misfit found
// while making a version current is counted under set_current, not register,
// so the metric tells a rollback from a deploy.
func TestSetCurrentVersionCountsAMisfitAtItsOwnStage(t *testing.T) {
	unit, err := domain.ParseResourceUnit(domain.ResourceUnitConfig{
		CPU: "250m", Memory: "512Mi", Enforce: domain.UnitEnforceWarn, MaxSize: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	misfits := &fakeUnitMisfits{}
	repo := &fakeVersionRepo{change: rollbackChange(), specs: map[string]domain.DAGSpec{"v1": {
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img@sha256:aa",
		Tasks: []domain.TaskSpec{{TaskID: "train", Type: "python", Entrypoint: "dag:a",
			Resources: &domain.Resources{Limits: &domain.ResourceQuantity{CPU: "2"}}}},
	}}}
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
		},
		Execution: ExecutionDeps{
			ResourceUnit: unit,
			UnitMisfits:  misfits,
		},
		Stores: StoreDeps{
			Versions: repo,
		},
	})
	if rec := authGet(srv, http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v1"}`); rec.Code != http.StatusOK {
		t.Fatalf("set current = %d %s", rec.Code, rec.Body.String())
	}
	if len(misfits.stages) != 1 || misfits.stages[0] != "set_current" {
		t.Errorf("misfits recorded = %v, want one at set_current", misfits.stages)
	}
}

// TestRegisterVersionIsPausedNeedsWriteOnDags: the deployer role registers
// versions with deploy:dag and may not pause, so is_paused on a register
// needs write:dag as PATCH /dags/{dag_id} does, else a deployer could pause
// or unpause through the register.
func TestRegisterVersionIsPausedNeedsWriteOnDags(t *testing.T) {
	for _, tc := range []struct {
		name     string
		perms    []auth.Permission
		query    string
		wantCode int
	}{
		{"deployer, no override", []auth.Permission{{Action: "deploy", Resource: "dag"}}, "", http.StatusCreated},
		{"deployer, override", []auth.Permission{{Action: "deploy", Resource: "dag"}}, "?is_paused=false", http.StatusForbidden},
		{"writer, override", []auth.Permission{{Action: "write", Resource: "dag"}}, "?is_paused=true", http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeVersionRepo{created: true}
			srv := newTestServer(Dependencies{
				Observability: ObservabilityDeps{
					Logger: discardLogger(),
				},
				Server: ServerDeps{
					CORSOrigins: []string{"*"},
				},
				Auth: AuthDeps{
					Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Permissions: tc.perms}},
					RateLimiter:   auth.NewRateLimiter(100, time.Minute),
				},
				Stores: StoreDeps{
					Versions: repo,
				},
			})
			rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions"+tc.query, validSpecJSON)
			if rec.Code != tc.wantCode {
				t.Fatalf("register = %d, want %d (%s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusForbidden {
				if !strings.Contains(rec.Body.String(), "write:dag") {
					t.Errorf("refusal %s does not name write:dag", rec.Body.String())
				}
				if len(repo.asked) != 0 {
					t.Errorf("a refused override still registered: %v", repo.asked)
				}
			}
		})
	}
}
