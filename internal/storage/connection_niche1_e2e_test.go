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

// TestDbtCloudConnectionURIShapeIntegration pins the **token, no-host** shape for
// dbt Cloud. A dbt Cloud connection carries the account id as login and the API
// token as password — there is no host (DbtCloudHook targets the dbt Cloud API).
// Its conn_type `dbt_cloud` has an underscore, which is not a legal URI scheme, so
// the builder rewrites it to the hyphenated `dbt-cloud` (Airflow does the same and
// reverses it in from_uri). The contract: the API token (with reserved characters)
// round-trips end-to-end via AIRFLOW_CONN_<ID> under the normalized scheme.
func TestDbtCloudConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 109)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawToken = "dbt_tok/1+2" //nolint:gosec // fixture, not a real token
	connID := fmt.Sprintf("e2e_dbt_cloud_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "dbt_cloud",
		Login:    "12345",
		Password: rawToken,
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
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI not parseable (the underscore scheme must be normalized): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "dbt-cloud" {
		t.Errorf("scheme = %q, want dbt-cloud (underscore normalized)", parsed.Scheme)
	}
	gotToken, _ := parsed.User.Password()
	if gotToken != rawToken {
		t.Errorf("token round-trip failed: got %q, want %q", gotToken, rawToken)
	}
}

// TestHiveServer2ConnectionURIShapeIntegration pins the **host-bearing** shape for
// HiveServer2. A connection carries Host, Port (10000 by default), Login, Password,
// and Schema (the Hive database); HiveServer2Hook reads host:port + credentials via
// the Thrift/PyHive client. The edge case this guards is a password with
// URI-reserved characters round-tripping intact through the delivery hop.
func TestHiveServer2ConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 113)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // fixture, not a credential
	connID := fmt.Sprintf("e2e_hiveserver2_%d", time.Now().UnixNano())
	port := 10000
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "hiveserver2",
		Host:     "hive.example.com",
		Port:     &port,
		Login:    "etl_user",
		Password: rawPassword,
		Schema:   "default",
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
		t.Fatalf("URI not parseable (HiveServer2Hook would fail): %q err=%v", uris[connID], perr)
	}
	if parsed.Scheme != "hiveserver2" {
		t.Errorf("scheme = %q, want hiveserver2", parsed.Scheme)
	}
	if want := "hive.example.com:10000"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	if parsed.User.Username() != "etl_user" {
		t.Errorf("username = %q, want etl_user", parsed.User.Username())
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}

// TestHiveCliConnectionURIShapeIntegration pins the **host + Extra** shape for the
// Hive CLI. Like HiveServer2 it is host-bearing (Host, Port 10000, Login, Password),
// but HiveCliHook also reads beeline/kerberos tuning from Extra (e.g. use_beeline,
// principal). The contract: the password (reserved characters) round-trips and the
// Extra blob survives end-to-end under `__extra__`.
func TestHiveCliConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 127)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		rawPassword = "p@ss/w0rd:!#$"                                       //nolint:gosec // fixture, not a credential
		rawExtra    = `{"use_beeline":true,"principal":"hive/_HOST@REALM"}` //nolint:gosec // fixture
	)
	connID := fmt.Sprintf("e2e_hive_cli_%d", time.Now().UnixNano())
	port := 10000
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "hive_cli",
		Host:     "hive.example.com",
		Port:     &port,
		Login:    "etl_user",
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
	parsed, perr := url.Parse(uris[connID])
	if perr != nil {
		t.Fatalf("URI not parseable (the underscore scheme must be normalized): %q err=%v", uris[connID], perr)
	}
	if parsed.Scheme != "hive-cli" {
		t.Errorf("scheme = %q, want hive-cli (underscore normalized)", parsed.Scheme)
	}
	if want := "hive.example.com:10000"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
	if got := parsed.Query().Get("__extra__"); got != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", got, rawExtra)
	}
}

// TestPowerBIConnectionURIShapeIntegration pins the **Extra-bearing** shape for
// Microsoft Power BI. A connection carries the application (client) id as login,
// the client secret as password, and the tenant id in Extra; PowerBIHook reads them
// to obtain a token. The contract: the client secret (reserved characters)
// round-trips and the tenant id survives end-to-end under `__extra__`.
func TestPowerBIConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 131)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		rawSecret = "client_secret/1+2" //nolint:gosec // fixture, not a credential
		rawExtra  = `{"tenant_id":"00000000-1111-2222-3333-444444444444"}`
	)
	connID := fmt.Sprintf("e2e_powerbi_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "powerbi",
		Login:    "client-id",
		Password: rawSecret,
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
	parsed, perr := url.Parse(uris[connID])
	if perr != nil {
		t.Fatalf("URI not parseable (PowerBIHook would fail): %q err=%v", uris[connID], perr)
	}
	if parsed.Scheme != "powerbi" {
		t.Errorf("scheme = %q, want powerbi", parsed.Scheme)
	}
	gotSecret, _ := parsed.User.Password()
	if gotSecret != rawSecret {
		t.Errorf("client secret round-trip failed: got %q, want %q", gotSecret, rawSecret)
	}
	if got := parsed.Query().Get("__extra__"); got != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", got, rawExtra)
	}
}

// TestMSGraphConnectionURIShapeIntegration pins the **Extra-bearing** shape for
// Microsoft Graph. A connection carries the application (client) id as login, the
// client secret as password, and the tenant id / api_version in Extra;
// KiotaRequestAdapterHook reads them to drive the Graph SDK. The contract: the
// client secret (reserved characters) round-trips and the tenant/api_version Extra
// survives end-to-end under `__extra__`.
func TestMSGraphConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 137)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		rawSecret = "client_secret/1+2" //nolint:gosec // fixture, not a credential
		rawExtra  = `{"tenant_id":"00000000-1111-2222-3333-444444444444","api_version":"v1.0"}`
	)
	connID := fmt.Sprintf("e2e_msgraph_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "msgraph",
		Login:    "client-id",
		Password: rawSecret,
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
	parsed, perr := url.Parse(uris[connID])
	if perr != nil {
		t.Fatalf("URI not parseable (KiotaRequestAdapterHook would fail): %q err=%v", uris[connID], perr)
	}
	if parsed.Scheme != "msgraph" {
		t.Errorf("scheme = %q, want msgraph", parsed.Scheme)
	}
	gotSecret, _ := parsed.User.Password()
	if gotSecret != rawSecret {
		t.Errorf("client secret round-trip failed: got %q, want %q", gotSecret, rawSecret)
	}
	if got := parsed.Query().Get("__extra__"); got != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", got, rawExtra)
	}
}

// TestLivyConnectionURIShapeIntegration pins the **host-bearing** shape for Apache
// Livy. A connection carries Host, Port (8998 by default), Login, and Password;
// LivyHook reads host:port + credentials to submit Spark batches over the Livy REST
// API. The edge case this guards is a password with URI-reserved characters
// round-tripping intact through the delivery hop.
func TestLivyConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 139)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // fixture, not a credential
	connID := fmt.Sprintf("e2e_livy_%d", time.Now().UnixNano())
	port := 8998
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "livy",
		Host:     "livy.example.com",
		Port:     &port,
		Login:    "etl_user",
		Password: rawPassword,
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
		t.Fatalf("URI not parseable (LivyHook would fail): %q err=%v", uris[connID], perr)
	}
	if parsed.Scheme != "livy" {
		t.Errorf("scheme = %q, want livy", parsed.Scheme)
	}
	if want := "livy.example.com:8998"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	if parsed.User.Username() != "etl_user" {
		t.Errorf("username = %q, want etl_user", parsed.User.Username())
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}

// TestGCPLookerConnectionURIShapeIntegration pins the **Extra-only** shape for
// Google Looker. There is no password on the Connection — the API3 client_id and
// client_secret live in Extra, and LookerHook reads them via the Looker SDK. Its
// conn_type `gcp_looker` has an underscore, so the builder rewrites it to the
// hyphenated `gcp-looker` (Airflow does the same and reverses it in from_uri). The
// contract: the client_id/client_secret Extra round-trips end-to-end under
// `__extra__` with the normalized scheme.
func TestGCPLookerConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 149)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawExtra = `{"client_id":"x","client_secret":"y/1+2"}` //nolint:gosec // fixture, not a credential
	connID := fmt.Sprintf("e2e_gcp_looker_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "gcp_looker",
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
	parsed, perr := url.Parse(uris[connID])
	if perr != nil {
		t.Fatalf("URI not parseable (the underscore scheme must be normalized): %q err=%v", uris[connID], perr)
	}
	if parsed.Scheme != "gcp-looker" {
		t.Errorf("scheme = %q, want gcp-looker (underscore normalized)", parsed.Scheme)
	}
	if got := parsed.Query().Get("__extra__"); got != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", got, rawExtra)
	}
}
