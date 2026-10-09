package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// scopedWrites is every non-read route a scoped token may call, with the
// scope it needs (ADR 0067 section 1). A write missing here must be refused
// to every scoped token.
var scopedWrites = map[string]string{
	"PATCH /api/v2/dags/:dag_id":                                                    auth.ScopeRun,
	"POST /api/v2/dags/:dag_id/dagRuns":                                             auth.ScopeRun,
	"PATCH /api/v2/dags/:dag_id/dagRuns/:dag_run_id":                                auth.ScopeRun,
	"PATCH /api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id":         auth.ScopeRun,
	"PATCH /api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id/*action": auth.ScopeRun,
	"POST /api/v2/dags/:dag_id/clearTaskInstances":                                  auth.ScopeRun,
	"POST /api/v2/dags/:dag_id/versions":                                            auth.ScopeDeploy,
}

// scopelessReads are the protected GET routes a scoped token cannot call at
// all, because they check no permission of their own: UI-only screens and
// stubs an API client has no use for. Every other protected GET needs
// dexaflow:read. Adding a GET route without RequirePermission or RequireScope
// fails TestScopeGateClassifiesEveryRoute until it is listed here or guarded.
var scopelessReads = map[string]bool{
	"GET /ui/auth/me":                                          true,
	"GET /ui/auth/menus":                                       true,
	"GET /ui/dashboard/dag_stats":                              true,
	"GET /ui/dashboard/historical_metrics_data":                true,
	"GET /ui/dependencies":                                     true,
	"GET /ui/calendar/:dag_id":                                 true,
	"GET /ui/backfills":                                        true,
	"GET /ui/teams":                                            true,
	"GET /ui/connections/hook_meta":                            true,
	"GET /ui/next_run_assets/:dag_id":                          true,
	"GET /api/v2/dagTags":                                      true,
	"GET /api/v2/dagWarnings":                                  true,
	"GET /api/v2/plugins":                                      true,
	"GET /api/v2/plugins/importErrors":                         true,
	"GET /api/v2/assets":                                       true,
	"GET /api/v2/assets/events":                                true,
	"GET /api/v2/providers":                                    true,
	"GET /api/v2/jobs":                                         true,
	"GET /api/v2/backfills":                                    true,
	"GET /api/v2/config":                                       true,
	"GET /api/v2/dags/:dag_id/dagRuns/:dag_run_id/hitlDetails": true,
}

// newFullScopeServer wires every optional dependency that adds routes, so the
// walk below sees the whole protected surface.
func newFullScopeServer(t *testing.T) *gin.Engine {
	t.Helper()
	ws, _ := newWorkspace(t)
	sched := "0 5 * * *"
	return NewServer(Dependencies{
		Logger:              discardLogger(),
		Authenticator:       engineAdmin{},
		RateLimiter:         auth.NewRateLimiter(1000, time.Minute),
		TokenTTLSecs:        900,
		Edition:             "pro",
		Dags:                &fakeDagRepo{dags: []domain.DAG{{DagID: "etl", Schedule: &sched}}},
		DagRuns:             &fakeRunRepo{},
		Tasks:               &fakeTaskRepo{},
		Versions:            &fakeVersionRepo{},
		Xcoms:               &fakeXComReader{},
		Specs:               &fakeSpecReader{},
		LatestRuns:          &fakeLatestRuns{},
		TaskSummary:         &fakeTaskSummary{},
		DagVersions:         &fakeVersionLister{},
		DashboardStats:      &fakeStatsReader{},
		AuditLog:            &fakeAuditReader{},
		Variables:           &fakeVariableStore{},
		Users:               &fakeUserStore{},
		UserAudit:           &fakeUserAudit{},
		Connections:         &fakeConnStore{},
		ConnectionTest:      &fakeConnTester{},
		Pools:               &fakePoolStore{},
		Favorites:           &fakeFavoriteStore{},
		ImportErrors:        &fakeImportErrorStore{},
		Workspace:           ws,
		ExamplesFS:          fstest.MapFS{},
		TrustedIssuerBearer: scopedIssuer{},
		TrustedIssuerUsers:  &fakeIssuerUsers{user: linkedAdmin(), active: true},
		AuthAudit:           &fakeAuthAudit{},
	})
}

// concretePath fills a route pattern's parameters with sample values.
func concretePath(pattern string) string {
	segs := strings.Split(pattern, "/")
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"):
			segs[i] = "x"
		case strings.HasPrefix(s, "*"):
			segs[i] = "x"
		}
	}
	return strings.Join(segs, "/")
}

// scopeRefused reports a 403 from the scope checks, not from a handler.
func scopeRefused(rec *httptest.ResponseRecorder) bool {
	body := rec.Body.String()
	return rec.Code == http.StatusForbidden &&
		(strings.Contains(body, "missing scope") || strings.Contains(body, "a scoped token cannot call this route"))
}

// TestScopeGateClassifiesEveryRoute walks every registered route and checks
// what a scoped token gets there (ADR 0067), so a route added later cannot
// stay open to scoped tokens by forgetting its permission check:
//   - a token with every scope reaches only the mapped writes, the guarded
//     reads, and nothing listed in scopelessReads;
//   - a token without dexaflow:read reaches no GET route;
//   - every write outside scopedWrites is refused whatever the scopes.
func TestScopeGateClassifiesEveryRoute(t *testing.T) {
	srv := newFullScopeServer(t)
	const all = "scopes:dexaflow:read dexaflow:run dexaflow:deploy"
	const noRead = "scopes:dexaflow:run dexaflow:deploy"

	seenWrites := map[string]bool{}
	for _, ri := range srv.Routes() {
		if isPublic(ri.Path) {
			continue
		}
		key := ri.Method + " " + ri.Path
		call := scopeCall{key, ri.Method, concretePath(ri.Path), "{}"}
		withAll := call.do(srv, all)
		switch {
		case ri.Method == http.MethodGet || ri.Method == http.MethodHead:
			if scopelessReads[key] {
				if !scopeRefused(withAll) {
					t.Errorf("%s with every scope = %d %s, want refused (listed as scopeless)", key, withAll.Code, withAll.Body.String())
				}
				continue
			}
			if scopeRefused(withAll) {
				t.Errorf("%s with every scope = %d %s: a GET route without a permission check; guard it or list it in scopelessReads", key, withAll.Code, withAll.Body.String())
			}
			if rec := call.do(srv, noRead); !scopeRefused(rec) {
				t.Errorf("%s without dexaflow:read = %d %s, want refused", key, rec.Code, rec.Body.String())
			}
		default:
			scope, mapped := scopedWrites[key]
			if !mapped {
				if !scopeRefused(withAll) {
					t.Errorf("%s with every scope = %d %s, want refused: no scope grants this write", key, withAll.Code, withAll.Body.String())
				}
				continue
			}
			seenWrites[key] = true
			if scopeRefused(withAll) {
				t.Errorf("%s with every scope = %d %s, want past the scope checks", key, withAll.Code, withAll.Body.String())
			}
			if rec := call.do(srv, "scopes:dexaflow:read"); !scopeRefused(rec) || !strings.Contains(rec.Body.String(), scope) {
				t.Errorf("%s with only dexaflow:read = %d %s, want refused naming %s", key, rec.Code, rec.Body.String(), scope)
			}
		}
	}
	for key := range scopedWrites {
		if !seenWrites[key] {
			t.Errorf("scopedWrites names %s, which is not registered", key)
		}
	}
}

// TestMonitorRoutesNeedOnlyTheReadScope: the control-plane health the MCP
// reads (monitor and version) checks no role permission, so a scoped token
// reaches it with dexaflow:read alone, and not without it.
func TestMonitorRoutesNeedOnlyTheReadScope(t *testing.T) {
	srv := newFullScopeServer(t)
	for _, path := range []string{"/api/v2/monitor/health", "/api/v2/monitor/executor", "/api/v2/version"} {
		c := scopeCall{path, http.MethodGet, path, ""}
		if rec := c.do(srv, "scopes:dexaflow:read"); scopeRefused(rec) {
			t.Errorf("%s with dexaflow:read = %d %s, want past the scope checks", path, rec.Code, rec.Body.String())
		}
		refusedForScope(t, c, c.do(srv, "scopes:dexaflow:run"), auth.ScopeRead)
		if rec := c.do(srv, "engine"); scopeRefused(rec) {
			t.Errorf("%s with an engine token = %d, want unscoped access", path, rec.Code)
		}
	}
}

// TestScopelessRouteRefusesAScopedToken: a protected route that checks no
// permission is closed to scoped tokens, and stays open to engine tokens.
func TestScopelessRouteRefusesAScopedToken(t *testing.T) {
	srv := newFullScopeServer(t)
	c := scopeCall{"dashboard", http.MethodGet, "/ui/dashboard/dag_stats", ""}

	rec := c.do(srv, "scopes:dexaflow:read dexaflow:run dexaflow:deploy")

	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "a scoped token cannot call this route") {
		t.Errorf("dashboard with every scope = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if rec := c.do(srv, "engine"); rec.Code != http.StatusOK {
		t.Errorf("dashboard with an engine token = %d, want 200", rec.Code)
	}
}
