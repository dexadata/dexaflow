package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// keysetRunRepo serves runs newest first, by offset and by cursor, and records
// what the handler asked for.
type keysetRunRepo struct {
	fakeRunRepo
	gotAfter  *domain.PageCursor
	gotLimit  int
	gotStates []string
}

func (f *keysetRunRepo) ListDagRunsAfter(_ context.Context, _, _ string, states []string, after domain.PageCursor, limit int) ([]domain.DagRun, int, error) {
	f.gotAfter, f.gotLimit, f.gotStates = &after, limit, states
	var out []domain.DagRun
	for _, r := range f.runs {
		if r.LogicalDate.Before(after.At) || (r.LogicalDate.Equal(after.At) && r.RunID < after.Key) {
			out = append(out, r)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, len(f.runs), nil
}

func (f *keysetRunRepo) ListDagRuns(_ context.Context, _, _ string, limit, offset int) ([]domain.DagRun, int, error) {
	end := min(offset+limit, len(f.runs))
	return f.runs[offset:end], len(f.runs), nil
}

func keysetRunServer(repo DagRunRepository) *gin.Engine {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	return NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: admin},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		DagRuns: repo,
	})
}

func fiveRuns() []domain.DagRun {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// Newest first; r3 and r2 share a logical date, so the run id breaks the tie.
	return []domain.DagRun{
		{DagID: "etl", RunID: "r5", LogicalDate: base.Add(4 * time.Hour), State: domain.DagRunStateSuccess},
		{DagID: "etl", RunID: "r4", LogicalDate: base.Add(3 * time.Hour), State: domain.DagRunStateSuccess},
		{DagID: "etl", RunID: "r3", LogicalDate: base.Add(2 * time.Hour), State: domain.DagRunStateFailed},
		{DagID: "etl", RunID: "r2", LogicalDate: base.Add(2 * time.Hour), State: domain.DagRunStateSuccess},
		{DagID: "etl", RunID: "r1", LogicalDate: base, State: domain.DagRunStateSuccess},
	}
}

type runPage struct {
	DagRuns []struct {
		DagRunID string `json:"dag_run_id"`
	} `json:"dag_runs"`
	TotalEntries int `json:"total_entries"`
}

func decodeRunPage(t *testing.T, body []byte) (ids []string, total int, keys []string) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k := range raw {
		keys = append(keys, k)
	}
	var p runPage
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, r := range p.DagRuns {
		ids = append(ids, r.DagRunID)
	}
	return ids, p.TotalEntries, keys
}

// Walking the runs with the cursor from the response header visits every run
// once, in the offset order, and every page keeps the /api/v2 body shape.
func TestDagRunsCursorWalksEveryRunOnce(t *testing.T) {
	repo := &keysetRunRepo{fakeRunRepo: fakeRunRepo{runs: fiveRuns()}}
	srv := keysetRunServer(repo)

	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns?limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var seen []string
	for page := 0; ; page++ {
		ids, total, keys := decodeRunPage(t, rec.Body.Bytes())
		if total != 5 {
			t.Errorf("page %d total_entries = %d, want 5", page, total)
		}
		if len(keys) != 2 {
			t.Errorf("page %d body keys = %v, want exactly dag_runs and total_entries", page, keys)
		}
		seen = append(seen, ids...)
		next := rec.Header().Get(nextCursorHeader)
		if next == "" {
			break
		}
		if page > 5 {
			t.Fatal("cursor never ran out")
		}
		if link := rec.Header().Get("Link"); page > 0 && !strings.Contains(link, "cursor="+next) || !strings.Contains(link, `rel="next"`) {
			t.Errorf("cursor page %d Link = %q, want a next link carrying the cursor", page, link)
		}
		rec = authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns?limit=2&cursor="+next, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
	}
	if got := strings.Join(seen, ","); got != "r5,r4,r3,r2,r1" {
		t.Fatalf("walked %s, want r5,r4,r3,r2,r1", got)
	}
	if repo.gotAfter == nil || repo.gotAfter.Key != "r2" || repo.gotLimit != 3 {
		t.Errorf("last cursor call = %+v limit %d, want after r2 with limit+1", repo.gotAfter, repo.gotLimit)
	}
}

// The state filter reaches the keyset query instead of the in-memory scan.
func TestDagRunsCursorForwardsStateFilter(t *testing.T) {
	repo := &keysetRunRepo{fakeRunRepo: fakeRunRepo{runs: fiveRuns()}}
	srv := keysetRunServer(repo)
	cur := encodeCursor(domain.PageCursor{At: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), Key: "z"})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns?state=failed&cursor="+cur, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(repo.gotStates) != 1 || repo.gotStates[0] != "failed" {
		t.Fatalf("states = %v, want [failed]", repo.gotStates)
	}
}

// A malformed cursor is the client's error.
func TestDagRunsCursorRejectsGarbage(t *testing.T) {
	srv := keysetRunServer(&keysetRunRepo{fakeRunRepo: fakeRunRepo{runs: fiveRuns()}})
	for _, bad := range []string{"!!!", "bm90LWpzb24", "e30"} {
		rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns?cursor="+bad, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("cursor %q status = %d, want 400", bad, rec.Code)
		}
	}
}

// Without a cursor the request is answered exactly as before: offset paging,
// the same body, the offset Link header.
func TestDagRunsOffsetPathUnchanged(t *testing.T) {
	repo := &keysetRunRepo{fakeRunRepo: fakeRunRepo{runs: fiveRuns()}}
	rec := authGet(keysetRunServer(repo), http.MethodGet, "/api/v2/dags/etl/dagRuns?limit=2&offset=2", "")
	ids, total, _ := decodeRunPage(t, rec.Body.Bytes())
	if strings.Join(ids, ",") != "r3,r2" || total != 5 {
		t.Fatalf("page = %v total %d", ids, total)
	}
	if repo.gotAfter != nil {
		t.Error("offset request went through the keyset query")
	}
	link := rec.Header().Get("Link")
	if !strings.Contains(link, "offset=4") || !strings.Contains(link, "offset=0") {
		t.Errorf("Link = %q, want the offset next and prev links", link)
	}
}

// keysetAuditReader serves audit entries by cursor.
type keysetAuditReader struct {
	fakeAuditReader
	gotAfter *domain.PageCursor
}

func (f *keysetAuditReader) ListAuditLogsAfter(_ context.Context, _, dagID string, after domain.PageCursor, limit int) ([]domain.AuditLogEntry, int, error) {
	f.gotDag, f.gotAfter = dagID, &after
	afterID, err := strconv.ParseInt(after.Key, 10, 64)
	if err != nil {
		return nil, 0, err
	}
	var out []domain.AuditLogEntry
	for _, e := range f.entries {
		if e.When.Before(after.At) || (e.When.Equal(after.At) && e.ID < afterID) {
			out = append(out, e)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, f.total, nil
}

func (f *keysetAuditReader) ListAuditLogs(_ context.Context, _, dagID string, limit, offset int) ([]domain.AuditLogEntry, int, error) {
	f.gotDag = dagID
	end := min(offset+limit, len(f.entries))
	return f.entries[offset:end], f.total, nil
}

// The event log walks by cursor too, with the dag filter kept on every page.
func TestEventLogsCursorWalksEveryEntryOnce(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := &keysetAuditReader{fakeAuditReader: fakeAuditReader{total: 3, entries: []domain.AuditLogEntry{
		{ID: 30, When: at, Action: "c"}, {ID: 20, When: at, Action: "b"}, {ID: 10, When: at.Add(-time.Hour), Action: "a"},
	}}}
	srv := auditServer(reader)
	rec := authGet(srv, http.MethodGet, "/api/v2/eventLogs?dag_id=etl&limit=2", "")
	next := rec.Header().Get(nextCursorHeader)
	if next == "" {
		t.Fatalf("no next cursor on a first page with more entries; headers %v", rec.Header())
	}
	rec = authGet(srv, http.MethodGet, "/api/v2/eventLogs?dag_id=etl&limit=2&cursor="+next, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		EventLogs []struct {
			ID int64 `json:"event_log_id"`
		} `json:"event_logs"`
		TotalEntries int `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.EventLogs) != 1 || got.EventLogs[0].ID != 10 || got.TotalEntries != 3 {
		t.Fatalf("second page = %+v", got)
	}
	if rec.Header().Get(nextCursorHeader) != "" {
		t.Error("next cursor on the last page")
	}
	if reader.gotDag != "etl" || reader.gotAfter == nil || reader.gotAfter.Key != "20" {
		t.Errorf("cursor call dag=%q after=%+v", reader.gotDag, reader.gotAfter)
	}
}
