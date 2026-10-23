package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCollectConnIDs(t *testing.T) {
	t.Parallel()
	dagJSON := []byte(`{
		"dag_id":"etl",
		"tasks":[
			{"task_id":"q","type":"airflow_operator",
			 "operator_args":{"snowflake_conn_id":"sf","sql":"SELECT 1"}},
			{"task_id":"load","type":"python","conn_id":"pg"},
			{"task_id":"dup","type":"airflow_operator","operator_args":{"conn_id":"pg"}},
			{"task_id":"none","type":"python"}
		]
	}`)
	got := collectConnIDs(dagJSON)
	want := []string{"pg", "sf"} // sorted, de-duped (pg appears twice)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collectConnIDs = %v, want %v", got, want)
	}
}

func TestCollectConnIDsNoneWhenAbsent(t *testing.T) {
	t.Parallel()
	if got := collectConnIDs([]byte(`{"dag_id":"x","tasks":[{"task_id":"a","type":"python"}]}`)); len(got) != 0 {
		t.Errorf("expected no conn ids, got %v", got)
	}
}

func TestSurfaceConnectionsPrintsReminder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "dag.json")
	if err := os.WriteFile(out, []byte(`{"dag_id":"etl","tasks":[{"task_id":"q","type":"airflow_operator","operator_args":{"conn_id":"sf"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	surfaceConnections(&sb, out)
	if !strings.Contains(sb.String(), "sf") || !strings.Contains(sb.String(), "connection") {
		t.Errorf("reminder = %q, want it to name the connection", sb.String())
	}
}

func TestSurfaceConnectionsSilentWhenNone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "dag.json")
	if err := os.WriteFile(out, []byte(`{"dag_id":"etl","tasks":[{"task_id":"a","type":"python"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	surfaceConnections(&sb, out)
	if sb.String() != "" {
		t.Errorf("expected no output when the DAG has no connections, got %q", sb.String())
	}
}
