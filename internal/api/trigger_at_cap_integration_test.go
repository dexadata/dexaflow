//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestTriggerAtMaxActiveRunsAnswersQueuedIntegration (#2426): a manual trigger
// of a DAG at its max_active_runs cap is accepted with the run in queued, as
// Airflow does; the scheduler starts it when a slot frees. The tenant's own
// max_runs_per_day is still a refusal.
func TestTriggerAtMaxActiveRunsAnswersQueuedIntegration(t *testing.T) {
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
	tenant := fmt.Sprintf("trigger_cap_%d", time.Now().UnixNano())
	perDay := 2
	if _, err = repo.EnsureTenant(ctx, tenant, "", 0, domain.TenantLimitsUpdate{MaxRunsPerDay: &perDay}); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	srv := clearGuardServer(repo, tenant)
	dagID := "serial_queue"
	spec := fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":"v1","image":"img:v1","max_active_runs":1,
		"tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`, dagID)
	if rec := authGet(srv, http.MethodPost, "/api/v2/dags/"+dagID+"/versions", spec); rec.Code != http.StatusCreated {
		t.Fatalf("register = %d (%s)", rec.Code, rec.Body.String())
	}
	trigger := func(runID string) (int, string, string) {
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/"+dagID+"/dagRuns", fmt.Sprintf(`{"dag_run_id":%q}`, runID))
		var body struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.State, rec.Body.String()
	}
	for _, id := range []string{"event_1", "event_2"} {
		code, state, raw := trigger(id)
		if code != http.StatusCreated || state != string(domain.DagRunStateQueued) {
			t.Fatalf("trigger %s with max_active_runs=1 = %d state %q (%s), want 201 with the run queued", id, code, state, raw)
		}
	}
	if code, _, raw := trigger("event_3"); code != http.StatusForbidden {
		t.Fatalf("third trigger of a tenant with max_runs_per_day=2 = %d (%s), want 403", code, raw)
	}
}
