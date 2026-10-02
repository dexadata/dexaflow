package migrations_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/migrations"
)

// TestDefaultIsTheOnlyTenantAnyMigrationCreates pins the sentence the boot-time
// OIDC name warning tells operators: map your tenant_claims values to "default",
// because nothing in this project creates another tenant.
//
// That remedy is a claim about these files, and it is the kind of claim a later
// migration can falsify without any test noticing: adding a second seeded tenant
// would leave every unit test green and the warning's advice wrong. The warning
// is also load-bearing in the other direction, since a claim value pointing at a
// tenant that does not exist denies every login carrying it with no supported
// way to fix it other than direct SQL.
//
// If a migration ever does seed a second tenant, this test is the place that
// says so: update the warning text in cmd/dexaflow-server/main.go, the chart
// comment on auth.oidc.tenantClaims and the configuration reference with it.
func TestDefaultIsTheOnlyTenantAnyMigrationCreates(t *testing.T) {
	// Matches an insert into the tenants table whatever the whitespace or case,
	// including one hidden inside a CTE or an INSERT ... SELECT.
	insert := regexp.MustCompile(`(?is)insert\s+into\s+tenants\b`)

	names, err := fs.Glob(migrations.Files, "*.up.sql")
	if err != nil {
		t.Fatalf("globbing migrations: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no up migrations were embedded, so this test proves nothing")
	}
	var found []string
	for _, name := range names {
		body, rerr := fs.ReadFile(migrations.Files, name)
		if rerr != nil {
			t.Fatalf("reading %s: %v", name, rerr)
		}
		for range insert.FindAllIndex(body, -1) {
			found = append(found, name)
		}
	}
	if len(found) != 1 || !strings.HasPrefix(found[0], "001_") {
		t.Fatalf("INSERT INTO tenants appears in %v; the OIDC boot warning and the chart both tell operators that the first migration creates the only tenant there is", found)
	}
	first, err := fs.ReadFile(migrations.Files, found[0])
	if err != nil {
		t.Fatalf("reading %s: %v", found[0], err)
	}
	if !strings.Contains(string(first), "VALUES ('default'") {
		t.Errorf("%s no longer seeds a tenant named 'default'; the warning tells operators to map their claim values to that name", found[0])
	}
}
