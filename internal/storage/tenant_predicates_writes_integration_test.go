//go:build integration

package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// runAndTI reads back the fixture run's state and version, and its task
// instance's state, try number and note.
func (f tenantPredicateFixture) runAndTI(t *testing.T) (runState string, version pgtype.UUID, tiState string, try int32, note *string) {
	t.Helper()
	if err := f.pg.Pool.QueryRow(t.Context(), `
		SELECT r.state::text, r.dag_version_id, ti.state::text, ti.try_number, ti.note
		FROM dag_runs r JOIN task_instances ti ON ti.dag_run_id = r.id
		WHERE r.id = $1 AND ti.task_id = $2`, f.runUUID, f.taskID).Scan(&runState, &version, &tiState, &try, &note); err != nil {
		t.Fatal(err)
	}
	return runState, version, tiState, try, note
}

// exec runs one statement against the fixture's database.
func (f tenantPredicateFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pg.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// TestRunWritesNeedTheOwningTenantIntegration covers the API write slice of
// #508 on runs and task instances: the writes the mark-state and clear
// endpoints make by internal id, sent with another tenant's id, change
// nothing; the owner's writes apply as before.
func TestRunWritesNeedTheOwningTenantIntegration(t *testing.T) {
	f, _, ctx := newTenantPredicateFixture(t)
	q := f.q
	f.exec(t, `UPDATE dag_runs SET state = 'success' WHERE id = $1`, f.runUUID)
	f.exec(t, `UPDATE task_instances SET state = 'failed' WHERE dag_run_id = $1`, f.runUUID)
	dag, err := q.GetDagByDagID(ctx, queries.GetDagByDagIDParams{TenantID: f.owner, DagID: f.dagIDTxt})
	if err != nil {
		t.Fatal(err)
	}
	ids, retries, slots := []string{f.taskID}, []int32{0}, []int32{1}

	if _, err = q.UpdateDagRunState(ctx, queries.UpdateDagRunStateParams{ID: f.runUUID, State: queries.DagRunStateFailed, TenantID: f.other}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("UpdateDagRunState as another tenant: err = %v, want no rows", err)
	}
	if err = q.ReopenDagRunKeepingVersion(ctx, queries.ReopenDagRunKeepingVersionParams{ID: f.runUUID, TenantID: f.other}); err != nil {
		t.Fatal(err)
	}
	if err = q.ResetDagRunToVersion(ctx, queries.ResetDagRunToVersionParams{ID: f.runUUID, DagVersionID: dag.CurrentVersionID, TenantID: f.other}); err != nil {
		t.Fatal(err)
	}
	if err = q.SetTaskInstanceStateByUser(ctx, queries.SetTaskInstanceStateByUserParams{State: queries.TaskStateSuccess, TenantID: f.other, DagRunID: f.runUUID, TaskID: f.taskID}); err != nil {
		t.Fatal(err)
	}
	if n, rerr := q.ResetFailedTaskInstance(ctx, queries.ResetFailedTaskInstanceParams{
		SpecTaskIds: ids, SpecRetries: retries, SpecPoolSlots: slots, TenantID: f.other, DagRunID: f.runUUID, TaskID: f.taskID,
	}); rerr != nil || n != 0 {
		t.Errorf("ResetFailedTaskInstance as another tenant = %d rows, %v; want 0", n, rerr)
	}
	if cleared, rerr := q.ResetAllFailedTaskInstances(ctx, queries.ResetAllFailedTaskInstancesParams{
		SpecTaskIds: ids, SpecRetries: retries, SpecPoolSlots: slots, TenantID: f.other, DagRunID: f.runUUID,
	}); rerr != nil || len(cleared) != 0 {
		t.Errorf("ResetAllFailedTaskInstances as another tenant = %v, %v; want none", cleared, rerr)
	}
	if err = q.ResetTaskInstanceToNone(ctx, queries.ResetTaskInstanceToNoneParams{
		SpecTaskIds: ids, SpecRetries: retries, SpecPoolSlots: slots, TenantID: f.other, DagRunID: f.runUUID, TaskID: f.taskID,
	}); err != nil {
		t.Fatal(err)
	}
	if keys, derr := q.DeleteXComIndexForTasks(ctx, queries.DeleteXComIndexForTasksParams{TenantID: f.other, DagRunID: f.runUUID, TaskIds: ids}); derr != nil || len(keys) != 0 {
		t.Errorf("DeleteXComIndexForTasks as another tenant = %v, %v; want none", keys, derr)
	}
	if runState, _, tiState, try, _ := f.runAndTI(t); runState != "success" || tiState != "failed" || try != 1 {
		t.Fatalf("after another tenant's writes: run %s, task %s try %d; want success, failed, 1", runState, tiState, try)
	}
	var archived int
	if err = f.pg.Pool.QueryRow(ctx, `SELECT count(*) FROM task_instance_history h JOIN task_instances ti ON ti.id = h.task_instance_id WHERE ti.dag_run_id = $1`, f.runUUID).Scan(&archived); err != nil || archived != 0 {
		t.Errorf("another tenant's clear archived %d attempts, %v; want 0", archived, err)
	}
	if _, err = q.GetXComEntry(ctx, queries.GetXComEntryParams{TenantID: f.owner, DagRunID: f.runUUID, TaskID: f.taskID, Key: f.xcomKey}); err != nil {
		t.Errorf("the XCom is gone after another tenant's delete: %v", err)
	}

	if n, rerr := q.ResetFailedTaskInstance(ctx, queries.ResetFailedTaskInstanceParams{
		SpecTaskIds: ids, SpecRetries: retries, SpecPoolSlots: slots, TenantID: f.owner, DagRunID: f.runUUID, TaskID: f.taskID,
	}); rerr != nil || n != 1 {
		t.Errorf("ResetFailedTaskInstance as the owner = %d rows, %v; want 1", n, rerr)
	}
	if keys, derr := q.DeleteXComIndexForTasks(ctx, queries.DeleteXComIndexForTasksParams{TenantID: f.owner, DagRunID: f.runUUID, TaskIds: ids}); derr != nil || len(keys) != 1 {
		t.Errorf("DeleteXComIndexForTasks as the owner = %v, %v; want one key", keys, derr)
	}
	if err = q.ReopenDagRunKeepingVersion(ctx, queries.ReopenDagRunKeepingVersionParams{ID: f.runUUID, TenantID: f.owner}); err != nil {
		t.Fatal(err)
	}
	if err = q.SetTaskInstanceStateByUser(ctx, queries.SetTaskInstanceStateByUserParams{State: queries.TaskStateSuccess, TenantID: f.owner, DagRunID: f.runUUID, TaskID: f.taskID}); err != nil {
		t.Fatal(err)
	}
	if runState, _, tiState, try, _ := f.runAndTI(t); runState != "queued" || tiState != "success" || try != 2 {
		t.Errorf("after the owner's writes: run %s, task %s try %d; want queued, success, 2", runState, tiState, try)
	}
	if _, err = q.UpdateDagRunState(ctx, queries.UpdateDagRunStateParams{ID: f.runUUID, State: queries.DagRunStateFailed, TenantID: f.owner}); err != nil {
		t.Errorf("UpdateDagRunState as the owner: %v", err)
	}
	if err = q.ResetDagRunToVersion(ctx, queries.ResetDagRunToVersionParams{ID: f.runUUID, DagVersionID: dag.CurrentVersionID, TenantID: f.owner}); err != nil {
		t.Fatal(err)
	}
	if runState, _, _, _, _ := f.runAndTI(t); runState != "queued" {
		t.Errorf("after the owner's re-open on the latest version: run %s, want queued", runState)
	}

	if n, cerr := q.ClearDagRuns(ctx, queries.ClearDagRunsParams{TenantID: f.other, DagID: f.dagUUID}); cerr != nil || n != 0 {
		t.Errorf("ClearDagRuns as another tenant = %d rows, %v; want 0", n, cerr)
	}
	if n, cerr := q.ClearDagRuns(ctx, queries.ClearDagRunsParams{TenantID: f.owner, DagID: f.dagUUID}); cerr != nil || n != 1 {
		t.Errorf("ClearDagRuns as the owner = %d rows, %v; want 1", n, cerr)
	}
}

// TestStartQueuedRunNeedsTheOwningTenantIntegration: the tick's guarded
// queued -> running write (#1766) sent with another tenant's id starts
// nothing; the owner's starts the run once, and a second start finds the run
// no longer queued.
func TestStartQueuedRunNeedsTheOwningTenantIntegration(t *testing.T) {
	f, _, ctx := newTenantPredicateFixture(t)
	q := f.q
	f.exec(t, `UPDATE dag_runs SET state = 'queued', started_at = NULL WHERE id = $1`, f.runUUID)
	if _, err := q.StartQueuedRun(ctx, queries.StartQueuedRunParams{ID: f.runUUID, TenantID: f.other}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("StartQueuedRun as another tenant: err = %v, want no rows", err)
	}
	if runState, _, _, _, _ := f.runAndTI(t); runState != "queued" {
		t.Fatalf("after another tenant's start: run %s, want queued", runState)
	}
	if _, err := q.StartQueuedRun(ctx, queries.StartQueuedRunParams{ID: f.runUUID, TenantID: f.owner}); err != nil {
		t.Fatalf("StartQueuedRun as the owner: %v", err)
	}
	if runState, _, _, _, _ := f.runAndTI(t); runState != "running" {
		t.Errorf("after the owner's start: run %s, want running", runState)
	}
	if _, err := q.StartQueuedRun(ctx, queries.StartQueuedRunParams{ID: f.runUUID, TenantID: f.owner}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("StartQueuedRun on a running run: err = %v, want no rows", err)
	}
}

// TestDagVersionQueriesNeedTheOwningTenantIntegration covers the dag_versions
// queries of #508. The table has no tenant_id; the tenant is its DAG's, so a
// DAG or version id of another tenant reads, inserts and re-points nothing.
func TestDagVersionQueriesNeedTheOwningTenantIntegration(t *testing.T) {
	f, _, ctx := newTenantPredicateFixture(t)
	q := f.q
	dag, err := q.GetDagByDagID(ctx, queries.GetDagByDagIDParams{TenantID: f.owner, DagID: f.dagIDTxt})
	if err != nil {
		t.Fatal(err)
	}
	current, err := q.GetDagVersionInTenant(ctx, queries.GetDagVersionInTenantParams{ID: dag.CurrentVersionID, TenantID: f.owner})
	if err != nil {
		t.Fatalf("GetDagVersionInTenant as the owner: %v", err)
	}
	if _, err = q.GetDagVersionInTenant(ctx, queries.GetDagVersionInTenantParams{ID: dag.CurrentVersionID, TenantID: f.other}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetDagVersionInTenant as another tenant: err = %v, want no rows", err)
	}
	if _, err = q.GetDagVersionByHash(ctx, queries.GetDagVersionByHashParams{DagID: f.dagUUID, SpecHash: current.SpecHash, TenantID: f.owner}); err != nil {
		t.Errorf("GetDagVersionByHash as the owner: %v", err)
	}
	if _, err = q.GetDagVersionByHash(ctx, queries.GetDagVersionByHashParams{DagID: f.dagUUID, SpecHash: current.SpecHash, TenantID: f.other}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetDagVersionByHash as another tenant: err = %v, want no rows", err)
	}

	insert := func(tenant pgtype.UUID, version string) (queries.DagVersion, error) {
		return q.InsertDagVersion(ctx, queries.InsertDagVersionParams{
			Version: version, ImageReference: "img:v2", Spec: []byte(`{}`), SpecHash: "hash-" + version,
			DagID: f.dagUUID, TenantID: tenant,
		})
	}
	if _, err = insert(f.other, "foreign"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("InsertDagVersion as another tenant: err = %v, want no rows", err)
	}
	var foreign int
	if err = f.pg.Pool.QueryRow(ctx, `SELECT count(*) FROM dag_versions WHERE dag_id = $1 AND version = 'foreign'`, f.dagUUID).Scan(&foreign); err != nil || foreign != 0 {
		t.Errorf("another tenant inserted %d versions, %v; want 0", foreign, err)
	}
	v2, err := insert(f.owner, "v2")
	if err != nil {
		t.Fatalf("InsertDagVersion as the owner: %v", err)
	}

	if err = q.SetCurrentDagVersion(ctx, queries.SetCurrentDagVersionParams{ID: f.dagUUID, CurrentVersionID: v2.ID, TenantID: f.other}); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.GetDagByDagID(ctx, queries.GetDagByDagIDParams{TenantID: f.owner, DagID: f.dagIDTxt}); got.CurrentVersionID != dag.CurrentVersionID {
		t.Errorf("another tenant re-pointed the DAG's current version")
	}
	if err = q.SetCurrentDagVersion(ctx, queries.SetCurrentDagVersionParams{ID: f.dagUUID, CurrentVersionID: v2.ID, TenantID: f.owner}); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.GetDagByDagID(ctx, queries.GetDagByDagIDParams{TenantID: f.owner, DagID: f.dagIDTxt}); got.CurrentVersionID != v2.ID {
		t.Errorf("the owner's SetCurrentDagVersion did not apply")
	}
}

// TestAPITokenRetireNeedsTheOwningTenantIntegration (#1768): retiring an
// expired API token's name, sent with another tenant's id, retires nothing
// even for the right owner and name; the owner's retire applies.
func TestAPITokenRetireNeedsTheOwningTenantIntegration(t *testing.T) {
	f, repo, ctx := newTenantPredicateFixture(t)
	userID, _ := apiTokenOwner(t, ctx, repo, "default", "deployer")
	_, tok := newStoredToken(t, ctx, repo, "default", userID, "nightly")
	expireToken(t, ctx, f.pg, tok.ID)
	var uid pgtype.UUID
	if err := uid.Scan(userID); err != nil {
		t.Fatal(err)
	}
	retire := func(tenant pgtype.UUID) ([]queries.RetireExpiredAPITokenNameRow, error) {
		return f.q.RetireExpiredAPITokenName(ctx, queries.RetireExpiredAPITokenNameParams{UserID: uid, TenantID: tenant, Name: "nightly"})
	}
	if rows, err := retire(f.other); err != nil || len(rows) != 0 {
		t.Errorf("RetireExpiredAPITokenName as another tenant = %d rows, %v; want none", len(rows), err)
	}
	var revoked *time.Time
	if err := f.pg.Pool.QueryRow(ctx, `SELECT revoked_at FROM api_tokens WHERE id = $1`, tok.ID).Scan(&revoked); err != nil || revoked != nil {
		t.Errorf("revoked_at = %v (%v) after a retire as another tenant, want NULL", revoked, err)
	}
	if rows, err := retire(f.owner); err != nil || len(rows) != 1 {
		t.Errorf("RetireExpiredAPITokenName as the owner = %d rows, %v; want 1", len(rows), err)
	}
}

// TestCLIDeviceApprovalNeedsTheUsersTenantIntegration (ADR 0081): approving
// a device sign-in as a user while naming another tenant approves nothing;
// naming the user's own tenant approves it.
func TestCLIDeviceApprovalNeedsTheUsersTenantIntegration(t *testing.T) {
	f, repo, ctx := newTenantPredicateFixture(t)
	userID, email := apiTokenOwner(t, ctx, repo, "default", "viewer")
	_, _, created := newDeviceAuthorization(t, ctx, repo, 10*time.Minute, time.Second)
	var id, uid pgtype.UUID
	if err := id.Scan(created.ID); err != nil {
		t.Fatal(err)
	}
	if err := uid.Scan(userID); err != nil {
		t.Fatal(err)
	}
	approve := func(tenant string) (int64, error) {
		return f.q.ApproveCLIDevice(ctx, queries.ApproveCLIDeviceParams{ID: id, UserID: uid, Tenant: tenant, Email: &email, Roles: []string{"viewer"}})
	}
	if n, err := approve(f.otherNm); err != nil || n != 0 {
		t.Errorf("ApproveCLIDevice naming another tenant = %d rows, %v; want 0", n, err)
	}
	if n, err := approve("default"); err != nil || n != 1 {
		t.Errorf("ApproveCLIDevice naming the user's tenant = %d rows, %v; want 1", n, err)
	}
}
