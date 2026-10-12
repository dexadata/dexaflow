//go:build integration

package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/mcp"
	"github.com/dexadata/dexaflow/internal/storage"
	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// This file proves ADR 0050's security posture (#508) end to end (#1469): the
// HTTP MCP is safe on a shared engine because identity is passed through and
// /api/v2 scopes every read to the caller's tenant. Two tenants share one real
// Postgres and one API server. Both own a DAG with the same id and a run with
// the same run id, so a read that ignores the tenant would return the other
// tenant's rows instead of failing. Tenant B also owns a DAG and a task that
// tenant A has no counterpart for. A token of tenant A then calls every tool
// and reads every resource the MCP server registers.

const isolationSecret = "tenant-isolation-test-secret"

// seededData is in every seeded log line and task error, and in no request.
const seededData = "SEEDED-DATA"

// isolationFixture is two seeded tenants behind a real API server, and an MCP
// session of tenant A's admin talking to it over the HTTP transport.
type isolationFixture struct {
	sess *mcpsdk.ClientSession
	// shared is the DAG id both tenants own; aOnly and bOnly belong to one.
	shared, aOnly, bOnly string
	// bTask is a task only tenant B's shared DAG has.
	bTask string
	// aMarker is in tenant A's log, source and run conf.
	aMarker string
	// bMarkers are strings only tenant B's data holds; none may reach A.
	bMarkers []string
	// sessB is tenant B's admin, for the cases that need something only B
	// can make (a plan); repo and tenantB read B's state back directly.
	sessB   *mcpsdk.ClientSession
	repo    *storage.Repository
	tenantB string
}

func newIsolationFixture(t *testing.T) *isolationFixture {
	t.Helper()
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
	sink := logs.NewDiskSink(t.TempDir())

	n := time.Now().UnixNano()
	f := &isolationFixture{
		shared:  fmt.Sprintf("iso_shared_%d", n),
		aOnly:   fmt.Sprintf("iso_a_only_%d", n),
		bOnly:   fmt.Sprintf("iso_b_only_%d", n),
		bTask:   fmt.Sprintf("b_secret_task_%d", n),
		aMarker: fmt.Sprintf("A-MARKER-%d", n),
	}
	tenantA, tenantB := fmt.Sprintf("iso-a-%d", n), fmt.Sprintf("iso-b-%d", n)
	bMarker := fmt.Sprintf("B-MARKER-%d", n)
	f.bMarkers = []string{tenantB, bMarker, f.bTask, f.bOnly}

	seed := isolationSeed{t: t, ctx: ctx, pg: pg, repo: repo, sink: sink}
	seed.tenant(tenantA, f.aMarker, "failed", f.shared, f.aOnly, "load")
	seed.tenant(tenantB, bMarker, "success", f.shared, f.bOnly, "load", f.bTask)
	user, err := repo.CreateUser(ctx, tenantA, fmt.Sprintf("admin-%d@a.example", n), "pw-not-used-here", []string{"admin"})
	if err != nil {
		t.Fatalf("create tenant A admin: %v", err)
	}

	authn := auth.NewJWTAuthenticator(repo, isolationSecret, time.Hour)
	apiSrv := httptest.NewServer(api.NewServer(api.Dependencies{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Authenticator: authn,
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		TokenTTLSecs:  3600,
		HealthChecks:  map[string]api.HealthChecker{},
		Dags:          repo,
		DagRuns:       repo,
		Tasks:         repo,
		Versions:      repo,
		Specs:         repo,
		DagVersions:   repo,
		Logs:          storage.NewLogReader(pg, sink, nil),
	}))
	t.Cleanup(apiSrv.Close)

	// The token is minted the way a login mints it, and every request reloads
	// the user from the database, as in production.
	token, err := auth.MintUserToken(isolationSecret, time.Hour, auth.User{ID: user.ID, TenantID: tenantA, Email: user.Email, Roles: user.Roles})
	if err != nil {
		t.Fatal(err)
	}
	f.sess = connectMCP(t, apiSrv.URL, token)

	userB, err := repo.CreateUser(ctx, tenantB, fmt.Sprintf("admin-%d@b.example", n), "pw-not-used-here", []string{"admin"})
	if err != nil {
		t.Fatalf("create tenant B admin: %v", err)
	}
	tokenB, err := auth.MintUserToken(isolationSecret, time.Hour, auth.User{ID: userB.ID, TenantID: tenantB, Email: userB.Email, Roles: userB.Roles})
	if err != nil {
		t.Fatal(err)
	}
	f.sessB, f.repo, f.tenantB = connectMCP(t, apiSrv.URL, tokenB), repo, tenantB
	return f
}

// bState renders what tenant A's run control must never change in tenant B:
// its own DAG's paused flag and run count, and the task instances of both of
// its r1 runs.
func (f *isolationFixture) bState(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dag, err := f.repo.GetDag(ctx, f.tenantB, f.bOnly)
	if err != nil {
		t.Fatalf("read tenant B's DAG: %v", err)
	}
	_, runs, err := f.repo.ListDagRuns(ctx, f.tenantB, f.bOnly, 100, 0)
	if err != nil {
		t.Fatalf("read tenant B's runs: %v", err)
	}
	state := fmt.Sprintf("paused=%v runs=%d", dag.IsPaused, runs)
	for _, dagID := range []string{f.shared, f.bOnly} {
		tis, _, err := f.repo.ListTaskInstances(ctx, f.tenantB, dagID, "r1", 100, 0)
		if err != nil {
			t.Fatalf("read tenant B's task instances: %v", err)
		}
		for _, ti := range tis {
			state += fmt.Sprintf(" %s/%s=%s#%d", dagID, ti.TaskID, ti.State, ti.TryNumber)
		}
	}
	return state
}

// assertBUnchanged runs act, then fails if tenant B's state moved.
func (f *isolationFixture) assertBUnchanged(t *testing.T, act func()) {
	t.Helper()
	before := f.bState(t)
	act()
	if after := f.bState(t); after != before {
		t.Errorf("tenant B changed:\n before %s\n after  %s", before, after)
	}
}

// isolationSeed writes one tenant's data straight through the repository, the
// way a deploy and the scheduler would leave it.
type isolationSeed struct {
	t    *testing.T
	ctx  context.Context
	pg   *storage.Postgres
	repo *storage.Repository
	sink logs.Sink
}

// tenant creates the tenant, the shared DAG and its own DAG, each with a run
// r1 in runState, every task failed on try 1 with marker in its log, error,
// source and run conf. The run states differ between the tenants because a
// run's detail carries nothing else that tells the two r1 runs apart.
func (s isolationSeed) tenant(name, marker, runState, shared, own string, tasks ...string) {
	s.t.Helper()
	if _, err := s.repo.EnsureTenant(s.ctx, name, name, 0, domain.TenantLimitsUpdate{}); err != nil {
		s.t.Fatalf("create tenant %s: %v", name, err)
	}
	tenantUUID, err := s.repo.TenantUUID(s.ctx, name)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, dagID := range []string{shared, own} {
		s.dag(name, tenantUUID, dagID, marker, runState, tasks)
	}
}

func (s isolationSeed) dag(tenant, tenantUUID, dagID, marker, runState string, tasks []string) {
	s.t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Description: "owned by " + marker,
		Source:      "# " + marker + "\nfrom airflow import DAG\n",
	}
	for _, task := range tasks {
		spec.Tasks = append(spec.Tasks, domain.TaskSpec{TaskID: task, Type: domain.TaskTypePython, Entrypoint: "dag:" + task})
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.repo.RegisterDagVersion(s.ctx, tenant, spec, hash); err != nil {
		s.t.Fatalf("register %s in %s: %v", dagID, tenant, err)
	}
	if _, err := s.repo.CreateDagRun(s.ctx, tenant, dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
		Conf: json.RawMessage(fmt.Sprintf(`{"owner":%q}`, marker)),
	}); err != nil {
		s.t.Fatalf("create run of %s in %s: %v", dagID, tenant, err)
	}
	var runUUID string
	if err := s.pg.Pool.QueryRow(s.ctx, `UPDATE dag_runs dr SET state = $3::dag_run_state, ended_at = now()
		FROM dags d WHERE d.id = dr.dag_id AND dr.tenant_id = $1::uuid AND d.dag_id = $2 AND dr.run_id = 'r1'
		RETURNING dr.id::text`, tenantUUID, dagID, runState).Scan(&runUUID); err != nil {
		s.t.Fatalf("fail run of %s in %s: %v", dagID, tenant, err)
	}
	for _, task := range tasks {
		if _, err := s.pg.Pool.Exec(s.ctx, `INSERT INTO task_instances
			(tenant_id, dag_run_id, task_id, try_number, state, operator, error_message, ended_at)
			VALUES ($1::uuid, $2::uuid, $3, 1, 'failed', 'PythonOperator', $4, now())`,
			tenantUUID, runUUID, task, "boom "+seededData+" "+marker); err != nil {
			s.t.Fatalf("seed task %s of %s in %s: %v", task, dagID, tenant, err)
		}
		w, err := s.sink.Open(logs.Ref{TenantID: tenantUUID, DagID: dagID, RunID: runUUID, TaskID: task, TryNumber: 1})
		if err != nil {
			s.t.Fatal(err)
		}
		for _, line := range []string{"starting " + task, "ValueError: boom " + seededData + " " + marker, "done"} {
			if err := w.WriteEvent(logs.Event{Time: time.Now().UTC(), Level: "info", Stream: "stdout", Message: line}); err != nil {
				s.t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			s.t.Fatal(err)
		}
	}
}

// bearerTransport stamps the caller's bearer on every MCP HTTP request, as an
// MCP client configured with a token header does.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// connectMCP serves the MCP server over Streamable HTTP, the transport a
// shared engine exposes, and connects a client carrying token.
func connectMCP(t *testing.T, apiURL, token string) *mcpsdk.ClientSession {
	t.Helper()
	base, err := apiclient.New(apiURL, "")
	if err != nil {
		t.Fatal(err)
	}
	// A server option that registers more (a tool, resource or prompt behind
	// a flag) must be turned on here, so what it adds is enumerated and needs
	// a case too.
	srv := mcp.NewServer(base, apiURL, "test", true, isolationServerOptions()...)
	mcpHTTP := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return srv },
		&mcpsdk.StreamableHTTPOptions{Stateless: true},
	))
	t.Cleanup(mcpHTTP.Close)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "isolation-test", Version: "0"}, nil)
	sess, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{
		Endpoint:   mcpHTTP.URL,
		HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// outcome is what one MCP call returned: the whole result or error as text,
// whether it was an error, and the request, whose own identifiers may be
// echoed back.
type outcome struct {
	request string
	text    string
	isError bool
}

func (f *isolationFixture) callTool(t *testing.T, name string, args map[string]any) outcome {
	t.Helper()
	req, _ := json.Marshal(args)
	res, err := f.sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return outcome{request: string(req), text: err.Error(), isError: true}
	}
	body, _ := json.Marshal(res)
	return outcome{request: string(req), text: string(body), isError: res.IsError}
}

func (f *isolationFixture) readResource(t *testing.T, uri string) outcome {
	t.Helper()
	res, err := f.sess.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{URI: uri})
	if err != nil {
		return outcome{request: uri, text: err.Error(), isError: true}
	}
	body, _ := json.Marshal(res)
	return outcome{request: uri, text: string(body)}
}

func (f *isolationFixture) getPrompt(t *testing.T, name string, args map[string]string) outcome {
	t.Helper()
	req, _ := json.Marshal(map[string]any{"prompt": name, "arguments": args})
	res, err := f.sess.GetPrompt(context.Background(), &mcpsdk.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		return outcome{request: string(req), text: err.Error(), isError: true}
	}
	body, _ := json.Marshal(res)
	return outcome{request: string(req), text: string(body)}
}

// assertNoLeak fails when o carries anything only tenant B holds. An
// identifier the request itself named may be echoed back (a not-found error
// naming the DAG asked for leaks nothing).
func (f *isolationFixture) assertNoLeak(t *testing.T, o outcome) {
	t.Helper()
	for _, m := range f.bMarkers {
		if strings.Contains(o.request, m) {
			continue
		}
		if strings.Contains(o.text, m) {
			t.Errorf("request %s returned tenant B's %q:\n%s", o.request, m, o.text)
		}
	}
}

// assertOwnData fails unless o succeeded with tenant A's own data and nothing
// of tenant B's.
func (f *isolationFixture) assertOwnData(t *testing.T, o outcome) {
	t.Helper()
	if o.isError {
		t.Errorf("request %s failed, want tenant A's own data: %s", o.request, o.text)
	}
	if !strings.Contains(o.text, f.aMarker) {
		t.Errorf("request %s did not return tenant A's data (%s):\n%s", o.request, f.aMarker, o.text)
	}
	f.assertNoLeak(t, o)
}

// assertNothing fails unless o, a request for something only tenant B has,
// was refused, and carries nothing of tenant B. A successful answer is a
// failure even without seeded data in it: tenant B's run detail or spec holds
// no marker, so only the refusal itself proves the read stayed in tenant A.
func (f *isolationFixture) assertNothing(t *testing.T, o outcome) {
	t.Helper()
	if !o.isError {
		t.Errorf("request %s succeeded, want it refused:\n%s", o.request, o.text)
	}
	f.assertNoLeak(t, o)
}

// assertEmpty is assertNothing for a read whose control-plane answer to a
// missing attempt is an empty success rather than an error (a log): o must be
// refused or carry exactly the empty form, and nothing of tenant B.
func (f *isolationFixture) assertEmpty(t *testing.T, o outcome, emptyForm string) {
	t.Helper()
	if !o.isError && (!strings.Contains(o.text, emptyForm) || strings.Contains(o.text, seededData)) {
		t.Errorf("request %s returned data, want it refused or %q:\n%s", o.request, emptyForm, o.text)
	}
	f.assertNoLeak(t, o)
}

// The empty forms a missing log attempt comes back as.
const (
	emptyLog    = "No logs available for this attempt."
	emptySearch = `\"total_matches\":0`
)

// isolationCases has one entry per tool name, resource URI, resource template
// and prompt the MCP server registers. A new one without an entry fails
// TestMCPTenantIsolation, so nothing reaches a shared engine untested.
func isolationCases() map[string]func(*testing.T, *isolationFixture) {
	return map[string]func(*testing.T, *isolationFixture){
		"list_dags": func(t *testing.T, f *isolationFixture) {
			o := f.callTool(t, "list_dags", map[string]any{"limit": 200})
			f.assertNoLeak(t, o)
			if o.isError || !strings.Contains(o.text, f.aOnly) || !strings.Contains(o.text, f.shared) {
				t.Errorf("list_dags lacks tenant A's DAGs:\n%s", o.text)
			}
		},
		"diagnose_run": func(t *testing.T, f *isolationFixture) {
			f.assertOwnData(t, f.callTool(t, "diagnose_run", map[string]any{"dag_id": f.shared, "run_id": "r1"}))
			f.assertNothing(t, f.callTool(t, "diagnose_run", map[string]any{"dag_id": f.bOnly, "run_id": "r1"}))
		},
		"search_logs": func(t *testing.T, f *isolationFixture) {
			f.assertOwnData(t, f.callTool(t, "search_logs", map[string]any{"dag_id": f.shared, "run_id": "r1", "query": "boom"}))
			f.assertOwnData(t, f.callTool(t, "search_logs", map[string]any{"dag_id": f.shared, "run_id": "r1", "task_id": "load", "query": "boom"}))
			f.assertEmpty(t, f.callTool(t, "search_logs", map[string]any{"dag_id": f.shared, "run_id": "r1", "task_id": f.bTask, "query": "boom"}), emptySearch)
			f.assertNothing(t, f.callTool(t, "search_logs", map[string]any{"dag_id": f.bOnly, "run_id": "r1", "query": "boom"}))
		},
		"dag://list": func(t *testing.T, f *isolationFixture) {
			o := f.readResource(t, "dag://list")
			f.assertNoLeak(t, o)
			if o.isError || !strings.Contains(o.text, f.aOnly) {
				t.Errorf("dag://list lacks tenant A's DAGs:\n%s", o.text)
			}
		},
		"health://control-plane": func(t *testing.T, f *isolationFixture) {
			f.assertNoLeak(t, f.readResource(t, "health://control-plane"))
		},
		"run://detail/{dag_id}/{run_id}": func(t *testing.T, f *isolationFixture) {
			o := f.readResource(t, "run://detail/"+f.shared+"/r1")
			f.assertNoLeak(t, o)
			// Tenant A's r1 failed and tenant B's r1 succeeded.
			if o.isError || !strings.Contains(o.text, `\"state\":\"failed\"`) {
				t.Errorf("run://detail is not tenant A's failed run:\n%s", o.text)
			}
			f.assertNothing(t, f.readResource(t, "run://detail/"+f.bOnly+"/r1"))
		},
		"task://instances/{dag_id}/{run_id}": func(t *testing.T, f *isolationFixture) {
			o := f.readResource(t, "task://instances/"+f.shared+"/r1")
			f.assertNoLeak(t, o)
			if o.isError || !strings.Contains(o.text, "load") {
				t.Errorf("task://instances lacks tenant A's task load:\n%s", o.text)
			}
			f.assertNothing(t, f.readResource(t, "task://instances/"+f.bOnly+"/r1"))
		},
		"log://task/{dag_id}/{run_id}/{task_id}/{try_number}": func(t *testing.T, f *isolationFixture) {
			f.assertOwnData(t, f.readResource(t, "log://task/"+f.shared+"/r1/load/1"))
			f.assertEmpty(t, f.readResource(t, "log://task/"+f.shared+"/r1/"+f.bTask+"/1"), emptyLog)
			f.assertEmpty(t, f.readResource(t, "log://task/"+f.bOnly+"/r1/load/1"), emptyLog)
		},
		"dag://source/{dag_id}": func(t *testing.T, f *isolationFixture) {
			f.assertOwnData(t, f.readResource(t, "dag://source/"+f.shared))
			f.assertNothing(t, f.readResource(t, "dag://source/"+f.bOnly))
		},
		// Tenant A's r1 runs failed and tenant B's succeeded, so a prompt that
		// read B's runs would name B's DAG or count a success.
		promptCase + "diagnose_latest_failure": func(t *testing.T, f *isolationFixture) {
			o := f.getPrompt(t, "diagnose_latest_failure", nil)
			f.assertNoLeak(t, o)
			if o.isError || !strings.Contains(o.text, `run \"r1\"`) ||
				(!strings.Contains(o.text, f.shared) && !strings.Contains(o.text, f.aOnly)) {
				t.Errorf("diagnose_latest_failure did not pick tenant A's failed run:\n%s", o.text)
			}
			f.assertNothing(t, f.getPrompt(t, "diagnose_latest_failure", map[string]string{"dag_id": f.bOnly}))
		},
		promptCase + "pipeline_health_today": func(t *testing.T, f *isolationFixture) {
			o := f.getPrompt(t, "pipeline_health_today", nil)
			f.assertNoLeak(t, o)
			if o.isError || strings.Contains(o.text, "success") || !strings.Contains(o.text, f.aOnly) {
				t.Errorf("pipeline_health_today is not tenant A's runs alone:\n%s", o.text)
			}
		},
		// Run control: tenant A's token aimed at tenant B's ids is refused,
		// and tenant B's state does not move.
		"trigger_run": func(t *testing.T, f *isolationFixture) {
			f.assertBUnchanged(t, func() {
				f.assertNothing(t, f.callTool(t, "trigger_run", map[string]any{"dag_id": f.bOnly}))
			})
		},
		"pause_dag": func(t *testing.T, f *isolationFixture) {
			f.assertBUnchanged(t, func() {
				f.assertNothing(t, f.callTool(t, "pause_dag", map[string]any{"dag_id": f.bOnly}))
			})
		},
		"unpause_dag": func(t *testing.T, f *isolationFixture) {
			f.assertBUnchanged(t, func() {
				f.assertNothing(t, f.callTool(t, "unpause_dag", map[string]any{"dag_id": f.bOnly}))
			})
		},
		"clear_task": func(t *testing.T, f *isolationFixture) {
			f.assertBUnchanged(t, func() {
				// The control plane previews a clear of a DAG the caller's
				// tenant does not own as an empty set, not a 404.
				f.assertEmpty(t, f.callTool(t, "clear_task", map[string]any{"dag_id": f.bOnly, "run_id": "r1", "only_failed": false}), "Nothing to clear")
				// The shared DAG id resolves to tenant A's DAG, which has no
				// bTask: nothing to clear, and B's failed bTask stays failed.
				f.assertEmpty(t, f.callTool(t, "clear_task", map[string]any{"dag_id": f.shared, "run_id": "r1", "task_ids": []string{f.bTask}}), "Nothing to clear")
			})
		},
		"apply_plan": func(t *testing.T, f *isolationFixture) {
			// Tenant B plans a clear of its own two failed task instances;
			// tenant A, handed the plan_id, cannot apply it.
			res, err := f.sessB.CallTool(context.Background(), &mcpsdk.CallToolParams{
				Name: "clear_task", Arguments: map[string]any{"dag_id": f.shared, "run_id": "r1"},
			})
			if err != nil || res.IsError {
				t.Fatalf("tenant B's clear_task = %+v, %v; want a plan", res, err)
			}
			var planned struct {
				PlanID string `json:"plan_id"`
			}
			b, _ := json.Marshal(res.StructuredContent)
			if err := json.Unmarshal(b, &planned); err != nil || planned.PlanID == "" {
				t.Fatalf("tenant B's clear_task returned no plan_id: %s", b)
			}
			f.assertBUnchanged(t, func() {
				f.assertNothing(t, f.callTool(t, "apply_plan", map[string]any{"plan_id": planned.PlanID}))
			})
		},
		"dag://spec/{dag_id}": func(t *testing.T, f *isolationFixture) {
			o := f.readResource(t, "dag://spec/"+f.shared)
			f.assertNoLeak(t, o)
			if o.isError || !strings.Contains(o.text, "load") {
				t.Errorf("dag://spec lacks tenant A's spec:\n%s", o.text)
			}
			f.assertNothing(t, f.readResource(t, "dag://spec/"+f.bOnly))
		},
	}
}

// TestMCPTenantIsolation is #1469: a token of tenant A calls every MCP tool
// and reads every MCP resource with tenant B's identifiers, on one engine
// shared with tenant B, and gets nothing of B's.
func TestMCPTenantIsolation(t *testing.T) {
	f := newIsolationFixture(t)
	cases := isolationCases()

	registered := registeredNames(t, f.sess)
	var missing []string
	for _, name := range registered {
		if _, ok := cases[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("MCP tools or resources with no cross-tenant case: %v. Add one to isolationCases.", missing)
	}

	for _, name := range registered {
		t.Run(name, func(t *testing.T) { cases[name](t, f) })
	}
	for name := range cases {
		if !slices.Contains(registered, name) {
			t.Errorf("isolation case %q matches no registered tool or resource; remove or rename it", name)
		}
	}
}

// promptCase prefixes a prompt's name in isolationCases, so a prompt and a
// tool of the same name stay two cases.
const promptCase = "prompt:"

// isolationServerOptions turns on every option that registers more, so all
// of it is enumerated by TestMCPTenantIsolation.
func isolationServerOptions() []mcp.Option {
	return []mcp.Option{mcp.WithUIBaseURL("https://ui.example"), mcp.WithRunControl([]byte(isolationPlanKey))}
}

// isolationPlanKey signs run control plans; both tenants' sessions share it,
// as every replica of a shared engine does.
const isolationPlanKey = "tenant-isolation-plan-key-0123456789"

// registeredNames lists every tool name, resource URI, resource template and
// prompt (as promptCase + name) the server advertises to a client.
func registeredNames(t *testing.T, sess *mcpsdk.ClientSession) []string {
	t.Helper()
	ctx := context.Background()
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	resources, err := sess.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	templates, err := sess.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	prompts, err := sess.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	names := make([]string, 0, len(tools.Tools)+len(resources.Resources)+len(templates.ResourceTemplates)+len(prompts.Prompts))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	for _, r := range resources.Resources {
		names = append(names, r.URI)
	}
	for _, rt := range templates.ResourceTemplates {
		names = append(names, rt.URITemplate)
	}
	for _, p := range prompts.Prompts {
		names = append(names, promptCase+p.Name)
	}
	sort.Strings(names)
	return names
}
