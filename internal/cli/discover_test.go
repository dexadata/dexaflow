package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestDiscoverProjects_TableDriven covers the multi-DAG workspace discovery
// contract documented at docs/dag-authoring.md#discovery-rules:
//
//   - A subdir with dag.py is a project (yaml optional).
//   - Workspace root with dag.py + dexaflow.yaml stays a project (back-compat).
//   - Walk depth caps at MaxWorkspaceDepth (5) from the workspace root.
//   - Skips exclude_paths defaults (.git, __pycache__, *.pyc, .venv, venv).
//   - Skips hidden dirs (.*).
//   - Duplicate dag_id across discovered projects is an error listing both
//     colliding paths — never silent last-write-wins.
//   - dag_id resolves from yaml when present, else from the subdir basename.
//
// Each case builds a tmpdir, runs DiscoverProjects, and asserts a stable set
// of project paths + dag_ids (sort-stable).
func TestDiscoverProjects_TableDriven(t *testing.T) {
	type want struct {
		projects  []string // relative paths from workspace, sorted
		dagIDs    []string // dag_ids in same order as projects
		errSubstr string   // when non-empty, expect an error containing this
		mustList  []string // when error expected, every string must appear in err
	}
	type setup struct {
		name string
		// files maps relative-path → file contents. Empty contents => directory.
		// A path ending in "/" creates an empty dir (no file).
		files map[string]string
		want  want
	}

	cases := []setup{
		{
			name:  "empty workspace returns no projects",
			files: map[string]string{},
			want:  want{projects: nil},
		},
		{
			name: "single subdir project with yaml",
			files: map[string]string{
				"sales_etl/dexaflow.yaml": "dag_id: sales_etl\n",
				"sales_etl/dag.py":        "from airflow.sdk import DAG\nwith DAG('sales_etl', schedule=None):\n    pass\n",
			},
			want: want{projects: []string{"sales_etl"}, dagIDs: []string{"sales_etl"}},
		},
		{
			name: "subdir without yaml uses basename as dag_id",
			files: map[string]string{
				"yamlless/dag.py": "from airflow.sdk import DAG\nwith DAG('whatever', schedule=None):\n    pass\n",
			},
			want: want{projects: []string{"yamlless"}, dagIDs: []string{"yamlless"}},
		},
		{
			name: "dbt-only project (dexaflow.yaml with dbt:, no dag.py) is discovered",
			files: map[string]string{
				"shopdbt/dexaflow.yaml":   "dag_id: shopdbt\ndbt:\n  project: .\n",
				"shopdbt/dbt_project.yml": "name: shop\n",
			},
			want: want{projects: []string{"shopdbt"}, dagIDs: []string{"shopdbt"}},
		},
		{
			name: "dexaflow.yaml without dag.py and without a dbt: block is NOT a project",
			files: map[string]string{
				"notaproj/dexaflow.yaml": "dag_id: notaproj\n",
			},
			want: want{projects: nil},
		},
		{
			name: "root project (backward compat) is discovered",
			files: map[string]string{
				"dexaflow.yaml": "dag_id: root_dag\n",
				"dag.py":        "from airflow.sdk import DAG\nwith DAG('root_dag', schedule=None):\n    pass\n",
			},
			want: want{projects: []string{"."}, dagIDs: []string{"root_dag"}},
		},
		{
			name: "multiple sibling subdirs all discovered",
			files: map[string]string{
				"a/dexaflow.yaml": "dag_id: a\n",
				"a/dag.py":        "x = 1\n",
				"b/dexaflow.yaml": "dag_id: b\n",
				"b/dag.py":        "x = 1\n",
				"c/dag.py":        "x = 1\n", // no yaml — dag_id defaults to "c"
			},
			want: want{
				projects: []string{"a", "b", "c"},
				dagIDs:   []string{"a", "b", "c"},
			},
		},
		{
			name: "nested at depth 5 is discovered (workspace=0, project=5)",
			files: map[string]string{
				"a/b/c/d/deep/dexaflow.yaml": "dag_id: deep\n",
				"a/b/c/d/deep/dag.py":        "x = 1\n",
			},
			want: want{projects: []string{"a/b/c/d/deep"}, dagIDs: []string{"deep"}},
		},
		{
			name: "beyond max depth (>5) is ignored",
			files: map[string]string{
				"a/b/c/d/e/toodeep/dexaflow.yaml": "dag_id: toodeep\n",
				"a/b/c/d/e/toodeep/dag.py":        "x = 1\n",
			},
			want: want{projects: nil},
		},
		{
			name: "excluded dirs are skipped (.git, __pycache__, .venv, etc.)",
			files: map[string]string{
				".git/dag.py":        "x = 1\n",
				"__pycache__/dag.py": "x = 1\n",
				".venv/dag.py":       "x = 1\n",
				"venv/dag.py":        "x = 1\n",
				"normal/dag.py":      "x = 1\n",
			},
			want: want{projects: []string{"normal"}, dagIDs: []string{"normal"}},
		},
		{
			name: "hidden dirs (any name starting with .) are skipped",
			files: map[string]string{
				".hidden_proj/dag.py": "x = 1\n",
				"visible/dag.py":      "x = 1\n",
			},
			want: want{projects: []string{"visible"}, dagIDs: []string{"visible"}},
		},
		{
			name: "subdir with dexaflow.yaml but no dag.py is not a project",
			files: map[string]string{
				"orphan_yaml/dexaflow.yaml": "dag_id: orphan\n",
				"real/dag.py":               "x = 1\n",
			},
			want: want{projects: []string{"real"}, dagIDs: []string{"real"}},
		},
		{
			name: "duplicate dag_id across subdirs is a hard error",
			files: map[string]string{
				"foo/dexaflow.yaml": "dag_id: shared\n",
				"foo/dag.py":        "x = 1\n",
				"bar/dexaflow.yaml": "dag_id: shared\n",
				"bar/dag.py":        "x = 1\n",
			},
			want: want{
				errSubstr: "duplicate",
				mustList:  []string{"shared", "foo", "bar"},
			},
		},
		{
			name: "duplicate dag_id from yaml vs subdir-basename fallback collides",
			files: map[string]string{
				// subdir basename "collide" + a yaml elsewhere claiming dag_id "collide"
				"collide/dag.py":      "x = 1\n",
				"other/dexaflow.yaml": "dag_id: collide\n",
				"other/dag.py":        "x = 1\n",
			},
			want: want{
				errSubstr: "duplicate",
				mustList:  []string{"collide", "other"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			for rel, content := range tc.files {
				abs := filepath.Join(ws, rel)
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
				}
				if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
					t.Fatalf("write %s: %v", abs, err)
				}
			}

			projects, err := DiscoverProjects(ws)

			if tc.want.errSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (projects: %+v)", tc.want.errSubstr, projects)
				}
				if !strings.Contains(err.Error(), tc.want.errSubstr) {
					t.Errorf("error %q should contain %q", err.Error(), tc.want.errSubstr)
				}
				for _, must := range tc.want.mustList {
					if !strings.Contains(err.Error(), must) {
						t.Errorf("error %q should list %q", err.Error(), must)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Project paths come back absolute; normalize to relative-from-workspace
			// and sort for deterministic comparison.
			gotPaths := make([]string, len(projects))
			gotIDs := make([]string, len(projects))
			for i, p := range projects {
				rel, rerr := filepath.Rel(ws, p.Path)
				if rerr != nil {
					t.Fatalf("rel %s vs %s: %v", ws, p.Path, rerr)
				}
				gotPaths[i] = rel
				gotIDs[i] = p.DagID
			}
			sort.Strings(gotPaths)
			// align dagIDs to sorted paths
			sortedIDs := make([]string, len(projects))
			for i, want := range gotPaths {
				for j, orig := range projects {
					rel, _ := filepath.Rel(ws, orig.Path)
					if rel == want {
						sortedIDs[i] = projects[j].DagID
						break
					}
				}
			}

			if len(gotPaths) == 0 && len(tc.want.projects) == 0 {
				return // both empty — pass
			}
			if !equalSlices(gotPaths, tc.want.projects) {
				t.Errorf("projects:\n  got:  %v\n  want: %v", gotPaths, tc.want.projects)
			}
			if tc.want.dagIDs != nil && !equalSlices(sortedIDs, tc.want.dagIDs) {
				t.Errorf("dag_ids:\n  got:  %v\n  want: %v", sortedIDs, tc.want.dagIDs)
			}
		})
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
