//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// recordingXComDeleter records the XCom value keys a clear deletes, or fails
// every delete with err.
type recordingXComDeleter struct {
	keys []string
	err  error
}

func (d *recordingXComDeleter) Delete(_ context.Context, key string) error {
	if d.err != nil {
		return d.err
	}
	d.keys = append(d.keys, key)
	return nil
}

// seedXCom indexes one XCom value for a task of the run.
func seedXCom(t *testing.T, pg *storage.Postgres, ctx context.Context, runUUID, taskID, key string) string {
	t.Helper()
	redisKey := fmt.Sprintf("xcom:test:%s:%s:%s", runUUID, taskID, key)
	if _, err := pg.Pool.Exec(ctx, `
		INSERT INTO xcom_index (tenant_id, dag_run_id, task_id, key, redis_key, size_bytes, expires_at)
		SELECT tenant_id, id, $2, $3, $4, 2, now() + interval '1 day' FROM dag_runs WHERE id = $1::uuid`,
		runUUID, taskID, key, redisKey); err != nil {
		t.Fatalf("seed xcom %s/%s: %v", taskID, key, err)
	}
	return redisKey
}

// indexedXComKeys lists the xcom_index keys of a task of the run.
func indexedXComKeys(t *testing.T, pg *storage.Postgres, ctx context.Context, runUUID, taskID string) []string {
	t.Helper()
	rows, err := pg.Pool.Query(ctx,
		"SELECT key FROM xcom_index WHERE dag_run_id=$1::uuid AND task_id=$2 ORDER BY key", runUUID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if serr := rows.Scan(&k); serr != nil {
			t.Fatal(serr)
		}
		keys = append(keys, k)
	}
	return keys
}

// TestClearDeletesTheClearedAttemptsXCom is D3 of #1131. XCom is keyed by
// (run, task, key) with no try number, and nothing deleted it, so a cleared
// task's values stayed readable by its downstream until the new attempt happened
// to overwrite the same key. A key the new attempt does not write kept serving
// the last attempt's value. Airflow never lets one attempt read another's XCom:
// a cleared task instance starts without any.
//
// Both halves have to go: the index row (what the API lists and reads by name)
// and the stored value (what an agent fetches by key). A task that is not
// cleared keeps its XCom.
func TestClearDeletesTheClearedAttemptsXCom(t *testing.T) {
	for _, mode := range clearModes {
		t.Run(mode.name, func(t *testing.T) {
			repo, sched, pg, ctx := openInfra(t)
			deleter := &recordingXComDeleter{}
			repo.SetXComBackend(deleter)
			dagID := fmt.Sprintf("clear_xcom_%d", time.Now().UnixNano())
			tasks := []domain.TaskSpec{
				{TaskID: "keep", Type: domain.TaskTypePython},
				{TaskID: "t", Type: domain.TaskTypePython, DependsOn: []string{"keep"}},
			}
			runUUID := seedClearRun(t, repo, sched, ctx,
				domain.DAGSpec{DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: tasks}, tasks)
			k1 := seedXCom(t, pg, ctx, runUUID, "t", "return_value")
			k2 := seedXCom(t, pg, ctx, runUUID, "t", "extra")
			seedXCom(t, pg, ctx, runUUID, "keep", "return_value")

			if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", mode.taskIDs, mode.onlyFailed, domain.ClearOptions{ResetDagRun: true}); err != nil {
				t.Fatalf("ClearTaskInstances: %v", err)
			}
			if got := indexedXComKeys(t, pg, ctx, runUUID, "t"); len(got) != 0 {
				t.Errorf("cleared task still indexes XCom %v; downstream would read the previous attempt's values", got)
			}
			if got := indexedXComKeys(t, pg, ctx, runUUID, "keep"); len(got) != 1 {
				t.Errorf("task that was not cleared lost its XCom: %v", got)
			}
			deleted := map[string]bool{}
			for _, k := range deleter.keys {
				deleted[k] = true
			}
			if len(deleter.keys) != 2 || !deleted[k1] || !deleted[k2] {
				t.Errorf("stored values deleted = %v, want exactly [%s %s]", deleter.keys, k1, k2)
			}
		})
	}
}

// TestClearIsAllOrNothingWhenXComCannotBeDeleted: the stored values are deleted
// before the clear commits, so a backend failure rolls the whole clear back. The
// task is not re-queued with the old attempt's values still readable, and the
// caller gets the error and can simply clear again.
func TestClearIsAllOrNothingWhenXComCannotBeDeleted(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	repo.SetXComBackend(&recordingXComDeleter{err: errors.New("redis unavailable")})
	dagID := fmt.Sprintf("clear_xcom_fail_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	runUUID := seedClearRun(t, repo, sched, ctx,
		domain.DAGSpec{DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: tasks}, tasks)
	seedXCom(t, pg, ctx, runUUID, "t", "return_value")

	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", []string{"t"}, true, domain.ClearOptions{ResetDagRun: true}); err == nil {
		t.Fatal("ClearTaskInstances succeeded although the XCom values could not be deleted")
	}
	var state string
	var try int
	if err := pg.Pool.QueryRow(ctx,
		"SELECT state::text, try_number FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).Scan(&state, &try); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || try != 1 {
		t.Errorf("task after a failed clear = %s try %d, want failed try 1 (rolled back)", state, try)
	}
	if got := indexedXComKeys(t, pg, ctx, runUUID, "t"); len(got) != 1 {
		t.Errorf("xcom index after a failed clear = %v, want the row kept", got)
	}
}
