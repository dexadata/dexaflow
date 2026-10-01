package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeConnTester struct {
	ok      bool
	message string
	gotType string
}

func (f *fakeConnTester) Test(_ context.Context, c domain.Connection) (ok bool, message string) {
	f.gotType = c.ConnType
	return f.ok, f.message
}

func probeServer(tester ConnectionTester) *gin.Engine {
	return NewServer(Dependencies{
		Logger:         discardLogger(),
		Authenticator:  &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:    auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:    []string{"*"},
		ConnectionTest: tester,
	})
}

func TestConnectionTestEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
		msg  string
	}{
		{"reachable", true, "reachable: db:5432"},
		{"unreachable", false, "cannot reach db:5432: timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tester := &fakeConnTester{ok: tc.ok, message: tc.msg}
			rec := authGet(probeServer(tester), http.MethodPost, "/api/v2/connections/test",
				`{"connection_id":"c","conn_type":"postgres","host":"db"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
			}
			var got connectionTestResultDTO
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			// The reachability result is preserved verbatim …
			if got.Status != tc.ok || !strings.Contains(got.Message, tc.msg) {
				t.Errorf("got %+v, want status=%v msg containing %q", got, tc.ok, tc.msg)
			}
			// … and a connector nudge is appended for a known conn_type, since the
			// probe response is our surface (the form itself is Airflow's SPA). This
			// is the setup-time reminder that the Connection alone is not enough —
			// the DAG must declare the provider. ADR 0038 #1.
			if !strings.Contains(got.Message, "connectors: [postgres]") {
				t.Errorf("message %q missing the connectors: nudge", got.Message)
			}
			if tester.gotType != "postgres" {
				t.Errorf("tester saw conn_type %q, want postgres", tester.gotType)
			}
		})
	}
}

// TestConnectionTestNudgeOnlyForKnownConnectors guards that the nudge is keyed on
// the catalog: a curated conn_type gets the connectors: hint; an unknown one does
// not (no misleading suggestion for a type the sugar cannot expand).
func TestConnectionTestNudgeOnlyForKnownConnectors(t *testing.T) {
	tester := &fakeConnTester{ok: true, message: "reachable: x:1"}
	rec := authGet(probeServer(tester), http.MethodPost, "/api/v2/connections/test",
		`{"connection_id":"c","conn_type":"totally_made_up","host":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	var got connectionTestResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Message, "connectors:") {
		t.Errorf("unknown conn_type should get no nudge; message = %q", got.Message)
	}
}

// The GCP probe is structural (no cloud call): a well-formed service-account key
// validates, a malformed/incomplete one fails with a clear message, and an empty
// (keyless) connection reports ADC/Workload Identity as ok.
func TestGCPConnectionProbe(t *testing.T) {
	validKey := `{"type":"service_account","client_email":"x@p.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nABC\n-----END PRIVATE KEY-----\n","project_id":"p"}`
	for _, tc := range []struct {
		name    string
		extra   string
		wantOK  bool
		wantSub string // substring expected in the message
	}{
		{"keyfile_dict object", `{"keyfile_dict":` + validKey + `}`, true, "service-account key looks valid"},
		{"keyfile_dict stringified", `{"keyfile_dict":` + jsonString(validKey) + `}`, true, "service-account key looks valid"},
		{"legacy extra name", `{"extra__google_cloud_platform__keyfile_dict":` + validKey + `}`, true, "looks valid"},
		{"invalid extra json", `{not json`, false, "invalid Extra JSON"},
		{"missing field", `{"keyfile_dict":{"type":"service_account","client_email":"x","project_id":"p"}}`, false, "missing required field: private_key"},
		{"wrong type", `{"keyfile_dict":{"type":"user","client_email":"x","private_key":"k","project_id":"p"}}`, false, `must be "service_account"`},
		{"key_path", `{"key_path":"/etc/gcp/key.json"}`, true, "key_path set"},
		{"keyless empty", ``, true, "keyless"},
		{"keyless explicit", `{"project":"p","scopes":["https://www.googleapis.com/auth/cloud-platform"]}`, true, "keyless"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, msg := testGCPConnection(domain.Connection{ConnType: "google_cloud_platform", Extra: tc.extra})
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v (msg %q)", ok, tc.wantOK, msg)
			}
			if !strings.Contains(msg, tc.wantSub) {
				t.Errorf("msg = %q, want substring %q", msg, tc.wantSub)
			}
		})
	}
}

// jsonString returns s as a JSON string literal (quoted + escaped).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The connection test is STRUCTURAL ONLY — the control plane makes NO network
// call (no SSRF / internal port-scan; go/request-forgery). Regression guard: an
// UNROUTABLE host with a valid shape still validates, which it could not if the
// probe dialed it. Live reachability/auth is tested where the connection is used
// (the task/executor), not from the privileged control plane.
func TestConnectionTestIsStructuralAndMakesNoNetworkCall(t *testing.T) {
	tester := defaultConnectionTester{}
	p := func(n int) *int { return &n }
	for _, tc := range []struct {
		name   string
		conn   domain.Connection
		wantOK bool
		sub    string
	}{
		// 192.0.2.0/24 is TEST-NET-1 (RFC 5737), guaranteed unroutable: a real dial
		// or GET would fail. ok=true proves the probe never touches the network.
		{"http unroutable validates", domain.Connection{ConnType: "http", Host: "192.0.2.1", Port: p(9)}, true, "looks valid"},
		{"postgres unroutable validates", domain.Connection{ConnType: "postgres", Host: "192.0.2.1", Port: p(5432)}, true, "looks valid"},
		{"default port applied", domain.Connection{ConnType: "redis", Host: "cache.internal"}, true, "6379"},
		{"missing host fails", domain.Connection{ConnType: "postgres"}, false, "no host"},
		{"http missing host fails", domain.Connection{ConnType: "http"}, false, "no host"},
		{"unknown type without port fails", domain.Connection{ConnType: "totally_made_up", Host: "x"}, false, "set a port"},
		{"schema selects scheme in the validated URL", domain.Connection{ConnType: "http", Host: "h", Port: p(8443), Schema: "https"}, true, "https://h:8443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, msg := tester.Test(context.Background(), tc.conn)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v (msg %q)", ok, tc.wantOK, msg)
			}
			if !strings.Contains(msg, tc.sub) {
				t.Errorf("msg = %q, want substring %q", msg, tc.sub)
			}
		})
	}
}
