package cli

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// dexaflow.yaml `size` compiles to the task's pool_slots (ADR 0066 §2):
// task override > the task's own pool_slots from dag.py > defaults.size > 1.

func intPtr(n int) *int { return &n }

// TestOverlayProjectSizeSetsPoolSlots: a per-task size and a DAG-wide default
// size both land on the compiled tasks as pool_slots.
func TestOverlayProjectSizeSetsPoolSlots(t *testing.T) {
	path := writeDagJSON(t)
	cfg := &domain.LeoflowConfig{
		DagID:    "proj",
		Defaults: &domain.ConfigDefaults{Size: intPtr(2)},
		Tasks:    map[string]*domain.TaskConfig{"transform": {Size: intPtr(4)}},
	}
	if err := overlayProject(path, cfg); err != nil {
		t.Fatalf("overlayProject: %v", err)
	}
	got := readSpec(t, path)
	if ts, _ := taskByID(got, "transform"); ts.PoolSlots != 4 {
		t.Errorf("transform pool_slots = %d, want 4 (task override)", ts.PoolSlots)
	}
	if ts, _ := taskByID(got, "extract"); ts.PoolSlots != 2 {
		t.Errorf("extract pool_slots = %d, want 2 (defaults.size)", ts.PoolSlots)
	}
}

// TestOverlayProjectDagPyPoolSlotsBeatsDefaultSize: a task that set pool_slots
// in dag.py keeps it over the DAG-wide default; the YAML task override still
// beats both.
func TestOverlayProjectDagPyPoolSlotsBeatsDefaultSize(t *testing.T) {
	path := writeDagJSON(t)
	spec := readSpec(t, path)
	for i := range spec.Tasks {
		spec.Tasks[i].PoolSlots = 3
	}
	data, err := json.Marshal(&spec)
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(path, data, 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg := &domain.LeoflowConfig{
		DagID:    "proj",
		Defaults: &domain.ConfigDefaults{Size: intPtr(2)},
		Tasks:    map[string]*domain.TaskConfig{"transform": {Size: intPtr(5)}},
	}
	if oerr := overlayProject(path, cfg); oerr != nil {
		t.Fatalf("overlayProject: %v", oerr)
	}
	got := readSpec(t, path)
	if ts, _ := taskByID(got, "extract"); ts.PoolSlots != 3 {
		t.Errorf("extract pool_slots = %d, want 3 (dag.py beats defaults.size)", ts.PoolSlots)
	}
	if ts, _ := taskByID(got, "transform"); ts.PoolSlots != 5 {
		t.Errorf("transform pool_slots = %d, want 5 (YAML task override beats dag.py)", ts.PoolSlots)
	}
}

// TestOverlayProjectNoSizeLeavesPoolSlotsUnset: without size anywhere the
// compiled task carries no pool_slots, so dag.json is unchanged for every DAG
// that does not use the knob.
func TestOverlayProjectNoSizeLeavesPoolSlotsUnset(t *testing.T) {
	path := writeDagJSON(t)
	cfg := &domain.LeoflowConfig{
		DagID: "proj",
		Tasks: map[string]*domain.TaskConfig{"transform": {Retries: intPtr(1)}},
	}
	if err := overlayProject(path, cfg); err != nil {
		t.Fatalf("overlayProject: %v", err)
	}
	for _, ts := range readSpec(t, path).Tasks {
		if ts.PoolSlots != 0 {
			t.Errorf("%s pool_slots = %d, want unset", ts.TaskID, ts.PoolSlots)
		}
	}
}
