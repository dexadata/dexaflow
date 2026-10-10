//go:build integration

package storage_test

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
)

// saasDeliver runs the shared chain-of-custody hop for a single Connection and
// returns the parsed delivery URI. It POSTs the Connection, resolves the
// tenant, fetches the SecretConnectionURIs map, looks up the entry, and parses
// it. Each connector test supplies its own cipher seed (so concurrent runs
// don't collide on the AES-GCM key) and asserts the connector-specific shape on
// the returned *url.URL.
func saasDeliver(t *testing.T, seed byte, conn domain.Connection) *url.URL {
	t.Helper()
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i) + seed
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	if cerr := repo.SetConnection(ctx, "default", conn); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", conn.ConnID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[conn.ConnID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v",
			conn.ConnID, mapKeys(uris))
	}
	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != conn.ConnType {
		t.Errorf("scheme = %q, want %q", parsed.Scheme, conn.ConnType)
	}
	return parsed
}

// assertHostPort asserts the parsed URI's host (and optional port) matches the
// Connection the operator posted. Connectors with no host (token-only or
// extra-only) skip this helper.
func assertHostPort(t *testing.T, parsed *url.URL, host string, port *int) {
	t.Helper()
	want := host
	if port != nil {
		want = fmt.Sprintf("%s:%d", host, *port)
	}
	if parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
}

// assertPassword asserts the percent-escaped password round-trips through the
// URI builder and net/url's parser back to the raw value the operator typed.
func assertPassword(t *testing.T, parsed *url.URL, want string) {
	t.Helper()
	got, _ := parsed.User.Password()
	if got != want {
		t.Errorf("password round-trip failed: got %q, want %q (the URI builder must percent-escape; net/url must un-escape on parse)",
			got, want)
	}
}

// assertExtra asserts the Extra JSON blob round-trips through the `__extra__`
// query parameter unchanged.
func assertExtra(t *testing.T, parsed *url.URL, want string) {
	t.Helper()
	if got := parsed.Query().Get("__extra__"); got != want {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", got, want)
	}
}

// TestTrinoConnectionURIShapeIntegration pins the delivery chain for the trino
// query-engine connector: a host:port coordinator, a login/password pair with
// URI-reserved characters in the password, and a catalog carried in Schema.
// TrinoHook reads `AIRFLOW_CONN_<ID>` and connects to the coordinator.
func TestTrinoConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
	port := 8080
	parsed := saasDeliver(t, 61, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_trino_%d", time.Now().UnixNano()),
		ConnType: "trino",
		Host:     "trino.example.com",
		Port:     &port,
		Login:    "etl_user",
		Password: rawPassword,
		Schema:   "hive",
	})
	assertHostPort(t, parsed, "trino.example.com", &port)
	if parsed.User.Username() != "etl_user" {
		t.Errorf("username = %q, want etl_user", parsed.User.Username())
	}
	assertPassword(t, parsed, rawPassword)
}

// TestPrestoConnectionURIShapeIntegration pins the delivery chain for the presto
// connector. Identical in shape to trino (host:port + login/password + catalog
// in Schema), only the scheme differs. PrestoHook consumes the same URI form.
func TestPrestoConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
	port := 8080
	parsed := saasDeliver(t, 67, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_presto_%d", time.Now().UnixNano()),
		ConnType: "presto",
		Host:     "trino.example.com",
		Port:     &port,
		Login:    "etl_user",
		Password: rawPassword,
		Schema:   "hive",
	})
	assertHostPort(t, parsed, "trino.example.com", &port)
	if parsed.User.Username() != "etl_user" {
		t.Errorf("username = %q, want etl_user", parsed.User.Username())
	}
	assertPassword(t, parsed, rawPassword)
}

// TestJdbcConnectionURIShapeIntegration pins the delivery chain for the jdbc
// connector. Only the host:port + login/password delivery is contract-pinned
// here; JdbcHook additionally needs driver_path / driver_class in Extra and a
// JDBC URL, documented in the cookbook page but out of scope for this test.
func TestJdbcConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
	port := 5432
	parsed := saasDeliver(t, 71, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_jdbc_%d", time.Now().UnixNano()),
		ConnType: "jdbc",
		Host:     "db.example.com",
		Port:     &port,
		Login:    "etl_user",
		Password: rawPassword,
	})
	assertHostPort(t, parsed, "db.example.com", &port)
	if parsed.User.Username() != "etl_user" {
		t.Errorf("username = %q, want etl_user", parsed.User.Username())
	}
	assertPassword(t, parsed, rawPassword)
}

// TestDockerConnectionURIShapeIntegration pins the delivery chain for the docker
// registry connector: a host:port registry, registry credentials in
// login/password, and registry options (email, reauth) in Extra. DockerHook
// uses these to authenticate against a private registry.
func TestDockerConnectionURIShapeIntegration(t *testing.T) {
	const (
		rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
		rawExtra    = `{"email":"a@b.com","reauth":false}`
	)
	port := 5000
	parsed := saasDeliver(t, 73, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_docker_%d", time.Now().UnixNano()),
		ConnType: "docker",
		Host:     "registry.example.com",
		Port:     &port,
		Login:    "etl_user",
		Password: rawPassword,
		Extra:    rawExtra,
	})
	assertHostPort(t, parsed, "registry.example.com", &port)
	assertPassword(t, parsed, rawPassword)
	assertExtra(t, parsed, rawExtra)
}

// TestSalesforceConnectionURIShapeIntegration pins the delivery chain for the
// salesforce connector: there is no host — the instance URL, security token,
// and API version live in Extra, with the username in Login and password in
// Password. SalesforceHook reconstructs the session from these.
func TestSalesforceConnectionURIShapeIntegration(t *testing.T) {
	const (
		rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
		rawExtra    = `{"instance_url":"https://x.my.salesforce.com","security_token":"tok+en/1","version":"59.0"}`
	)
	parsed := saasDeliver(t, 79, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_salesforce_%d", time.Now().UnixNano()),
		ConnType: "salesforce",
		Login:    "user@example.com",
		Password: rawPassword,
		Extra:    rawExtra,
	})
	assertPassword(t, parsed, rawPassword)
	assertExtra(t, parsed, rawExtra)
}

// TestTelegramConnectionURIShapeIntegration pins the delivery chain for the
// telegram connector: the bot token lives in Password and there is no host
// (token-only shape, like slack). TelegramHook reads the token and posts to
// the Bot API.
func TestTelegramConnectionURIShapeIntegration(t *testing.T) {
	const rawToken = "1234567:AAH/token+abc" //nolint:gosec // hardcoded test fixture, not a credential
	parsed := saasDeliver(t, 83, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_telegram_%d", time.Now().UnixNano()),
		ConnType: "telegram",
		Password: rawToken,
	})
	assertPassword(t, parsed, rawToken)
}

// TestDiscordConnectionURIShapeIntegration pins the delivery chain for the
// discord connector: the webhook endpoint path lives in Extra and the webhook
// token in Password. DiscordWebhookHook joins the host's webhook base with the
// endpoint to post a message.
func TestDiscordConnectionURIShapeIntegration(t *testing.T) {
	const (
		rawToken = "webhook-token" //nolint:gosec // hardcoded test fixture, not a credential
		rawExtra = `{"webhook_endpoint":"webhooks/123/abc"}`
	)
	parsed := saasDeliver(t, 89, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_discord_%d", time.Now().UnixNano()),
		ConnType: "discord",
		Password: rawToken,
		Extra:    rawExtra,
	})
	assertPassword(t, parsed, rawToken)
	assertExtra(t, parsed, rawExtra)
}

// TestPagerdutyConnectionURIShapeIntegration pins the delivery chain for the
// pagerduty connector: the REST API token lives in Password and the
// Events-API routing key in Extra. PagerdutyHook uses the token for the REST
// API and the routing key for Events v2 alerts.
func TestPagerdutyConnectionURIShapeIntegration(t *testing.T) {
	const (
		rawToken = "api-token/1+2"                   //nolint:gosec // hardcoded test fixture, not a credential
		rawExtra = `{"routing_key":"R0UTING+KEY/1"}` // gitleaks:allow test fixture, not a real routing key
	)
	parsed := saasDeliver(t, 97, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_pagerduty_%d", time.Now().UnixNano()),
		ConnType: "pagerduty",
		Password: rawToken,
		Extra:    rawExtra,
	})
	assertPassword(t, parsed, rawToken)
	assertExtra(t, parsed, rawExtra)
}

// TestDatadogConnectionURIShapeIntegration pins the delivery chain for the
// datadog connector: there is no password and no host — the API host, API key,
// app key, and source type all live in Extra. DatadogHook reads them to submit
// metrics and events.
func TestDatadogConnectionURIShapeIntegration(t *testing.T) {
	const rawExtra = `{"api_host":"https://api.datadoghq.eu","api_key":"k/1+2","app_key":"a/3+4","source_type_name":"airflow"}`
	parsed := saasDeliver(t, 101, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_datadog_%d", time.Now().UnixNano()),
		ConnType: "datadog",
		Extra:    rawExtra,
	})
	assertExtra(t, parsed, rawExtra)
}

// TestTableauConnectionURIShapeIntegration pins the delivery chain for the
// tableau connector: a host (Tableau Server), login/password credentials, and
// a site carried in Schema. TableauHook signs in to the REST API with these.
func TestTableauConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
	parsed := saasDeliver(t, 103, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_tableau_%d", time.Now().UnixNano()),
		ConnType: "tableau",
		Host:     "tableau.example.com",
		Login:    "etl_user",
		Password: rawPassword,
		Schema:   "default",
	})
	assertHostPort(t, parsed, "tableau.example.com", nil)
	if parsed.User.Username() != "etl_user" {
		t.Errorf("username = %q, want etl_user", parsed.User.Username())
	}
	assertPassword(t, parsed, rawPassword)
}

// TestGithubConnectionURIShapeIntegration pins the delivery chain for the
// github connector: the personal access token lives in Password and there is no
// host (token-only shape, like slack). GithubHook authenticates to the GitHub
// API with the PAT.
func TestGithubConnectionURIShapeIntegration(t *testing.T) {
	const rawToken = "ghp_token/1+2" //nolint:gosec // hardcoded test fixture, not a credential
	parsed := saasDeliver(t, 107, domain.Connection{
		ConnID:   fmt.Sprintf("e2e_github_%d", time.Now().UnixNano()),
		ConnType: "github",
		Password: rawToken,
	})
	assertPassword(t, parsed, rawToken)
}
