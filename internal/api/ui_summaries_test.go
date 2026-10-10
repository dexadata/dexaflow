package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeTaskSummary struct {
	tis     []domain.TaskInstance
	gotRuns []string
}

func (f *fakeTaskSummary) TaskInstancesForRuns(_ context.Context, _, _ string, runIDs []string) ([]domain.TaskInstance, error) {
	f.gotRuns = runIDs
	return f.tis, nil
}

func summariesServer(reader TaskSummaryReader) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		TaskSummary:   reader,
	})
}

func decodeNDJSON(t *testing.T, body string) []gridTISummariesDTO {
	t.Helper()
	var recs []gridTISummariesDTO
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec gridTISummariesDTO
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad ndjson line %q: %v", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func TestTISummariesStreamsOnePerRun(t *testing.T) {
	start := time.Date(2026, 5, 22, 1, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	reader := &fakeTaskSummary{tis: []domain.TaskInstance{
		// extract: two tries; latest (try 1) is success.
		{RunID: "r1", TaskID: "extract", TryNumber: 0, State: domain.TaskStateFailed, StartedAt: &start, EndedAt: &end},
		{RunID: "r1", TaskID: "extract", TryNumber: 1, State: domain.TaskStateSuccess, StartedAt: &start, EndedAt: &end},
		{RunID: "r1", TaskID: "load", TryNumber: 0, State: domain.TaskStateNone},
		{RunID: "r2", TaskID: "extract", TryNumber: 0, State: domain.TaskStateRunning, StartedAt: &start},
	}}
	rec := authGet(summariesServer(reader), http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1,r2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ti_summaries = %d (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Errorf("content-type = %q, want application/x-ndjson", ct)
	}
	recs := decodeNDJSON(t, rec.Body.String())
	if len(recs) != 2 {
		t.Fatalf("want 2 NDJSON records (one per run), got %d", len(recs))
	}
	if recs[0].RunID != "r1" || recs[1].RunID != "r2" {
		t.Errorf("records out of requested order: %v", []string{recs[0].RunID, recs[1].RunID})
	}
	// extract's latest try (success) wins over the earlier failure.
	var extract *lightTISummaryDTO
	for i := range recs[0].TaskInstances {
		if recs[0].TaskInstances[i].TaskID == "extract" {
			extract = &recs[0].TaskInstances[i]
		}
	}
	if extract == nil || extract.State == nil || *extract.State != "success" {
		t.Errorf("extract latest-try state = %v, want success", extract)
	}
	// load is none -> state null.
	for _, ti := range recs[0].TaskInstances {
		if ti.TaskID == "load" && ti.State != nil {
			t.Errorf("none state should be null, got %v", *ti.State)
		}
	}
}

func TestTISummariesEmptyRunIDs(t *testing.T) {
	rec := authGet(summariesServer(&fakeTaskSummary{}), http.MethodGet, "/ui/grid/ti_summaries/etl", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Errorf("no run_ids should stream nothing, got %q", rec.Body.String())
	}
}

func TestTISummariesConditionalGET(t *testing.T) {
	reader := &fakeTaskSummary{tis: []domain.TaskInstance{{RunID: "r1", TaskID: "x", State: domain.TaskStateSuccess}}}
	srv := summariesServer(reader)
	first := authGet(srv, http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1", "")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1", http.NoBody)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("matching If-None-Match = %d, want 304", rec.Code)
	}
}

// TestTISummariesETagInvalidatesOnStateChange pins #289: the mark-state PATCH
// flips a finished task's state but leaves its StartedAt/EndedAt timestamps
// untouched. The original ETag formula (count + max(started, ended)) was
// therefore identical before and after the mutation, so the grid view kept
// receiving 304 with no body and the SPA showed the OLD state until a manual
// reload. The ETag must include a fingerprint of the per-task states so any
// state change invalidates it.
func TestTISummariesETagInvalidatesOnStateChange(t *testing.T) {
	// Same TI fixture but with a different state — only the state field
	// changes between the two reads.
	started := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 6, 1, 10, 0, 5, 0, time.UTC)
	before := &fakeTaskSummary{tis: []domain.TaskInstance{
		{RunID: "r1", TaskID: "x", State: domain.TaskStateSuccess, StartedAt: &started, EndedAt: &ended, TryNumber: 1},
	}}
	after := &fakeTaskSummary{tis: []domain.TaskInstance{
		{RunID: "r1", TaskID: "x", State: domain.TaskStateFailed, StartedAt: &started, EndedAt: &ended, TryNumber: 1},
	}}
	etagBefore := authGet(summariesServer(before), http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1", "").Header().Get("ETag")
	etagAfter := authGet(summariesServer(after), http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1", "").Header().Get("ETag")
	if etagBefore == "" || etagAfter == "" {
		t.Fatalf("missing ETag (before=%q after=%q)", etagBefore, etagAfter)
	}
	if etagBefore == etagAfter {
		t.Errorf("ETag must differ when a TI's state changes (timestamps unchanged); both = %q — grid keeps stale data (#289)", etagBefore)
	}
}
