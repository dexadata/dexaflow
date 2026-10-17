package storage

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// The run conf reaches the runtime's dag_run.conf as the conf alone (#1843). A
// run without one gets "{}", so the runtime can tell it from a control plane
// that sends nothing.
func TestRunConfJSON(t *testing.T) {
	cases := map[string]string{
		"":                   "{}",
		"null":               "{}",
		"{}":                 "{}",
		`{"table":"orders"}`: `{"table":"orders"}`,
	}
	for in, want := range cases {
		if got := runConfJSON([]byte(in)); got != want {
			t.Errorf("runConfJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

// The run type reaches dag_run.run_type in the Task SDK's DagRunType vocabulary
// (#1843): the dataset trigger is the SDK's asset_triggered.
func TestTaskSDKRunType(t *testing.T) {
	cases := map[queries.DagRunTrigger]string{
		queries.DagRunTriggerManual:    "manual",
		queries.DagRunTriggerScheduled: "scheduled",
		queries.DagRunTriggerBackfill:  "backfill",
		queries.DagRunTriggerDataset:   "asset_triggered",
	}
	for in, want := range cases {
		if got := taskSDKRunType(in); got != want {
			t.Errorf("taskSDKRunType(%q) = %q, want %q", in, got, want)
		}
	}
}
