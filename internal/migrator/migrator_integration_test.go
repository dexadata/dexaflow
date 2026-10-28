//go:build integration

package migrator

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/migrations"
)

// adminURL is DATABASE_URL (the migrated integration database of make
// test-integration), used only to create and drop the scratch databases each
// test migrates.
func adminURL(t *testing.T) *url.URL {
	t.Helper()
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL not set; run via make test-integration")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("DATABASE_URL: %v", err)
	}
	return u
}

func withDatabase(u *url.URL, name string) string {
	c := *u
	c.Path = "/" + name
	return c.String()
}

func scratchName(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "dexaflow_migrator_" + hex.EncodeToString(b)
}

// createScratch creates an empty database and drops it when the test ends.
func createScratch(t *testing.T, admin *url.URL) string {
	t.Helper()
	name := scratchName(t)
	db, err := sql.Open("pgx", admin.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("CREATE DATABASE %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("dropping %s: %v", name, err)
		}
	})
	return withDatabase(admin, name)
}

func exec(t *testing.T, dsn, stmt string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

func mustRun(t *testing.T, wantCode int, args ...string) (stdout, stderr string) {
	t.Helper()
	code, stdout, stderr := runMain(t, args...)
	if code != wantCode {
		t.Fatalf("dexaflow-migrate %s: exit %d, want %d\nstdout: %s\nstderr: %s",
			strings.Join(args[1:], " "), code, wantCode, stdout, stderr)
	}
	return stdout, stderr
}

func TestIntegrationMissingDatabase(t *testing.T) {
	admin := adminURL(t)
	name := scratchName(t)
	_, stderr := mustRun(t, ExitMissingDatabase, "-database="+withDatabase(admin, name), "up")
	for _, s := range []string{
		`database "` + name + `" does not exist on ` + admin.Host,
		"CREATE DATABASE " + name + ";",
		"database.url",
		"SQLSTATE 3D000",
	} {
		if !strings.Contains(stderr, s) {
			t.Errorf("stderr lacks %q:\n%s", s, stderr)
		}
	}
}

func TestIntegrationWrongPassword(t *testing.T) {
	admin := adminURL(t)
	bad := *admin
	bad.User = url.UserPassword(admin.User.Username(), "not-the-password")
	code, stdout, stderr := runMain(t, "-database="+bad.String(), "version")
	if code == 0 {
		t.Skipf("the server accepted a wrong password (trust auth for this host?); stdout: %s", stdout)
	}
	if code != ExitAuth {
		t.Fatalf("exit %d, want %d\nstderr: %s", code, ExitAuth, stderr)
	}
	if !strings.Contains(stderr, `rejected user "`+admin.User.Username()+`"`) {
		t.Errorf("stderr does not name the user:\n%s", stderr)
	}
	if strings.Contains(stderr, "not-the-password") {
		t.Errorf("stderr leaks the password:\n%s", stderr)
	}
}

// The happy path, then the commands the upgrade guide's recovery steps use:
// version, force and goto, over both the embedded migrations and -path.
func TestIntegrationUpVersionDirtyForceGoto(t *testing.T) {
	admin := adminURL(t)
	dsn := createScratch(t, admin)
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}
	want := strconv.FormatUint(uint64(latest), 10)

	stdout, _ := mustRun(t, 0, "-database="+dsn, "up")
	if !strings.HasSuffix(strings.TrimSpace(stdout), want) {
		t.Fatalf("up did not end at version %s:\n%s", want, stdout)
	}
	stdout, _ = mustRun(t, 0, "-database="+dsn, "up")
	if !strings.Contains(stdout, "no change") {
		t.Errorf("a second up did not report no change:\n%s", stdout)
	}
	stdout, _ = mustRun(t, 0, "-path=../../migrations", "-database="+dsn, "version")
	if strings.TrimSpace(stdout) != want {
		t.Fatalf("version = %q, want %s", stdout, want)
	}

	exec(t, dsn, "UPDATE schema_migrations SET dirty = true")
	stdout, _ = mustRun(t, 0, "-database="+dsn, "version")
	if strings.TrimSpace(stdout) != want+" (dirty)" {
		t.Fatalf("version on a dirty schema = %q, want %q", stdout, want+" (dirty)")
	}
	_, stderr := mustRun(t, ExitDirty, "-database="+dsn, "up")
	for _, s := range []string{"dirty at version " + want, "#an-interrupted-migration", "Dirty database version " + want} {
		if !strings.Contains(stderr, s) {
			t.Errorf("stderr lacks %q:\n%s", s, stderr)
		}
	}
	mustRun(t, 0, "-database="+dsn, "force", want)

	prev := strconv.FormatUint(uint64(latest-1), 10)
	mustRun(t, 0, "-path=../../migrations", "-database="+dsn, "goto", prev)
	stdout, _ = mustRun(t, 0, "-database="+dsn, "version")
	if strings.TrimSpace(stdout) != prev {
		t.Fatalf("version after goto %s = %q", prev, stdout)
	}
	mustRun(t, 0, "-database="+dsn, "up")
	stdout, _ = mustRun(t, 0, "-database="+dsn, "version")
	if strings.TrimSpace(stdout) != want {
		t.Fatalf("version after the second up = %q, want %s", stdout, want)
	}
}
