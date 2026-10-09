package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

func testProtectedResource() ProtectedResource {
	return ProtectedResource{
		Resource:             "https://acme.example.com/mcp",
		AuthorizationServers: []string{"https://auth.example.com"},
		Scopes:               []string{"dexaflow:read"},
	}
}

// httpFixture serves NewServer through HTTPHandler, as cmd/dexaflow-mcp
// does, in front of a control plane that records the bearer it saw.
type httpFixture struct {
	url     string
	gotAuth string
}

func newHTTPFixture(t *testing.T, pr ProtectedResource) *httpFixture {
	t.Helper()
	f := &httpFixture{}
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dags":[],"total_entries":0}`))
	}))
	t.Cleanup(cp.Close)
	base, err := apiclient.New(cp.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(HTTPHandler(NewServer(base, cp.URL, "test", true), pr))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// reply is a response read in full, its body already closed.
type reply struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (f *httpFixture) do(t *testing.T, method, path, bearer string) reply {
	t.Helper()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	req, err := http.NewRequestWithContext(context.Background(), method, f.url+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{StatusCode: resp.StatusCode, Header: resp.Header, Body: b}
}

// TestProtectedResourceMetadataShape locks the RFC 9728 document an MCP client
// reads to find the authorization server, at the path RFC 9728 derives from
// the resource and at the bare well-known path.
func TestProtectedResourceMetadataShape(t *testing.T) {
	f := newHTTPFixture(t, testProtectedResource())
	want := map[string]any{
		"resource":                 "https://acme.example.com/mcp",
		"authorization_servers":    []any{"https://auth.example.com"},
		"bearer_methods_supported": []any{"header"},
		"scopes_supported":         []any{"dexaflow:read"},
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		t.Run(path, func(t *testing.T) {
			resp := f.do(t, http.MethodGet, path, "")

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
			}
			var got map[string]any
			if err := json.Unmarshal(resp.Body, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("metadata = %v, want %v", got, want)
			}
		})
	}
}

// TestProtectedResourceMetadataOmitsUnsetScopes keeps scopes_supported out of
// the document when the operator lists none, rather than an empty list.
func TestProtectedResourceMetadataOmitsUnsetScopes(t *testing.T) {
	pr := testProtectedResource()
	pr.Scopes = nil
	f := newHTTPFixture(t, pr)

	resp := f.do(t, http.MethodGet, "/.well-known/oauth-protected-resource/mcp", "")

	var got map[string]any
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["scopes_supported"]; ok {
		t.Errorf("metadata = %v, want no scopes_supported", got)
	}
}

// TestBearerlessMCPRequestIsChallenged is how a client learns where to sign
// in: a request without a bearer gets 401 and a pointer to the metadata.
func TestBearerlessMCPRequestIsChallenged(t *testing.T) {
	f := newHTTPFixture(t, testProtectedResource())

	resp := f.do(t, http.MethodPost, "/mcp", "")

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /mcp without a bearer = %d, want 401", resp.StatusCode)
	}
	want := `Bearer resource_metadata="https://acme.example.com/.well-known/oauth-protected-resource/mcp"`
	if got := resp.Header.Get("WWW-Authenticate"); got != want {
		t.Errorf("WWW-Authenticate = %q, want %q", got, want)
	}
}

// TestBearerReachesTheControlPlaneWithMetadataOn locks that the challenge only
// stops bearer-less requests: a request with a bearer is served, and the
// bearer is what reaches /api/v2.
func TestBearerReachesTheControlPlaneWithMetadataOn(t *testing.T) {
	f := newHTTPFixture(t, testProtectedResource())
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "oauth-test", Version: "0"}, nil)
	sess, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{
		Endpoint:   f.url + "/mcp",
		HTTPClient: &http.Client{Transport: authRoundTripper{token: "usertok", base: http.DefaultTransport}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "list_dags"})

	if err != nil || res.IsError {
		t.Fatalf("list_dags = %+v, %v", res, err)
	}
	if f.gotAuth != "Bearer usertok" {
		t.Errorf("control plane saw %q, want Bearer usertok", f.gotAuth)
	}
}

// TestNoMetadataKeepsTodaysBehaviour locks the default: without a resource
// there is no metadata document and no challenge.
func TestNoMetadataKeepsTodaysBehaviour(t *testing.T) {
	f := newHTTPFixture(t, ProtectedResource{})

	if resp := f.do(t, http.MethodGet, "/.well-known/oauth-protected-resource", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET metadata without config = %d, want 404", resp.StatusCode)
	}
	resp := f.do(t, http.MethodPost, "/mcp", "")
	if resp.StatusCode == http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != "" {
		t.Errorf("POST /mcp without a bearer = %d (WWW-Authenticate %q), want no challenge", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

// TestHealthzNeedsNoBearer keeps the probe outside the challenge.
func TestHealthzNeedsNoBearer(t *testing.T) {
	for _, pr := range []ProtectedResource{{}, testProtectedResource()} {
		f := newHTTPFixture(t, pr)

		if resp := f.do(t, http.MethodGet, "/healthz", ""); resp.StatusCode != http.StatusOK {
			t.Errorf("GET /healthz (resource %q) = %d, want 200", pr.Resource, resp.StatusCode)
		}
	}
}

func TestProtectedResourceMetadataURL(t *testing.T) {
	cases := map[string]string{
		"https://acme.example.com/mcp":  "https://acme.example.com/.well-known/oauth-protected-resource/mcp",
		"https://acme.example.com/mcp/": "https://acme.example.com/.well-known/oauth-protected-resource/mcp",
		"https://acme.example.com":      "https://acme.example.com/.well-known/oauth-protected-resource",
		"http://localhost:9099/mcp":     "http://localhost:9099/.well-known/oauth-protected-resource/mcp",
	}
	for resource, want := range cases {
		pr := ProtectedResource{Resource: resource, AuthorizationServers: []string{"https://auth.example.com"}}

		if got := pr.MetadataURL(); got != want {
			t.Errorf("MetadataURL(%q) = %q, want %q", resource, got, want)
		}
	}
}

// TestProtectedResourceValidate is the boot check: a half-set or malformed
// configuration fails start-up instead of advertising a broken sign-in.
func TestProtectedResourceValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ProtectedResource)
		wantErr bool
	}{
		{"complete", func(*ProtectedResource) {}, false},
		{"unset", func(p *ProtectedResource) { *p = ProtectedResource{} }, false},
		{"loopback http", func(p *ProtectedResource) { p.Resource = "http://127.0.0.1:9099/mcp" }, false},
		{"resource without servers", func(p *ProtectedResource) { p.AuthorizationServers = nil }, true},
		{"servers without resource", func(p *ProtectedResource) { p.Resource = "" }, true},
		{"relative resource", func(p *ProtectedResource) { p.Resource = "/mcp" }, true},
		{"plain http resource", func(p *ProtectedResource) { p.Resource = "http://acme.example.com/mcp" }, true},
		{"resource with a fragment", func(p *ProtectedResource) { p.Resource = "https://acme.example.com/mcp#x" }, true},
		{"resource with a query", func(p *ProtectedResource) { p.Resource = "https://acme.example.com/mcp?t=1" }, true},
		{"plain http server", func(p *ProtectedResource) { p.AuthorizationServers = []string{"http://auth.example.com"} }, true},
		{"empty server", func(p *ProtectedResource) { p.AuthorizationServers = []string{""} }, true},
		{"empty scope", func(p *ProtectedResource) { p.Scopes = []string{""} }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := testProtectedResource()
			tc.mutate(&pr)

			err := pr.Validate()

			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
