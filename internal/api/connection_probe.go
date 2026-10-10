package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/connectors"
	"github.com/dexadata/dexaflow/internal/domain"
)

// ConnectionTester checks whether a connection is well-formed. The default
// implementation validates STRUCTURE only and makes no network call: the Go
// control plane must never reach out to a user-configured host (SSRF / internal
// port-scan — go/request-forgery), and "reachable from the control plane" is the
// wrong question anyway, since a connection is used in the task's network scope,
// not the control plane's. Live reachability/auth is tested where the connection
// is actually used (the task/executor) — tracked as a follow-up.
type ConnectionTester interface {
	Test(ctx context.Context, c domain.Connection) (ok bool, message string)
}

// connectionTestResultDTO is the Airflow ConnectionTestResponse shape.
type connectionTestResultDTO struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
}

// testConnectionHandler powers POST /api/v2/connections/test (the panel's "Test"
// button). It tests the posted connection body without persisting anything.
func testConnectionHandler(tester ConnectionTester) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body connectionBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid connection body"})
			return
		}
		ok, msg := tester.Test(c.Request.Context(), body.toDomain(body.ConnectionID))
		if hint := connectorDependencyNudge(body.ConnType); hint != "" {
			msg = msg + " · " + hint
		}
		c.JSON(http.StatusOK, connectionTestResultDTO{Status: ok, Message: msg})
	}
}

// connectorDependencyNudge returns a setup-time reminder that a Connection alone
// is not enough — the DAG must also declare the provider so the hook is installed
// (ADR 0038 #1). It is appended to the probe response (our surface; the form is
// Airflow's SPA). Empty for a conn_type the catalog cannot expand, to avoid
// suggesting a connectors: entry that would fail the compile.
func connectorDependencyNudge(connType string) string {
	if _, ok := connectors.PackageFor(connType); !ok {
		return ""
	}
	return "to use this connection from a task hook, declare the provider in your " +
		"DAG's dexaflow.yaml: connectors: [" + connType + "]"
}

// defaultConnPorts maps connection types to their well-known port, used when the
// connection does not pin one.
var defaultConnPorts = map[string]int{
	"postgres": 5432, "redshift": 5439, "mysql": 3306, "mariadb": 3306,
	"redis": 6379, "mongo": 27017, "mssql": 1433, "oracle": 1521,
	"ftp": 21, "sftp": 22, "ssh": 22, "kafka": 9092,
}

// defaultConnectionTester validates a connection's STRUCTURE using only the
// stdlib — it makes no network call (see ConnectionTester).
type defaultConnectionTester struct{}

// Test validates that the connection is well-formed without contacting the host:
// GCP keys are checked structurally, HTTP(S) URLs are parsed, and host/port
// endpoint types must carry a host and a (declared or default) port. It never
// dials — reachability/auth is exercised when a task uses the connection, in the
// task's own network scope, not the control plane's.
func (defaultConnectionTester) Test(_ context.Context, conn domain.Connection) (ok bool, message string) {
	t := strings.ToLower(conn.ConnType)
	if t == "google_cloud_platform" {
		return testGCPConnection(conn)
	}
	if t == "http" || t == "https" {
		return validateHTTPConnection(conn)
	}
	if conn.Host == "" {
		return false, "no host configured"
	}
	port := 0
	if conn.Port != nil {
		port = *conn.Port
	}
	if port == 0 {
		port = defaultConnPorts[t]
	}
	if port == 0 {
		return false, "set a port — no default known for conn_type " + conn.ConnType
	}
	return true, "connection looks valid: " + net.JoinHostPort(conn.Host, strconv.Itoa(port)) +
		" — reachability is tested when a task uses it"
}

// testGCPConnection structurally validates a google_cloud_platform connection
// from the control plane. It does NOT call Google: the control plane runs no
// provider hooks (Python), and reachability is meaningless for GCP (no host).
// It validates the service-account key shape when a key is present, or reports
// keyless (ADC / Workload Identity), which is resolved at task runtime. Short
// field names are preferred, with Airflow's legacy extra__google_cloud_platform__
// names accepted as a fallback.
func testGCPConnection(conn domain.Connection) (ok bool, message string) {
	extra := map[string]any{}
	if conn.Extra != "" {
		if err := json.Unmarshal([]byte(conn.Extra), &extra); err != nil {
			return false, "invalid Extra JSON: " + err.Error()
		}
	}
	field := func(name string) any {
		if v, present := extra[name]; present && v != nil && v != "" {
			return v
		}
		return extra["extra__google_cloud_platform__"+name]
	}

	if kd := field("keyfile_dict"); kd != nil {
		return validateGCPKeyfileDict(kd)
	}
	if kp, isStr := field("key_path").(string); isStr && kp != "" {
		return true, "key_path set — validated at task runtime: " + kp
	}
	return true, "keyless (ADC / Workload Identity) — resolved at task runtime, not from the control plane"
}

// validateGCPKeyfileDict structurally validates an inline service-account key
// (the keyfile_dict field). It accepts the key either as a nested JSON object or
// as a stringified JSON blob (Airflow emits both forms) and checks the fields
// every service-account key carries. It performs no network call — the actual
// token exchange happens in the task at runtime.
func validateGCPKeyfileDict(kd any) (ok bool, message string) {
	var key map[string]any
	switch v := kd.(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &key); err != nil {
			return false, "keyfile_dict is not valid JSON: " + err.Error()
		}
	case map[string]any:
		key = v
	default:
		return false, "keyfile_dict must be a JSON object or a JSON string"
	}
	// Capture each required field via an ok-checked type assertion (the A+ linter
	// forbids discarding the assertion's ok value).
	got := make(map[string]string, 4)
	for _, name := range []string{"type", "client_email", "private_key", "project_id"} {
		s, isStr := key[name].(string)
		if !isStr || s == "" {
			return false, "keyfile_dict missing required field: " + name
		}
		got[name] = s
	}
	if got["type"] != "service_account" {
		return false, `keyfile_dict "type" must be "service_account"`
	}
	return true, "service-account key looks valid (" + got["client_email"] + ")"
}

// validateHTTPConnection structurally validates an http/https connection's URL
// WITHOUT making a request (no SSRF). It mirrors Airflow's HttpHook URL assembly
// ({schema}://{host}:{port}) so a malformed host is caught, then parses it.
func validateHTTPConnection(conn domain.Connection) (ok bool, message string) {
	if conn.Host == "" {
		return false, "no host configured"
	}
	target := conn.Host
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		// The Airflow connection form sends host, schema and port as separate
		// fields; mirror Airflow's HttpHook, which builds {schema}://{host}:{port}.
		// `schema` selects the URL scheme; conn_type ("https") is the fallback.
		scheme := "http"
		if strings.EqualFold(conn.Schema, "https") || strings.EqualFold(conn.ConnType, "https") {
			scheme = "https"
		}
		target = scheme + "://" + conn.Host
		// Honor an explicitly pinned port unless the host already carries one.
		if conn.Port != nil && *conn.Port != 0 && !strings.Contains(conn.Host, ":") {
			target += ":" + strconv.Itoa(*conn.Port)
		}
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return false, "invalid URL: " + target
	}
	return true, "connection looks valid: " + target + " — reachability is tested when a task uses it"
}
