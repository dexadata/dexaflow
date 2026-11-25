package api

// Which runs and tasks a clear reaches.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

func TestClearIncludePastFutureFansAcrossRuns(t *testing.T) {
	now := time.Now().UTC()
	runs := &fakeRunRepo{runs: []domain.DagRun{
		{DagID: "etl", RunID: "past", LogicalDate: now.Add(-2 * time.Hour)},
		{DagID: "etl", RunID: "cur", LogicalDate: now},
		{DagID: "etl", RunID: "future", LogicalDate: now.Add(2 * time.Hour)},
	}}
	// fakeTaskRepo returns its single TI for any run, so the affected count equals
	// the number of target runs the clear fans out to.
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", State: domain.TaskStateFailed}}}
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
			TokenTTLSecs:  3600,
		},
		Stores: StoreDeps{
			Tasks:   tasks,
			DagRuns: runs,
		},
	})
	count := func(extra string) int {
		body := `{"dag_run_id":"cur","task_ids":["extract"],"dry_run":true` + extra + `}`
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", body)
		var got struct {
			TotalEntries int `json:"total_entries"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return got.TotalEntries
	}
	if n := count(""); n != 1 {
		t.Errorf("no flags: %d target runs, want 1 (cur only)", n)
	}
	if n := count(`,"include_past":true`); n != 2 {
		t.Errorf("include_past: %d, want 2 (cur+past)", n)
	}
	if n := count(`,"include_future":true`); n != 2 {
		t.Errorf("include_future: %d, want 2 (cur+future)", n)
	}
	if n := count(`,"include_past":true,"include_future":true`); n != 3 {
		t.Errorf("past+future: %d, want 3 (all runs)", n)
	}
}

// TestExpandClearTasksDispatch covers the wrapper that decides whether to
// expand seed task_ids along the DAG topology when the UI's Clear dialog has
// "include upstream/downstream" ticked. It is the gate between the public
// clear handler and expandTaskIDs; a regression here silently drops the flags
// or fails open into expanding when the spec lookup hiccups, both of which
// surface to the user as confusing partial clears.
func TestExpandClearTasksDispatch(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "a"},
		{TaskID: "b", DependsOn: []string{"a"}},
		{TaskID: "c", DependsOn: []string{"b"}},
	}
	specs := &fakeSpecReader{spec: domain.DAGSpec{Tasks: tasks}}
	mkCtx := func() *gin.Context {
		c := &gin.Context{}
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/dags/etl/clearTaskInstances", http.NoBody)
		c.Params = gin.Params{{Key: "dag_id", Value: "etl"}}
		return c
	}

	t.Run("specs nil → seeds untouched", func(t *testing.T) {
		got := expandClearTasks(mkCtx(), nil, clearRequest{
			TaskIDs: clearTaskIDs{"b"}, IncludeUpstream: true, IncludeDownstream: true,
		})
		if len(got) != 1 || got[0] != "b" {
			t.Errorf("specs=nil: got %v, want [b]", got)
		}
	})

	t.Run("empty seeds → empty seeds (clear-whole-run)", func(t *testing.T) {
		got := expandClearTasks(mkCtx(), specs, clearRequest{IncludeUpstream: true, IncludeDownstream: true})
		if len(got) != 0 {
			t.Errorf("empty seeds: got %v, want []", got)
		}
	})

	t.Run("no flags → seeds returned unchanged (no DAG lookup)", func(t *testing.T) {
		got := expandClearTasks(mkCtx(), specs, clearRequest{TaskIDs: []string{"b"}})
		if len(got) != 1 || got[0] != "b" {
			t.Errorf("no flags: got %v, want [b]", got)
		}
	})

	t.Run("spec lookup error → seeds untouched (fail safe)", func(t *testing.T) {
		broken := &fakeSpecReader{err: errors.New("postgres briefly unreachable")}
		got := expandClearTasks(mkCtx(), broken, clearRequest{
			TaskIDs: []string{"b"}, IncludeDownstream: true,
		})
		if len(got) != 1 || got[0] != "b" {
			t.Errorf("spec error: got %v, want [b] (fail-safe seed-only)", got)
		}
	})

	t.Run("happy path → fans out downstream of seed", func(t *testing.T) {
		got := expandClearTasks(mkCtx(), specs, clearRequest{
			TaskIDs: []string{"a"}, IncludeDownstream: true,
		})
		set := map[string]bool{}
		for _, id := range got {
			set[id] = true
		}
		for _, id := range []string{"a", "b", "c"} {
			if !set[id] {
				t.Errorf("downstream(a) missing %s: %v", id, got)
			}
		}
	})
}

func TestExpandTaskIDs(t *testing.T) {
	// a -> b -> c (c depends on b, b depends on a), plus a sibling d off a.
	tasks := []domain.TaskSpec{
		{TaskID: "a"},
		{TaskID: "b", DependsOn: []string{"a"}},
		{TaskID: "c", DependsOn: []string{"b"}},
		{TaskID: "d", DependsOn: []string{"a"}},
	}
	set := func(ids []string) map[string]bool {
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	// seeds only.
	if got := set(expandTaskIDs(tasks, []string{"b"}, false, false)); len(got) != 1 || !got["b"] {
		t.Errorf("seeds-only = %v, want {b}", got)
	}
	// downstream of a = a,b,c,d (transitive).
	got := set(expandTaskIDs(tasks, []string{"a"}, false, true))
	for _, id := range []string{"a", "b", "c", "d"} {
		if !got[id] {
			t.Errorf("downstream(a) missing %s: %v", id, got)
		}
	}
	// upstream of c = c,b,a (transitive), not d.
	got = set(expandTaskIDs(tasks, []string{"c"}, true, false))
	if !got["a"] || !got["b"] || !got["c"] || got["d"] {
		t.Errorf("upstream(c) = %v, want {a,b,c}", got)
	}
}
