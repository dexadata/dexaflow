package migrations

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// lockSafetyBaseline is the newest migration that shipped before the lock
// check existed (#2312). Those files were timed at rc review and are already
// applied on every upgraded database, so rewriting them would change nothing;
// the check covers every migration after it.
const lockSafetyBaseline = 58

// TestMigrationsDoNotLockLargeTables fails on every finding of LockFindings in
// a migration newer than lockSafetyBaseline, up or down, that
// lock_exceptions.txt does not justify, and on an exception that matches no
// finding.
func TestMigrationsDoNotLockLargeTables(t *testing.T) {
	exceptions := readLockExceptions(t)
	used := map[string]bool{}
	filePattern := regexp.MustCompile(`^(\d+)_.+\.(up|down)\.sql$`)
	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		t.Fatalf("reading embedded migrations: %v", err)
	}
	for _, e := range entries {
		m := filePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		if v <= lockSafetyBaseline {
			continue
		}
		body, err := fs.ReadFile(Files, e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		for _, f := range LockFindings(string(body)) {
			key := e.Name() + " " + f.Rule
			if exceptions[key] {
				used[key] = true
				continue
			}
			t.Errorf("%s: %s on %s: %s\n  fix: %s\n  or justify it in migrations/lock_exceptions.txt as %q",
				e.Name(), f.Rule, strings.ToLower(f.Table), strings.TrimSpace(f.Statement), f.Fix, key+" <reason>")
		}
	}
	for key := range exceptions {
		if !used[key] {
			t.Errorf("lock_exceptions.txt: %q matches no finding; remove it", key)
		}
	}
}

func readLockExceptions(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("lock_exceptions.txt")
	if err != nil {
		t.Fatalf("reading lock_exceptions.txt: %v", err)
	}
	out := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			t.Errorf("lock_exceptions.txt:%d: want <file> <rule> <reason>, got %q", n, line)
			continue
		}
		out[f[0]+" "+f[1]] = true
	}
	return out
}

// TestLockFindingsCatchTheBlockingShapes drives each rule with the statement
// it exists for, on a large table that the file does not create.
func TestLockFindingsCatchTheBlockingShapes(t *testing.T) {
	cases := map[string]string{
		"index-not-concurrent":                "CREATE INDEX idx_ti_x ON task_instances (x);",
		"reindex-not-concurrent":              "REINDEX TABLE dag_runs;",
		"rewrite":                             "VACUUM FULL task_instances;",
		"column-type-change":                  "BEGIN; SET LOCAL lock_timeout = '5s'; ALTER TABLE task_instances ALTER COLUMN note TYPE varchar(10); COMMIT;",
		"add-column-not-null-without-default": "BEGIN; SET LOCAL lock_timeout = '5s'; ALTER TABLE dag_runs ADD COLUMN x int NOT NULL; COMMIT;",
		"add-column-volatile-default":         "BEGIN; SET LOCAL lock_timeout = '5s'; ALTER TABLE dag_runs ADD COLUMN x uuid DEFAULT gen_random_uuid(); COMMIT;",
		"set-not-null":                        "BEGIN; SET LOCAL lock_timeout = '5s'; ALTER TABLE task_instances ALTER COLUMN note SET NOT NULL; COMMIT;",
		"constraint-validated-in-place":       "BEGIN; SET LOCAL lock_timeout = '5s'; ALTER TABLE task_instances ADD CONSTRAINT c CHECK (try_number >= 0); COMMIT;",
		"unique-without-index":                "BEGIN; SET LOCAL lock_timeout = '5s'; ALTER TABLE dag_runs ADD CONSTRAINT u UNIQUE (run_id); COMMIT;",
		"lock-table":                          "BEGIN; LOCK TABLE task_instances IN ACCESS EXCLUSIVE MODE; COMMIT;",
		"full-table-write":                    "UPDATE task_instances SET note = '';",
		"no-lock-timeout":                     "ALTER TABLE task_instances ADD COLUMN y text;",
	}
	for rule, sql := range cases {
		t.Run(rule, func(t *testing.T) {
			if !hasRule(LockFindings(sql), rule) {
				t.Errorf("%s: no %s finding in %q (got %+v)", rule, rule, sql, LockFindings(sql))
			}
		})
	}
}

// TestLockFindingsAcceptTheSafePatterns: the patterns the fixes point at, the
// shapes the migrations from 048 on use, and small tables pass.
func TestLockFindingsAcceptTheSafePatterns(t *testing.T) {
	safe := map[string]string{
		"concurrent index":            "CREATE INDEX CONCURRENTLY idx_ti_x ON task_instances (x);",
		"nullable column":             "BEGIN; SET LOCAL lock_timeout = '5s'; ALTER TABLE task_instances ADD COLUMN IF NOT EXISTS y TIMESTAMPTZ; COMMIT;",
		"constant default":            "BEGIN;\nSET LOCAL lock_timeout = '5s';\nALTER TABLE task_instances ADD COLUMN z int NOT NULL DEFAULT 0;\nCOMMIT;",
		"not valid constraint":        "BEGIN; SET LOCAL lock_timeout = '5s'; ALTER TABLE task_instances ADD CONSTRAINT c CHECK (try_number >= 0) NOT VALID; COMMIT;",
		"validate in its own step":    "ALTER TABLE task_instances VALIDATE CONSTRAINT c; SET lock_timeout = '5s';",
		"unique from a built index":   "BEGIN; SET LOCAL lock_timeout = '5s'; ALTER TABLE dag_runs ADD CONSTRAINT u UNIQUE USING INDEX idx_u; COMMIT;",
		"bounded update":              "UPDATE task_instances SET note = '' WHERE id = '00000000-0000-0000-0000-000000000000';",
		"small table":                 "CREATE INDEX idx_users_x ON users (x); ALTER TABLE users ALTER COLUMN x SET NOT NULL;",
		"table created in the file":   "CREATE TABLE IF NOT EXISTS usage_events (id bigint); CREATE INDEX idx_u ON usage_events (id);",
		"default set as its own step": "BEGIN; SET LOCAL lock_timeout = '5s'; ALTER TABLE dag_runs ALTER COLUMN id SET DEFAULT dexaflow_uuidv7(); COMMIT;",
		"statement only in a comment": "-- CREATE INDEX idx ON task_instances (x);\n/* VACUUM FULL dag_runs; */ SELECT 1;",
		"function body is opaque":     "CREATE OR REPLACE FUNCTION f() RETURNS void LANGUAGE sql AS $$ UPDATE task_instances SET note = '' $$;",
		"semicolon inside a literal":  "COMMENT ON TABLE task_instances IS 'a; CREATE INDEX i ON task_instances (x)';",
	}
	for name, sql := range safe {
		t.Run(name, func(t *testing.T) {
			if got := LockFindings(sql); len(got) != 0 {
				t.Errorf("%s: want no finding in %q, got %+v", name, sql, got)
			}
		})
	}
}

// TestLockFindingsWouldHaveCaughtMigration015 pins the check against a file
// that shipped: 015 built idx_ti_running_heartbeat on task_instances without
// CONCURRENTLY and with no lock_timeout, the shape #2312 is about (030 later
// dropped that index).
func TestLockFindingsWouldHaveCaughtMigration015(t *testing.T) {
	body, err := fs.ReadFile(Files, "015_ti_heartbeat.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	got := LockFindings(string(body))
	if !hasRule(got, "index-not-concurrent") || !hasRule(got, "no-lock-timeout") {
		t.Errorf("015: got %+v, want index-not-concurrent and no-lock-timeout", got)
	}
}

func hasRule(findings []LockFinding, rule string) bool {
	for _, f := range findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}
