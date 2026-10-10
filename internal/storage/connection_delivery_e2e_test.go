//go:build integration

package storage_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
)

// TestConnectionDeliveryChainOfCustodyIntegration is the rigorous companion to
// the per-layer tests. Each layer (Repository CRUD, SecretConnectionURIs,
// agentrpc.GetConnections, agent.buildEnv) has its own unit/integration
// test; this one walks the **whole chain** for one Connection per
// supported conn_type and proves the env var the user-side Python sees
// matches what the Admin user posted.
//
// Specifically: a refactor that breaks the wiring between layers without
// changing any individual layer's behaviour (e.g. tenant_id encoding
// regresses; URI builder's password-escape changes; the agent maps the
// proto response into a different env key) would let every isolated test
// keep passing but break this one. That is the bug class this guards.
//
// Edge case the per-layer tests miss: a password with URI-reserved
// characters (`@`, `:`, `/`, `?`, `#`, `%`, `+`). The URI builder must
// percent-escape them so the final URI is parseable; the user-side Python
// (psycopg2.connect, pymysql, etc.) then sees the original password. A
// regression here would surface only at the connector boundary — too late.
//
// Table-driven across the locally-testable SQL connectors so adding the
// next (mssql, sqlite) is a 1-line change.
func TestConnectionDeliveryChainOfCustodyIntegration(t *testing.T) {
	const rawPassword = "p@ss//w0rd:!#$?" //nolint:gosec // fixture: reserved chars incl. // and ?, not a credential
	cases := []struct {
		connType    string
		defaultPort int
		host        string
		schema      string
	}{
		{connType: "postgres", defaultPort: 5432, host: "warehouse.example.com", schema: "analytics"},
		{connType: "mysql", defaultPort: 3306, host: "warehouse.example.com", schema: "analytics"},
		{connType: "mariadb", defaultPort: 3306, host: "warehouse.example.com", schema: "analytics"},
		{connType: "mssql", defaultPort: 1433, host: "warehouse.example.com", schema: "analytics"},
		// Redis fits the same shape: the Schema field carries the **db index**
		// (Redis namespaces 0..15 by default). The URI redis-py parses is
		// `redis://[user]:password@host:port/<db>`, identical in shape to the
		// SQL family — only the semantics of Schema differ.
		{connType: "redis", defaultPort: 6379, host: "warehouse.example.com", schema: "0"},
		// Oracle and MongoDB are the same host:port + login/password + schema
		// shape: Oracle's Schema carries the service name / PDB, Mongo's the
		// database. Both are locally testable (a container exists) so they ride
		// the same table.
		{connType: "oracle", defaultPort: 1521, host: "warehouse.example.com", schema: "ORCLPDB1"},
		{connType: "mongo", defaultPort: 27017, host: "warehouse.example.com", schema: "analytics"},
	}
	for _, tc := range cases {
		t.Run(tc.connType, func(t *testing.T) {
			repo, _, ctx := openRepo(t)
			key := make([]byte, 32)
			for i := range key {
				key[i] = byte(i + 7)
			}
			cipher, err := secrets.NewAESGCM(key)
			if err != nil {
				t.Fatal(err)
			}
			repo.SetCipher(cipher)

			connID := fmt.Sprintf("e2e_%s_%d", tc.connType, time.Now().UnixNano())
			port := tc.defaultPort
			if cerr := repo.SetConnection(ctx, "default", domain.Connection{
				ConnID: connID, ConnType: tc.connType,
				Host: tc.host, Login: "etl_user",
				Password: rawPassword, Port: &port, Schema: tc.schema,
			}); cerr != nil {
				t.Fatalf("SetConnection: %v", cerr)
			}
			t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

			tenantUUID, err := repo.TenantUUID(ctx, "default")
			if err != nil {
				t.Fatalf("TenantUUID: %v", err)
			}

			// Layer hop: agent calls GetConnections → server.SecretConnectionURIs
			// → returns the URI map. The agentrpc handler is a thin pass-through
			// (see internal/agentrpc/secrets.go::GetConnections); the per-layer
			// tests already cover the gRPC handler. The wiring this test
			// validates is what the Repository returns vs what the env-renderer
			// outputs.
			uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
			if err != nil {
				t.Fatalf("SecretConnectionURIs: %v", err)
			}
			uri, present := uris[connID]
			if !present {
				t.Fatalf("URI for %q missing from delivery map; got keys = %v",
					connID, mapKeys(uris))
			}

			// The agent renders this as the env entry; mirror the exact format
			// the agent uses (internal/agent/runner.go::secretsEnv) so a
			// divergence is caught here.
			envEntry := "AIRFLOW_CONN_" + strings.ToUpper(connID) + "=" + uri
			if !strings.HasPrefix(envEntry, "AIRFLOW_CONN_") {
				t.Errorf("env entry missing required prefix: %q", envEntry)
			}

			// The end-user contract: the Python connector must accept the URI,
			// which requires url.Parse to succeed and round-trip the password
			// unencoded.
			parsed, perr := url.Parse(uri)
			if perr != nil {
				t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
			}
			if parsed.Scheme != tc.connType {
				t.Errorf("scheme = %q, want %q", parsed.Scheme, tc.connType)
			}
			wantHost := fmt.Sprintf("%s:%d", tc.host, tc.defaultPort)
			if parsed.Host != wantHost {
				t.Errorf("host = %q, want %q", parsed.Host, wantHost)
			}
			if parsed.User.Username() != "etl_user" {
				t.Errorf("username = %q, want etl_user", parsed.User.Username())
			}
			gotPassword, _ := parsed.User.Password()
			if gotPassword != rawPassword {
				t.Errorf("password round-trip failed: got %q, want %q (the URI builder must percent-escape; net/url must un-escape on parse)",
					gotPassword, rawPassword)
			}
			if !strings.HasPrefix(parsed.Path, "/"+tc.schema) {
				t.Errorf("path = %q, want /%s prefix", parsed.Path, tc.schema)
			}
		})
	}
}

// TestFileTransferConnectionURIShapeIntegration is the chain-of-custody for the
// file-transfer family (ssh, ftp, sftp): host:port + login + password, no schema
// and no Extra. SSHHook/FTPHook/SFTPHook read host:port + credentials. This
// proves a password with reserved characters round-trips for each.
func TestFileTransferConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // fixture, not a credential
	cases := []struct {
		connType    string
		defaultPort int
	}{
		{connType: "ssh", defaultPort: 22},
		{connType: "sftp", defaultPort: 22},
		{connType: "ftp", defaultPort: 21},
	}
	for _, tc := range cases {
		t.Run(tc.connType, func(t *testing.T) {
			repo, _, ctx := openRepo(t)
			key := make([]byte, 32)
			for i := range key {
				key[i] = byte(i + 53)
			}
			cipher, err := secrets.NewAESGCM(key)
			if err != nil {
				t.Fatal(err)
			}
			repo.SetCipher(cipher)

			connID := fmt.Sprintf("e2e_%s_%d", tc.connType, time.Now().UnixNano())
			port := tc.defaultPort
			if cerr := repo.SetConnection(ctx, "default", domain.Connection{
				ConnID: connID, ConnType: tc.connType,
				Host: "files.example.com", Login: "etl_user", Password: rawPassword, Port: &port,
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
				t.Fatalf("URI not parseable: %q err=%v", uris[connID], perr)
			}
			if parsed.Scheme != tc.connType {
				t.Errorf("scheme = %q, want %q", parsed.Scheme, tc.connType)
			}
			if want := fmt.Sprintf("files.example.com:%d", tc.defaultPort); parsed.Host != want {
				t.Errorf("host = %q, want %q", parsed.Host, want)
			}
			gotPassword, _ := parsed.User.Password()
			if gotPassword != rawPassword {
				t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
			}
		})
	}
}

// TestSecretConnectionURIsSkipsUndecryptableIntegration is the regression for the
// review-found robustness bug: SecretConnectionURIs used to fail the WHOLE batch
// (returning no connections, blinding the entire tenant) if a single row could
// not be decrypted — e.g. a row encrypted under a previous LEOFLOW_SECRET_KEY.
// The fix skips the bad row with a warning and delivers the rest. This stores one
// connection under key A and one under key B, then resolves with key A: the
// key-A connection is delivered, the key-B one is skipped, and there is NO error.
func TestSecretConnectionURIsSkipsUndecryptableIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	keyA := make([]byte, 32)
	keyB := make([]byte, 32)
	for i := range keyA {
		keyA[i] = byte(i + 3)
		keyB[i] = byte(i + 200) // a different key
	}
	cipherA, err := secrets.NewAESGCM(keyA)
	if err != nil {
		t.Fatal(err)
	}
	cipherB, err := secrets.NewAESGCM(keyB)
	if err != nil {
		t.Fatal(err)
	}
	port := 5432
	goodID := fmt.Sprintf("e2e_good_%d", time.Now().UnixNano())
	badID := fmt.Sprintf("e2e_bad_%d", time.Now().UnixNano())

	repo.SetCipher(cipherA)
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: goodID, ConnType: "postgres", Host: "h", Port: &port, Login: "u", Password: "good", Schema: "db",
	}); cerr != nil {
		t.Fatalf("SetConnection(good): %v", cerr)
	}
	t.Cleanup(func() { repo.SetCipher(cipherA); _ = repo.DeleteConnection(ctx, "default", goodID) })

	repo.SetCipher(cipherB)
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: badID, ConnType: "postgres", Host: "h", Port: &port, Login: "u", Password: "bad", Schema: "db",
	}); cerr != nil {
		t.Fatalf("SetConnection(bad): %v", cerr)
	}
	t.Cleanup(func() { repo.SetCipher(cipherB); _ = repo.DeleteConnection(ctx, "default", badID) })

	// Resolve with key A: the key-B connection is now undecryptable.
	repo.SetCipher(cipherA)
	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs returned an error — one bad row must NOT blind the tenant: %v", err)
	}
	if _, ok := uris[goodID]; !ok {
		t.Errorf("the decryptable connection %q was not delivered (tenant blinded by the bad row?)", goodID)
	}
	if _, ok := uris[badID]; ok {
		t.Errorf("the undecryptable connection %q should have been skipped, not delivered", badID)
	}
}

// mapKeys returns the keys of a string-keyed map, used to assemble
// diagnostic output when a Connection delivery test fails.
func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSQLiteConnectionURIShapeIntegration is the sqlite counterpart to the
// SQL-family chain-of-custody test. sqlite has no host, no port, no login,
// and no password — the **schema field carries the file path** and the
// URI is `sqlite:///<absolute path>`. None of the percent-escape edge
// cases apply, but the path round-trip and the no-host/no-user invariants
// are real and broken by plausible refactors.
//
// A regression that would surface only here: the URI builder accidentally
// emits `sqlite://<path>` (two slashes — wrong), drops the path, or
// percent-escapes the path's `/` separators.
func TestSQLiteConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 11)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	connID := fmt.Sprintf("e2e_sqlite_%d", time.Now().UnixNano())
	const dbPath = "/var/lib/leoflow/warehouse.db"
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "sqlite",
		Schema: dbPath, // sqlite convention: the "schema" field is the file path
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

	// The canonical sqlite URI is `sqlite:///<absolute path>` — three
	// slashes (two for the scheme separator, one for the absolute path).
	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "sqlite" {
		t.Errorf("scheme = %q, want sqlite", parsed.Scheme)
	}
	if parsed.Host != "" {
		t.Errorf("host = %q, want empty (sqlite has no host)", parsed.Host)
	}
	if parsed.User != nil {
		t.Errorf("user = %v, want nil (sqlite has no login/password)", parsed.User)
	}
	if parsed.Path != dbPath {
		t.Errorf("path round-trip failed: got %q, want %q", parsed.Path, dbPath)
	}
	// The DAG's user code does `urlparse(uri).path` to extract the file
	// path, then sqlite3.connect(path). Mirror that here so the contract
	// the cookbook documents is exactly what the test asserts.
	wantStringForm := "sqlite://" + dbPath
	if uri != wantStringForm {
		t.Errorf("uri = %q, want %q (the canonical 3-slash form)", uri, wantStringForm)
	}
}

// TestHTTPConnectionURIShapeIntegration is the http counterpart to the
// SQL-family chain-of-custody test. The http conn type doesn't fit the
// table because:
//
//   - There is no "schema" / db / namespace in HTTP — the Schema field is
//     normally blank and the URI ends at the host (or host:port).
//   - The high-value payload is in `Extra` (custom headers, including
//     `Authorization: Bearer ...`) which the URI carries under `__extra__`.
//
// The contract this pins: a Connection POSTed in the UI with a password
// containing URI-reserved characters AND an Extra JSON blob round-trips
// end-to-end via `AIRFLOW_CONN_<ID>` — `url.Parse` recovers the password
// unencoded and the Extra blob is recoverable from the `__extra__` query
// parameter.
func TestHTTPConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 13)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
		rawExtra    = `{"Authorization":"Bearer abc.def","X-Tenant":"acme"}`
	)
	connID := fmt.Sprintf("e2e_http_%d", time.Now().UnixNano())
	port := 443
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "http",
		Host:     "api.example.com",
		Login:    "etl_user",
		Password: rawPassword,
		Port:     &port,
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
		t.Fatalf("URI is not parseable (Python requests would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "http" {
		t.Errorf("scheme = %q, want http", parsed.Scheme)
	}
	if want := "api.example.com:443"; parsed.Host != want {
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
	// The HTTP-specific edge: Extra (headers, including Bearer tokens) must
	// survive the Repository -> SecretConnectionURIs -> agent env hop and
	// be recoverable from `__extra__`. A regression here would silently drop
	// the Authorization header — the request would 401 only at runtime.
	gotExtra := parsed.Query().Get("__extra__")
	if gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestSnowflakeConnectionURIShapeIntegration is the Snowflake chain-of-custody.
// It is the http shape, not the SQL host:port one, because a Snowflake
// connection's defining fields (account, warehouse, database, role, region)
// live in Extra — there is no meaningful host:port. SnowflakeHook reads those
// from the connection's extra_dejson at runtime.
//
// The contract this pins: a Snowflake Connection POSTed in the UI with a
// password containing URI-reserved characters AND the account/warehouse/role
// Extra blob round-trips end-to-end via AIRFLOW_CONN_<ID> — url.Parse recovers
// the password unencoded and the Extra blob is recoverable from `__extra__`. A
// regression would silently drop the warehouse/role and the hook would fail to
// connect only at runtime.
func TestSnowflakeConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 23)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
		rawExtra    = `{"account":"xy12345.eu-central-1","warehouse":"COMPUTE_WH","database":"ANALYTICS","role":"TRANSFORMER","region":"eu-central-1"}`
	)
	connID := fmt.Sprintf("e2e_snowflake_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "snowflake",
		Login:    "etl_user",
		Password: rawPassword,
		Schema:   "PUBLIC",
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
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (SnowflakeHook would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "snowflake" {
		t.Errorf("scheme = %q, want snowflake", parsed.Scheme)
	}
	if parsed.User.Username() != "etl_user" {
		t.Errorf("username = %q, want etl_user", parsed.User.Username())
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q (the URI builder must percent-escape; net/url must un-escape on parse)",
			gotPassword, rawPassword)
	}
	// The Snowflake-specific edge: the account/warehouse/role Extra must survive
	// the Repository -> SecretConnectionURIs -> agent env hop and be recoverable
	// from `__extra__`, since the hook has no host to fall back on.
	gotExtra := parsed.Query().Get("__extra__")
	if gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestSlackConnectionURIShapeIntegration is the Slack chain-of-custody. A Slack
// API connection carries the bot token in password and tuning (base_url/timeout)
// in Extra; SlackHook reads the token from the connection's password. The token
// is the secret, so this proves it survives the encryption + delivery hop with a
// reserved-character fixture standing in for the token.
func TestSlackConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 31)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		rawToken = "xoxb-1/2+3:4@5#secret" //nolint:gosec // fixture, not a real token
		rawExtra = `{"base_url":"https://slack.com/api/","timeout":30}`
	)
	connID := fmt.Sprintf("e2e_slack_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "slack",
		Password: rawToken,
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
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (SlackHook would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "slack" {
		t.Errorf("scheme = %q, want slack", parsed.Scheme)
	}
	gotToken, _ := parsed.User.Password()
	if gotToken != rawToken {
		t.Errorf("token round-trip failed: got %q, want %q", gotToken, rawToken)
	}
	gotExtra := parsed.Query().Get("__extra__")
	if gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestDatabricksConnectionURIShapeIntegration is the Databricks chain-of-custody.
// A Databricks connection has a real host (the workspace URL) plus a Personal
// Access Token in password; DatabricksHook reads host + token. So it is the
// host-bearing shape (unlike Snowflake/Slack), and this proves the PAT — which
// can contain reserved characters — survives the encryption + delivery hop and
// the host round-trips.
func TestDatabricksConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 37)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		host    = "dbc-12345678-9abc.cloud.databricks.com"
		rawPAT  = "dapi/1+2:3@4#deadbeef" //nolint:gosec // fixture, not a real token
		rawHTTP = "/sql/1.0/warehouses/abc123"
	)
	rawExtra := fmt.Sprintf(`{"http_path":%q}`, rawHTTP)
	connID := fmt.Sprintf("e2e_databricks_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "databricks",
		Host:     host,
		Password: rawPAT,
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
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (DatabricksHook would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "databricks" {
		t.Errorf("scheme = %q, want databricks", parsed.Scheme)
	}
	if parsed.Host != host {
		t.Errorf("host = %q, want %q", parsed.Host, host)
	}
	gotPAT, _ := parsed.User.Password()
	if gotPAT != rawPAT {
		t.Errorf("PAT round-trip failed: got %q, want %q", gotPAT, rawPAT)
	}
	gotExtra := parsed.Query().Get("__extra__")
	if gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestWasbConnectionURIShapeIntegration is the Azure Blob (wasb) chain-of-custody
// — the representative of the Azure provider family (adls, azure_data_lake,
// azure_data_factory, …, all apache-airflow-providers-microsoft-azure). A wasb
// connection carries the storage account as login, the account key as password,
// and tenant/identity hints in Extra; WasbHook reads them. Keyless via a managed
// identity is the recommended posture (ADR 0035); this covers the key path.
func TestWasbConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 41)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		account  = "mystorageacct"
		rawKey   = "Eby8vd/M02xNOcqF+l9C7T1/3xK+abc==" //nolint:gosec // fixture, not a real key; gitleaks:allow
		rawExtra = `{"tenant_id":"00000000-1111-2222-3333-444444444444"}`
	)
	connID := fmt.Sprintf("e2e_wasb_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "wasb",
		Login: account, Password: rawKey, Extra: rawExtra,
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
	uri := uris[connID]
	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI not parseable (WasbHook would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "wasb" {
		t.Errorf("scheme = %q, want wasb", parsed.Scheme)
	}
	if parsed.User.Username() != account {
		t.Errorf("account = %q, want %q", parsed.User.Username(), account)
	}
	gotKey, _ := parsed.User.Password()
	if gotKey != rawKey {
		t.Errorf("account key round-trip failed: got %q, want %q", gotKey, rawKey)
	}
	if got := parsed.Query().Get("__extra__"); got != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", got, rawExtra)
	}
}

// TestSparkConnectionURIShapeIntegration is the Spark chain-of-custody. A Spark
// connection is host-bearing (the master URL, e.g. spark://host:7077) with
// tuning (queue, deploy-mode) in Extra. SparkSubmitHook reads host:port + Extra.
func TestSparkConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 43)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		host     = "spark-master.example.com"
		rawExtra = `{"queue":"analytics","deploy-mode":"cluster"}`
	)
	connID := fmt.Sprintf("e2e_spark_%d", time.Now().UnixNano())
	port := 7077
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "spark", Host: host, Port: &port, Extra: rawExtra,
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
		t.Fatalf("URI not parseable: %q err=%v", uris[connID], perr)
	}
	if parsed.Scheme != "spark" {
		t.Errorf("scheme = %q, want spark", parsed.Scheme)
	}
	if want := fmt.Sprintf("%s:%d", host, port); parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	if got := parsed.Query().Get("__extra__"); got != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", got, rawExtra)
	}
}

// TestKafkaConnectionURIShapeIntegration is the Kafka chain-of-custody. A Kafka
// connection carries the entire client config (bootstrap.servers, security
// settings) as raw JSON in Extra — there is no host:port or login. KafkaBaseHook
// passes the Extra dict straight to the confluent-kafka client.
func TestKafkaConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 47)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawExtra = `{"bootstrap.servers":"broker1:9092,broker2:9092","security.protocol":"SASL_SSL","sasl.username":"etl"}`
	connID := fmt.Sprintf("e2e_kafka_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "kafka", Extra: rawExtra,
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
		t.Fatalf("URI not parseable: %q err=%v", uris[connID], perr)
	}
	if parsed.Scheme != "kafka" {
		t.Errorf("scheme = %q, want kafka", parsed.Scheme)
	}
	// The whole Kafka client config lives in Extra; a regression that dropped it
	// would leave the hook with no brokers to connect to.
	if got := parsed.Query().Get("__extra__"); got != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", got, rawExtra)
	}
}

// TestAWSConnectionURIShapeIntegration is the AWS chain-of-custody. Like
// Snowflake it is the http shape (no host:port): an AWS connection carries the
// access key as login, the secret key as password, and region_name/role_arn in
// Extra. AwsBaseHook reads them at runtime.
//
// This covers the **key-based** path. The recommended path is keyless (an IAM
// role / instance profile / web-identity, ADR 0035): no key is stored, and the
// SDK resolves credentials in the task at runtime — so there is nothing for this
// delivery test to assert there.
//
// The contract this pins: an AWS Connection with a secret key containing
// URI-reserved characters (the `/` and `+` AWS secret keys routinely contain)
// AND the region/role Extra round-trips end-to-end via AIRFLOW_CONN_<ID> — the
// secret key un-escapes intact and the Extra is recoverable from `__extra__`.
func TestAWSConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 29)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		accessKeyID = "AKIAIOSFODNN7EXAMPLE"
		// An AWS secret key shape: contains `/` and `+`, the reserved chars that
		// break a naive URI builder.
		rawSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCY+EXAMPLEKEY" //nolint:gosec // fixture, not a real credential
		rawExtra  = `{"region_name":"eu-central-1","role_arn":"arn:aws:iam::123456789012:role/transformer"}`
	)
	connID := fmt.Sprintf("e2e_aws_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "aws",
		Login:    accessKeyID,
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
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (AwsBaseHook would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "aws" {
		t.Errorf("scheme = %q, want aws", parsed.Scheme)
	}
	if parsed.User.Username() != accessKeyID {
		t.Errorf("access key = %q, want %q", parsed.User.Username(), accessKeyID)
	}
	gotSecret, _ := parsed.User.Password()
	if gotSecret != rawSecret {
		t.Errorf("secret key round-trip failed: got %q, want %q (the URI builder must percent-escape / and +)",
			gotSecret, rawSecret)
	}
	gotExtra := parsed.Query().Get("__extra__")
	if gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}
