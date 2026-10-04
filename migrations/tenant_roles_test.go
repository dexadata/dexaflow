package migrations_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/migrations"
)

// defaultOnlyRoleMigrations lists the up migrations allowed to touch roles or
// role_permissions while naming the default tenant, each with the reason.
//
// Tenants created through the service API (#1283) copy their built-in roles
// from "default" once, at creation time. A migration that changes the built-in
// roles of "default" only therefore leaves every other tenant behind, and a
// grant it revokes survives there (#1305). Adding a file here is the wrong fix
// for a failure of TestRoleMigrationsApplyToEveryTenant: write the migration
// against every tenant's is_system roles instead.
var defaultOnlyRoleMigrations = map[string]string{
	"001_init_tenants_and_rbac.up.sql":         "it creates the only tenant there is at that point, so default is every tenant",
	"025_role_ladder.up.sql":                   "027_reconcile_tenant_system_roles brings every other tenant's built-in roles in line with default afterwards",
	"027_reconcile_tenant_system_roles.up.sql": "it reads default as the reference ladder and writes only to the other tenants",
}

// writesRoles matches a statement that changes roles or role_permissions,
// whatever the case, whitespace, ONLY keyword, schema prefix or identifier
// quoting: INSERT INTO, UPDATE, DELETE FROM and MERGE INTO.
var writesRoles = regexp.MustCompile(`(?is)\b(insert\s+into|update|delete\s+from|merge\s+into)\s+(only\s+)?("?\w+"?\s*\.\s*)?"?(roles|role_permissions)"?([^\w"]|$)`)

// namesDefault matches the default tenant's name as a SQL string, in any
// position: "t.name = 'default'", "'default' = t.name", "name IN ('default')",
// a CTE or subquery that selects it, an E” string or a dollar-quoted string.
// Matching the literal rather than one spelling of the comparison is what keeps
// a reworded filter from slipping past.
var namesDefault = regexp.MustCompile(`(?i)'default'|\$\w*\$default\$\w*\$`)

// TestRoleMigrationsApplyToEveryTenant fails when an up migration that changes
// roles or role_permissions names the default tenant (#1305).
//
// The convention it enforces: a migration that adds, changes or revokes a
// built-in role or one of its grants applies to every tenant by joining on
// roles.is_system, never to "default" alone. Custom roles (is_system false) are
// the tenant's own and a migration never touches them.
func TestRoleMigrationsApplyToEveryTenant(t *testing.T) {
	names, err := fs.Glob(migrations.Files, "*.up.sql")
	if err != nil {
		t.Fatalf("globbing migrations: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no up migrations were embedded, so this test proves nothing")
	}
	for _, name := range names {
		body, rerr := fs.ReadFile(migrations.Files, name)
		if rerr != nil {
			t.Fatalf("reading %s: %v", name, rerr)
		}
		if !writesDefaultOnlyRoles(string(body)) {
			continue
		}
		if _, ok := defaultOnlyRoleMigrations[name]; ok {
			continue
		}
		t.Errorf("%s changes roles or role_permissions and names the default tenant; apply it to every tenant's built-in roles (JOIN on roles.is_system across all tenants) so tenants created by the service API do not drift (#1305)", name)
	}
	for name := range defaultOnlyRoleMigrations {
		if _, serr := fs.Stat(migrations.Files, name); serr != nil {
			t.Errorf("%s is exempted in defaultOnlyRoleMigrations but is not embedded: %v", name, serr)
		}
	}
}

// TestRoleGuardCatchesOtherSpellings pins what the guard above recognizes, so
// a reworded default-only migration cannot pass it unnoticed and prose in a
// comment does not trip it.
func TestRoleGuardCatchesOtherSpellings(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"INSERT INTO roles (tenant_id, name) SELECT t.id, 'x' FROM tenants t WHERE t.name = 'default';", true},
		{"insert into role_permissions select r.id, p.id from roles r join tenants t on t.id = r.tenant_id and 'default' = t.name, permissions p;", true},
		{"DELETE FROM role_permissions WHERE role_id IN (SELECT r.id FROM roles r JOIN tenants t ON t.id = r.tenant_id WHERE t.name IN ('default'));", true},
		{"WITH d AS (SELECT id FROM tenants WHERE \"name\" = 'default')\nUPDATE roles SET description = 'x' FROM d WHERE roles.tenant_id = d.id;", true},
		{"UPDATE ONLY public.roles SET description = 'x' WHERE tenant_id = (SELECT id FROM tenants WHERE name = E'default');", true},
		{"INSERT INTO \"role_permissions\" SELECT r.id, p.id FROM roles r, permissions p, tenants t WHERE t.id = r.tenant_id AND t.name = $$default$$;", true},
		{"MERGE INTO roles r USING (SELECT id FROM tenants WHERE name = 'default') d ON r.tenant_id = d.id WHEN MATCHED THEN DO NOTHING;", true},
		{"-- unlike 025, this does not filter on t.name = 'default'\nINSERT INTO role_permissions SELECT r.id, p.id FROM roles r, permissions p WHERE r.is_system;", false},
		{"/* t.name = 'default' */ INSERT INTO role_permissions SELECT r.id, p.id FROM roles r, permissions p WHERE r.is_system;", false},
		{"SELECT 1 FROM roles r JOIN tenants t ON t.id = r.tenant_id WHERE t.name = 'default';", false},
		{"INSERT INTO roles_audit SELECT * FROM tenants WHERE name = 'default';", false},
	}
	for _, c := range cases {
		if got := writesDefaultOnlyRoles(c.sql); got != c.want {
			t.Errorf("writesDefaultOnlyRoles(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}

// writesDefaultOnlyRoles reports whether sql, with its comments removed,
// changes roles or role_permissions and names the default tenant anywhere.
func writesDefaultOnlyRoles(sql string) bool {
	code := stripComments(sql)
	return writesRoles.MatchString(code) && namesDefault.MatchString(code)
}

// blockComment matches a /* ... */ comment, across lines.
var blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// stripComments drops "--" and "/* */" comments so prose that mentions the
// default tenant is not mistaken for a filter on it.
func stripComments(sql string) string {
	sql = blockComment.ReplaceAllString(sql, " ")
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
