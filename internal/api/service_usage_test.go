package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// fakeUsageReader answers ReadEvents with a canned page and records the call.
type fakeUsageReader struct {
	page     domain.UsagePage
	err      error
	called   bool
	gotAfter domain.UsageCursor
	gotLimit int
}

func (f *fakeUsageReader) ReadEvents(_ context.Context, after domain.UsageCursor, limit int) (domain.UsagePage, error) {
	f.called, f.gotAfter, f.gotLimit = true, after, limit
	return f.page, f.err
}

func usageServer(t *testing.T, usage *fakeUsageReader) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{Logger: discardLogger()},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Issuers: IssuerDeps{ServiceToken: testServiceToken, ServiceTenants: &fakeServiceStore{}, ServiceUsage: usage},
	})
}

type usagePageBody struct {
	Events []struct {
		Cursor         string     `json:"cursor"`
		Tenant         string     `json:"tenant"`
		DagID          string     `json:"dag_id"`
		RunID          string     `json:"run_id"`
		TaskInstanceID string     `json:"task_instance_id"`
		TaskID         string     `json:"task_id"`
		TryNumber      int        `json:"try_number"`
		AttemptEpoch   int        `json:"attempt_epoch"`
		Revision       int        `json:"revision"`
		State          string     `json:"state"`
		PreviousState  string     `json:"previous_state"`
		PoolSlots      int        `json:"pool_slots"`
		StartedAt      *time.Time `json:"started_at"`
		EndedAt        *time.Time `json:"ended_at"`
	} `json:"events"`
	NextCursor   string `json:"next_cursor"`
	HasMore      bool   `json:"has_more"`
	OldestCursor string `json:"oldest_cursor"`
}

func decodeUsageBody(t *testing.T, raw []byte) usagePageBody {
	t.Helper()
	var b usagePageBody
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return b
}

// TestServiceUsageServesAPageAndItsCursor covers ADR 0096 point 4: the route
// returns the events in order with the fields a consumer keys on, and the
// cursor it returns reads the page after them.
func TestServiceUsageServesAPageAndItsCursor(t *testing.T) {
	// Arrange
	start := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	end := start.Add(90 * time.Second)
	usage := &fakeUsageReader{page: domain.UsagePage{Enabled: true, Events: []domain.UsageEvent{
		{Cursor: domain.UsageCursor{Xid: 7, ID: 1}, Tenant: "acme", DagID: "etl", RunID: "r1", TaskInstanceID: "ti-1", TaskID: "load",
			TryNumber: 1, Revision: 0, State: domain.TaskStateSuccess, PreviousState: domain.TaskStateRunning, PoolSlots: 2, StartedAt: &start, EndedAt: &end},
		{Cursor: domain.UsageCursor{Xid: 9, ID: 4}, Tenant: "acme", TaskInstanceID: "ti-2", TaskID: "skip", State: domain.TaskStateSkipped, PreviousState: domain.TaskStateNone, PoolSlots: 1},
	}}}
	h := usageServer(t, usage)

	// Act
	rec := callService(h, http.MethodGet, "/api/v2/service/usage?limit=2", testServiceToken, "")
	body := decodeUsageBody(t, rec.Body.Bytes())
	next := callService(h, http.MethodGet, "/api/v2/service/usage?after="+body.NextCursor, testServiceToken, "")

	// Assert
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if len(body.Events) != 2 || !body.HasMore || body.NextCursor != body.Events[1].Cursor {
		t.Fatalf("page = %+v, want two events, more to come, and the last event's cursor as next", body)
	}
	e := body.Events[0]
	if e.Tenant != "acme" || e.DagID != "etl" || e.RunID != "r1" || e.TaskInstanceID != "ti-1" || e.TryNumber != 1 ||
		e.State != "success" || e.PreviousState != "running" || e.PoolSlots != 2 || e.StartedAt == nil || !e.EndedAt.Equal(end) {
		t.Errorf("first event = %+v", e)
	}
	if body.Events[1].StartedAt != nil {
		t.Errorf("a skipped task has started_at %v, want null", body.Events[1].StartedAt)
	}
	if next.Code != http.StatusOK || usage.gotAfter != (domain.UsageCursor{Xid: 9, ID: 4}) || usage.gotLimit != 500 {
		t.Errorf("next page: status %d, read after %+v limit %d; want after the last event with the default limit", next.Code, usage.gotAfter, usage.gotLimit)
	}
}

// TestServiceUsageEmptyPageKeepsTheCursor: a page with nothing new returns the
// cursor it was given, so a consumer can store it unconditionally.
func TestServiceUsageEmptyPageKeepsTheCursor(t *testing.T) {
	// Arrange
	usage := &fakeUsageReader{page: domain.UsagePage{Enabled: true}}
	h := usageServer(t, usage)
	cursor := encodeUsageCursor(domain.UsageCursor{Xid: 12, ID: 30})

	// Act
	rec := callService(h, http.MethodGet, "/api/v2/service/usage?after="+cursor, testServiceToken, "")

	// Assert
	body := decodeUsageBody(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || len(body.Events) != 0 || body.HasMore || body.NextCursor != cursor {
		t.Errorf("status %d, body %+v; want an empty page that returns the same cursor", rec.Code, body)
	}
	if body.Events == nil {
		t.Error("events is null, want an empty list")
	}
}

// TestServiceUsageRefusals covers points 5 and 7 and the input checks:
// recording off is 404, a cursor below the pruned mark is 410 with the oldest
// complete cursor, a bad cursor or limit is 400 before the store is read, and
// a store failure is 500.
func TestServiceUsageRefusals(t *testing.T) {
	cases := map[string]struct {
		page      domain.UsagePage
		err       error
		query     string
		want      int
		readStore bool
	}{
		"recording off":   {page: domain.UsagePage{}, want: http.StatusNotFound, readStore: true},
		"cursor too old":  {page: domain.UsagePage{Enabled: true, Gone: true, Oldest: domain.UsageCursor{Xid: 5, ID: 8}}, query: "?after=" + encodeUsageCursor(domain.UsageCursor{Xid: 1, ID: 1}), want: http.StatusGone, readStore: true},
		"bad cursor":      {query: "?after=not-a-cursor", want: http.StatusBadRequest},
		"limit zero":      {query: "?limit=0", want: http.StatusBadRequest},
		"limit too big":   {query: "?limit=5001", want: http.StatusBadRequest},
		"limit not a num": {query: "?limit=ten", want: http.StatusBadRequest},
		"store fails":     {err: errors.New("connection refused"), want: http.StatusInternalServerError, readStore: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Arrange
			usage := &fakeUsageReader{page: tc.page, err: tc.err}
			h := usageServer(t, usage)

			// Act
			rec := callService(h, http.MethodGet, "/api/v2/service/usage"+tc.query, testServiceToken, "")

			// Assert
			if rec.Code != tc.want || usage.called != tc.readStore {
				t.Fatalf("status = %d (store read %v), want %d (store read %v); body %s", rec.Code, usage.called, tc.want, tc.readStore, rec.Body)
			}
			if tc.want == http.StatusGone {
				if got := decodeUsageBody(t, rec.Body.Bytes()).OldestCursor; got != encodeUsageCursor(tc.page.Oldest) {
					t.Errorf("oldest_cursor = %q, want the pruned mark", got)
				}
			}
		})
	}
}

// TestServiceUsageNeedsTheServiceToken: a user session never reads usage.
func TestServiceUsageNeedsTheServiceToken(t *testing.T) {
	// Arrange
	usage := &fakeUsageReader{page: domain.UsagePage{Enabled: true}}
	h := usageServer(t, usage)

	// Act
	rec := callService(h, http.MethodGet, "/api/v2/service/usage", "a.b.c", "")

	// Assert
	if rec.Code != http.StatusUnauthorized || usage.called {
		t.Errorf("status = %d (store read %v), want 401 without reading", rec.Code, usage.called)
	}
}
