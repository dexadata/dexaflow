//go:build integration

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/xcom"
)

// The route walk of #2315: every route the API registers is called by a
// caller of tenant A with the ids of tenant B's objects. A read answers a
// refusal or an answer that carries nothing of B; a write answers a refusal,
// or lands in A's own namespace; and after the walk every object of B is
// still there, unchanged. A route the walk cannot address fails the suite
// until it is given its parameters, its write body or a reason in
// notTenantScoped.

// notTenantScoped are the routes the walk skips, each with the reason it has
// no tenant data to protect.
var notTenantScoped = map[string]string{
	"GET /api/v2/auth/login":        "sign-in flow, answers before any tenant is known",
	"GET /api/v2/auth/logout":       "clears the caller's own cookie",
	"GET /api/v2/auth/methods":      "lists the configured sign-in methods",
	"POST /auth/token":              "password sign-in, the tenant comes from the credentials",
	"POST /ui/auth/token":           "password sign-in, the tenant comes from the credentials",
	"GET /api/v2/version":           "build version",
	"GET /api/v2/monitor/health":    "process health",
	"GET /api/v2/monitor/executor":  "executor mode of the process",
	"GET /api/v2/config":            "Airflow config stub, the same for every tenant",
	"GET /api/v2/providers":         "static provider list",
	"GET /api/v2/plugins":           "static plugin list",
	"GET /api/v2/deployDefaults":    "cluster deploy defaults, the same for every tenant",
	"GET /ui/config":                "UI settings of the process",
	"GET /ui/auth/menus":            "menu entries for the caller's own roles",
	"GET /ui/auth/me":               "the caller's own principal",
	"GET /ui/teams":                 "stub",
	"GET /ui/connections/hook_meta": "static connection form metadata",
	"POST /api/v2/connections/test": "tests the connection in the request body, stores nothing",
	"GET /dexaflow/api-tokens":      "static page; the token routes are walked",
	// The Lite editor workspace is the local directory of a single-user
	// process, not tenant data; Cluster never mounts it.
	"GET /ide":                          "Lite editor shell",
	"GET /ide/vs/*filepath":             "Lite editor assets",
	"HEAD /ide/vs/*filepath":            "Lite editor assets",
	"GET /api/v2/ide/tree":              "Lite editor workspace",
	"GET /api/v2/ide/file":              "Lite editor workspace",
	"PUT /api/v2/ide/file":              "Lite editor workspace",
	"POST /api/v2/ide/file":             "Lite editor workspace",
	"DELETE /api/v2/ide/file":           "Lite editor workspace",
	"POST /api/v2/ide/move":             "Lite editor workspace",
	"POST /api/v2/ide/examples/install": "Lite editor workspace",
}

// xtWriteBodies is the body a write is sent with: one the handler would
// accept from B for B's own object, so A's refusal comes from the tenant
// check and not from validation. Routes absent here are sent without a body,
// which is right for DELETE and for the routes that read none.
func xtWriteBodies(f *routeWalkFixture) map[string]string {
	spec := f.spec("v9")
	return map[string]string{
		"PATCH /api/v2/dags/:dag_id":                                                    `{"is_paused":true}`,
		"POST /api/v2/dags/:dag_id/dagRuns":                                             `{"logical_date":null,"conf":{}}`,
		"PATCH /api/v2/dags/:dag_id/dagRuns/:dag_run_id":                                `{"state":"failed"}`,
		"PATCH /api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id":         `{"new_state":"failed"}`,
		"PATCH /api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id/*action": `{"new_state":"failed"}`,
		"POST /api/v2/dags/:dag_id/clearTaskInstances": fmt.Sprintf(
			`{"dry_run":false,"task_ids":["t1"],"dag_run_id":%q,"only_failed":false}`, f.runID),
		"POST /api/v2/dags/:dag_id/dagRuns/:dag_run_id/clear": `{"dry_run":false}`,
		"POST /api/v2/dags/:dag_id/versions":                  spec,
		"POST /api/v2/dags/:dag_id/versions/dry_run":          spec,
		"PUT /api/v2/dags/:dag_id/currentVersion":             `{"version_number":1}`,
		"PATCH /api/v2/variables/:variable_key":               fmt.Sprintf(`{"key":%q,"value":"changed-by-a"}`, f.variable),
		"PATCH /api/v2/connections/:connection_id": fmt.Sprintf(
			`{"connection_id":%q,"conn_type":"http","host":"changed-by-a"}`, f.connection),
		"PATCH /api/v2/pools/:pool_name": fmt.Sprintf(`{"name":%q,"slots":99,"description":"changed-by-a"}`, f.pool),
		"POST /api/v2/variables":         fmt.Sprintf(`{"key":%q,"value":"owned-by-a"}`, f.variable),
		"POST /api/v2/connections": fmt.Sprintf(
			`{"connection_id":%q,"conn_type":"http","host":"owned-by-a"}`, f.connection),
		"POST /api/v2/pools":           fmt.Sprintf(`{"name":%q,"slots":2,"description":"owned-by-a"}`, f.pool),
		"POST /api/v2/users":           fmt.Sprintf(`{"username":%q,"email":%q,"password":"a-Strong-pass-123","roles":["viewer"]}`, f.userEmail, f.userEmail),
		"POST /api/v2/apiTokens":       fmt.Sprintf(`{"name":%q,"scope":"read","expires_in_seconds":3600}`, f.tokenName),
		"PATCH /api/v2/users/:user_id": `{"is_active":false}`,
		"PUT /api/v2/importErrors":     fmt.Sprintf(`{"filename":%q,"stack_trace":"owned-by-a"}`, f.importFile),
	}
}

// ownTenantWrites are the writes that may succeed for A: they create or
// change A's own object under a name B also uses, or they are idempotent
// and report success whether or not A had anything to change. The final
// check proves B's objects survived them.
var ownTenantWrites = map[string]bool{
	"POST /api/v2/dags/:dag_id/versions":         true,
	"POST /api/v2/dags/:dag_id/versions/dry_run": true,
	"POST /api/v2/variables":                     true,
	"POST /api/v2/connections":                   true,
	"POST /api/v2/pools":                         true,
	"POST /api/v2/users":                         true,
	"POST /api/v2/apiTokens":                     true,
	"PUT /api/v2/importErrors":                   true,
	"DELETE /api/v2/importErrors":                true,
	// Favorites are rows of A's own tenant keyed by a DAG id string, so A's
	// favorite of B's id changes nothing of B and shows A nothing of B.
	"POST /api/v2/dags/:dag_id/favorite":   true,
	"POST /api/v2/dags/:dag_id/unfavorite": true,
}

// taskInstanceActions are the sub-resources the task instance catch-all
// serves; the walk calls each one.
var taskInstanceActions = []string{
	"logs/1", "tries", "tries/1", "links", "xcomEntries", "xcomEntries/xt_key", "-1", "-1/tries",
}

// routeWalkFixture holds tenant B's objects, the servers of A and B over the
// same database, and the markers that must never reach A.
type routeWalkFixture struct {
	repo       *storage.Repository
	pg         *storage.Postgres
	ctx        context.Context
	a, b       *gin.Engine
	tenantA    string
	tenantB    string
	sfx        string
	marker     string // a value only B's objects carry
	dagID      string
	runID      string
	runUUID    string
	variable   string
	connection string
	pool       string
	importFile string
	userEmail  string
	tokenName  string
	tokenID    string
	userID     string
	xcomKey    string
}

func (f *routeWalkFixture) spec(version string) string {
	return fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":%q,"image":"img:%s",`+
		`"description":%q,"tags":[%q],`+
		`"tasks":[{"task_id":"t1","type":"python","entrypoint":"dag:t1"},`+
		`{"task_id":"t2","type":"python","entrypoint":"dag:t2","depends_on":["t1"]}]}`,
		f.dagID, version, version, "desc "+f.marker, "tag_"+f.marker)
}

func newRouteWalkFixture(t *testing.T) *routeWalkFixture {
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
		key[i] = byte(i + 7)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo := storage.NewRepository(pg, storage.WithCipher(cipher))
	sfx := fmt.Sprintf("%d", time.Now().UnixNano())
	f := &routeWalkFixture{
		repo: repo, pg: pg, ctx: ctx, sfx: sfx,
		tenantA: "rw_a_" + sfx, tenantB: "rw_b_" + sfx,
		marker:     "secretb" + sfx,
		dagID:      "rw_dag_" + sfx,
		runID:      "rw_run_" + sfx,
		variable:   "rw_var_" + sfx,
		connection: "rw_conn_" + sfx,
		pool:       "rw_pool_" + sfx,
		importFile: "dags/rw_broken_" + sfx + ".py",
		userEmail:  "rw_user_" + sfx + "@example.com",
		tokenName:  "rw_token_" + sfx,
		xcomKey:    "xt_key",
	}
	users := map[string]*auth.User{}
	for _, name := range []string{f.tenantA, f.tenantB} {
		if _, err = repo.EnsureTenant(ctx, name, name, 0, domain.TenantLimitsUpdate{}); err != nil {
			t.Fatalf("creating tenant %s: %v", name, err)
		}
		u, uerr := repo.CreateUser(ctx, name, "admin_"+name+"@example.com", "a-Strong-pass-123", []string{"admin"})
		if uerr != nil {
			t.Fatalf("creating the admin of %s: %v", name, uerr)
		}
		users[name] = &auth.User{ID: u.ID, TenantID: name, Email: u.Email, Roles: []string{"admin"}}
	}
	logDir := t.TempDir()
	sink := logs.NewDiskSink(logDir)
	xcomBackend := xcom.NewPostgresBackend(pg.Pool)
	server := func(tenant string) *gin.Engine {
		ws, _ := newWorkspace(t)
		return newTestServer(Dependencies{
			Observability: ObservabilityDeps{Logger: discardLogger()},
			Server:        ServerDeps{CORSOrigins: []string{"*"}, Pools: true},
			Auth: AuthDeps{
				Authenticator: &fakeAuthn{user: users[tenant]},
				RateLimiter:   auth.NewRateLimiter(1000, time.Minute),
				TokenTTLSecs:  3600,
				AuthAudit:     repo,
			},
			UI: UIDeps{Workspace: ws, ExamplesFS: fstest.MapFS{}},
			Stores: StoreDeps{
				Dags:           repo,
				DagRuns:        repo,
				Tasks:          repo,
				Versions:       repo,
				Xcoms:          storage.NewXComReader(pg, xcomBackend),
				Logs:           storage.NewLogReader(pg, sink, nil),
				Specs:          repo,
				DagSources:     repo,
				LatestRuns:     repo,
				TaskSummary:    repo,
				DagVersions:    repo,
				DashboardStats: repo,
				AuditLog:       repo,
				Variables:      repo,
				Users:          repo,
				UserAudit:      repo,
				APITokens:      repo,
				Connections:    repo,
				Pools:          repo,
				Favorites:      repo,
				ImportErrors:   repo,
				Audit:          repo,
			},
		})
	}
	f.a, f.b = server(f.tenantA), server(f.tenantB)
	f.seedB(t, sink, xcomBackend)
	return f
}

// seedB gives tenant B one of every object the API addresses by id.
func (f *routeWalkFixture) seedB(t *testing.T, sink logs.Sink, backend xcom.Backend) {
	t.Helper()
	b := f.b
	xtExpect(t, b, http.MethodPost, "/api/v2/dags/"+f.dagID+"/versions", f.spec("v1"), http.StatusCreated)
	if _, err := f.repo.CreateDagRun(f.ctx, f.tenantB, f.dagID, domain.DagRun{
		RunID: f.runID, State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("creating B's run: %v", err)
	}
	if err := f.pg.Pool.QueryRow(f.ctx, `SELECT r.id::text FROM dag_runs r JOIN dags d ON d.id = r.dag_id
		JOIN tenants tn ON tn.id = d.tenant_id WHERE tn.name = $1 AND d.dag_id = $2 AND r.run_id = $3`,
		f.tenantB, f.dagID, f.runID).Scan(&f.runUUID); err != nil {
		t.Fatalf("reading B's run id: %v", err)
	}
	sched := storage.NewSchedulerStore(f.pg)
	if err := sched.MaterializeTasks(f.ctx, f.runUUID, []domain.TaskSpec{
		{TaskID: "t1", Type: domain.TaskTypePython}, {TaskID: "t2", Type: domain.TaskTypePython},
	}); err != nil {
		t.Fatalf("materializing B's tasks: %v", err)
	}
	if _, err := f.pg.Pool.Exec(f.ctx, `UPDATE task_instances SET state = 'success', try_number = 1,
		started_at = now() - interval '1 minute', ended_at = now() WHERE dag_run_id = $1::uuid AND task_id = 't1'`, f.runUUID); err != nil {
		t.Fatalf("finishing B's t1: %v", err)
	}
	tenantUUID, err := f.repo.TenantUUID(f.ctx, f.tenantB)
	if err != nil {
		t.Fatal(err)
	}
	w, err := sink.Open(logs.Ref{TenantID: tenantUUID, DagID: f.dagID, RunID: f.runUUID, TaskID: "t1", TryNumber: 1})
	if err != nil {
		t.Fatalf("opening B's log: %v", err)
	}
	if err = w.WriteEvent(logs.Event{Time: time.Now(), Level: "info", Stream: "stdout", Message: "log " + f.marker}); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	svc := xcom.NewService(backend, storage.NewXComIndex(f.pg), xcom.DefaultTTL)
	if err = svc.Push(f.ctx, xcom.Key{TenantID: tenantUUID, DagID: f.dagID, RunID: f.runUUID, TaskID: "t1", Name: f.xcomKey},
		[]byte(fmt.Sprintf(`{"v":%q}`, "xcom "+f.marker)), "application/json", nil); err != nil {
		t.Fatalf("pushing B's xcom: %v", err)
	}
	xtExpect(t, b, http.MethodPost, "/api/v2/variables",
		fmt.Sprintf(`{"key":%q,"value":%q}`, f.variable, "var "+f.marker), http.StatusCreated)
	xtExpect(t, b, http.MethodPost, "/api/v2/connections",
		fmt.Sprintf(`{"connection_id":%q,"conn_type":"http","host":%q}`, f.connection, "conn"+f.marker), http.StatusCreated)
	xtExpect(t, b, http.MethodPost, "/api/v2/pools",
		fmt.Sprintf(`{"name":%q,"slots":3,"description":%q}`, f.pool, "pool "+f.marker), http.StatusCreated)
	xtExpect(t, b, http.MethodPut, "/api/v2/importErrors",
		fmt.Sprintf(`{"filename":%q,"stack_trace":%q}`, f.importFile, "trace "+f.marker), http.StatusNoContent)
	xtExpect(t, b, http.MethodPost, "/api/v2/dags/"+f.dagID+"/favorite", "", http.StatusNoContent)
	u, err := f.repo.CreateUser(f.ctx, f.tenantB, f.userEmail, "a-Strong-pass-123", []string{"viewer"})
	if err != nil {
		t.Fatalf("creating B's user: %v", err)
	}
	f.userID = u.ID
	tok := xtExpect(t, b, http.MethodPost, "/api/v2/apiTokens",
		fmt.Sprintf(`{"name":%q,"scope":"read","expires_in_seconds":3600}`, f.tokenName), http.StatusCreated)
	f.tokenID = xtField(t, tok, "id")
	if f.tokenID == "" {
		t.Fatalf("B's token has no id: %s", tok)
	}
	if err = f.repo.RecordTaskActionAudit(f.ctx, f.tenantB, "u-b", "clear", f.dagID, f.runID, "t1", 1); err != nil {
		t.Fatalf("recording B's audit entry: %v", err)
	}
}

// params maps each route parameter to B's object; a route with a parameter
// missing here fails the walk.
func (f *routeWalkFixture) params() map[string]string {
	return map[string]string{
		":dag_id":         f.dagID,
		":dag_run_id":     f.runID,
		":run_id":         f.runID,
		":task_id":        "t1",
		":key":            f.xcomKey,
		":variable_key":   f.variable,
		":connection_id":  f.connection,
		":pool_name":      f.pool,
		":token_id":       f.tokenID,
		":version_number": "1",
		":asset_id":       "1",
		":group_id":       "g1",
		":file_token":     "x",
		":user_id":        f.userID,
	}
}

// bIDs are the names of B's objects that A's answers must not carry, except
// where A sent the name itself in the path.
func (f *routeWalkFixture) bIDs() []string {
	return []string{f.dagID, f.runID, f.runUUID, f.variable, f.connection, f.pool, f.importFile, f.userEmail, f.tokenName}
}

// queries are the query strings a route needs to reach its lookup.
func (f *routeWalkFixture) queries() map[string]string {
	now := time.Now().UTC()
	return map[string]string{
		"DELETE /api/v2/importErrors":      "?filename=" + url.QueryEscape(f.importFile),
		"GET /ui/structure/structure_data": "?dag_id=" + url.QueryEscape(f.dagID),
		"GET /ui/dashboard/historical_metrics_data": "?start_date=" + url.QueryEscape(now.Add(-time.Hour).Format(time.RFC3339)) +
			"&end_date=" + url.QueryEscape(now.Add(time.Hour).Format(time.RFC3339)),
	}
}

type walkCall struct {
	key, method, path, body string
}

// calls expands one route into the requests the walk sends.
func (f *routeWalkFixture) calls(t *testing.T, ri gin.RouteInfo, bodies map[string]string) []walkCall {
	t.Helper()
	key := ri.Method + " " + ri.Path
	segs := strings.Split(ri.Path, "/")
	wildcard := -1
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"):
			v, ok := f.params()[s]
			if !ok {
				t.Errorf("%s: the walk has no value for %s; add it to params()", key, s)
				return nil
			}
			segs[i] = url.PathEscape(v)
		case strings.HasPrefix(s, "*"):
			wildcard = i
		}
	}
	body := bodies[key]
	if ri.Method != http.MethodGet && ri.Method != http.MethodDelete && ri.Method != http.MethodHead && body == "" &&
		!strings.Contains(ri.Path, "favorite") && !strings.HasSuffix(ri.Path, "/clear") {
		body = "{}"
	}
	query := f.queries()[key]
	if wildcard < 0 {
		return []walkCall{{key, ri.Method, strings.Join(segs, "/") + query, body}}
	}
	out := make([]walkCall, 0, len(taskInstanceActions))
	for _, action := range taskInstanceActions {
		if ri.Method == http.MethodPatch && action != "-1" {
			continue
		}
		s := append([]string(nil), segs...)
		s[wildcard] = action
		out = append(out, walkCall{key, ri.Method, strings.Join(s, "/") + query, body})
	}
	return out
}

// refused reports an answer that gives A nothing: not found, forbidden, or a
// feature Dexaflow does not serve.
func refused(code int) bool {
	return code == http.StatusNotFound || code == http.StatusForbidden || code == http.StatusNotImplemented
}

// checkNoLeak fails when an answer to A carries B's marker, or names one of
// B's objects that A did not name in the request.
func (f *routeWalkFixture) checkNoLeak(t *testing.T, c walkCall, code int, body string) {
	t.Helper()
	if strings.Contains(body, f.marker) {
		t.Errorf("%s %s = %d carries B's data: %s", c.method, c.path, code, clip(body))
	}
	for _, id := range f.bIDs() {
		if strings.Contains(body, id) && !strings.Contains(c.path, url.PathEscape(id)) && !strings.Contains(c.path, url.QueryEscape(id)) &&
			!strings.Contains(c.body, id) {
			t.Errorf("%s %s = %d names B's %s: %s", c.method, c.path, code, id, clip(body))
		}
	}
}

func clip(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

// phase orders the walk so no write changes what a later call sees: reads,
// then writes that must miss, then deletes, then the writes that create A's
// own object under B's names.
func phase(ri gin.RouteInfo) int {
	key := ri.Method + " " + ri.Path
	switch {
	case ri.Method == http.MethodGet || ri.Method == http.MethodHead:
		return 0
	case ownTenantWrites[key] && ri.Method != http.MethodDelete:
		return 3
	case ri.Method == http.MethodDelete:
		return 2
	default:
		return 1
	}
}

// TestTenantIsolationEveryRouteIntegration walks the whole route table as
// tenant A against tenant B's objects (#2315).
func TestTenantIsolationEveryRouteIntegration(t *testing.T) {
	f := newRouteWalkFixture(t)
	bodies := xtWriteBodies(f)
	routes := f.a.Routes()
	sort.SliceStable(routes, func(i, j int) bool { return phase(routes[i]) < phase(routes[j]) })

	walked := map[string]bool{}
	for _, ri := range routes {
		key := ri.Method + " " + ri.Path
		if _, skip := notTenantScoped[key]; skip || isPublic(ri.Path) {
			continue
		}
		walked[key] = true
		for _, c := range f.calls(t, ri, bodies) {
			// Assert the ids are live for B, so A's refusal means something.
			if ri.Method == http.MethodGet {
				own := authGet(f.b, c.method, c.path, c.body)
				if (own.Code < 200 || own.Code > 299) && own.Code != http.StatusNotImplemented {
					t.Errorf("B's own %s %s = %d (%s): the fixture does not exercise this route", c.method, c.path, own.Code, clip(own.Body.String()))
				}
			}
			rec := authGet(f.a, c.method, c.path, c.body)
			body := rec.Body.String()
			switch {
			case refused(rec.Code):
			case rec.Code >= 200 && rec.Code < 300:
				isRead := ri.Method == http.MethodGet || ri.Method == http.MethodHead
				if !isRead && !ownTenantWrites[key] {
					t.Errorf("%s %s by A = %d on B's object, want 404 or 403 (%s)", c.method, c.path, rec.Code, clip(body))
				}
				f.checkNoLeak(t, c, rec.Code, body)
			default:
				t.Errorf("%s %s by A = %d (%s): want a refusal or a 2xx without B's data; a 4xx from validation means the walk's body or parameters do not reach the tenant check", c.method, c.path, rec.Code, clip(body))
			}
		}
	}
	for key := range notTenantScoped {
		if walked[key] {
			t.Errorf("notTenantScoped names %s, which the walk also called", key)
		}
	}
	for key := range bodies {
		if !walked[key] {
			t.Errorf("xtWriteBodies names %s, which is not a walked route", key)
		}
	}
	f.assertBIntact(t)
}

// assertBIntact reads every object of B through B's own server after A's
// walk: none was removed or changed.
func (f *routeWalkFixture) assertBIntact(t *testing.T) {
	t.Helper()
	b := f.b
	dag := xtExpect(t, b, http.MethodGet, "/api/v2/dags/"+f.dagID, "", http.StatusOK)
	if !strings.Contains(dag, `"is_paused":false`) {
		t.Errorf("B's DAG changed after A's walk: %s", dag)
	}
	if !strings.Contains(dag, f.marker) {
		t.Errorf("B's DAG lost its description after A's walk: %s", dag)
	}
	versions := xtExpect(t, b, http.MethodGet, "/api/v2/dags/"+f.dagID+"/dagVersions", "", http.StatusOK)
	if strings.Contains(versions, `"v9"`) {
		t.Errorf("A's register landed in B's DAG: %s", versions)
	}
	run := xtExpect(t, b, http.MethodGet, "/api/v2/dags/"+f.dagID+"/dagRuns/"+f.runID, "", http.StatusOK)
	if xtField(t, run, "state") != "running" {
		t.Errorf("B's run changed after A's walk: %s", run)
	}
	ti := xtExpect(t, b, http.MethodGet, "/api/v2/dags/"+f.dagID+"/dagRuns/"+f.runID+"/taskInstances/t1", "", http.StatusOK)
	if xtField(t, ti, "state") != "success" {
		t.Errorf("B's task instance changed after A's walk: %s", ti)
	}
	if v := xtField(t, xtExpect(t, b, http.MethodGet, "/api/v2/variables/"+f.variable, "", http.StatusOK), "value"); v != "var "+f.marker {
		t.Errorf("B's variable after A's walk = %q", v)
	}
	if h := xtField(t, xtExpect(t, b, http.MethodGet, "/api/v2/connections/"+f.connection, "", http.StatusOK), "host"); h != "conn"+f.marker {
		t.Errorf("B's connection after A's walk = %q", h)
	}
	if d := xtField(t, xtExpect(t, b, http.MethodGet, "/api/v2/pools/"+f.pool, "", http.StatusOK), "description"); d != "pool "+f.marker {
		t.Errorf("B's pool after A's walk = %q", d)
	}
	if body := xtExpect(t, b, http.MethodGet, "/api/v2/importErrors", "", http.StatusOK); !strings.Contains(body, f.marker) {
		t.Errorf("B's import error is gone after A's walk: %s", clip(body))
	}
	if body := xtExpect(t, b, http.MethodGet, "/api/v2/users?limit=1000", "", http.StatusOK); !strings.Contains(body, f.userEmail) {
		t.Errorf("B's user is gone after A's walk: %s", clip(body))
	}
	var active bool
	if err := f.pg.Pool.QueryRow(f.ctx, `SELECT is_active FROM users WHERE id = $1::uuid`, f.userID).Scan(&active); err != nil || !active {
		t.Errorf("B's user after A's walk: active=%v err=%v, want still active", active, err)
	}
	if body := xtExpect(t, b, http.MethodGet, "/api/v2/apiTokens", "", http.StatusOK); !strings.Contains(body, f.tokenID) {
		t.Errorf("B's API token is gone after A's walk: %s", clip(body))
	}
	if body := xtExpect(t, b, http.MethodGet, "/api/v2/dags/"+f.dagID+"/details", "", http.StatusOK); !strings.Contains(body, `"is_favorite":true`) {
		t.Errorf("B's favorite is gone after A's walk: %s", clip(body))
	}
	xc := xtExpect(t, b, http.MethodGet, "/api/v2/xcoms/"+f.dagID+"/"+f.runID+"/t1/"+f.xcomKey, "", http.StatusOK)
	if !strings.Contains(xc, f.marker) {
		t.Errorf("B's XCom is gone after A's walk: %s", clip(xc))
	}
	logBody := xtExpect(t, b, http.MethodGet, "/api/v2/dags/"+f.dagID+"/dagRuns/"+f.runID+"/taskInstances/t1/logs/1", "", http.StatusOK)
	if !strings.Contains(logBody, f.marker) {
		t.Errorf("B's log is unreadable after A's walk: %s", clip(logBody))
	}
}
