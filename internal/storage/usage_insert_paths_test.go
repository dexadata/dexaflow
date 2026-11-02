package storage

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// The usage event trigger of migration 055 fires on UPDATE only (ADR 0096
// point 1): a task instance inserted already in an attempt-ending state would
// finish an attempt that no event records. These tests fail when an insert
// path appears that could do that, so it is reviewed against the ADR instead
// of under-counting in silence.

// knownTaskInstanceInserts are the queries allowed to insert task instances.
// Both take the state as a parameter; TestMaterializedTaskInstancesStartInNone
// and TestNoProductionCallerOfCreateTaskInstance pin what their callers pass.
var knownTaskInstanceInserts = map[string]bool{
	"CreateTaskInstance":  true,
	"CreateTaskInstances": true,
}

// TestEveryTaskInstanceInsertIsKnown lists the queries that insert into
// task_instances and refuses one this file does not know.
func TestEveryTaskInstanceInsertIsKnown(t *testing.T) {
	// Arrange
	files, err := filepath.Glob(filepath.Join("queries", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no query files found: %v", err)
	}
	named := regexp.MustCompile(`(?m)^-- name: (\w+)`)
	insert := regexp.MustCompile(`(?i)\bINSERT\s+INTO\s+task_instances\b`)
	var found []string

	// Act
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		names := named.FindAllStringSubmatchIndex(string(src), -1)
		for i, m := range names {
			end := len(src)
			if i+1 < len(names) {
				end = names[i+1][0]
			}
			if insert.Match(src[m[0]:end]) {
				found = append(found, string(src[m[2]:m[3]]))
			}
		}
	}

	// Assert
	if len(found) == 0 {
		t.Fatal("found no insert into task_instances; the scan no longer matches the query files")
	}
	for _, name := range found {
		if !knownTaskInstanceInserts[name] {
			t.Errorf("query %s inserts into task_instances: the usage event trigger only sees updates (ADR 0096), so it must insert in 'none' and be added to knownTaskInstanceInserts", name)
		}
	}
}

// TestMaterializedTaskInstancesStartInNone pins the state the COPY of
// MaterializeTasks and the rebind of a clear write.
func TestMaterializedTaskInstancesStartInNone(t *testing.T) {
	// Arrange
	tasks := []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}, {TaskID: "b", Type: domain.TaskTypePython}}

	// Act
	rows := taskInstanceRows(pgtype.UUID{Valid: true}, pgtype.UUID{Valid: true}, tasks)

	// Assert
	for _, r := range rows {
		if r.State != queries.TaskStateNone {
			t.Errorf("task %s is inserted in %q, want none: an attempt-ending state on insert records no usage event (ADR 0096)", r.TaskID, r.State)
		}
	}
}

// TestNoProductionCallerOfCreateTaskInstance keeps the single-row insert, whose
// state is a free parameter, to tests.
func TestNoProductionCallerOfCreateTaskInstance(t *testing.T) {
	// Arrange
	call := regexp.MustCompile(`\.CreateTaskInstance\(`)
	var callers []string

	// Act
	err := filepath.WalkDir(filepath.Join("..", ".."), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "node_modules" || name == ".git" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, filepath.Join("storage", "queries")) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if call.Match(src) {
			callers = append(callers, path)
		}
		return nil
	})

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) > 0 {
		t.Errorf("production code calls CreateTaskInstance: %v; insert task instances through MaterializeTasks, in 'none', so the usage event trigger sees every finished attempt (ADR 0096)", callers)
	}
}
