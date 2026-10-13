//go:build integration

package storage_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// agentIdentity is the identity of the fixture's running attempt (try 1,
// epoch 3) as a task token of tenant names it.
func (f tenantPredicateFixture) agentIdentity(tenant pgtype.UUID) auth.AgentIdentity {
	return auth.AgentIdentity{
		TenantID:        uuid.UUID(tenant.Bytes).String(),
		DagID:           f.dagIDTxt,
		RunID:           uuid.UUID(f.runUUID.Bytes).String(),
		TaskID:          f.taskID,
		TryNumber:       1,
		AttemptEpoch:    3,
		HasAttemptEpoch: true,
	}
}

// tiRow reads back the fixture's task instance fields the agent calls write.
func (f tenantPredicateFixture) tiRow(t *testing.T) (state string, heartbeat, rescheduleAt pgtype.Timestamptz) {
	t.Helper()
	if err := f.pg.Pool.QueryRow(t.Context(), `
		SELECT state::text, last_heartbeat_at, reschedule_at FROM task_instances
		WHERE dag_run_id = $1 AND task_id = $2`, f.runUUID, f.taskID).Scan(&state, &heartbeat, &rescheduleAt); err != nil {
		t.Fatal(err)
	}
	return state, heartbeat, rescheduleAt
}

// TestAgentCallsNeedTheTokensTenantIntegration covers the agent RPC slice of
// #508: the execution store calls keyed by the run id of a signed agent token
// also filter on the token's tenant. A token naming the run of one tenant but
// carrying another tenant's id reads no spec, sees the attempt as not live,
// and writes nothing; the token of the owning tenant works as before.
func TestAgentCallsNeedTheTokensTenantIntegration(t *testing.T) {
	f, _, ctx := newTenantPredicateFixture(t)
	store := storage.NewExecutionStore(f.pg)
	foreign := f.agentIdentity(f.other)

	if _, err := store.TaskSpec(ctx, foreign); err == nil {
		t.Error("TaskSpec with another tenant's token returned a spec")
	}
	if live, err := store.IsTaskInstanceLive(ctx, foreign); err != nil || live {
		t.Errorf("IsTaskInstanceLive with another tenant's token = %v, %v; want false", live, err)
	}
	if err := store.RecordHeartbeat(ctx, foreign); !errors.Is(err, agentrpc.ErrStaleReport) {
		t.Errorf("RecordHeartbeat with another tenant's token: err = %v, want stale", err)
	}
	if err := store.Reschedule(ctx, foreign, time.Now().Add(time.Minute)); !errors.Is(err, agentrpc.ErrStaleReport) {
		t.Errorf("Reschedule with another tenant's token: err = %v, want stale", err)
	}
	if err := store.ReportState(ctx, foreign, domain.TaskStateFailed, 1, "foreign"); !errors.Is(err, agentrpc.ErrStaleReport) {
		t.Errorf("ReportState with another tenant's token: err = %v, want stale", err)
	}
	if state, hb, at := f.tiRow(t); state != "running" || hb.Valid || at.Valid {
		t.Fatalf("after foreign calls: state %s, heartbeat set %v, reschedule set %v; want running and untouched", state, hb.Valid, at.Valid)
	}
	if _, err := f.q.TaskInstanceAttemptFields(ctx, queries.TaskInstanceAttemptFieldsParams{
		TenantID: f.other, DagRunID: f.runUUID, TaskID: f.taskID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("TaskInstanceAttemptFields as another tenant: err = %v, want no rows", err)
	}

	own := f.agentIdentity(f.owner)
	if _, err := store.TaskSpec(ctx, own); err != nil {
		t.Errorf("TaskSpec with the owner's token: %v", err)
	}
	if live, err := store.IsTaskInstanceLive(ctx, own); err != nil || !live {
		t.Errorf("IsTaskInstanceLive with the owner's token = %v, %v; want true", live, err)
	}
	if err := store.RecordHeartbeat(ctx, own); err != nil {
		t.Errorf("RecordHeartbeat with the owner's token: %v", err)
	}
	if _, err := f.q.TaskInstanceAttemptFields(ctx, queries.TaskInstanceAttemptFieldsParams{
		TenantID: f.owner, DagRunID: f.runUUID, TaskID: f.taskID,
	}); err != nil {
		t.Errorf("TaskInstanceAttemptFields as the owner: %v", err)
	}
	if err := store.ReportState(ctx, own, domain.TaskStateSuccess, 0, ""); err != nil {
		t.Errorf("ReportState with the owner's token: %v", err)
	}
	if state, hb, _ := f.tiRow(t); state != "success" || !hb.Valid {
		t.Errorf("after the owner's calls: state %s, heartbeat set %v; want success and set", state, hb.Valid)
	}
}

// TestAgentRescheduleWithTheOwnersTokenIntegration checks the owner's token
// still parks a sensor in up_for_reschedule (#508 keeps the single-tenant
// path unchanged).
func TestAgentRescheduleWithTheOwnersTokenIntegration(t *testing.T) {
	f, _, ctx := newTenantPredicateFixture(t)
	store := storage.NewExecutionStore(f.pg)
	if err := store.Reschedule(ctx, f.agentIdentity(f.owner), time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("Reschedule with the owner's token: %v", err)
	}
	if state, _, at := f.tiRow(t); state != "up_for_reschedule" || !at.Valid {
		t.Errorf("after reschedule: state %s, reschedule set %v; want up_for_reschedule and set", state, at.Valid)
	}
}

var (
	defaultTenantOnce sync.Once
	defaultTenantUUID string
)

// defaultTenantID is the id of the default tenant, the tenant_id claim a task
// token of that tenant carries. The agent calls filter on it (#508). It is
// empty when the database cannot be read, which fails the calls that use it.
func defaultTenantID() string {
	defaultTenantOnce.Do(func() {
		pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
		if err != nil {
			return
		}
		defer pool.Close()
		_ = pool.QueryRow(context.Background(), `SELECT id::text FROM tenants WHERE name = 'default'`).Scan(&defaultTenantUUID) //nolint:errcheck // an empty id fails the caller's assertions
	})
	return defaultTenantUUID
}
