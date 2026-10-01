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

// TestCassandraConnectionURIShapeIntegration walks the full delivery chain
// for a Cassandra Connection (CassandraHook) and proves the host:port,
// keyspace (Schema), and reserved-character password round-trip end-to-end
// via AIRFLOW_CONN_<ID>.
func TestCassandraConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
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

	connID := fmt.Sprintf("e2e_cassandra_%d", time.Now().UnixNano())
	port := 9042
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "cassandra",
		Host: "warehouse.example.com", Login: "etl_user",
		Password: rawPassword, Port: &port, Schema: "analytics",
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
		t.Fatalf("URI is not parseable: %q err=%v", uri, perr)
	}
	if parsed.Scheme != "cassandra" {
		t.Errorf("scheme = %q, want cassandra", parsed.Scheme)
	}
	if want := "warehouse.example.com:9042"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}

// TestNeo4jConnectionURIShapeIntegration walks the full delivery chain for a
// Neo4j Connection (Neo4jHook) and proves the host:port, database (Schema),
// and reserved-character password round-trip end-to-end via
// AIRFLOW_CONN_<ID>.
func TestNeo4jConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
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

	connID := fmt.Sprintf("e2e_neo4j_%d", time.Now().UnixNano())
	port := 7687
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "neo4j",
		Host: "warehouse.example.com", Login: "etl_user",
		Password: rawPassword, Port: &port, Schema: "neo4j",
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
		t.Fatalf("URI is not parseable: %q err=%v", uri, perr)
	}
	if parsed.Scheme != "neo4j" {
		t.Errorf("scheme = %q, want neo4j", parsed.Scheme)
	}
	if want := "warehouse.example.com:7687"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}

// TestVerticaConnectionURIShapeIntegration walks the full delivery chain for
// a Vertica Connection (VerticaHook) and proves the host:port, database
// (Schema), and reserved-character password round-trip end-to-end via
// AIRFLOW_CONN_<ID>.
func TestVerticaConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
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

	connID := fmt.Sprintf("e2e_vertica_%d", time.Now().UnixNano())
	port := 5433
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "vertica",
		Host: "warehouse.example.com", Login: "etl_user",
		Password: rawPassword, Port: &port, Schema: "analytics",
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
		t.Fatalf("URI is not parseable: %q err=%v", uri, perr)
	}
	if parsed.Scheme != "vertica" {
		t.Errorf("scheme = %q, want vertica", parsed.Scheme)
	}
	if want := "warehouse.example.com:5433"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}

// TestInfluxdbConnectionURIShapeIntegration walks the full delivery chain for
// an InfluxDB Connection (InfluxDBHook). InfluxDB 2.x authenticates with an
// org + token carried in Extra rather than login/password, so this test
// asserts the scheme, host:port, and the __extra__ (token-bearing) blob
// round-trip end-to-end via AIRFLOW_CONN_<ID>.
func TestInfluxdbConnectionURIShapeIntegration(t *testing.T) {
	const rawExtra = `{"org":"acme","token":"tok/en+1:2"}`
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

	connID := fmt.Sprintf("e2e_influxdb_%d", time.Now().UnixNano())
	port := 8086
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "influxdb",
		Host: "warehouse.example.com", Port: &port,
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
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable: %q err=%v", uri, perr)
	}
	if parsed.Scheme != "influxdb" {
		t.Errorf("scheme = %q, want influxdb", parsed.Scheme)
	}
	if want := "warehouse.example.com:8086"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotExtra := parsed.Query().Get("__extra__")
	if gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestDruidConnectionURIShapeIntegration walks the full delivery chain for a
// Druid Connection (DruidDbApiHook) and proves the broker host:port, the
// schema, and the reserved-character password round-trip end-to-end via
// AIRFLOW_CONN_<ID>.
func TestDruidConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
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

	connID := fmt.Sprintf("e2e_druid_%d", time.Now().UnixNano())
	port := 8082
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "druid",
		Host: "warehouse.example.com", Login: "etl_user",
		Password: rawPassword, Port: &port, Schema: "druid",
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
		t.Fatalf("URI is not parseable: %q err=%v", uri, perr)
	}
	if parsed.Scheme != "druid" {
		t.Errorf("scheme = %q, want druid", parsed.Scheme)
	}
	if want := "warehouse.example.com:8082"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}

// TestPinotConnectionURIShapeIntegration walks the full delivery chain for a
// Pinot Connection (PinotDbApiHook) and proves the broker host:port, the
// schema, and the reserved-character password round-trip end-to-end via
// AIRFLOW_CONN_<ID>.
func TestPinotConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 83)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	connID := fmt.Sprintf("e2e_pinot_%d", time.Now().UnixNano())
	port := 8000
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "pinot",
		Host: "warehouse.example.com", Login: "etl_user",
		Password: rawPassword, Port: &port, Schema: "default",
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
		t.Fatalf("URI is not parseable: %q err=%v", uri, perr)
	}
	if parsed.Scheme != "pinot" {
		t.Errorf("scheme = %q, want pinot", parsed.Scheme)
	}
	if want := "warehouse.example.com:8000"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}

// TestElasticsearchConnectionURIShapeIntegration walks the full delivery
// chain for an Elasticsearch Connection (ElasticsearchSQLHook) and proves the
// host:port, the schema, and the reserved-character password round-trip
// end-to-end via AIRFLOW_CONN_<ID>.
func TestElasticsearchConnectionURIShapeIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 89)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	connID := fmt.Sprintf("e2e_elasticsearch_%d", time.Now().UnixNano())
	port := 9200
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "elasticsearch",
		Host: "warehouse.example.com", Login: "etl_user",
		Password: rawPassword, Port: &port, Schema: "default",
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
		t.Fatalf("URI is not parseable: %q err=%v", uri, perr)
	}
	if parsed.Scheme != "elasticsearch" {
		t.Errorf("scheme = %q, want elasticsearch", parsed.Scheme)
	}
	if want := "warehouse.example.com:9200"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}
