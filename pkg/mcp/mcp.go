// Package mcp builds the Dexaflow Model Context Protocol server for programs
// that want the stock tools plus tools of their own (#2178): a runbook lookup
// or a diagnosis specific to one install, without forking the server.
//
// The server talks to the control plane only through pkg/client, with the
// caller's identity: on the HTTP transport every request carries its own
// bearer and the server holds no token of its own; on stdio the one local
// caller is the process token. A tool added with AddTool receives the client
// for that identity and must not build its own.
//
// Types come from the official SDK (github.com/modelcontextprotocol/go-sdk),
// so a tool is declared exactly as for any go-sdk server.
//
//	s := mcp.NewServer(api, serverURL, version, mcp.TransportHTTP)
//	mcp.AddTool(s, &mcpsdk.Tool{Name: "runbook", Description: "..."}, runbook)
//	http.ListenAndServe(addr, mcp.HTTPHandler(s, mcp.ProtectedResource{}))
package mcp

import (
	"context"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	imcp "github.com/dexadata/dexaflow/internal/mcp"
	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// CapabilitiesURI is the resource that reports the server's version, its
// transport, which optional tool groups are on and every tool it serves.
const CapabilitiesURI = imcp.CapabilitiesURI

// Transport selects the identity policy of a server.
type Transport int

const (
	// TransportStdio serves one local caller whose identity is the token of
	// the client given to NewServer.
	TransportStdio Transport = iota
	// TransportHTTP serves many callers over Streamable HTTP; each request
	// must carry its own bearer, and one without is refused.
	TransportHTTP
)

// Option configures NewServer.
type Option = imcp.Option

// WithUIBaseURL makes results carry web_url links into the Dexaflow UI served
// at base. Check base with ValidateUIBaseURL first; "" leaves links off.
func WithUIBaseURL(base string) Option { return imcp.WithUIBaseURL(base) }

// WithRunControl registers the run control tools (trigger_run, clear_task,
// pause_dag, unpause_dag, apply_plan), with key signing their plans. A nil
// key draws a random one, enough for a single stdio process; replicas of an
// HTTP server must share a key, read with LoadPlanKey.
func WithRunControl(key []byte) Option { return imcp.WithRunControl(key) }

// LoadPlanKey reads a run control plan key of at least 32 bytes from path.
func LoadPlanKey(path string) ([]byte, error) { return imcp.LoadPlanKey(path) }

// ValidateUIBaseURL reports whether s can be given to WithUIBaseURL.
func ValidateUIBaseURL(s string) error { return imcp.ValidateUIBaseURL(s) }

// ProtectedResource is the OAuth protected resource metadata (RFC 9728) the
// HTTP transport serves; the zero value serves none.
type ProtectedResource = imcp.ProtectedResource

// Server is a Dexaflow MCP server: the stock read tools, resources and
// prompts, the run control tools when enabled, and any tool added with
// AddTool.
type Server struct {
	s *imcp.Server
}

// NewServer builds a server that reaches the control plane at serverURL
// through api. On TransportHTTP, api should carry no token: each request's
// own bearer is used instead. version is reported to clients.
func NewServer(api *apiclient.ClientWithResponses, serverURL, version string, transport Transport, opts ...Option) *Server {
	return &Server{s: imcp.New(api, serverURL, version, transport == TransportHTTP, opts...)}
}

// SDK returns the go-sdk server, to run it over a transport (Run with
// &mcpsdk.StdioTransport{}) or to add resources and prompts.
func (s *Server) SDK() *mcpsdk.Server { return s.s.SDK() }

// ToolHandler handles a call of a tool added with AddTool. api is the
// control-plane client of the caller; the other arguments and results are
// those of a go-sdk typed tool handler.
type ToolHandler[In, Out any] func(ctx context.Context, api *apiclient.ClientWithResponses, req *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, Out, error)

// AddTool registers t on s. Its input and output schemas are inferred from In
// and Out as by the go-sdk AddTool. Each call resolves the caller's client
// before h runs, so a request the identity policy refuses never reaches h.
// The tool appears in tools/list and in the capabilities resource.
func AddTool[In, Out any](s *Server, t *mcpsdk.Tool, h ToolHandler[In, Out]) {
	mcpsdk.AddTool(s.SDK(), t, func(ctx context.Context, req *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, Out, error) {
		var extra *mcpsdk.RequestExtra
		if req != nil {
			extra = req.Extra
		}
		api, err := s.s.ClientFor(extra)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return h(ctx, api, req, in)
	})
	s.s.RecordTool(t.Name)
}

// HTTPHandler serves s over Streamable HTTP at POST /mcp, statelessly, with
// pr's metadata when it is set.
func HTTPHandler(s *Server, pr ProtectedResource) http.Handler {
	return imcp.HTTPHandler(s.SDK(), pr)
}
