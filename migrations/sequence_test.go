package migrations

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestMigrationVersionsAreContiguous guards against two branches that each
// added "the next" migration merging in either order: golang-migrate walks
// versions by the files it finds, so a gap or a duplicate number silently
// changes what an upgrade applies. Up versions must run 1..N with no gap, no
// version may appear twice, and every up file needs its down file.
func TestMigrationVersionsAreContiguous(t *testing.T) {
	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		t.Fatalf("reading embedded migrations: %v", err)
	}
	filePattern := regexp.MustCompile(`^(\d+)_(.+)\.(up|down)\.sql$`)
	ups := map[uint64]string{}
	downs := map[uint64]string{}
	for _, e := range entries {
		m := filePattern.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("%s does not match NNN_name.up.sql or NNN_name.down.sql", e.Name())
			continue
		}
		v, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			t.Errorf("%s: version %q: %v", e.Name(), m[1], err)
			continue
		}
		files := ups
		if m[3] == "down" {
			files = downs
		}
		if prev, ok := files[v]; ok {
			t.Errorf("version %d is used by both %s and %s", v, prev, e.Name())
		}
		files[v] = e.Name()
	}
	for v := uint64(1); v <= uint64(len(ups)); v++ {
		up, ok := ups[v]
		if !ok {
			t.Errorf("no up migration for version %d: versions must be contiguous from 1", v)
			continue
		}
		down, ok := downs[v]
		if !ok {
			t.Errorf("%s has no down migration", up)
			continue
		}
		if strings.TrimSuffix(up, ".up.sql") != strings.TrimSuffix(down, ".down.sql") {
			t.Errorf("version %d: up %s and down %s have different names", v, up, down)
		}
	}
	for v, down := range downs {
		if _, ok := ups[v]; !ok {
			t.Errorf("%s has no up migration", down)
		}
	}
}

// TestConcurrentIndexBuildsFailOnRetry guards the recovery path of an
// interrupted CREATE INDEX CONCURRENTLY. The interruption leaves an INVALID
// index with the final name behind; IF NOT EXISTS would see that name on the
// retry, skip the build and record the migration as applied with a useless
// index. Without it the retry fails loudly and the operator drops the index
// first, as each file's comment explains.
func TestConcurrentIndexBuildsFailOnRetry(t *testing.T) {
	createConcurrently := regexp.MustCompile(`(?is)\bCREATE\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\b`)
	lineComment := regexp.MustCompile(`--[^\n]*`)
	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		t.Fatalf("reading embedded migrations: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := fs.ReadFile(Files, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if createConcurrently.Match(lineComment.ReplaceAll(body, nil)) {
			t.Errorf("%s: CREATE INDEX CONCURRENTLY IF NOT EXISTS would keep an INVALID index left by an interrupted build", e.Name())
		}
	}
}

// TestFillfactorMigrationBoundsItsLockWait guards 031: ALTER TABLE ... SET
// (fillfactor) queues for a SHARE UPDATE EXCLUSIVE lock on task_instances,
// and while it waits behind a long autovacuum every later request for a
// conflicting lock waits behind it. lock_timeout turns that wait into a
// prompt, retryable failure.
func TestFillfactorMigrationBoundsItsLockWait(t *testing.T) {
	body, err := fs.ReadFile(Files, "031_task_instances_fillfactor.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	stmt := regexp.MustCompile(`(?is)\bBEGIN;\s*SET\s+LOCAL\s+lock_timeout\s*=\s*'[^']+';\s*ALTER\s+TABLE\s+task_instances\s+SET\s*\(\s*fillfactor\s*=\s*85\s*\);\s*COMMIT;`)
	if !stmt.Match(regexp.MustCompile(`--[^\n]*`).ReplaceAll(body, nil)) {
		t.Errorf("031 must run the ALTER TABLE in a transaction with SET LOCAL lock_timeout:\n%s", body)
	}
}
