package storage

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// The tick's read comes in no order (#2325): groupByRun must put every row in
// its own run's slice, and an append to one run's slice must not overwrite the
// next run's first row.
func TestGroupByRunAnyOrder(t *testing.T) {
	run := func(b byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{b}, Valid: true} }
	tis := []planTaskInstance{
		{DagRunID: run(2), TaskID: "x"},
		{DagRunID: run(1), TaskID: "a"},
		{DagRunID: run(2), TaskID: "y"},
		{DagRunID: run(1), TaskID: "b"},
		{DagRunID: run(3), TaskID: "z"},
		{DagRunID: run(1), TaskID: "c"},
	}
	got := groupByRun(tis, 3)
	want := map[byte][]string{1: {"a", "b", "c"}, 2: {"x", "y"}, 3: {"z"}}
	if len(got) != len(want) {
		t.Fatalf("got %d runs, want %d", len(got), len(want))
	}
	for b, tasks := range want {
		rows := got[run(b)]
		seen := map[string]bool{}
		for _, r := range rows {
			if r.DagRunID != run(b) {
				t.Errorf("run %d holds a row of run %v", b, r.DagRunID.Bytes[0])
			}
			seen[r.TaskID] = true
		}
		if len(rows) != len(tasks) {
			t.Errorf("run %d: %d rows, want %d", b, len(rows), len(tasks))
		}
		for _, task := range tasks {
			if !seen[task] {
				t.Errorf("run %d is missing task %s", b, task)
			}
		}
	}
	first := got[run(2)][0]
	_ = append(got[run(1)], planTaskInstance{TaskID: "overflow"})
	if got[run(2)][0] != first {
		t.Error("an append to one run's slice overwrote the next run's rows")
	}
}
