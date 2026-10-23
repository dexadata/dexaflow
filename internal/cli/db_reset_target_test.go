package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// liteHomeOnPort gives the test a HOME whose Lite install recorded port as
// its Docker Postgres port, the file `db reset` resolves its target from.
func liteHomeOnPort(t *testing.T, port string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SUDO_USER", "")
	t.Setenv("DEXAFLOW_DATABASE_URL", "")
	t.Setenv("LEOFLOW_DATABASE_URL", "")
	devDir := filepath.Join(home, ".dexaflow", "dev")
	if err := os.MkdirAll(devDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devDir, "db-port"), []byte(port), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Lite owns its database and never reads DEXAFLOW_DATABASE_URL, so a reset
// with the variable naming another database dropped Lite's while the operator
// believed it acted on the one they named (#1185). It now refuses, names both,
// and drops nothing. LEOFLOW_DATABASE_URL, the name from before the rename,
// is refused the same way.
func TestDBResetRefusesWhenTheDatabaseURLNamesAnotherDatabase(t *testing.T) {
	for _, name := range []string{"DEXAFLOW_DATABASE_URL", "LEOFLOW_DATABASE_URL"} {
		t.Run(name, func(t *testing.T) {
			liteHomeOnPort(t, "55439")
			t.Setenv(name, "postgres://admin:s3cret@db.example.com:6543/warehouse?sslmode=require")
			_, _, err := run(t, "db", "reset", "--yes")
			if err == nil {
				t.Fatal("db reset ran with the variable naming another database")
			}
			msg := err.Error()
			for _, want := range []string{
				"refusing", name, "nothing was dropped",
				"host db.example.com, port 6543, database warehouse",
				"host localhost, port 55439, database leoflow_dev",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("the refusal must contain %q, got: %v", want, err)
				}
			}
			if strings.Contains(msg, "s3cret") {
				t.Errorf("the refusal printed the password: %v", err)
			}
		})
	}
}

// Each part of the target counts: the same server with another database, or
// the same database name on another port, is another database.
func TestDBResetRefusesADatabaseURLThatDiffersInOnePart(t *testing.T) {
	for _, url := range []string{
		"postgres://leoflow:leoflow@localhost:55439/leoflow?sslmode=disable",
		"postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable",
		"postgres://leoflow:leoflow@db.internal:55439/leoflow_dev?sslmode=disable",
		"not a database url ::",
	} {
		t.Run(url, func(t *testing.T) {
			liteHomeOnPort(t, "55439")
			t.Setenv("DEXAFLOW_DATABASE_URL", url)
			_, _, err := run(t, "db", "reset", "--yes")
			if err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Errorf("db reset with DEXAFLOW_DATABASE_URL=%s: err = %v, want a refusal", url, err)
			}
		})
	}
}

// A variable that names Lite's own database is not a disagreement: the reset
// goes ahead (and here fails to connect, since nothing listens on the port).
// localhost and 127.0.0.1 are the same host.
func TestDBResetProceedsWhenTheDatabaseURLNamesLitesDatabase(t *testing.T) {
	for _, url := range []string{
		"postgres://leoflow:leoflow@localhost:55439/leoflow_dev?sslmode=disable",
		"postgres://leoflow:leoflow@127.0.0.1:55439/leoflow_dev?sslmode=disable",
	} {
		t.Run(url, func(t *testing.T) {
			liteHomeOnPort(t, "55439")
			t.Setenv("LEOFLOW_DATABASE_URL", url)
			_, _, err := run(t, "db", "reset", "--yes")
			if err == nil {
				t.Fatal("expected the reset to fail to connect to the unused port")
			}
			if strings.Contains(err.Error(), "refusing") {
				t.Errorf("a variable naming Lite's own database was refused: %v", err)
			}
		})
	}
}

// The confirmation names the database the reset would drop, so an operator
// sees where it points before passing --yes.
func TestDBResetConfirmationNamesTheTarget(t *testing.T) {
	liteHomeOnPort(t, "55439")
	_, _, err := run(t, "db", "reset")
	if err == nil {
		t.Fatal("db reset ran without --yes")
	}
	for _, want := range []string{"--yes", "host localhost, port 55439, database leoflow_dev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the confirmation must contain %q, got: %v", want, err)
		}
	}
}

// Lite's server is handed Lite's own database URL. One exported in the shell
// must not reach it: the server prefers DEXAFLOW_* over LEOFLOW_*, so an
// inherited DEXAFLOW_DATABASE_URL would beat the LEOFLOW_DATABASE_URL Lite
// sets and the server would run on the operator's other database.
func TestLiteServerEnvironIgnoresAnExportedDatabaseURL(t *testing.T) {
	t.Parallel()
	base := []string{
		"PATH=/bin",
		"DEXAFLOW_DATABASE_URL=postgres://u:p@db.example.com:5432/warehouse",
		"LEOFLOW_DATABASE_URL=postgres://u:p@db.example.com:5432/warehouse",
	}
	env := liteServerEnviron(base, sharedServerEnv(liteEnvParams{secretKey: "k"}))
	var urls []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "DEXAFLOW_DATABASE_URL=") || strings.HasPrefix(kv, "LEOFLOW_DATABASE_URL=") {
			urls = append(urls, kv)
		}
	}
	if len(urls) != 1 || urls[0] != "LEOFLOW_DATABASE_URL="+devDSNs().database {
		t.Errorf("the server's database variables = %q, want only Lite's own %q", urls, devDSNs().database)
	}
}

// A backup dumps Lite's own database whatever DEXAFLOW_DATABASE_URL says, so
// one set to another database gets a note saying which database the archive
// holds, rather than an archive the operator believes is of theirs (#1185).
func TestLiteBackupNotesADatabaseURLItDoesNotUse(t *testing.T) {
	liteHomeOnPort(t, "55439")
	home := os.Getenv("HOME")
	if err := os.WriteFile(filepath.Join(home, ".dexaflow", "config.yaml"), []byte("workspace: "+t.TempDir()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEXAFLOW_DATABASE_URL", "postgres://admin:s3cret@db.example.com:6543/warehouse")
	out, _, err := run(t, "lite", "backup", "--output", filepath.Join(t.TempDir(), "b.tar.gz"))
	if err == nil {
		t.Fatal("expected the backup to fail without a managed pg_dump")
	}
	for _, want := range []string{"note: DEXAFLOW_DATABASE_URL", "host db.example.com, port 6543, database warehouse", "host localhost, port 55439, database leoflow_dev"} {
		if !strings.Contains(out, want) {
			t.Errorf("the backup output must contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s3cret") {
		t.Errorf("the note printed the password:\n%s", out)
	}
}

// No variable, or one naming Lite's own database, needs no note.
func TestNoteForeignDatabaseURLIsQuietWhenNothingDisagrees(t *testing.T) {
	liteHomeOnPort(t, "55439")
	var b strings.Builder
	noteForeignDatabaseURL(&b, "x")
	t.Setenv("LEOFLOW_DATABASE_URL", "postgres://leoflow:leoflow@127.0.0.1:55439/leoflow_dev?sslmode=disable")
	noteForeignDatabaseURL(&b, "x")
	if b.Len() != 0 {
		t.Errorf("unexpected note: %s", b.String())
	}
}
