package api

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

func revalidatingSummariesServer(reader TaskSummaryReader) *gin.Engine {
	return NewServer(Dependencies{
		Logger:             discardLogger(),
		Authenticator:      &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:        auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:        []string{"*"},
		TaskSummary:        reader,
		UIETagRevalidation: true,
	})
}

// TestTISummariesKeepsNoStoreByDefault pins the default of the
// ui.etag_revalidation gate: nothing changes until an operator opts in.
func TestTISummariesKeepsNoStoreByDefault(t *testing.T) {
	reader := &fakeTaskSummary{tis: []domain.TaskInstance{{RunID: "r1", TaskID: "x", State: domain.TaskStateSuccess}}}
	rec := authGet(summariesServer(reader), http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1", "")
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store, must-revalidate" {
		t.Errorf("Cache-Control = %q, want the no-store default", cc)
	}
}

// TestTISummariesRevalidatesWhenEnabled pins the opt-in: the ETag route lets the
// browser keep a private copy it must revalidate on every use, keyed by the
// credential that fetched it, so the 304 path is reachable.
func TestTISummariesRevalidatesWhenEnabled(t *testing.T) {
	reader := &fakeTaskSummary{tis: []domain.TaskInstance{{RunID: "r1", TaskID: "x", State: domain.TaskStateSuccess}}}
	srv := revalidatingSummariesServer(reader)
	first := authGet(srv, http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1", "")
	assertRevalidationHeaders(t, first)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1", http.NoBody)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("If-None-Match", first.Header().Get("ETag"))
	again := httptest.NewRecorder()
	srv.ServeHTTP(again, req)
	if again.Code != http.StatusNotModified {
		t.Fatalf("revalidation = %d, want 304", again.Code)
	}
	assertRevalidationHeaders(t, again)
}

// TestTISummariesRevalidationStillAuthorizes pins that a cached copy is never
// a way around authorization: without a credential the route answers 401, not
// 304, even with a matching validator.
func TestTISummariesRevalidationStillAuthorizes(t *testing.T) {
	reader := &fakeTaskSummary{tis: []domain.TaskInstance{{RunID: "r1", TaskID: "x", State: domain.TaskStateSuccess}}}
	srv := revalidatingSummariesServer(reader)
	etag := authGet(srv, http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1", "").Header().Get("ETag")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1", http.NoBody)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated revalidation = %d, want 401", rec.Code)
	}
}

func assertRevalidationHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Errorf("Cache-Control = %q, want private, no-cache", cc)
	}
	vary := rec.Header().Get("Vary")
	for _, h := range []string{"Authorization", "Cookie"} {
		if !strings.Contains(vary, h) {
			t.Errorf("Vary = %q, want it to name %s", vary, h)
		}
	}
}

func fingerprintRows(n int) []domain.TaskInstance {
	start := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	tis := make([]domain.TaskInstance, n)
	for i := range tis {
		end := start.Add(time.Duration(i) * time.Second)
		tis[i] = domain.TaskInstance{
			RunID: fmt.Sprintf("r%d", i%25), TaskID: fmt.Sprintf("t%d", i/25),
			TryNumber: 1, State: domain.TaskStateSuccess, StartedAt: &start, EndedAt: &end,
		}
	}
	return tis
}

// TestGridFingerprintIgnoresRowOrder pins that the fingerprint is a property of
// the set of rows, so the database's row order never changes the ETag.
func TestGridFingerprintIgnoresRowOrder(t *testing.T) {
	tis := fingerprintRows(200)
	want := gridFingerprint(tis)
	r := rand.New(rand.NewPCG(3, 5))
	r.Shuffle(len(tis), func(i, j int) { tis[i], tis[j] = tis[j], tis[i] })
	if got := gridFingerprint(tis); got != want {
		t.Fatalf("fingerprint changed with row order: %x != %x", got, want)
	}
}

// TestGridFingerprintTracksEveryRenderedField pins that any field that reaches
// the body (state, try, start, end, run, task) moves the fingerprint.
func TestGridFingerprintTracksEveryRenderedField(t *testing.T) {
	base := fingerprintRows(3)
	want := gridFingerprint(base)
	later := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	mutations := map[string]func(*domain.TaskInstance){
		"state":   func(ti *domain.TaskInstance) { ti.State = domain.TaskStateFailed },
		"try":     func(ti *domain.TaskInstance) { ti.TryNumber = 2 },
		"started": func(ti *domain.TaskInstance) { ti.StartedAt = &later },
		"ended":   func(ti *domain.TaskInstance) { ti.EndedAt = nil },
		"run":     func(ti *domain.TaskInstance) { ti.RunID = "other" },
		"task":    func(ti *domain.TaskInstance) { ti.TaskID = "other" },
	}
	for name, mutate := range mutations {
		tis := append([]domain.TaskInstance(nil), base...)
		mutate(&tis[1])
		if gridFingerprint(tis) == want {
			t.Errorf("changing %s left the fingerprint unchanged", name)
		}
	}
}

// TestGridFingerprintDoesNotAllocate pins the O(n), allocation-free shape: the
// old fingerprint built, sorted and joined one string per row (7.5 MB per poll
// at 25k rows).
func TestGridFingerprintDoesNotAllocate(t *testing.T) {
	tis := fingerprintRows(1000)
	if allocs := testing.AllocsPerRun(10, func() { _ = gridFingerprint(tis) }); allocs != 0 {
		t.Errorf("gridFingerprint allocated %.0f times per call, want 0", allocs)
	}
}

// BenchmarkGridFingerprint measures the ETag cost of one grid poll.
func BenchmarkGridFingerprint(b *testing.B) {
	tis := fingerprintRows(25000)
	b.ReportAllocs()
	for b.Loop() {
		_ = gridFingerprint(tis)
	}
}
