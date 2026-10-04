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

// TestRoleMigrationsApplyToEveryTenant fails when an up migration that changes
// roles or role_permissions filters on the default tenant's name (#1305).
//
// The convention it enforces: a migration that adds, changes or revokes a
// built-in role or one of its grants applies to every tenant by joining on
// roles.is_system, never to "default" alone. Custom roles (is_system false) are
// the tenant's own and a migration never touches them.
func TestRoleMigrationsApplyToEveryTenant(t *testing.T) {
	writesRoles := regexp.MustCompile(`(?is)\b(insert\s+into|update|delete\s+from)\s+(roles|role_permissions)\b`)
	namesDefault := regexp.MustCompile(`(?i)\bname\s*=\s*'default'`)

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
		code := stripLineComments(string(body))
		if !writesRoles.MatchString(code) || !namesDefault.MatchString(code) {
			continue
		}
		if _, ok := defaultOnlyRoleMigrations[name]; ok {
			continue
		}
		t.Errorf("%s changes roles or role_permissions and filters on name = 'default'; apply it to every tenant's built-in roles (JOIN on roles.is_system across all tenants) so tenants created by the service API do not drift (#1305)", name)
	}
	for name := range defaultOnlyRoleMigrations {
		if _, serr := fs.Stat(migrations.Files, name); serr != nil {
			t.Errorf("%s is exempted in defaultOnlyRoleMigrations but is not embedded: %v", name, serr)
		}
	}
}

// stripLineComments drops "--" comments so prose that mentions the default
// tenant is not mistaken for a filter on it.
func stripLineComments(sql string) string {
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
