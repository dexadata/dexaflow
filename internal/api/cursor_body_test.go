package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// The Airflow 3.3 UI pages the runs and task instance tables by cursor only:
// it sends `cursor` (empty on the first page) and no offset, and moves with
// the next_cursor and previous_cursor the body names. These tests pin that
// contract; without it the second page repeated the first.

type cursorBody struct {
	DagRuns []struct {
		DagRunID string `json:"dag_run_id"`
	} `json:"dag_runs"`
	TaskInstances []struct {
		TaskID string `json:"task_id"`
	} `json:"task_instances"`
	TotalEntries   int     `json:"total_entries"`
	NextCursor     *string `json:"next_cursor"`
	PreviousCursor *string `json:"previous_cursor"`
}

func getCursorPage(t *testing.T, get func(string) (int, []byte), path string) cursorBody {
	t.Helper()
	code, body := get(path)
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d (%s)", path, code, body)
	}
	var p cursorBody
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return p
}

func (p cursorBody) runIDs() string {
	ids := make([]string, 0, len(p.DagRuns))
	for _, r := range p.DagRuns {
		ids = append(ids, r.DagRunID)
	}
	return strings.Join(ids, ",")
}

func (p cursorBody) taskIDs() string {
	ids := make([]string, 0, len(p.TaskInstances))
	for _, ti := range p.TaskInstances {
		ids = append(ids, ti.TaskID)
	}
	return strings.Join(ids, ",")
}

func walkRunsByBodyCursor(t *testing.T, repo DagRunRepository) {
	t.Helper()
	srv := keysetRunServer(repo)
	get := func(p string) (int, []byte) {
		rec := authGet(srv, http.MethodGet, p, "")
		return rec.Code, rec.Body.Bytes()
	}
	const base = "/api/v2/dags/etl/dagRuns?limit=2&order_by=-run_after&cursor="

	p1 := getCursorPage(t, get, base)
	if p1.runIDs() != "r5,r4" || p1.TotalEntries != 5 {
		t.Fatalf("page 1 = %s total %d", p1.runIDs(), p1.TotalEntries)
	}
	if p1.PreviousCursor != nil {
		t.Errorf("page 1 previous_cursor = %q, want none", *p1.PreviousCursor)
	}
	if p1.NextCursor == nil {
		t.Fatal("page 1 has no next_cursor")
	}
	p2 := getCursorPage(t, get, base+url.QueryEscape(*p1.NextCursor))
	if p2.runIDs() != "r3,r2" {
		t.Fatalf("page 2 = %s, want r3,r2", p2.runIDs())
	}
	if p2.NextCursor == nil || p2.PreviousCursor == nil {
		t.Fatalf("page 2 cursors next=%v previous=%v, want both", p2.NextCursor, p2.PreviousCursor)
	}
	p3 := getCursorPage(t, get, base+url.QueryEscape(*p2.NextCursor))
	if p3.runIDs() != "r1" {
		t.Fatalf("page 3 = %s, want r1", p3.runIDs())
	}
	if p3.NextCursor != nil {
		t.Errorf("last page next_cursor = %q, want none", *p3.NextCursor)
	}
	if p3.PreviousCursor == nil {
		t.Fatal("page 3 has no previous_cursor")
	}
	back := getCursorPage(t, get, base+url.QueryEscape(*p3.PreviousCursor))
	if back.runIDs() != "r3,r2" {
		t.Fatalf("previous of page 3 = %s, want r3,r2", back.runIDs())
	}
	first := getCursorPage(t, get, base+url.QueryEscape(*back.PreviousCursor))
	if first.runIDs() != "r5,r4" || first.PreviousCursor != nil {
		t.Fatalf("previous of page 2 = %s (previous %v), want r5,r4 and no previous", first.runIDs(), first.PreviousCursor)
	}
}

// With a keyset-capable store the forward cursor is the keyset one.
func TestDagRunsBodyCursorsWalkBothWays(t *testing.T) {
	repo := &keysetRunRepo{fakeRunRepo: fakeRunRepo{runs: fiveRuns()}}
	walkRunsByBodyCursor(t, repo)
	if repo.gotAfter == nil {
		t.Error("forward pages never used the keyset query")
	}
}

// offsetOnlyRunRepo pages by offset and cannot page by keyset.
type offsetOnlyRunRepo struct{ fakeRunRepo }

func (f *offsetOnlyRunRepo) ListDagRuns(_ context.Context, _, _ string, limit, offset int) ([]domain.DagRun, int, error) {
	end := min(offset+limit, len(f.runs))
	return f.runs[offset:end], len(f.runs), nil
}

// A store without keyset paging still walks both ways.
func TestDagRunsBodyCursorsWithoutKeysetStore(t *testing.T) {
	walkRunsByBodyCursor(t, &offsetOnlyRunRepo{fakeRunRepo{runs: fiveRuns()}})
}

// A client that sends no cursor (Airflow 3.2 and every existing integration)
// gets the body it always got: no cursor fields.
func TestDagRunsNoCursorParamKeepsBody(t *testing.T) {
	repo := &keysetRunRepo{fakeRunRepo: fakeRunRepo{runs: fiveRuns()}}
	rec := authGet(keysetRunServer(repo), http.MethodGet, "/api/v2/dags/etl/dagRuns?limit=2", "")
	_, _, keys := decodeRunPage(t, rec.Body.Bytes())
	if len(keys) != 2 {
		t.Fatalf("body keys = %v, want exactly dag_runs and total_entries", keys)
	}
}

// pagedTaskRepo serves a run's task instances by limit and offset.
type pagedTaskRepo struct{ fakeTaskRepo }

func (f *pagedTaskRepo) ListTaskInstances(_ context.Context, _, _, _ string, limit, offset int) ([]domain.TaskInstance, int, error) {
	if offset > len(f.tis) {
		offset = len(f.tis)
	}
	end := min(offset+limit, len(f.tis))
	return f.tis[offset:end], len(f.tis), nil
}

func TestTaskInstancesBodyCursorsWalkBothWays(t *testing.T) {
	tasks := &pagedTaskRepo{fakeTaskRepo{tis: []domain.TaskInstance{
		{TaskID: "a", RunID: "r1"}, {TaskID: "b", RunID: "r1"}, {TaskID: "c", RunID: "r1"},
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
			TokenTTLSecs:  3600,
		},
		Stores: StoreDeps{
			Tasks:   tasks,
			DagRuns: &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1"}}},
		},
	})
	get := func(p string) (int, []byte) {
		rec := authGet(srv, http.MethodGet, p, "")
		return rec.Code, rec.Body.Bytes()
	}
	const base = "/api/v2/dags/etl/dagRuns/r1/taskInstances?limit=2&order_by=-id&cursor="

	p1 := getCursorPage(t, get, base)
	if p1.taskIDs() != "a,b" || p1.NextCursor == nil || p1.PreviousCursor != nil {
		t.Fatalf("page 1 = %s next=%v previous=%v", p1.taskIDs(), p1.NextCursor, p1.PreviousCursor)
	}
	p2 := getCursorPage(t, get, base+url.QueryEscape(*p1.NextCursor))
	if p2.taskIDs() != "c" || p2.NextCursor != nil || p2.PreviousCursor == nil {
		t.Fatalf("page 2 = %s next=%v previous=%v", p2.taskIDs(), p2.NextCursor, p2.PreviousCursor)
	}
	back := getCursorPage(t, get, base+url.QueryEscape(*p2.PreviousCursor))
	if back.taskIDs() != "a,b" {
		t.Fatalf("previous of page 2 = %s, want a,b", back.taskIDs())
	}

	// Without a cursor parameter the body keeps its 3.2 shape.
	code, body := get("/api/v2/dags/etl/dagRuns/r1/taskInstances?limit=2")
	var raw map[string]json.RawMessage
	if code != http.StatusOK || json.Unmarshal(body, &raw) != nil || len(raw) != 2 {
		t.Fatalf("no-cursor body = %d %s, want exactly task_instances and total_entries", code, body)
	}

	// A cursor this server did not issue is the client's error.
	if code, _ := get(base + "garbage!"); code != http.StatusBadRequest {
		t.Errorf("garbage cursor = %d, want 400", code)
	}
}
