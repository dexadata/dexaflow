package cli

import (
	"bytes"
	"strings"
	"testing"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

func TestPrintDagListEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := printDagList(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No DAGs registered") {
		t.Errorf("empty collection = %q, want the friendly line", buf.String())
	}
}

func TestPrintDagListRows(t *testing.T) {
	sp := func(s string) *string { return &s }
	paused := true
	owners := []string{"team-a"}
	coll := &apiclient.DAGCollection{Dags: &[]apiclient.DAG{
		{DagId: sp("etl"), IsPaused: &paused, Owners: &owners},
		{DagId: sp("ingest")}, // no paused/owners → defaults
	}}
	var buf bytes.Buffer
	if err := printDagList(&buf, coll); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"DAG_ID", "PAUSED", "OWNERS", "etl", "yes", "team-a", "ingest", "no"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}
