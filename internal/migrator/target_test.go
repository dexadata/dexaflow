package migrator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		dsn  string
		want Target
	}{
		{
			dsn:  "postgres://u:p@db.example:6543/app?sslmode=verify-full&sslrootcert=/etc/leoflow/db-ca/ca.crt",
			want: Target{Host: "db.example", Port: "6543", Database: "app", User: "u", SSLRootCert: "/etc/leoflow/db-ca/ca.crt"},
		},
		{dsn: "postgres://u@db/app", want: Target{Host: "db", Port: "5432", Database: "app", User: "u"}},
		{dsn: "postgresql://u@db/app", want: Target{Host: "db", Port: "5432", Database: "app", User: "u"}},
		{dsn: "pgx5://u@db:5433/app", want: Target{Host: "db", Port: "5433", Database: "app", User: "u"}},
		{dsn: "postgres://u@[::1]:5432/app", want: Target{Host: "::1", Port: "5432", Database: "app", User: "u"}},
	}
	for _, tc := range cases {
		got, err := ParseTarget(tc.dsn)
		if err != nil {
			t.Errorf("ParseTarget(%q): %v", tc.dsn, err)
			continue
		}
		got.password = ""
		if got != tc.want {
			t.Errorf("ParseTarget(%q) = %+v, want %+v", tc.dsn, got, tc.want)
		}
	}
}

func TestParseTargetRejects(t *testing.T) {
	cases := map[string]string{
		"":                               "empty",
		"host=db dbname=app":             "postgres://",
		"mysql://u:p@db/app":             `scheme "mysql"`,
		"postgres://u:p@db:5432/":        "no database name",
		"postgres://u:s3cret@db:bad/app": "postgres://",
	}
	for dsn, want := range cases {
		_, err := ParseTarget(dsn)
		if err == nil {
			t.Errorf("ParseTarget(%q) accepted it", dsn)
			continue
		}
		var f *Failure
		if !errors.As(err, &f) || f.Kind != KindBadDSN || f.Code != ExitBadDSN {
			t.Errorf("ParseTarget(%q) = %v, want a KindBadDSN failure", dsn, err)
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ParseTarget(%q) message lacks %q: %v", dsn, want, err)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("ParseTarget(%q) leaks the password: %v", dsn, err)
		}
	}
}

func TestDriverURL(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@db/app?sslmode=disable":   "pgx5://u:p@db/app?sslmode=disable",
		"postgresql://u:p@db/app?sslmode=disable": "pgx5://u:p@db/app?sslmode=disable",
		"pgx5://u:p@db/app":                       "pgx5://u:p@db/app",
	}
	for in, want := range cases {
		if got := DriverURL(in); got != want {
			t.Errorf("DriverURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPreflightCA(t *testing.T) {
	present := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "system", present} {
		if err := PreflightCA(Target{SSLRootCert: path}); err != nil {
			t.Errorf("PreflightCA(%q) = %v, want nil", path, err)
		}
	}

	missing := "/etc/leoflow/db-ca/ca.crt-not-here"
	err := PreflightCA(Target{SSLRootCert: missing})
	var f *Failure
	if !errors.As(err, &f) || f.Kind != KindMissingCA || f.Code != ExitMissingCA {
		t.Fatalf("PreflightCA(missing) = %v, want a KindMissingCA failure", err)
	}
	for _, s := range []string{missing, "database.caConfigMap", "ca.crt", "Verified TLS to managed Postgres"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("message lacks %q:\n%s", s, err)
		}
	}
}

// The cause line of a Failure quotes the driver error, which pgx builds from
// the DSN; a password must never reach the Job log through it.
func TestRedactRemovesThePassword(t *testing.T) {
	tg, err := ParseTarget("postgres://u:s3cret@db/app")
	if err != nil {
		t.Fatal(err)
	}
	got := tg.redact("cannot parse `postgres://u:s3cret@db/app` (password=s3cret)")
	if strings.Contains(got, "s3cret") {
		t.Fatalf("redact kept the password: %s", got)
	}

	// A password equal to the user name is the dev default; the user and the
	// database name in pgx's "failed to connect to `user=leoflow
	// database=leoflow_nope`" must stay readable.
	same, err := ParseTarget("postgres://leoflow:leoflow@db/leoflow_nope")
	if err != nil {
		t.Fatal(err)
	}
	line := "failed to connect to `user=leoflow database=leoflow_nope`"
	if got := same.redact(line); got != line {
		t.Fatalf("redact changed a line with no password in it: %s", got)
	}
}
