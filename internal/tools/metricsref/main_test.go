package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is the repository root seen from this package's directory.
const repoRoot = "../../.."

// TestPageIsCurrent fails when the committed metrics reference differs from
// what the generator writes, which is how a new or changed metric that was not
// regenerated is caught in CI (`go test ./...`).
func TestPageIsCurrent(t *testing.T) {
	page, err := os.ReadFile(filepath.Join(repoRoot, pagePath))
	if err != nil {
		t.Fatal(err)
	}
	fams, err := families()
	if err != nil {
		t.Fatal(err)
	}
	want, err := render(page, fams)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, page) {
		t.Fatalf("%s is stale: run `make gen-metrics-reference` and commit the result", pagePath)
	}
}

// TestCheckFailsOnStalePage drives the -check entry point against a copy of
// the page with one row removed by hand, and the write path that restores it.
func TestCheckFailsOnStalePage(t *testing.T) {
	root := t.TempDir()
	data, err := os.ReadFile(filepath.Join(repoRoot, pagePath))
	if err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(root, pagePath)
	if mkErr := os.MkdirAll(filepath.Dir(page), 0o750); mkErr != nil {
		t.Fatal(mkErr)
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "| `dexaflow_xcom_push_total` |") {
			kept = append(kept, line)
		}
	}
	if wErr := os.WriteFile(page, []byte(strings.Join(kept, "\n")), 0o600); wErr != nil {
		t.Fatal(wErr)
	}
	if cErr := run(root, true); cErr == nil {
		t.Fatal("check passed on a page missing a metric, want it to fail")
	}
	if wErr := run(root, false); wErr != nil {
		t.Fatalf("regenerating: %v", wErr)
	}
	if cErr := run(root, true); cErr != nil {
		t.Fatalf("check after regenerating: %v", cErr)
	}
}

// TestFamiliesCoverTheServer locks the wiring: the generator reads the same
// constructors the server registers, so a family from each of them, and the
// type and labels it declares, reach the table.
func TestFamiliesCoverTheServer(t *testing.T) {
	fams, err := families()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]family{}
	for _, f := range fams {
		byName[f.name] = f
	}
	cases := []struct {
		name, kind, labels string
	}{
		{"dexaflow_http_requests_total", "counter", "method,path,status"},
		{"dexaflow_task_duration_seconds", "histogram", "dag_id,task_id,task_type"},
		{"dexaflow_dispatch_queue_depth", "gauge", ""},
		{"dexaflow_schema_wait_seconds", "gauge", ""},
		{"dexaflow_db_pool_idle_conns", "gauge", "pool"},
		{"dexaflow_db_pool_empty_acquires_total", "counter", "pool"},
	}
	for _, c := range cases {
		f, ok := byName[c.name]
		if !ok {
			t.Errorf("%s is missing from the generated families", c.name)
			continue
		}
		if f.kind != c.kind || strings.Join(f.labels, ",") != c.labels {
			t.Errorf("%s = %s %v, want %s [%s]", c.name, f.kind, f.labels, c.kind, c.labels)
		}
	}
}
