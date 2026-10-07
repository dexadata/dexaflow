package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

func intPtr(n int) *int { return &n }

// fmtLimit renders a limit pointer for test messages.
func fmtLimit(p *int) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprint(*p)
}

// TestServiceEnsureTenantPassesLimits: each limit in the body reaches the store;
// one left out (or null) stays nil, which leaves the stored value alone, and 0
// is passed through as "unlimited".
func TestServiceEnsureTenantPassesLimits(t *testing.T) {
	cases := map[string]struct {
		body string
		want domain.TenantLimitsUpdate
	}{
		"all set": {
			`{"max_dags": 10, "max_runs_per_day": 50, "min_schedule_interval_seconds": 900, "max_task_pool_slots": 2}`,
			domain.TenantLimitsUpdate{MaxDags: intPtr(10), MaxRunsPerDay: intPtr(50), MinScheduleIntervalSeconds: intPtr(900), MaxTaskPoolSlots: intPtr(2)},
		},
		"task size": {`{"max_task_pool_slots": 8}`, domain.TenantLimitsUpdate{MaxTaskPoolSlots: intPtr(8)}},
		"one set":   {`{"max_runs_per_day": 5}`, domain.TenantLimitsUpdate{MaxRunsPerDay: intPtr(5)}},
		"cleared":   {`{"max_dags": 0}`, domain.TenantLimitsUpdate{MaxDags: intPtr(0)}},
		"omitted":   {`{"display_name":"Acme Corp"}`, domain.TenantLimitsUpdate{}},
		"null":      {`{"max_dags": null}`, domain.TenantLimitsUpdate{}},
		"no body":   {``, domain.TenantLimitsUpdate{}},
		"with pool": {`{"default_pool_slots": 4, "max_dags": 3}`, domain.TenantLimitsUpdate{MaxDags: intPtr(3)}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeServiceStore{tenantCreated: true}
			h := serviceServer(t, store, true)

			rec := callService(h, http.MethodPut, "/api/v2/service/tenants/acme", testServiceToken, tc.body)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d %s, want 201", rec.Code, rec.Body.String())
			}
			got := store.gotLimits
			if fmtLimit(got.MaxTaskPoolSlots) != fmtLimit(tc.want.MaxTaskPoolSlots) {
				t.Errorf("store got max_task_pool_slots %s, want %s", fmtLimit(got.MaxTaskPoolSlots), fmtLimit(tc.want.MaxTaskPoolSlots))
			}
			if fmtLimit(got.MaxDags) != fmtLimit(tc.want.MaxDags) ||
				fmtLimit(got.MaxRunsPerDay) != fmtLimit(tc.want.MaxRunsPerDay) ||
				fmtLimit(got.MinScheduleIntervalSeconds) != fmtLimit(tc.want.MinScheduleIntervalSeconds) {
				t.Errorf("store got limits (%s, %s, %s), want (%s, %s, %s)",
					fmtLimit(got.MaxDags), fmtLimit(got.MaxRunsPerDay), fmtLimit(got.MinScheduleIntervalSeconds),
					fmtLimit(tc.want.MaxDags), fmtLimit(tc.want.MaxRunsPerDay), fmtLimit(tc.want.MinScheduleIntervalSeconds))
			}
		})
	}
}

// TestServiceEnsureTenantRejectsBadLimits: a limit must be a whole number from 0
// to 2147483647; anything else is a 400 and never reaches the store.
func TestServiceEnsureTenantRejectsBadLimits(t *testing.T) {
	for _, field := range []string{"max_dags", "max_runs_per_day", "min_schedule_interval_seconds", "max_task_pool_slots"} {
		for _, value := range []string{"-1", "2147483648", `"10"`, "1.5"} {
			body := `{"` + field + `": ` + value + `}`
			store := &fakeServiceStore{tenantCreated: true}
			h := serviceServer(t, store, true)

			rec := callService(h, http.MethodPut, "/api/v2/service/tenants/acme", testServiceToken, body)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", body, rec.Code)
			}
			if store.gotTenant != "" {
				t.Errorf("%s: reached the store", body)
			}
		}
	}
}

// TestServiceEnsureTenantAuditsLimits: each limit given is recorded in the audit
// entry, and none is recorded when none is given.
func TestServiceEnsureTenantAuditsLimits(t *testing.T) {
	audit := &fakeAuthAudit{}
	h := serviceServerWith(t, &fakeServiceStore{tenantCreated: true}, true, []string{"acme"}, audit)

	callService(h, http.MethodPut, "/api/v2/service/tenants/acme", testServiceToken,
		`{"max_dags": 10, "max_runs_per_day": 0}`)
	callService(h, http.MethodPut, "/api/v2/service/tenants/acme", testServiceToken, `{}`)

	if len(audit.events) != 2 {
		t.Fatalf("audit events = %+v, want 2", audit.events)
	}
	first := audit.events[0].extra
	if first["max_dags"] != "10" || first["max_runs_per_day"] != "0" {
		t.Errorf("audit extra = %v, want max_dags=10 and max_runs_per_day=0", first)
	}
	if _, ok := first["min_schedule_interval_seconds"]; ok {
		t.Errorf("audit extra = %v records a limit that was not given", first)
	}
	for _, k := range []string{"max_dags", "max_runs_per_day", "min_schedule_interval_seconds"} {
		if _, ok := audit.events[1].extra[k]; ok {
			t.Errorf("audit extra of a call without limits = %v, has %s", audit.events[1].extra, k)
		}
	}
}

// TestLimitExceededIsForbidden: a tenant limit refusal is a 403 that names the
// limit, so the caller knows which one it hit and that retrying will not help.
func TestLimitExceededIsForbidden(t *testing.T) {
	err := domain.Safef(domain.ErrLimitExceeded, "tenant limit max_runs_per_day of 50 reached")
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", http.NoBody)

	handleRepoError(c, err)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "max_runs_per_day of 50") {
		t.Errorf("body does not name the limit: %s", rec.Body.String())
	}
}
