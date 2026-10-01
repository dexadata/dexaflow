package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// authRoundTripper stamps every outbound request with a bearer token — the job
// an MCP HTTP client (or a Pro gateway) does for the caller.
type authRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (a authRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+a.token)
	return a.base.RoundTrip(r)
}

// TestHTTPTransportPassesTokenThrough is the Streamable-HTTP end-to-end (ADR 0050
// Phase 3): a real MCP client talks to NewStreamableHTTPHandler over HTTP carrying
// its own bearer, and the token reaches the control plane on the resulting tool
// call. This exercises the whole chain server-side — HTTP handler → session →
// tool handler → per-request client — that the Pro transport depends on (D9),
// without spawning a binary.
func TestHTTPTransportPassesTokenThrough(t *testing.T) {
	ctx := context.Background()

	// Mock control plane: records the bearer it was called with.
	var gotAuth string
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/dags" {
			gotAuth = r.Header.Get("Authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dags":[{"dag_id":"etl","is_paused":false}],"total_entries":1}`))
	}))
	defer cp.Close()

	// Base client has NO token; serverURL points at the mock control plane so the
	// server can mint a per-request client from the caller's header.
	base, err := apiclient.New(cp.URL, "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	srv := NewServer(base, cp.URL, "test", true) // http transport: per-request bearer

	// The MCP server, served over Streamable HTTP exactly as cmd/leoflow-mcp does.
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return srv },
		&mcpsdk.StreamableHTTPOptions{Stateless: true},
	)
	mcpHTTP := httptest.NewServer(mcpHandler)
	defer mcpHTTP.Close()

	// An MCP client that carries a caller bearer on every HTTP request.
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-test-client", Version: "0"}, nil)
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint: mcpHTTP.URL,
		HTTPClient: &http.Client{Transport: authRoundTripper{
			token: "usertok", base: http.DefaultTransport,
		}},
	}
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_dags"})
	if err != nil {
		t.Fatalf("CallTool(list_dags): %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned an error result: %+v", res.Content)
	}
	if gotAuth != "Bearer usertok" {
		t.Errorf("caller token did not reach the control plane over HTTP; saw %q", gotAuth)
	}
}
