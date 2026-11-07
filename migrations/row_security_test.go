package migrations

import (
	"regexp"
	"slices"
	"testing"
)

// Migration 059 and RowSecurityTables must name the same tables: a table
// with a policy the list misses is never switched on, and a listed table
// without one would refuse every row once row level security is on.
func TestRowSecurityTablesMatchMigration(t *testing.T) {
	up, err := Files.ReadFile("059_tenant_row_security.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`CREATE POLICY ` + RowSecurityPolicy + ` ON (\w+)`)
	matches := re.FindAllStringSubmatch(string(up), -1)
	got := make([]string, 0, len(matches))
	for _, m := range matches {
		got = append(got, m[1])
	}
	want := slices.Clone(RowSecurityTables)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("migration 059 creates policies on %v, RowSecurityTables lists %v", got, want)
	}
	down, err := Files.ReadFile("059_tenant_row_security.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range want {
		if !regexp.MustCompile(`ALTER TABLE ` + tbl + ` DISABLE ROW LEVEL SECURITY`).Match(down) {
			t.Errorf("the down file leaves row level security on for %s", tbl)
		}
	}
}
