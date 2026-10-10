package storage

import (
	"net/url"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func TestAirflowConnURI(t *testing.T) {
	port := 5432
	got := airflowConnURI(domain.Connection{
		ConnID: "pg", ConnType: "postgres", Host: "db.internal",
		Login: "user", Password: "p@ss word", Port: &port, Schema: "analytics",
	})
	want := "postgres://user:p%40ss%20word@db.internal:5432/analytics"
	if got != want {
		t.Errorf("uri = %q, want %q", got, want)
	}
	// No port / no schema / no creds.
	if bare := airflowConnURI(domain.Connection{ConnID: "h", ConnType: "http", Host: "api.example.com"}); bare != "http://api.example.com" {
		t.Errorf("minimal uri = %q, want http://api.example.com", bare)
	}
	// Extra is carried under __extra__ so Airflow recovers it.
	got = airflowConnURI(domain.Connection{ConnID: "p", ConnType: "postgres", Host: "h", Extra: `{"sslmode":"require"}`})
	if !strings.Contains(got, "__extra__=") || !strings.Contains(got, "sslmode") {
		t.Errorf("uri = %q, want extra carried under __extra__", got)
	}
}

// TestAirflowConnURIGCP pins the google_cloud_platform contract: GCP carries no
// host/login/password — everything (incl. an inline service-account key) rides
// in Extra under __extra__. The service-account private_key contains embedded
// newlines (PEM), a real round-trip edge case; this asserts they survive intact.
func TestAirflowConnURIGCP(t *testing.T) {
	extra := `{"keyfile_dict":{"type":"service_account","client_email":"x@p.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nABC\nDEF\n-----END PRIVATE KEY-----\n","project_id":"p"},"project":"p"}`
	got := airflowConnURI(domain.Connection{ConnID: "google_cloud_default", ConnType: "google_cloud_platform", Extra: extra})

	// No host/login/password → the conn_type is the scheme and there is no
	// authority component (no "//", no "@" userinfo). The client_email's "@" is
	// percent-encoded inside __extra__, so a literal "@" must not appear.
	// The scheme is hyphenated (RFC 3986 — `_` is illegal in a scheme); Airflow
	// reverses `-`→`_` in from_uri, so google-cloud-platform → google_cloud_platform.
	const prefix = "google-cloud-platform:?"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("uri = %q, want %q prefix", got, prefix)
	}
	if strings.Contains(got, "@") {
		t.Errorf("uri = %q, should carry no userinfo (@) for GCP", got)
	}
	// The Extra (with the newline-bearing PEM private_key) round-trips exactly.
	q, err := url.ParseQuery(strings.TrimPrefix(got, prefix))
	if err != nil {
		t.Fatalf("parsing query: %v", err)
	}
	if q.Get("__extra__") != extra {
		t.Errorf("__extra__ = %q, want exact round-trip %q", q.Get("__extra__"), extra)
	}
}

// TestAirflowConnURIPasswordHardening pins the nastiest passwords through the
// real builder: a double-slash (`//`, the classic that breaks naive URI string
// concatenation), plus every URI-reserved char (@ : / ? # % + & = and a space).
// The contract: airflowConnURI percent-encodes them so the result is parseable
// and url.Parse (what the user-side Python connector mirrors) recovers the
// password byte-for-byte. A regression that hand-rolled the URI instead of using
// net/url would surface here.
func TestAirflowConnURIPasswordHardening(t *testing.T) {
	port := 5432
	for _, pw := range []string{
		"pa//ss",                   // double slash
		"a//b:c@d//e",              // double slash mixed with @ and :
		"//leading",                // leading double slash
		"trailing//",               // trailing double slash
		"p@ss/w0rd:!#$%+&=? ",      // the full reserved set incl. ? and a space
		"a%23b?c//d",               // a literal "%23" must NOT decode to '#'; plus ? and //
		"%2F%2F already-encoded//", // a literal %2F must NOT be double-decoded
	} {
		got := airflowConnURI(domain.Connection{
			ConnID: "c", ConnType: "postgres",
			Host: "h", Port: &port, Login: "u", Password: pw, Schema: "db",
		})
		parsed, err := url.Parse(got)
		if err != nil {
			t.Errorf("password %q → unparseable URI %q: %v", pw, got, err)
			continue
		}
		recovered, _ := parsed.User.Password()
		if recovered != pw {
			t.Errorf("password round-trip failed: built %q, recovered %q, want %q", got, recovered, pw)
		}
	}
}

// TestAirflowConnURIExtraWithURL pins that a full URL living in Extra (the common
// case: webhook endpoints, instance_url, api_host) survives the __extra__
// round-trip byte-for-byte — including the `//` after the scheme, `//` in the
// path, query params, and a fragment. A regression that re-encoded or split the
// Extra would corrupt the URL and the user-side hook would call the wrong
// endpoint. This complements the per-connector coverage (slack/salesforce/datadog
// all carry https:// URLs in Extra).
func TestAirflowConnURIExtraWithURL(t *testing.T) {
	extras := []string{
		`{"webhook":"https://hooks.example.com//team//channel/path?token=ab/cd+ef&x=1#frag"}`,
		`{"instance_url":"https://my.salesforce.com/","api_host":"http://10.0.0.1:8080//v2/"}`,
		`{"endpoint":"https://例え.example.com/パス//x"}`, // non-ASCII host + path
	}
	for _, extra := range extras {
		got := airflowConnURI(domain.Connection{ConnID: "c", ConnType: "http", Extra: extra})
		parsed, err := url.Parse(got)
		if err != nil {
			t.Errorf("extra %q → unparseable URI %q: %v", extra, got, err)
			continue
		}
		if recovered := parsed.Query().Get("__extra__"); recovered != extra {
			t.Errorf("Extra URL round-trip failed:\n built     %q\n recovered %q\n want      %q", got, recovered, extra)
		}
	}
}

// TestAirflowConnURISchemeUnderscoreNormalized pins the RFC 3986 fix: a conn_type
// with an underscore (google_ads, azure_data_lake, spark_sql, …) is NOT a legal
// URI scheme. Airflow rewrites `_`→`-` for the scheme (and reverses it in
// from_uri), so we must too — otherwise the built URI fails url.Parse with a host
// ("first path segment cannot contain colon") and Python's urllib reads an empty
// scheme. This asserts the scheme is hyphenated AND the result is parseable.
func TestAirflowConnURISchemeUnderscoreNormalized(t *testing.T) {
	for _, connType := range []string{"google_ads", "azure_data_lake", "spark_sql"} {
		port := 443
		got := airflowConnURI(domain.Connection{
			ConnID: "c", ConnType: connType,
			Host: "h.example.com", Port: &port, Login: "u", Password: "p",
		})
		wantScheme := strings.ReplaceAll(connType, "_", "-")
		parsed, err := url.Parse(got)
		if err != nil {
			t.Errorf("%s: url.Parse(%q) failed: %v", connType, got, err)
			continue
		}
		if parsed.Scheme != wantScheme {
			t.Errorf("%s: scheme = %q, want %q", connType, parsed.Scheme, wantScheme)
		}
	}
}

// TestAirflowConnURISQLitePath pins the sqlite contract: the Schema field
// carries the database file path, and the builder must render the canonical
// 3-slash form `sqlite:///<absolute path>` whether the operator typed the path
// with or without a leading slash. A double-prepend bug here produces 4
// slashes and breaks SQLAlchemy / `urlparse(...).path` parsing in user DAGs.
func TestAirflowConnURISQLitePath(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		want   string
	}{
		{
			name:   "absolute path with leading slash",
			schema: "/var/lib/leoflow/warehouse.db",
			want:   "sqlite:///var/lib/leoflow/warehouse.db",
		},
		{
			name:   "relative path without leading slash",
			schema: "var/lib/leoflow/warehouse.db",
			want:   "sqlite:///var/lib/leoflow/warehouse.db",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := airflowConnURI(domain.Connection{
				ConnID: "sqlite_target", ConnType: "sqlite", Schema: tc.schema,
			})
			if got != tc.want {
				t.Errorf("uri = %q, want %q", got, tc.want)
			}
		})
	}
}
