package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzDAGSpecValidate feeds arbitrary dag.json documents through the decode and
// validation a registration runs (Validate, ValidateSchedule, CanonicalHash).
// A dag.json is author-supplied, and a hand-written one reaches the API
// directly, so none of these may panic whatever the document holds.
func FuzzDAGSpecValidate(f *testing.F) {
	valid, err := json.Marshal(validDAGSpec())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	// The repository's sample dag.json: a three-task cycle.
	if sample, rerr := os.ReadFile(filepath.Join("..", "..", "dag.json")); rerr == nil {
		f.Add(sample)
	}
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema_version":"1.0","dag_id":"d","tasks":[{"task_id":"t","type":"bash","entrypoint":"true","depends_on":["t"]}]}`))
	f.Add([]byte(`{"schema_version":"1.0","dag_id":"d","schedule":"*/5 * * * *","tasks":[{"task_id":"t","type":"bash","entrypoint":"true"}],` +
		`"params":{"n":{"schema":{"type":"integer","minimum":1},"default":0}}}`))
	f.Add([]byte(`{"schema_version":"1.0","dag_id":"d","tasks":[{"task_id":"t","type":"python","entrypoint":"m:f",` +
		`"resources":{"limits":{"memory":"2GB"}},"execution":{"labels":{"leoflow.io/x":"y"}}}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var spec DAGSpec
		if err := json.Unmarshal(data, &spec); err != nil {
			return
		}
		_ = spec.Validate()         //nolint:errcheck // only the absence of a panic is under test here
		_ = spec.ValidateSchedule() //nolint:errcheck // idem
		_, _ = spec.CanonicalHash() //nolint:errcheck // idem
	})
}

// FuzzSchedule feeds arbitrary schedule expressions to every schedule parser
// the domain exposes: the registration check and the tenant minimum-interval
// limit. A schedule is a free-form string in an author's dag.json.
func FuzzSchedule(f *testing.F) {
	for _, s := range []string{
		"", "@once", "@continuous", "@daily", "@every 90s", "*/3 * * * *",
		"0 0 31 2 *", "0 9-17 * * 1-5", "CRON_TZ=Europe/Lisbon 30 2 * * *",
		"TZ=UTC 0 * * * *", "* * * *", "61 * * * *",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		spec := DAGSpec{Schedule: &expr}
		valid := spec.ValidateSchedule() == nil
		interval, ok := MinScheduleInterval(expr)
		if ok && !valid {
			t.Fatalf("MinScheduleInterval(%q) = %v for a schedule registration rejects", expr, interval)
		}
		if ok && interval < time.Second {
			t.Fatalf("MinScheduleInterval(%q) = %v, want at least one second", expr, interval)
		}
	})
}
