package cli

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func TestDbtProfileConn(t *testing.T) {
	got := dbtProfileConn("python -m leoflow_runtime --dbt-profile warehouse_pg shop && dbt run --select stg")
	if got != "warehouse_pg" {
		t.Errorf("dbtProfileConn = %q, want warehouse_pg", got)
	}
	if c := dbtProfileConn("dbt run --select stg"); c != "" {
		t.Errorf("no profile step should yield empty, got %q", c)
	}
}

// A per-task connections override on a dbt task must NOT drop the compiler-injected
// managed connection the profile step depends on (#10 regression guard): it is
// re-added, while the author's extra connection is still applied.
func TestApplyTaskOverrideKeepsDbtConnection(t *testing.T) {
	task := &domain.TaskSpec{
		TaskID:      "stg",
		Entrypoint:  "python -m leoflow_runtime --dbt-profile warehouse_pg shop && dbt run --select stg",
		Connections: []string{"warehouse_pg"}, // as the dbt compiler declared it
	}
	applyTaskOverride(task, &domain.TaskConfig{Connections: []string{"other_conn"}})

	if !containsString(task.Connections, "warehouse_pg") {
		t.Errorf("override dropped the dbt managed connection: %v", task.Connections)
	}
	if !containsString(task.Connections, "other_conn") {
		t.Errorf("author's override connection not applied: %v", task.Connections)
	}
}

// A non-dbt task keeps the documented replace semantics — no phantom connection
// is invented.
func TestApplyTaskOverrideNonDbtReplaces(t *testing.T) {
	task := &domain.TaskSpec{
		TaskID:      "t",
		Entrypoint:  "python -m leoflow_runtime run",
		Connections: []string{"compiled_conn"},
	}
	applyTaskOverride(task, &domain.TaskConfig{Connections: []string{"only_this"}})
	if len(task.Connections) != 1 || task.Connections[0] != "only_this" {
		t.Errorf("non-dbt override must replace, got %v", task.Connections)
	}
}
