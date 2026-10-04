package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseNamedQueries(t *testing.T) {
	src := `-- name: First :one
-- A comment that belongs to First.
SELECT 1
FROM a
WHERE id = $1;

-- name: Second :many
SELECT 2;
`
	got := parseNamedQueries(src)
	if len(got) != 2 {
		t.Fatalf("want 2 queries, got %d: %v", len(got), got)
	}
	first := got["First"]
	if !strings.HasPrefix(first, "-- A comment") || !strings.HasSuffix(first, "WHERE id = $1") {
		t.Errorf("First: unexpected body %q", first)
	}
	if got["Second"] != "SELECT 2" {
		t.Errorf("Second: want %q, got %q", "SELECT 2", got["Second"])
	}
}

func TestBindSqlcArgs(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantSQL   string
		wantNames []string
	}{
		{
			name:    "no named args",
			in:      "SELECT * FROM t WHERE a = $1 LIMIT $2",
			wantSQL: "SELECT * FROM t WHERE a = $1 LIMIT $2",
		},
		{
			name:      "narg after positionals, reused name keeps its number",
			in:        "WHERE a = $1 AND (sqlc.narg('paused')::bool IS NULL OR p = sqlc.narg('paused')) AND s = sqlc.narg('state') LIMIT $2",
			wantSQL:   "WHERE a = $1 AND ($3::bool IS NULL OR p = $3) AND s = $4 LIMIT $2",
			wantNames: []string{"paused", "state"},
		},
		{
			name:      "unquoted arg",
			in:        "WHERE started_at <= now() - make_interval(secs => sqlc.arg(grace_seconds)::float8)",
			wantSQL:   "WHERE started_at <= now() - make_interval(secs => $1::float8)",
			wantNames: []string{"grace_seconds"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, names := bindSqlcArgs(tc.in)
			if sql != tc.wantSQL {
				t.Errorf("sql:\n got %q\nwant %q", sql, tc.wantSQL)
			}
			if !reflect.DeepEqual(names, tc.wantNames) {
				t.Errorf("names: got %v, want %v", names, tc.wantNames)
			}
		})
	}
}

func TestSeqScans(t *testing.T) {
	plan := map[string]any{
		"Node Type": "Hash Join",
		"Plans": []any{
			map[string]any{"Node Type": "Seq Scan", "Relation Name": "dag_runs"},
			map[string]any{"Node Type": "Index Scan", "Relation Name": "dags"},
			map[string]any{"Node Type": "Hash", "Plans": []any{
				map[string]any{"Node Type": "Seq Scan", "Relation Name": "task_instances"},
				map[string]any{"Node Type": "Seq Scan", "Relation Name": "dag_runs"},
			}},
		},
	}
	got := seqScans(plan)
	want := []string{"dag_runs", "task_instances"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
