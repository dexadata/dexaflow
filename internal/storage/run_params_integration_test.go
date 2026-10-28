//go:build integration

// DAG param defaults and task params on the agent spec of a scheduled run
// (#1841, #1844). A scheduled run is inserted with no conf, so the spec the agent
// fetches must layer the DAG's declared defaults and the task's own params itself.
// Skips without DATABASE_URL.

package storage_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

func TestScheduledRunSpecCarriesDagAndTaskParamsIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)

	dagID := fmt.Sprintf("run_params_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{
		TaskID:        "copy",
		Type:          domain.TaskTypeAirflowOperator,
		OperatorClass: "pkg.mod.CopyOperator",
		OperatorArgs:  map[string]any{"params": map[string]any{"table": "orders"}},
	}}
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: tasks,
		Params: map[string]domain.ParamSpec{
			"table":  {Default: json.RawMessage(`"dag_table"`)},
			"region": {Default: json.RawMessage(`"us"`)},
		},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register %s: created=%v err=%v", dagID, created, rerr)
	}
	if err := sched.CreateScheduledRun(ctx, defaultTenantID(), dagID, time.Now().UTC().Truncate(time.Second)); err != nil {
		t.Fatalf("CreateScheduledRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}

	got, err := exec.TaskSpec(ctx, auth.AgentIdentity{TenantID: defaultTenantID(), RunID: runUUID, TaskID: "copy", TryNumber: 1})
	if err != nil {
		t.Fatalf("TaskSpec: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(got.ParamsJSON), &params); err != nil {
		t.Fatalf("ParamsJSON %q is not a JSON object: %v", got.ParamsJSON, err)
	}
	// The task param wins over the DAG default of the same name; the other DAG
	// default still arrives although the run carries no conf.
	if len(params) != 2 || params["table"] != "orders" || params["region"] != "us" {
		t.Fatalf("ParamsJSON = %s, want {\"region\":\"us\",\"table\":\"orders\"}", got.ParamsJSON)
	}
	// dag_run.conf is the conf alone, without the layered defaults, and the
	// run type is the Task SDK's (#1843).
	if got.RunConfJSON != "{}" || got.RunType != "scheduled" {
		t.Fatalf("RunConfJSON/RunType = %q/%q, want {}/scheduled", got.RunConfJSON, got.RunType)
	}
}
