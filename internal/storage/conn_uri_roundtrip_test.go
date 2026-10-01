package storage

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func intp(n int) *int { return &n }

// TestConnURIRoundTrip is the guard that makes seeding a connection from the
// environment safe (#1103). Lite reads AIRFLOW_CONN_<ID> and stores it as a
// Connection; the control plane then delivers that Connection back to tasks as a
// URI. If the parser is not the emitter's inverse, a connection is silently
// mangled between the value the developer exported and the value their task
// receives — and the failure surfaces inside the task, far from the cause.
//
// The property asserted is the one that matters: what a task RECEIVES is
// unchanged. Comparing parsed structs instead would fail on representations
// that are equivalent in the URI (Airflow's from_uri is lossy for an absolute
// path, deliberately), so the round trip is anchored on the delivered form.
func TestConnURIRoundTrip(t *testing.T) {
	for _, c := range []domain.Connection{
		{ConnID: "pg", ConnType: "postgres", Host: "db.internal", Login: "app", Password: "s3cr3t", Port: intp(5432), Schema: "leoflow"},
		{ConnID: "nopass", ConnType: "postgres", Host: "db", Login: "readonly", Port: intp(5432)},
		{ConnID: "noport", ConnType: "mysql", Host: "mysql.local", Login: "u", Password: "p", Schema: "app"},
		{ConnID: "hostonly", ConnType: "http", Host: "api.example.com"},
		// conn_type with underscores: not a legal URI scheme, rewritten to `-`
		// by the emitter and back by the parser. Getting this wrong turns
		// google_cloud_platform into google-cloud-platform in the vault.
		{ConnID: "gcp", ConnType: "google_cloud_platform", Host: "unused"},
		{ConnID: "spark", ConnType: "spark_sql", Host: "spark", Port: intp(10000)},
		// extra travels as __extra__; losing it drops sslmode and friends.
		{ConnID: "withextra", ConnType: "postgres", Host: "db", Port: intp(5432), Schema: "app", Extra: `{"sslmode":"require"}`},
		// Credentials needing percent-encoding: a naive parser splits on the
		// wrong ':' or '@' and silently truncates the password.
		{ConnID: "weird", ConnType: "postgres", Host: "db", Login: "user@corp", Password: "p@ss:w/rd", Port: intp(5432)},
		{ConnID: "sqlite", ConnType: "sqlite", Schema: "/tmp/leoflow.db"},
		{ConnID: "relsqlite", ConnType: "sqlite", Schema: "local.db"},
		// The degenerate form: a type and nothing else. The emitter produces
		// `scheme:` with no `//`, so the parser's "missing //" refusal must not
		// reject what we ourselves deliver.
		{ConnID: "typeonly", ConnType: "fs"},
		{ConnID: "extraonly", ConnType: "fs", Extra: `{"path":"/data"}`},
	} {
		t.Run(c.ConnID, func(t *testing.T) {
			delivered := airflowConnURI(c)
			parsed, err := domain.ParseConnectionURI(c.ConnID, delivered)
			if err != nil {
				t.Fatalf("parsing %q: %v", delivered, err)
			}
			if again := airflowConnURI(parsed); again != delivered {
				t.Errorf("a task would receive a different connection after a round trip\n  emitted: %s\n  after:   %s", delivered, again)
			}
		})
	}
}

// TestParseConnectionURIRefusesUnusable pins the inputs that must NOT become a
// stored connection. Storing a typed-less or empty connection would replace
// "the connection is missing" — which registration reports clearly — with a
// connection that exists and fails inside the task.
func TestParseConnectionURIRefusesUnusable(t *testing.T) {
	for _, tc := range []struct{ name, uri string }{
		{"empty", ""},
		{"blank", "   "},
		{"no scheme", "db.internal:5432/app"},
		{"non-numeric port", "postgres://db:not-a-port/app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := domain.ParseConnectionURI("x", tc.uri); err == nil {
				t.Errorf("ParseConnectionURI(%q) = nil error, want a refusal", tc.uri)
			}
		})
	}
}

// TestParseConnectionURIErrorsNeverEchoTheValue is a security property, not a
// style preference: the input is a credential, and an error is the single most
// likely thing to be logged, wrapped into another error, or printed to a
// terminal someone screenshots.
func TestParseConnectionURIErrorsNeverEchoTheValue(t *testing.T) {
	const secret = "hunter2"
	for _, uri := range []string{
		"postgres://user:" + secret + "@h:5432/db\x7f",
		"h:5432/db?p=" + secret,
		"postgres://db:" + secret + "/app",
	} {
		_, err := domain.ParseConnectionURI("c", uri)
		if err == nil {
			continue // parsed fine; nothing to leak
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks the connection value: %v", err)
		}
	}
}
