//go:build integration

package storage_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

// TestMain records every task instance and dag run state change made by a
// named storage query while the package's integration tests run, and then
// checks the recording against the scheduler's transition tables (#2146).
//
// The transitions happen in the storage SQL (UPDATE ... WHERE state IN (...)),
// not through scheduler.CanTransition, so the tables can drift from what the
// queries do. A trigger on task_instances and dag_runs logs (old, new, query)
// for each changed state. Only statements that sqlc generated count: they start
// with "-- name: <Query>", so the raw UPDATEs that tests use to set up a
// fixture are left out. After a full run (no -run filter):
//
//   - every transition a guarded query made must be legal in the tables, so a
//     query that gains a transition without a table entry fails here;
//   - every table entry must have been made by some query, so an entry nothing
//     performs (or a transition no test drives) fails here too.
//
// The queries in callerChosen write the state their caller passes, so neither
// check counts them: the planner's writes are checked by
// TestPlannedTransitionsAreInTheTable (whose exact set,
// scheduler.PlannerTransitions, covers its entries here), a user's mark is a
// verdict outside the machine, and tests use these queries to set up
// fixtures. TestStateTransitionsTheTableListsIntegration drives the entries
// no other test of the package makes.
func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(runRecordingTransitions(m))
}

const transitionRecorder = `
CREATE UNLOGGED TABLE IF NOT EXISTS zz_test_state_transitions (
	tbl TEXT NOT NULL, from_state TEXT NOT NULL, to_state TEXT NOT NULL, query TEXT NOT NULL,
	PRIMARY KEY (tbl, from_state, to_state, query));
CREATE OR REPLACE FUNCTION zz_test_record_state_transition() RETURNS trigger AS $$
DECLARE q TEXT := current_query();
BEGIN
	IF q LIKE '-- name: %' AND OLD.state IS DISTINCT FROM NEW.state THEN
		INSERT INTO zz_test_state_transitions VALUES
			(TG_TABLE_NAME, OLD.state, NEW.state, split_part(substr(q, 10), ' ', 1))
		ON CONFLICT DO NOTHING;
	END IF;
	RETURN NULL;
END $$ LANGUAGE plpgsql;
CREATE OR REPLACE TRIGGER zz_test_ti_state AFTER UPDATE OF state ON task_instances
	FOR EACH ROW EXECUTE FUNCTION zz_test_record_state_transition();
CREATE OR REPLACE TRIGGER zz_test_dr_state AFTER UPDATE OF state ON dag_runs
	FOR EACH ROW EXECUTE FUNCTION zz_test_record_state_transition();
TRUNCATE zz_test_state_transitions;`

const transitionRecorderDrop = `
DROP TRIGGER IF EXISTS zz_test_ti_state ON task_instances;
DROP TRIGGER IF EXISTS zz_test_dr_state ON dag_runs;
DROP FUNCTION IF EXISTS zz_test_record_state_transition();
DROP TABLE IF EXISTS zz_test_state_transitions;`

// callerChosen lists the queries whose target state is the caller's argument.
var callerChosen = map[string]bool{
	"UpdateTaskInstanceState":            true, // fixtures only
	"UpdateTaskInstanceStateByRunTask":   true, // SchedulerStore.ApplyTransition: the planner's choice
	"UpdateTaskInstanceStatesByRunTasks": true, // SchedulerStore.ApplyTransitions: the planner's choice
	"SetTaskInstanceStateByUser":         true, // a user's mark
	"UpdateDagRunState":                  true, // a user's mark
}

func runRecordingTransitions(m *testing.M) int {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return m.Run()
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "state transitions: connect: %v\n", err)
		return m.Run()
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err = conn.Exec(ctx, transitionRecorder); err != nil {
		fmt.Fprintf(os.Stderr, "state transitions: install the recorder: %v\n", err)
		return 1
	}
	defer func() { _, _ = conn.Exec(context.Background(), transitionRecorderDrop) }()
	code := m.Run()
	if code != 0 || flag.Lookup("test.run").Value.String() != "" || flag.Lookup("test.skip").Value.String() != "" {
		return code
	}
	problems, err := checkRecordedTransitions(ctx, conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "state transitions: %v\n", err)
		return 1
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: state transitions made by storage queries disagree with internal/scheduler/state_machine.go (#2146):\n  %s\n",
			strings.Join(problems, "\n  "))
		return 1
	}
	return 0
}

// checkRecordedTransitions compares what the queries did with the tables.
func checkRecordedTransitions(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `SELECT tbl, from_state, to_state, array_agg(query ORDER BY query)
		FROM zz_test_state_transitions GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`)
	if err != nil {
		return nil, fmt.Errorf("reading the recording: %w", err)
	}
	seen := map[string]bool{}
	var problems []string
	for rows.Next() {
		var tbl, from, to string
		var all []string
		if err := rows.Scan(&tbl, &from, &to, &all); err != nil {
			return nil, fmt.Errorf("reading the recording: %w", err)
		}
		guarded := slices.DeleteFunc(slices.Clone(all), func(q string) bool { return callerChosen[q] })
		if len(guarded) == 0 {
			continue
		}
		seen[tbl+":"+from+">"+to] = true
		queries := strings.Join(guarded, ", ")
		legal := false
		switch tbl {
		case "task_instances":
			legal = scheduler.CanTransition(domain.TaskState(from), domain.TaskState(to))
		case "dag_runs":
			legal = scheduler.CanTransitionDagRun(domain.DagRunState(from), domain.DagRunState(to))
		}
		if !legal {
			problems = append(problems, fmt.Sprintf("%s %s -> %s is made by %s but is not in the table", tbl, from, to, queries))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the recording: %w", err)
	}
	for _, tr := range scheduler.PlannerTransitions() {
		seen["task_instances:"+string(tr[0])+">"+string(tr[1])] = true
	}
	for _, tr := range scheduler.TaskTransitions() {
		if !seen["task_instances:"+string(tr[0])+">"+string(tr[1])] {
			problems = append(problems, fmt.Sprintf("task_instances %s -> %s is in the table but no storage query made it", tr[0], tr[1]))
		}
	}
	for _, tr := range scheduler.DagRunTransitions() {
		if !seen["dag_runs:"+string(tr[0])+">"+string(tr[1])] {
			problems = append(problems, fmt.Sprintf("dag_runs %s -> %s is in the table but no storage query made it", tr[0], tr[1]))
		}
	}
	slices.Sort(problems)
	return problems, nil
}
