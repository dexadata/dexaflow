package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// TestRegisterVersionDryRunAnswersWhatWouldHappen: POST .../versions/dry_run
// reaches the repository as a dry run, answers 200 whatever the register
// would create, says what it would do, and marks the answer as a dry run in
// its body and in the Dexaflow-Dry-Run header (#1705).
func TestRegisterVersionDryRunAnswersWhatWouldHappen(t *testing.T) {
	repo := &fakeVersionRepo{created: true, dagCreated: true, paused: true}
	rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl/versions/dry_run", validSpecJSON)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(repo.dryRuns) != 1 || !repo.dryRuns[0] {
		t.Errorf("repository dry runs = %v, want [true]", repo.dryRuns)
	}
	if got := rec.Header().Get(dryRunHeader); got != "true" {
		t.Errorf("%s header = %q, want true", dryRunHeader, got)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"dry_run": true, "created": true, "dag_created": true, "is_paused": true, "dag_id": "etl", "version": "v1"} {
		if resp[k] != want {
			t.Errorf("%s = %v, want %v (%s)", k, resp[k], want, rec.Body.String())
		}
	}
	if w, ok := resp["warnings"].([]any); !ok || len(w) != 0 {
		t.Errorf("warnings = %v, want an empty list", resp["warnings"])
	}
}

// TestRegisterVersionIsNeverADryRun: the register route registers for real,
// a dry_run query included (the dry run is a route of its own, which an
// older server answers with 404), with no dry run marks.
func TestRegisterVersionIsNeverADryRun(t *testing.T) {
	for _, q := range []string{"", "?dry_run=true"} {
		repo := &fakeVersionRepo{created: true}
		rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl/versions"+q, validSpecJSON)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%q: register = %d, want 201", q, rec.Code)
		}
		if len(repo.dryRuns) != 1 || repo.dryRuns[0] {
			t.Errorf("%q: repository dry runs = %v, want [false]", q, repo.dryRuns)
		}
		if rec.Header().Get(dryRunHeader) != "" || strings.Contains(rec.Body.String(), "dry_run") {
			t.Errorf("%q: a register carries dry run marks: %v %s", q, rec.Header(), rec.Body.String())
		}
	}
}

// TestRegisterVersionDryRunRefusesAsTheRegisterWould: every refusal of a
// register (the handler's own checks, the operator's policy, the
// repository's) answers a dry run with the same status and body, and the
// Dexaflow-Dry-Run header, so a client can tell a server that knows dry_run
// from one that ignored it.
func TestRegisterVersionDryRunRefusesAsTheRegisterWould(t *testing.T) {
	scopedDeploy := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}, Scoped: true, Scopes: []string{auth.ScopeDeploy}}
	policy, perr := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{Images: []string{"registry.example.com/team-a/*"}})
	if perr != nil {
		t.Fatal(perr)
	}
	for _, tc := range []struct {
		name  string
		path  string
		query string
		body  string
		user  *auth.User
		err   error
		pol   *domain.ExecutionPolicy
		code  int
	}{
		{name: "dag_id mismatch", path: "other", body: validSpecJSON, code: http.StatusBadRequest},
		{name: "empty body", path: "etl", body: "{}", code: http.StatusBadRequest},
		{name: "invalid spec", path: "etl", body: `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[]}`, code: http.StatusBadRequest},
		{name: "image outside the allowlist", path: "etl", body: validSpecJSON, pol: policy, code: http.StatusBadRequest},
		{name: "unknown connection", path: "etl", body: validSpecJSON, err: domain.Safef(domain.ErrValidation, "dag %q declares unknown connection(s) %s", "etl", "warehouse"), code: http.StatusBadRequest},
		{name: "version conflict", path: "etl", body: validSpecJSON, err: domain.ErrConflict, code: http.StatusConflict},
		{name: "tenant limit", path: "etl", body: validSpecJSON, err: domain.Safef(domain.ErrLimitExceeded, "the limit max_dags of 1 is reached"), code: http.StatusForbidden},
		{name: "is_paused without the run scope", path: "etl", query: "is_paused=true", body: validSpecJSON, user: scopedDeploy, code: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := tc.user
			if user == nil {
				user = &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
			}
			answer := func(dry bool) (int, string, string) {
				srv := newTestServer(Dependencies{
					Observability: ObservabilityDeps{
						Logger: discardLogger(),
					},
					Server: ServerDeps{
						CORSOrigins: []string{"*"},
					},
					Auth: AuthDeps{
						Authenticator: &fakeAuthn{user: user},
						RateLimiter:   auth.NewRateLimiter(100, time.Minute),
					},
					Execution: ExecutionDeps{
						ExecutionPolicy: tc.pol,
					},
					Stores: StoreDeps{
						Versions: &fakeVersionRepo{created: true, err: tc.err},
					},
				})
				route := "/versions"
				if dry {
					route = "/versions/dry_run"
				}
				q := ""
				if tc.query != "" {
					q = "?" + tc.query
				}
				rec := authGet(srv, http.MethodPost, "/api/v2/dags/"+tc.path+route+q, tc.body)
				return rec.Code, problemWithoutInstance(t, rec.Body.String()), rec.Header().Get(dryRunHeader)
			}
			realCode, realBody, _ := answer(false)
			dryCode, dryBody, header := answer(true)
			if realCode != tc.code {
				t.Fatalf("register = %d, want %d (%s)", realCode, tc.code, realBody)
			}
			if dryCode != realCode || dryBody != realBody {
				t.Errorf("dry run = %d %s, want the register's %d %s", dryCode, dryBody, realCode, realBody)
			}
			if header != "true" {
				t.Errorf("a refused dry run has %s %q, want true", dryRunHeader, header)
			}
		})
	}
}

// TestRegisterVersionDryRunReportsAMisfitWithoutCountingIt: under
// executor.unit.enforce=warn a dry run lists the misfit as a warning, and
// does not count it: nothing was let through.
func TestRegisterVersionDryRunReportsAMisfitWithoutCountingIt(t *testing.T) {
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
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions/dry_run", big)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run of a misfit under warn = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "train") {
		t.Errorf("warnings = %q, want one naming task train", resp.Warnings)
	}
	if len(misfits.stages) != 0 {
		t.Errorf("a dry run counted misfits: %v", misfits.stages)
	}
}

// problemWithoutInstance is a problem body without its instance, the path
// that was called, so two routes' refusals compare.
func problemWithoutInstance(t *testing.T, body string) string {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("not a problem body: %v: %s", err, body)
	}
	delete(p, "instance")
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
