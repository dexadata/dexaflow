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

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestPoolsAPIReportsWeightedSlots drives GET /api/v2/pools/{pool} over the
// real Repository (#1499). Two tasks of size 4 queued and running in a pool of
// 8 leave the admission gate (ADR 0066) no room, so the API must report 8
// occupied and 0 open, not 2 occupied and 6 open.
func TestPoolsAPIReportsWeightedSlots(t *testing.T) {
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
	sched := storage.NewSchedulerStore(pg)

	suffix := time.Now().UnixNano()
	dagID := fmt.Sprintf("api_pool_weighted_%d", suffix)
	poolName := fmt.Sprintf("api_weighted_%d", suffix)
	if err = repo.SetPool(ctx, "default", domain.Pool{Name: poolName, Slots: 8}); err != nil {
		t.Fatalf("SetPool: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeletePool(context.Background(), "default", poolName) })
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{
			{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a", Pool: poolName, PoolSlots: 4},
			{TaskID: "b", Type: domain.TaskTypePython, Entrypoint: "dag:b", Pool: poolName, PoolSlots: 4},
		},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
		t.Fatalf("register version: %v", err)
	}
	if _, err = repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := activeRunUUID(t, sched, ctx, dagID)
	if err = sched.MaterializeTasks(ctx, runUUID, spec.Tasks); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if err = sched.ApplyTransition(ctx, runUUID, "a", domain.TaskStateQueued); err != nil {
		t.Fatalf("queue a: %v", err)
	}
	if err = sched.ApplyTransition(ctx, runUUID, "b", domain.TaskStateRunning); err != nil {
		t.Fatalf("run b: %v", err)
	}

	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Pools:         repo,
		Edition:       "pro",
	})
	rec := authGet(srv, http.MethodGet, "/api/v2/pools/"+poolName, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET pool = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var got poolDTO
	if err = json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := poolDTO{Name: poolName, Slots: 8, OccupiedSlots: 8, RunningSlots: 4, QueuedSlots: 4, OpenSlots: 0}
	if got != want {
		t.Errorf("pool = %+v, want %+v", got, want)
	}
}

// activeRunUUID returns the internal UUID of dagID's active run.
func activeRunUUID(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, dagID string) string {
	t.Helper()
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	for _, r := range runs {
		if r.DagID == dagID {
			return r.RunID
		}
	}
	t.Fatalf("no active run for dag %s", dagID)
	return ""
}
