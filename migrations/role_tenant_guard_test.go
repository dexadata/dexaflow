package migrations_test

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/migrations"
)

// defaultOnlyRoleMigrations are the up migrations written before the rule in
// TestBuiltInRoleMigrationsReachEveryTenant existed. Each one writes built-in
// roles or their grants for the "default" tenant only, which was correct when
// it ran: "default" was the only tenant, and the service API copies its ladder
// into every tenant it creates (#1283). They are grandfathered by name so the
// rule binds every migration after them; do not add to this list.
var defaultOnlyRoleMigrations = map[string]string{
	"001_init_tenants_and_rbac.up.sql": "seeds the admin role before any other tenant can exist",
	"025_role_ladder.up.sql":           "seeds viewer/editor/operator before the service API could create tenants",
}

var (
	// sqlLineComment and sqlBlockComment strip comments, so prose such as
	// "created for the default tenant" never counts as a filter.
	sqlLineComment  = regexp.MustCompile(`--[^\n]*`)
	sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// roleWrite matches a statement that writes rows of roles or
	// role_permissions, whatever the whitespace or case, including one inside a
	// CTE or an INSERT ... SELECT.
	roleWrite = regexp.MustCompile(`(?is)\b(insert\s+into|update|delete\s+from)\s+(roles|role_permissions)\b`)
	// defaultTenantFilter matches a predicate that pins a name to 'default',
	// which in a statement writing roles is the default tenant filter
	// (t.name = 'default', name = 'default', name IN ('default')).
	defaultTenantFilter = regexp.MustCompile(`(?is)\bname\s*(=\s*'default'|in\s*\(\s*'default'\s*\))`)
)

// defaultOnlyRoleWrites returns the statements of sql that write roles or
// role_permissions while filtering on the default tenant by name. It splits on
// semicolons after stripping comments; the migrations hold no string literals
// or function bodies where that would cut a statement in a misleading place.
func defaultOnlyRoleWrites(sql string) []string {
	sql = sqlBlockComment.ReplaceAllString(sql, "")
	sql = sqlLineComment.ReplaceAllString(sql, "")
	var found []string
	for _, stmt := range strings.Split(sql, ";") {
		if roleWrite.MatchString(stmt) && defaultTenantFilter.MatchString(stmt) {
			found = append(found, strings.Join(strings.Fields(stmt), " "))
		}
	}
	return found
}

// TestBuiltInRoleMigrationsReachEveryTenant enforces the convention from #1305:
// a migration that changes the built-in roles or their permissions applies to
// every tenant (join roles on is_system across all tenants), not to "default"
// only.
//
// Tenants created through the service API copy default's ladder once, at
// creation, and the copy never deletes. A later migration that only touches
// "default" therefore leaves every other tenant on the old ladder: an added
// grant is missing there, and a revoked one survives where it should be gone.
// No other test notices, because the integration suite only compares a freshly
// created tenant with "default".
func TestBuiltInRoleMigrationsReachEveryTenant(t *testing.T) {
	names, err := fs.Glob(migrations.Files, "*.up.sql")
	if err != nil {
		t.Fatalf("globbing migrations: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no up migrations were embedded, so this test proves nothing")
	}
	offending := map[string]bool{}
	for _, name := range names {
		body, rerr := fs.ReadFile(migrations.Files, name)
		if rerr != nil {
			t.Fatalf("reading %s: %v", name, rerr)
		}
		stmts := defaultOnlyRoleWrites(string(body))
		if len(stmts) == 0 {
			continue
		}
		offending[name] = true
		if _, ok := defaultOnlyRoleMigrations[name]; ok {
			continue
		}
		for _, s := range stmts {
			t.Errorf("%s changes built-in roles for the default tenant only; join roles on is_system across all tenants instead (#1305):\n  %s", name, s)
		}
	}
	// An allowlist entry that no longer matches is stale: drop it rather than
	// leave a pass that a new migration could reuse by name.
	var stale []string
	for name := range defaultOnlyRoleMigrations {
		if !offending[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("grandfathered migrations no longer write default-only role rows (or are gone); remove them from defaultOnlyRoleMigrations: %v", stale)
	}
}

// TestDefaultOnlyRoleWritesDetector proves the guard above can fail: each
// sample is a statement shape a migration could plausibly use.
func TestDefaultOnlyRoleWritesDetector(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want int
	}{
		{
			name: "grant joined to the default tenant",
			sql: `INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r
JOIN tenants t ON t.id = r.tenant_id AND t.name = 'default'
JOIN permissions p ON p.action = 'read' AND p.resource = 'secret'
ON CONFLICT DO NOTHING;`,
			want: 1,
		},
		{
			name: "revoke through a default tenant subquery",
			sql: `delete from role_permissions
where role_id in (select r.id from roles r join tenants t on t.id = r.tenant_id
                  where t.name='default' and r.name = 'viewer')
  and permission_id = (select id from permissions where action = 'read' and resource = 'xcom');`,
			want: 1,
		},
		{
			name: "role seeded for default with IN",
			sql: `INSERT INTO roles (tenant_id, name, description, is_system)
SELECT id, 'auditor', 'Audit access', true FROM tenants WHERE name IN ('default');`,
			want: 1,
		},
		{
			name: "role renamed for default only",
			sql: `UPDATE roles SET description = 'x'
WHERE tenant_id = (SELECT id FROM tenants WHERE name = 'default') AND name = 'viewer';`,
			want: 1,
		},
		{
			name: "grant to the system role of every tenant",
			sql: `INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r
JOIN permissions p ON p.action = 'read' AND p.resource = 'secret'
WHERE r.is_system AND r.name = 'viewer'
ON CONFLICT DO NOTHING;`,
			want: 0,
		},
		{
			name: "role seeded in every tenant",
			sql: `-- Seed auditor for the default tenant and every other one.
INSERT INTO roles (tenant_id, name, description, is_system)
SELECT t.id, 'auditor', 'Audit access', true FROM tenants t
ON CONFLICT (tenant_id, name) DO NOTHING;`,
			want: 0,
		},
		{
			name: "default filter outside role tables",
			sql: `INSERT INTO pools (tenant_id, name, slots, is_default)
SELECT id, 'default_pool', 128, true FROM tenants WHERE name = 'default';
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE r.name = 'admin';`,
			want: 0,
		},
		{
			name: "filter only mentioned in a comment",
			sql: `/* not WHERE t.name = 'default' */
DELETE FROM role_permissions WHERE role_id IN (SELECT id FROM roles WHERE is_system); -- t.name = 'default'`,
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultOnlyRoleWrites(tc.sql); len(got) != tc.want {
				t.Errorf("defaultOnlyRoleWrites found %d statements %q, want %d", len(got), got, tc.want)
			}
		})
	}
}
