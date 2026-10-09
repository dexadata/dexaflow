package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// jwtLike builds a bearer shaped like a JWT with these claims. The MCP never
// verifies tokens (the control plane does); it only reads the claims to bind a
// plan to its caller.
func jwtLike(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJFUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(b) + ".c2ln"
}

var (
	anaToken  = jwtLike(map[string]any{"iss": "portal", "sub": "ana", "tenant": "acme", "azp": "claude", "iat": 1000, "exp": 1300})
	anaLater  = jwtLike(map[string]any{"iss": "portal", "sub": "ana", "tenant": "acme", "azp": "claude", "iat": 1250, "exp": 1550})
	bobToken  = jwtLike(map[string]any{"iss": "portal", "sub": "bob", "tenant": "acme", "azp": "claude", "iat": 1000, "exp": 1300})
	anaOther  = jwtLike(map[string]any{"iss": "portal", "sub": "ana", "tenant": "globex", "azp": "claude", "iat": 1000, "exp": 1300})
	anaClient = jwtLike(map[string]any{"iss": "portal", "sub": "ana", "tenant": "acme", "azp": "chatgpt", "iat": 1000, "exp": 1300})
)

// plannerClaims is the caller every binding case plans as; each case changes
// one claim of a copy of it for the apply.
func plannerClaims() map[string]any {
	return map[string]any{
		"iss": "portal", "sub": "ana", "tenant_id": "acme", "client_id": "claude",
		"scope": "dexaflow:read dexaflow:run", "roles": []any{"Op"}, "email": "ana@acme.example",
		"sid": "s1", "iat": 1000, "exp": 1300, "jti": "j1",
	}
}

// TestPlanBindsIssuerSubjectTenantClientAndScope: a plan is bound to exactly
// (iss, sub, tenant, azp or client_id, scope set). Another value of any of
// them is refused; a change elsewhere in the token (roles, email, session, a
// reordered scope) still applies, since the control plane re-checks the
// caller's permissions on the apply call anyway.
func TestPlanBindsIssuerSubjectTenantClientAndScope(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		ok     bool
	}{
		{"same caller, refreshed", func(c map[string]any) { c["iat"], c["exp"], c["jti"] = 1250, 1550, "j2" }, true},
		{"roles changed", func(c map[string]any) { c["roles"] = []any{"Admin"} }, true},
		{"email and session changed", func(c map[string]any) { c["email"], c["sid"] = "ana@new.example", "s2" }, true},
		{"scope reordered", func(c map[string]any) { c["scope"] = "dexaflow:run  dexaflow:read" }, true},
		{"another issuer", func(c map[string]any) { c["iss"] = "other-portal" }, false},
		{"another subject", func(c map[string]any) { c["sub"] = "bob" }, false},
		{"another tenant", func(c map[string]any) { c["tenant_id"] = "globex" }, false},
		{"another client_id", func(c map[string]any) { c["client_id"] = "chatgpt" }, false},
		{"azp names another client", func(c map[string]any) { c["azp"] = "chatgpt" }, false},
		{"narrower scope", func(c map[string]any) { c["scope"] = "dexaflow:read" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := &controlPlane{dryRun: []string{twoFailed}}
			h := rcHandlers(t, cp, clock())
			_, plan, err := h.clearTask(context.Background(), callAs(jwtLike(plannerClaims())), clearTaskInput{DagID: "etl", RunID: "r1", TaskIDs: []string{"load"}, IncludeDownstream: true})
			if err != nil {
				t.Fatalf("clearTask: %v", err)
			}
			applier := plannerClaims()
			tc.mutate(applier)

			_, _, err = h.applyPlan(context.Background(), callAs(jwtLike(applier)), applyPlanInput{PlanID: plan.PlanID})

			if (err == nil) != tc.ok {
				t.Errorf("applyPlan error = %v, want applied %v", err, tc.ok)
			}
		})
	}
}

// TestPlanNeedsSubjectAndTenant: a caller without a subject or a tenant
// cannot be bound, so it cannot plan.
func TestPlanNeedsSubjectAndTenant(t *testing.T) {
	for _, missing := range []string{"sub", "tenant_id", "iss"} {
		claims := plannerClaims()
		delete(claims, missing)
		h := rcHandlers(t, &controlPlane{dryRun: []string{twoFailed}}, clock())

		_, _, err := h.clearTask(context.Background(), callAs(jwtLike(claims)), clearTaskInput{DagID: "etl", RunID: "r1", TaskIDs: []string{"load"}, IncludeDownstream: true})

		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("planned without %s (error %v), want a refusal naming it", missing, err)
		}
	}
}

// TestBearerSchemeIsCaseInsensitive: "bearer" in any case names the token,
// as RFC 6750 and the HTTP transport's challenge treat it.
func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	cp := &controlPlane{dryRun: []string{twoFailed}}
	h := rcHandlers(t, cp, clock())
	req := &mcpsdk.CallToolRequest{Extra: &mcpsdk.RequestExtra{Header: http.Header{"Authorization": []string{"bearer " + anaToken}}}}

	_, plan, err := h.clearTask(context.Background(), req, clearTaskInput{DagID: "etl", RunID: "r1", TaskIDs: []string{"load"}, IncludeDownstream: true})
	if err != nil || plan.PlanID == "" {
		t.Fatalf("clearTask with a lowercase scheme = %+v, %v", plan, err)
	}
	if got := cp.recorded()[0].Auth; got != "Bearer "+anaToken {
		t.Errorf("control plane got %q, want the token", got)
	}
	if _, _, err := h.applyPlan(context.Background(), callAs(anaToken), applyPlanInput{PlanID: plan.PlanID}); err != nil {
		t.Errorf("applyPlan after a lowercase-scheme plan: %v", err)
	}
}

// TestPlanVersionIsChecked: a plan carries its format version, and one of
// another version is refused even when its signature verifies.
func TestPlanVersionIsChecked(t *testing.T) {
	h := rcHandlers(t, &controlPlane{}, clock())
	caller, err := h.callerKey(callAs(anaToken))
	if err != nil {
		t.Fatal(err)
	}
	p := plan{Action: planUnpause, DagID: "etl", Schedule: "0 * * * *", Caller: caller, Expires: clock().Add(time.Minute).Unix()}
	current, err := sealPlan(h.planKey, p)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := openPlan(h.planKey, current, caller, *clock()); err != nil || got.Version != planVersion {
		t.Fatalf("openPlan(current) = %+v, %v; want version %d", got, err, planVersion)
	}
	raw, _ := json.Marshal(map[string]any{"v": planVersion + 1, "a": planUnpause, "d": "etl", "w": caller, "x": p.Expires})
	payload := base64.RawURLEncoding.EncodeToString(raw)
	future := payload + "." + base64.RawURLEncoding.EncodeToString(planMAC(h.planKey, payload))

	if _, err := openPlan(h.planKey, future, caller, *clock()); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("openPlan(version %d) = %v, want refused for its version", planVersion+1, err)
	}
}

// TestWithRunControlRefusesAShortKey: a key shorter than PlanKeyMinBytes
// (empty included) would let anyone sign plans, so it is a programming error.
func TestWithRunControlRefusesAShortKey(t *testing.T) {
	for _, key := range [][]byte{{}, testPlanKey[:PlanKeyMinBytes-1]} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("WithRunControl(%d-byte key) accepted", len(key))
				}
			}()
			WithRunControl(key)(&handlers{})
		}()
	}
}

type cpCall struct {
	Method, Path, Auth string
	Body               map[string]any
}

// controlPlane is a fake /api/v2 that records every call. dryRun answers the
// clear previews in order (the last one repeats); dag is GET /dags/etl.
type controlPlane struct {
	mu     sync.Mutex
	calls  []cpCall
	dryRun []string
	dag    string
}

func (cp *controlPlane) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		c := cpCall{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &c.Body)
		}
		cp.calls = append(cp.calls, c)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/dags/locked/dagRuns":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"title":"forbidden","status":403,"detail":"missing scope dexaflow:run"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/dags/etl/dagRuns":
			_, _ = io.WriteString(w, `{"dag_id":"etl","dag_run_id":"manual__1","state":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/dags/etl":
			_, _ = io.WriteString(w, cp.dag)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v2/dags/etl":
			paused, _ := c.Body["is_paused"].(bool)
			b, _ := json.Marshal(map[string]any{"dag_id": "etl", "is_paused": paused})
			_, _ = w.Write(b)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/dags/etl/clearTaskInstances":
			// Like the control plane, the clear answers with the task
			// instances it touched: the ones its last preview listed.
			_, _ = io.WriteString(w, cp.dryRun[0])
			if dry, _ := c.Body["dry_run"].(bool); dry && len(cp.dryRun) > 1 {
				cp.dryRun = cp.dryRun[1:]
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (cp *controlPlane) recorded() []cpCall {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return append([]cpCall(nil), cp.calls...)
}

func (cp *controlPlane) count(method, path string) int {
	n := 0
	for _, c := range cp.recorded() {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

const (
	twoFailed = `{"task_instances":[` +
		`{"dag_id":"etl","dag_run_id":"r1","task_id":"load","map_index":-1,"state":"failed","try_number":1},` +
		`{"dag_id":"etl","dag_run_id":"r1","task_id":"report","map_index":-1,"state":"upstream_failed","try_number":0}],"total_entries":2}`
	oneFailed = `{"task_instances":[` +
		`{"dag_id":"etl","dag_run_id":"r1","task_id":"load","map_index":-1,"state":"failed","try_number":1}],"total_entries":1}`
	noneToClear  = `{"task_instances":[],"total_entries":0}`
	scheduledDag = `{"dag_id":"etl","is_paused":true,"schedule_interval":{"__type":"CronExpression","value":"0 * * * *"}}`
)

var testPlanKey = []byte("0123456789abcdef0123456789abcdef")

// rcHandlers builds HTTP-transport handlers (per-request bearer) with run
// control on, against cp, with a settable clock.
func rcHandlers(t *testing.T, cp *controlPlane, now *time.Time) *handlers {
	t.Helper()
	srv := cp.serve(t)
	base, err := apiclient.New(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{api: base, serverURL: srv.URL, requireBearer: true}
	WithRunControl(testPlanKey)(h)
	withClock(func() time.Time { return *now })(h)
	WithUIBaseURL(testUIBase)(h)
	return h
}

func callAs(token string) *mcpsdk.CallToolRequest {
	return &mcpsdk.CallToolRequest{Extra: &mcpsdk.RequestExtra{Header: http.Header{"Authorization": []string{"Bearer " + token}}}}
}

func clock() *time.Time {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	return &now
}

// TestRunControlToolsAbsentByDefault: with the flag off the tools are not
// registered at all (ADR 0050 D7), and with it on they are, annotated.
func TestRunControlToolsAbsentByDefault(t *testing.T) {
	names := func(sess *mcpsdk.ClientSession) map[string]*mcpsdk.Tool {
		res, err := sess.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		out := map[string]*mcpsdk.Tool{}
		for _, tl := range res.Tools {
			out[tl.Name] = tl
		}
		return out
	}
	control := []string{"trigger_run", "clear_task", "pause_dag", "unpause_dag", "apply_plan"}

	off := names(connect(t, notFound))
	for _, n := range control {
		if _, ok := off[n]; ok {
			t.Errorf("%s registered with run control off", n)
		}
	}

	on := names(connect(t, notFound, WithRunControl(testPlanKey)))
	for _, n := range control {
		tl, ok := on[n]
		if !ok {
			t.Errorf("%s not registered with run control on", n)
			continue
		}
		if tl.Annotations == nil || tl.Annotations.ReadOnlyHint {
			t.Errorf("%s annotations = %+v, want a non-read-only tool", n, tl.Annotations)
		}
	}
	if _, ok := on["list_dags"]; !ok {
		t.Error("read tools must stay registered with run control on")
	}
	destructive := func(n string) bool {
		a := on[n].Annotations
		return a != nil && a.DestructiveHint != nil && *a.DestructiveHint
	}
	for n, want := range map[string]bool{"clear_task": true, "apply_plan": true, "trigger_run": false, "pause_dag": false, "unpause_dag": false} {
		if on[n] != nil && destructive(n) != want {
			t.Errorf("%s destructiveHint = %v, want %v", n, destructive(n), want)
		}
	}
	for n, want := range map[string]bool{"pause_dag": true, "unpause_dag": true, "trigger_run": false, "clear_task": false} {
		if on[n] != nil && on[n].Annotations.IdempotentHint != want {
			t.Errorf("%s idempotentHint = %v, want %v", n, on[n].Annotations.IdempotentHint, want)
		}
	}
}

func TestTriggerRun(t *testing.T) {
	cp := &controlPlane{}
	h := rcHandlers(t, cp, clock())

	_, out, err := h.triggerRun(context.Background(), callAs(anaToken), triggerRunInput{DagID: "etl", Conf: map[string]any{"day": "2026-10-08"}})
	if err != nil {
		t.Fatalf("triggerRun: %v", err)
	}
	calls := cp.recorded()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/api/v2/dags/etl/dagRuns" {
		t.Fatalf("calls = %+v, want one POST /api/v2/dags/etl/dagRuns", calls)
	}
	if calls[0].Auth != "Bearer "+anaToken {
		t.Errorf("control plane saw %q, want the caller's token", calls[0].Auth)
	}
	if conf, _ := calls[0].Body["conf"].(map[string]any); conf["day"] != "2026-10-08" {
		t.Errorf("trigger body = %v, want the conf passed through", calls[0].Body)
	}
	if !out.Done || out.RunID != "manual__1" || out.WebURL != testUIBase+"/dags/etl/runs/manual__1" {
		t.Errorf("output = %+v", out)
	}
}

// TestTriggerRunRefused: a refusal comes back as an error carrying the
// control plane's reason, so the model can tell the user what is missing.
func TestTriggerRunRefused(t *testing.T) {
	h := rcHandlers(t, &controlPlane{}, clock())
	_, _, err := h.triggerRun(context.Background(), callAs(anaToken), triggerRunInput{DagID: "locked"})
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "missing scope dexaflow:run") {
		t.Errorf("err = %v, want the 403 and its detail", err)
	}
}

func TestPauseDag(t *testing.T) {
	cp := &controlPlane{}
	h := rcHandlers(t, cp, clock())

	_, out, err := h.pauseDag(context.Background(), callAs(anaToken), dagInput{DagID: "etl"})
	if err != nil {
		t.Fatalf("pauseDag: %v", err)
	}
	calls := cp.recorded()
	if len(calls) != 1 || calls[0].Method != http.MethodPatch || calls[0].Body["is_paused"] != true || calls[0].Auth != "Bearer "+anaToken {
		t.Fatalf("calls = %+v, want one PATCH is_paused=true with the caller's token", calls)
	}
	if !out.Done || out.IsPaused == nil || !*out.IsPaused {
		t.Errorf("output = %+v", out)
	}
}

// TestUnpauseUnscheduledIsDirect: an unscheduled DAG starts nothing when
// unpaused, so there is no plan step.
func TestUnpauseUnscheduledIsDirect(t *testing.T) {
	cp := &controlPlane{dag: `{"dag_id":"etl","is_paused":true}`}
	h := rcHandlers(t, cp, clock())

	_, out, err := h.unpauseDag(context.Background(), callAs(anaToken), dagInput{DagID: "etl"})
	if err != nil {
		t.Fatalf("unpauseDag: %v", err)
	}
	if !out.Done || out.PlanID != "" || cp.count(http.MethodPatch, "/api/v2/dags/etl") != 1 {
		t.Errorf("output = %+v, calls = %+v; want a direct unpause", out, cp.recorded())
	}
}

// TestUnpauseAlreadyUnpausedIsDirect: a DAG that already runs on its
// schedule starts nothing new, so there is nothing to plan.
func TestUnpauseAlreadyUnpausedIsDirect(t *testing.T) {
	cp := &controlPlane{dag: strings.Replace(scheduledDag, `"is_paused":true`, `"is_paused":false`, 1)}
	h := rcHandlers(t, cp, clock())
	_, out, err := h.unpauseDag(context.Background(), callAs(anaToken), dagInput{DagID: "etl"})
	if err != nil {
		t.Fatalf("unpauseDag: %v", err)
	}
	if out.PlanID != "" || !out.Done {
		t.Errorf("output = %+v, want no plan for a DAG that is not paused", out)
	}
}

// TestUnpauseScheduledPlansThenApplies: unpausing a scheduled DAG starts runs,
// so the call only plans; apply_plan makes the change.
func TestUnpauseScheduledPlansThenApplies(t *testing.T) {
	cp := &controlPlane{dag: scheduledDag}
	h := rcHandlers(t, cp, clock())

	_, plan, err := h.unpauseDag(context.Background(), callAs(anaToken), dagInput{DagID: "etl"})
	if err != nil {
		t.Fatalf("unpauseDag: %v", err)
	}
	if plan.Done || plan.PlanID == "" || !strings.Contains(plan.Summary, "0 * * * *") {
		t.Fatalf("output = %+v, want a plan naming the schedule", plan)
	}
	if cp.count(http.MethodPatch, "/api/v2/dags/etl") != 0 {
		t.Fatal("planning must not unpause")
	}

	_, out, err := h.applyPlan(context.Background(), callAs(anaToken), applyPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("applyPlan: %v", err)
	}
	calls := cp.recorded()
	last := calls[len(calls)-1]
	if !out.Done || last.Method != http.MethodPatch || last.Body["is_paused"] != false || last.Auth != "Bearer "+anaToken {
		t.Errorf("apply = %+v, last call = %+v; want PATCH is_paused=false as the caller", out, last)
	}
}

// TestUnpauseApplyRefusedWhenTheDagChanged: the plan showed a paused DAG on a
// schedule; if either changed, apply asks for a new plan instead of acting.
func TestUnpauseApplyRefusedWhenTheDagChanged(t *testing.T) {
	for name, now := range map[string]string{
		"already unpaused": `{"dag_id":"etl","is_paused":false,"schedule_interval":{"__type":"CronExpression","value":"0 * * * *"}}`,
		"schedule changed": `{"dag_id":"etl","is_paused":true,"schedule_interval":{"__type":"CronExpression","value":"* * * * *"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			cp := &controlPlane{dag: scheduledDag}
			h := rcHandlers(t, cp, clock())
			_, plan, err := h.unpauseDag(context.Background(), callAs(anaToken), dagInput{DagID: "etl"})
			if err != nil {
				t.Fatalf("unpauseDag: %v", err)
			}
			cp.dag = now
			if _, _, err := h.applyPlan(context.Background(), callAs(anaToken), applyPlanInput{PlanID: plan.PlanID}); err == nil {
				t.Error("apply succeeded on a DAG that no longer matches the plan")
			}
			if cp.count(http.MethodPatch, "/api/v2/dags/etl") != 0 {
				t.Error("a refused apply must not unpause")
			}
		})
	}
}

// TestClearOneTaskIsDirect: a clear that touches one task instance runs at
// once, after a dry run that counted it, with the same arguments.
func TestClearOneTaskIsDirect(t *testing.T) {
	cp := &controlPlane{dryRun: []string{oneFailed}}
	h := rcHandlers(t, cp, clock())

	_, out, err := h.clearTask(context.Background(), callAs(anaToken), clearTaskInput{DagID: "etl", RunID: "r1", TaskIDs: []string{"load"}})
	if err != nil {
		t.Fatalf("clearTask: %v", err)
	}
	calls := cp.recorded()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want a dry run then the clear", calls)
	}
	if calls[0].Body["dry_run"] != true || calls[1].Body["dry_run"] != false {
		t.Errorf("dry_run flags = %v, %v; want true then false", calls[0].Body["dry_run"], calls[1].Body["dry_run"])
	}
	assertSameClear(t, calls[0].Body, calls[1].Body)
	if ids, _ := calls[1].Body["task_ids"].([]any); len(ids) != 1 || ids[0] != "load" {
		t.Errorf("clear body = %v, want task_ids [load]", calls[1].Body)
	}
	if calls[1].Body["dag_run_id"] != "r1" || calls[1].Body["only_failed"] != true {
		t.Errorf("clear body = %v, want run r1 and only_failed defaulting to true", calls[1].Body)
	}
	if !out.Done || len(out.TaskInstances) != 1 || out.TaskInstances[0].WebURL != testUIBase+"/dags/etl/runs/r1/tasks/load?try_number=1" {
		t.Errorf("output = %+v", out)
	}
}

// assertSameClear: the executed clear is the previewed one, flag for flag.
func assertSameClear(t *testing.T, preview, exec map[string]any) {
	t.Helper()
	p, e := map[string]any{}, map[string]any{}
	for k, v := range preview {
		p[k] = v
	}
	for k, v := range exec {
		e[k] = v
	}
	delete(p, "dry_run")
	delete(e, "dry_run")
	pb, _ := json.Marshal(p)
	eb, _ := json.Marshal(e)
	if !bytes.Equal(pb, eb) {
		t.Errorf("executed clear %s differs from the preview %s", eb, pb)
	}
}

// assertNarrowedClear: the executed clear names exactly the previewed task
// ids, expands to nothing more, and keeps every other flag of the preview.
func assertNarrowedClear(t *testing.T, preview, exec map[string]any, taskIDs ...string) {
	t.Helper()
	ids, _ := exec["task_ids"].([]any)
	got := make([]string, 0, len(ids))
	for _, id := range ids {
		s, _ := id.(string)
		got = append(got, s)
	}
	if strings.Join(got, ",") != strings.Join(taskIDs, ",") {
		t.Errorf("executed clear task_ids = %v, want the previewed %v", got, taskIDs)
	}
	if exec["include_downstream"] != false || exec["include_upstream"] != false {
		t.Errorf("executed clear %v expands beyond the preview", exec)
	}
	for _, k := range []string{"dag_run_id", "only_failed", "run_on_latest_version"} {
		if exec[k] != preview[k] {
			t.Errorf("executed clear %s = %v, preview %v", k, exec[k], preview[k])
		}
	}
	if exec["dry_run"] != false {
		t.Errorf("executed clear dry_run = %v, want false", exec["dry_run"])
	}
}

// TestClearOneTaskNarrowsToThePreview: a whole-run clear whose preview finds
// one task instance runs at once, but names that task, so a task that fails
// between the preview and the clear is not cleared without a plan.
func TestClearOneTaskNarrowsToThePreview(t *testing.T) {
	cp := &controlPlane{dryRun: []string{oneFailed}}
	h := rcHandlers(t, cp, clock())

	_, out, err := h.clearTask(context.Background(), callAs(anaToken), clearTaskInput{DagID: "etl", RunID: "r1"})
	if err != nil {
		t.Fatalf("clearTask: %v", err)
	}
	calls := cp.recorded()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want a dry run then the clear", calls)
	}
	if _, ok := calls[0].Body["task_ids"]; ok {
		t.Errorf("preview = %v, want the whole run (no task_ids)", calls[0].Body)
	}
	assertNarrowedClear(t, calls[0].Body, calls[1].Body, "load")
	if !out.Done {
		t.Errorf("output = %+v", out)
	}
}

// TestClearSaysWhenItClearedMoreThanPreviewed: if the control plane cleared
// more than the preview showed (the state moved in between), the result says
// so instead of reporting the preview.
func TestClearSaysWhenItClearedMoreThanPreviewed(t *testing.T) {
	cp := &controlPlane{dryRun: []string{oneFailed, twoFailed}}
	h := rcHandlers(t, cp, clock())

	_, out, err := h.clearTask(context.Background(), callAs(anaToken), clearTaskInput{DagID: "etl", RunID: "r1", TaskIDs: []string{"load"}})
	if err != nil {
		t.Fatalf("clearTask: %v", err)
	}
	if !out.Done || len(out.TaskInstances) != 2 || !strings.Contains(out.Summary, "more than the 1") {
		t.Errorf("output = %+v, want both cleared task instances and a note that the preview showed 1", out)
	}
}

func TestClearNothingToClear(t *testing.T) {
	cp := &controlPlane{dryRun: []string{noneToClear}}
	h := rcHandlers(t, cp, clock())

	_, out, err := h.clearTask(context.Background(), callAs(anaToken), clearTaskInput{DagID: "etl", RunID: "r1"})
	if err != nil {
		t.Fatalf("clearTask: %v", err)
	}
	if out.Done || out.PlanID != "" || len(cp.recorded()) != 1 {
		t.Errorf("output = %+v, calls = %+v; want only the dry run and nothing done", out, cp.recorded())
	}
}

// TestClearSeveralPlansThenApplies: a clear over more than one task instance
// only plans; apply re-previews, then clears exactly what the plan showed.
func TestClearSeveralPlansThenApplies(t *testing.T) {
	cp := &controlPlane{dryRun: []string{twoFailed}}
	h := rcHandlers(t, cp, clock())
	in := clearTaskInput{DagID: "etl", RunID: "r1", TaskIDs: []string{"load"}, IncludeDownstream: true}

	_, plan, err := h.clearTask(context.Background(), callAs(anaToken), in)
	if err != nil {
		t.Fatalf("clearTask: %v", err)
	}
	if plan.Done || plan.PlanID == "" || len(plan.TaskInstances) != 2 || plan.ExpiresAt != "2026-10-08T12:10:00Z" {
		t.Fatalf("output = %+v, want a 10-minute plan listing both task instances", plan)
	}
	if len(cp.recorded()) != 1 {
		t.Fatalf("planning must only preview; calls = %+v", cp.recorded())
	}

	// A refreshed token for the same caller still applies its plan.
	_, out, err := h.applyPlan(context.Background(), callAs(anaLater), applyPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("applyPlan: %v", err)
	}
	calls := cp.recorded()
	if len(calls) != 3 || calls[2].Body["dry_run"] != false {
		t.Fatalf("calls = %+v, want preview, re-preview, clear", calls)
	}
	assertSameClear(t, calls[0].Body, calls[1].Body)
	assertNarrowedClear(t, calls[0].Body, calls[2].Body, "load", "report")
	if calls[2].Auth != "Bearer "+anaLater {
		t.Errorf("clear = %+v, want the applying caller's token", calls[2])
	}
	if !out.Done || len(out.TaskInstances) != 2 {
		t.Errorf("apply output = %+v", out)
	}
}

// TestClearApplyRefusedWhenStateChanged: if the task instances are no longer
// what the plan showed, apply refuses rather than clear a different set. This
// also makes a plan single-use: after a clear, its task instances moved on.
func TestClearApplyRefusedWhenStateChanged(t *testing.T) {
	changed := strings.Replace(twoFailed, `"upstream_failed"`, `"success"`, 1)
	cp := &controlPlane{dryRun: []string{twoFailed, changed}}
	h := rcHandlers(t, cp, clock())

	_, plan, err := h.clearTask(context.Background(), callAs(anaToken), clearTaskInput{DagID: "etl", RunID: "r1", TaskIDs: []string{"load"}, IncludeDownstream: true})
	if err != nil {
		t.Fatalf("clearTask: %v", err)
	}
	if _, _, err := h.applyPlan(context.Background(), callAs(anaToken), applyPlanInput{PlanID: plan.PlanID}); err == nil {
		t.Error("apply succeeded although the task instances changed")
	}
	for _, c := range cp.recorded() {
		if c.Body["dry_run"] == false {
			t.Errorf("a refused apply cleared: %+v", c)
		}
	}
}

// TestApplyPlanRefusals: a plan is bound to its caller (tenant, subject,
// client), lasts 10 minutes, and cannot be forged or edited.
func TestApplyPlanRefusals(t *testing.T) {
	now := clock()
	cp := &controlPlane{dryRun: []string{twoFailed}}
	h := rcHandlers(t, cp, now)
	_, plan, err := h.clearTask(context.Background(), callAs(anaToken), clearTaskInput{DagID: "etl", RunID: "r1", TaskIDs: []string{"load"}, IncludeDownstream: true})
	if err != nil {
		t.Fatalf("clearTask: %v", err)
	}

	payload, sig, _ := strings.Cut(plan.PlanID, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(payload)
	edited := strings.Replace(string(raw), `"report"`, `"other"`, 1)
	tampered := base64.RawURLEncoding.EncodeToString([]byte(edited)) + "." + sig

	otherKey := rcHandlers(t, &controlPlane{dryRun: []string{twoFailed}}, now)
	WithRunControl([]byte("ffffffffffffffffffffffffffffffff"))(otherKey)
	_, foreign, err := otherKey.clearTask(context.Background(), callAs(anaToken), clearTaskInput{DagID: "etl", RunID: "r1", TaskIDs: []string{"load"}, IncludeDownstream: true})
	if err != nil {
		t.Fatalf("clearTask with another key: %v", err)
	}

	cases := map[string]struct {
		token, planID string
		advance       time.Duration
	}{
		"another user":        {bobToken, plan.PlanID, 0},
		"another tenant":      {anaOther, plan.PlanID, 0},
		"another client":      {anaClient, plan.PlanID, 0},
		"after 10 minutes":    {anaToken, plan.PlanID, 10*time.Minute + time.Second},
		"edited":              {anaToken, tampered, 0},
		"signed by other key": {anaToken, foreign.PlanID, 0},
		"garbage":             {anaToken, "not-a-plan", 0},
		"empty":               {anaToken, "", 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			saved := *now
			*now = now.Add(c.advance)
			defer func() { *now = saved }()
			if _, _, err := h.applyPlan(context.Background(), callAs(c.token), applyPlanInput{PlanID: c.planID}); err == nil {
				t.Error("apply_plan accepted it")
			}
		})
	}
	for _, c := range cp.recorded() {
		if c.Body["dry_run"] == false {
			t.Errorf("a refused apply reached the clear: %+v", c)
		}
	}

	// Just inside the 10 minutes, the right caller still applies.
	*now = now.Add(10 * time.Minute)
	if _, _, err := h.applyPlan(context.Background(), callAs(anaToken), applyPlanInput{PlanID: plan.PlanID}); err != nil {
		t.Errorf("apply at exactly 10 minutes: %v", err)
	}
}

// TestPlanNeedsAJWTBearer: over HTTP a plan is bound to the bearer's claims,
// so a bearer that is not a JWT cannot plan.
func TestPlanNeedsAJWTBearer(t *testing.T) {
	cp := &controlPlane{dryRun: []string{twoFailed}}
	h := rcHandlers(t, cp, clock())
	unsigned := anaToken[:strings.LastIndex(anaToken, ".")]
	for _, bearer := range []string{"opaque-token", unsigned, anaToken + ".extra"} {
		if _, _, err := h.clearTask(context.Background(), callAs(bearer), clearTaskInput{DagID: "etl", RunID: "r1", IncludeDownstream: true, TaskIDs: []string{"load"}}); err == nil {
			t.Errorf("planned for bearer %q, whose caller cannot be bound", bearer)
		}
	}
}

// TestStdioPlans: on stdio the single local caller is the process, so a plan
// needs no bearer, and a random per-process key signs it.
func TestStdioPlans(t *testing.T) {
	cp := &controlPlane{dryRun: []string{twoFailed}}
	srv := cp.serve(t)
	base, err := apiclient.New(srv.URL, "process-token")
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{api: base}
	WithRunControl(nil)(h)
	if len(h.planKey) < 32 {
		t.Fatalf("stdio plan key = %d bytes, want a random key of at least 32", len(h.planKey))
	}
	_, plan, err := h.clearTask(context.Background(), nil, clearTaskInput{DagID: "etl", RunID: "r1", TaskIDs: []string{"load"}, IncludeDownstream: true})
	if err != nil || plan.PlanID == "" {
		t.Fatalf("clearTask = %+v, %v", plan, err)
	}
	if _, out, err := h.applyPlan(context.Background(), nil, applyPlanInput{PlanID: plan.PlanID}); err != nil || !out.Done {
		t.Errorf("applyPlan = %+v, %v", out, err)
	}
	if got := cp.recorded()[0].Auth; got != "Bearer process-token" {
		t.Errorf("stdio used %q, want the process token", got)
	}
}

func TestRunControlMissingArgs(t *testing.T) {
	h := rcHandlers(t, &controlPlane{}, clock())
	ctx := context.Background()
	if _, _, err := h.triggerRun(ctx, callAs(anaToken), triggerRunInput{}); err == nil {
		t.Error("trigger_run without dag_id accepted")
	}
	if _, _, err := h.pauseDag(ctx, callAs(anaToken), dagInput{}); err == nil {
		t.Error("pause_dag without dag_id accepted")
	}
	if _, _, err := h.unpauseDag(ctx, callAs(anaToken), dagInput{}); err == nil {
		t.Error("unpause_dag without dag_id accepted")
	}
	if _, _, err := h.clearTask(ctx, callAs(anaToken), clearTaskInput{DagID: "etl"}); err == nil {
		t.Error("clear_task without run_id accepted")
	}
}

func TestLoadPlanKey(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(good, append(testPlanKey, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(short, []byte("too-short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := LoadPlanKey(good)
	if err != nil || !bytes.Equal(key, testPlanKey) {
		t.Errorf("LoadPlanKey(good) = %q, %v; want the key without its trailing newline", key, err)
	}
	for _, p := range []string{short, filepath.Join(dir, "missing"), ""} {
		if _, err := LoadPlanKey(p); err == nil {
			t.Errorf("LoadPlanKey(%q) accepted", p)
		}
	}
}

// TestInstructionsFollowRunControl: the initialize instructions say the
// server only reads when run control is off, and explain plans when it is on.
func TestInstructionsFollowRunControl(t *testing.T) {
	off := connect(t, notFound).InitializeResult().Instructions
	if !strings.Contains(off, "never changes anything") || strings.Contains(off, "apply_plan") {
		t.Errorf("instructions with run control off: %q", off)
	}
	on := connect(t, notFound, WithRunControl(testPlanKey)).InitializeResult().Instructions
	if strings.Contains(on, "never changes anything") {
		t.Errorf("instructions claim read-only with run control on: %q", on)
	}
	for _, want := range []string{"apply_plan", "Ask the user", "web_url"} {
		if !strings.Contains(on, want) {
			t.Errorf("instructions with run control on miss %q: %q", want, on)
		}
	}
}
