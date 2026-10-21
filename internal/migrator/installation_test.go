package migrator

import (
	"strings"
	"testing"
)

// The plan is ADR 0090 decision 3 as a table: what the wrapper does with the
// installation id for each combination of what the ConfigMap holds, what the
// chart pins and what the database carries.
func TestPlanInstallation(t *testing.T) {
	newID := func() string { return "fresh" }
	cases := []struct {
		name   string
		cm     storedID
		pinned string
		db     databaseID
		want   installationPlan
		refuse []string // substrings of the refusal; empty when the plan proceeds
	}{
		{
			name: "first install: no ConfigMap, no schema",
			db:   databaseID{},
			want: installationPlan{id: "fresh", writeDB: true, createCM: true},
		},
		{
			name:   "first install with a pinned id",
			pinned: "pinned",
			want:   installationPlan{id: "pinned", writeDB: true, createCM: true},
		},
		{
			name: "existing installation upgrading: schema from before the table",
			db:   databaseID{schema: true},
			want: installationPlan{id: "fresh", writeDB: true, createCM: true},
		},
		{
			name: "no ConfigMap, the database carries an id: the release moved namespace, or the ConfigMap write was cut short",
			db:   databaseID{schema: true, id: "db"},
			want: installationPlan{id: "db", createCM: true},
		},
		{
			name:   "no ConfigMap, the database carries another id than the pinned one",
			pinned: "pinned",
			db:     databaseID{schema: true, id: "db"},
			refuse: []string{`"db"`, `"pinned"`, "installation.id"},
		},
		{
			name: "established release, same database",
			cm:   storedID{found: true, id: "cm"},
			db:   databaseID{schema: true, id: "cm"},
			want: installationPlan{id: "cm"},
		},
		{
			name:   "established release pinned to its own id",
			cm:     storedID{found: true, id: "cm"},
			pinned: "cm",
			db:     databaseID{schema: true, id: "cm"},
			want:   installationPlan{id: "cm"},
		},
		{
			name:   "established release, emptied database",
			cm:     storedID{found: true, id: "cm"},
			db:     databaseID{},
			refuse: []string{"database replaced", "has no schema", `"cm"`},
		},
		{
			name:   "established release, a schema without an id",
			cm:     storedID{found: true, id: "cm"},
			db:     databaseID{schema: true},
			refuse: []string{"database replaced", "carries no installation id"},
		},
		{
			name:   "established release, another installation's database",
			cm:     storedID{found: true, id: "cm"},
			db:     databaseID{schema: true, id: "other"},
			refuse: []string{"database replaced", `"other"`, `"cm"`},
		},
		{
			name:   "acknowledged: adopt the database's id",
			cm:     storedID{found: true, id: "cm"},
			pinned: "other",
			db:     databaseID{schema: true, id: "other"},
			want:   installationPlan{id: "other", updateCM: true},
		},
		{
			name:   "acknowledged: a new id for an emptied database",
			cm:     storedID{found: true, id: "cm"},
			pinned: "new",
			db:     databaseID{},
			want:   installationPlan{id: "new", writeDB: true, updateCM: true},
		},
		{
			name:   "acknowledged: a new id for a schema without an id",
			cm:     storedID{found: true, id: "cm"},
			pinned: "new",
			db:     databaseID{schema: true},
			want:   installationPlan{id: "new", writeDB: true, updateCM: true},
		},
		{
			name:   "a new pinned id does not re-key another installation's database",
			cm:     storedID{found: true, id: "cm"},
			pinned: "new",
			db:     databaseID{schema: true, id: "other"},
			refuse: []string{`"other"`, `"new"`},
		},
		{
			name:   "a new pinned id does not re-key this release's own database",
			cm:     storedID{found: true, id: "cm"},
			pinned: "new",
			db:     databaseID{schema: true, id: "cm"},
			refuse: []string{`"cm"`, `"new"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, f := planInstallation(tc.cm, tc.pinned, tc.db, newID)
			if len(tc.refuse) > 0 {
				if f == nil {
					t.Fatalf("plan = %+v, want a refusal", got)
				}
				if f.Kind != KindReplaced || f.Code != ExitReplaced {
					t.Errorf("refusal kind %v code %d, want %v %d", f.Kind, f.Code, KindReplaced, ExitReplaced)
				}
				for _, s := range tc.refuse {
					if !strings.Contains(f.Summary, s) {
						t.Errorf("refusal %q does not name %s", f.Summary, s)
					}
				}
				return
			}
			if f != nil {
				t.Fatalf("refused: %s", f.Summary)
			}
			if got != tc.want {
				t.Errorf("plan = %+v, want %+v", got, tc.want)
			}
		})
	}
}
