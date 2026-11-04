//go:build integration

package storage_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// DagRunVersionLabels reads, in one query, the same label GetDagRun returns for
// each run: r1 stays on the first version, r2 is created on the second. A run
// id with no run is left out, and another tenant finds no DAG.
func TestDagRunVersionLabelsMatchGetDagRunIntegration(t *testing.T) {
	f := seedDriftedRun(t, "run_labels", []string{"a"}, pythonTasks([]string{"a"}))
	ctx := t.Context()
	if _, err := f.repo.CreateDagRun(ctx, "default", f.dagID, domain.DagRun{
		RunID: "r2", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create r2: %v", err)
	}
	labels, err := f.repo.DagRunVersionLabels(ctx, "default", f.dagID, []string{"r1", "r2", "nope"})
	if err != nil {
		t.Fatalf("DagRunVersionLabels: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("labels = %v, want r1 and r2 only", labels)
	}
	for _, id := range []string{"r1", "r2"} {
		run, err := f.repo.GetDagRun(ctx, "default", f.dagID, id)
		if err != nil {
			t.Fatalf("GetDagRun %s: %v", id, err)
		}
		if run.Version == "" || labels[id] != run.Version {
			t.Errorf("run %s label = %q, GetDagRun says %q", id, labels[id], run.Version)
		}
	}
	if labels["r1"] == labels["r2"] {
		t.Errorf("r1 and r2 share label %q; r2 was created on the second version", labels["r1"])
	}

	other := fmt.Sprintf("labels_other_%d", time.Now().UnixNano())
	if _, err := f.pg.Pool.Exec(ctx, `INSERT INTO tenants (name, display_name) VALUES ($1, $1)`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.DagRunVersionLabels(ctx, other, f.dagID, []string{"r1"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("another tenant: err = %v, want not found", err)
	}
}
