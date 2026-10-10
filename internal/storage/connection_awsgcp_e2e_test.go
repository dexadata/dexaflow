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

// TestRedshiftConnectionURIShapeIntegration pins the **host-bearing** shape for
// Amazon Redshift. Redshift is wire-compatible with Postgres, so the
// Connection carries Host, Port (5439 by default), Login, Password, and
// Schema, and the delivered URI is `redshift://<login>:<password>@<host>:<port>/<schema>`.
// The edge case this guards is a password with URI-reserved characters: the
// URI builder must percent-escape it so the final URI is parseable, and the
// user-side RedshiftSQLHook recovers the original password via url.Parse.
func TestRedshiftConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 61)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
	connID := fmt.Sprintf("e2e_redshift_%d", time.Now().UnixNano())
	port := 5439
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "redshift",
		Host:     "warehouse.example.com",
		Login:    "etl_user",
		Password: rawPassword,
		Port:     &port,
		Schema:   "analytics",
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v",
			connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "redshift" {
		t.Errorf("scheme = %q, want redshift", parsed.Scheme)
	}
	if want := "warehouse.example.com:5439"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	if parsed.User.Username() != "etl_user" {
		t.Errorf("username = %q, want etl_user", parsed.User.Username())
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q (the URI builder must percent-escape; net/url must un-escape on parse)",
			gotPassword, rawPassword)
	}
}

// TestAthenaConnectionURIShapeIntegration pins the **Extra-bearing** shape for
// Amazon Athena. Athena has no host/port — the region, schema, and work group
// live in Extra, and the optional Password carries an AWS secret access key
// (for the explicit-credential path; keyless IAM is preferred per ADR 0035).
// The contract: the AWS secret round-trips via the URI's password, and the
// Extra JSON survives end-to-end under `__extra__` so the user-side
// AthenaSQLHook recovers region/work-group at runtime.
func TestAthenaConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 67)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		rawPassword = "wJalrXUtnFEMI/K7+EXAMPLEKEY" //nolint:gosec // hardcoded test fixture, not a credential
		rawExtra    = `{"region_name":"eu-central-1","schema_name":"default","work_group":"primary"}`
	)
	connID := fmt.Sprintf("e2e_athena_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "athena",
		Password: rawPassword,
		Extra:    rawExtra,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v",
			connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "athena" {
		t.Errorf("scheme = %q, want athena", parsed.Scheme)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q (the URI builder must percent-escape; net/url must un-escape on parse)",
			gotPassword, rawPassword)
	}
	gotExtra := parsed.Query().Get("__extra__")
	if gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestEmrConnectionURIShapeIntegration pins the **Extra-only** shape for
// Amazon EMR. EMR carries no host, no password (keyless IAM is the only
// supported posture per ADR 0035) — only the region lives in Extra. The
// contract: the region JSON survives the Repository -> SecretConnectionURIs
// -> agent hop and is recoverable from `__extra__` so the user-side EmrHook
// targets the right region.
func TestEmrConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 71)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawExtra = `{"region_name":"eu-central-1"}`
	connID := fmt.Sprintf("e2e_emr_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "emr",
		Extra: rawExtra,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v",
			connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "emr" {
		t.Errorf("scheme = %q, want emr", parsed.Scheme)
	}
	gotExtra := parsed.Query().Get("__extra__")
	if gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestGcpbigqueryConnectionURIShapeIntegration pins the **Extra-only,
// keyless** shape for Google BigQuery. No host, no password — credentials
// come from the runtime identity (Workload Identity / ADC); only the project
// and location live in Extra. The contract: the project/location JSON
// round-trips under `__extra__` so the user-side BigQueryHook resolves the
// dataset location without a service-account key.
func TestGcpbigqueryConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 73)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawExtra = `{"project":"my-proj","location":"EU"}`
	connID := fmt.Sprintf("e2e_gcpbigquery_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "gcpbigquery",
		Extra: rawExtra,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v",
			connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "gcpbigquery" {
		t.Errorf("scheme = %q, want gcpbigquery", parsed.Scheme)
	}
	gotExtra := parsed.Query().Get("__extra__")
	if gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestGcpcloudsqlConnectionURIShapeIntegration pins the **Extra-only,
// keyless** shape for Google Cloud SQL. The instance coordinates
// (database_type, project_id, location, instance) live in Extra; there is no
// password on the Connection — auth flows through the Cloud SQL connector via
// the runtime identity (ADC). The contract: the instance JSON round-trips
// under `__extra__` so the user-side CloudSQLHook can open the proxy to the
// right instance.
func TestGcpcloudsqlConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 79)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawExtra = `{"database_type":"postgres","project_id":"my-proj","location":"europe-west1","instance":"my-inst"}`
	connID := fmt.Sprintf("e2e_gcpcloudsql_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "gcpcloudsql",
		Extra: rawExtra,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v",
			connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "gcpcloudsql" {
		t.Errorf("scheme = %q, want gcpcloudsql", parsed.Scheme)
	}
	gotExtra := parsed.Query().Get("__extra__")
	if gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestGoogleAdsConnectionURIShapeIntegration pins the google_ads delivery. Its
// conn_type has an underscore, which is not a legal URI scheme — the builder now
// rewrites it to the hyphenated `google-ads` (Airflow does the same and reverses
// it in from_uri). Everything (developer token, OAuth client) rides in Extra.
// This is the test the parallel agent could not ship until the conn_uri.go
// scheme-normalization fix landed.
func TestGoogleAdsConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 91)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawExtra = `{"developer_token":"abc123","client_id":"x","client_secret":"y/1+2","refresh_token":"z"}`
	connID := fmt.Sprintf("e2e_google_ads_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "google_ads", Extra: rawExtra,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	parsed, perr := url.Parse(uris[connID])
	if perr != nil {
		t.Fatalf("URI not parseable (the underscore scheme must be normalized): %q err=%v", uris[connID], perr)
	}
	if parsed.Scheme != "google-ads" {
		t.Errorf("scheme = %q, want google-ads (underscore normalized)", parsed.Scheme)
	}
	if got := parsed.Query().Get("__extra__"); got != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", got, rawExtra)
	}
}
