---
title: "pkg/mcp"
linkTitle: "pkg/mcp"
weight: 14
---

```go
import "github.com/dexadata/dexaflow/pkg/mcp"
```

Package mcp builds the Dexaflow Model Context Protocol server for programs that want the stock tools plus tools of their own \(\#2178\): a runbook lookup or a diagnosis specific to one install, without forking the server.

The server talks to the control plane only through pkg/client, with the caller's identity: on the HTTP transport every request carries its own bearer and the server holds no token of its own; on stdio the one local caller is the process token. A tool added with AddTool receives the client for that identity and must not build its own.

Types come from the official SDK \(github.com/modelcontextprotocol/go\-sdk\), so a tool is declared exactly as for any go\-sdk server.

```
s := mcp.NewServer(api, serverURL, version, mcp.TransportHTTP)
mcp.AddTool(s, &mcpsdk.Tool{Name: "runbook", Description: "..."}, runbook)
http.ListenAndServe(addr, mcp.HTTPHandler(s, mcp.ProtectedResource{}))
```

## Index

- [Constants](<#constants>)
- [func AddTool\[In, Out any\]\(s \*Server, t \*mcpsdk.Tool, h ToolHandler\[In, Out\]\)](<#AddTool>)
- [func HTTPHandler\(s \*Server, pr ProtectedResource\) http.Handler](<#HTTPHandler>)
- [func LoadPlanKey\(path string\) \(\[\]byte, error\)](<#LoadPlanKey>)
- [func ValidateUIBaseURL\(s string\) error](<#ValidateUIBaseURL>)
- [type Option](<#Option>)
  - [func WithRunControl\(key \[\]byte\) Option](<#WithRunControl>)
  - [func WithUIBaseURL\(base string\) Option](<#WithUIBaseURL>)
- [type ProtectedResource](<#ProtectedResource>)
- [type Server](<#Server>)
  - [func NewServer\(api \*apiclient.ClientWithResponses, serverURL, version string, transport Transport, opts ...Option\) \*Server](<#NewServer>)
  - [func \(s \*Server\) SDK\(\) \*mcpsdk.Server](<#Server.SDK>)
- [type ToolHandler](<#ToolHandler>)
- [type Transport](<#Transport>)


## Constants

<a name="CapabilitiesURI"></a>CapabilitiesURI is the resource that reports the server's version, its transport, which optional tool groups are on and every tool it serves.

```go
const CapabilitiesURI = imcp.CapabilitiesURI
```

<a name="AddTool"></a>
## func [AddTool](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L95>)

```go
func AddTool[In, Out any](s *Server, t *mcpsdk.Tool, h ToolHandler[In, Out])
```

AddTool registers t on s. Its input and output schemas are inferred from In and Out as by the go\-sdk AddTool. Each call resolves the caller's client before h runs, so a request the identity policy refuses never reaches h. The tool appears in tools/list and in the capabilities resource.

<details><summary>Example</summary>
<p>

A program that serves the stock tools plus one of its own. In a real binary the server runs over stdio \(s.SDK\(\).Run with &mcpsdk.StdioTransport\{\}\) or over HTTP \(mcp.HTTPHandler\); here an in\-memory client calls the tool.

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
	"github.com/dexadata/dexaflow/pkg/mcp"
)

type runbookIn struct {
	DagID string `json:"dag_id" jsonschema:"the DAG to find the runbook of"`
}

type runbookOut struct {
	URL    string   `json:"url"`
	Owners []string `json:"owners"`
}

// runbook is a site tool: it looks the DAG up through the caller's client and
// answers with the install's runbook page for it.
func runbook(ctx context.Context, api *apiclient.ClientWithResponses, _ *mcpsdk.CallToolRequest, in runbookIn) (*mcpsdk.CallToolResult, runbookOut, error) {
	resp, err := api.GetDagWithResponse(ctx, in.DagID)
	if err != nil {
		return nil, runbookOut{}, err
	}
	if resp.JSON200 == nil {
		return nil, runbookOut{}, fmt.Errorf("dag %q: HTTP %d", in.DagID, resp.StatusCode())
	}
	out := runbookOut{URL: "https://wiki.example.com/runbooks/" + in.DagID}
	if resp.JSON200.Owners != nil {
		out.Owners = *resp.JSON200.Owners
	}
	return nil, out, nil
}

// A program that serves the stock tools plus one of its own. In a real binary
// the server runs over stdio (s.SDK().Run with &mcpsdk.StdioTransport{}) or
// over HTTP (mcp.HTTPHandler); here an in-memory client calls the tool.
func main() {
	ctx := context.Background()
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dag_id":"sales","owners":["data-eng"]}`))
	}))
	defer controlPlane.Close()

	api, err := apiclient.NewClientWithResponses(controlPlane.URL)
	if err != nil {
		panic(err)
	}
	s := mcp.NewServer(api, controlPlane.URL, "v0.5.9", mcp.TransportStdio)
	mcp.AddTool(s, &mcpsdk.Tool{Name: "runbook", Description: "Find the runbook of a DAG."}, runbook)

	serverT, clientT := mcpsdk.NewInMemoryTransports()
	if _, err := s.SDK().Connect(ctx, serverT, nil); err != nil {
		panic(err)
	}
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "example"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		panic(err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "runbook", Arguments: map[string]any{"dag_id": "sales"}})
	if err != nil {
		panic(err)
	}
	fmt.Println(res.StructuredContent)
}
```

#### Output

```
map[owners:[data-eng] url:https://wiki.example.com/runbooks/sales]
```

</p>
</details>

<a name="HTTPHandler"></a>
## func [HTTPHandler](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L113>)

```go
func HTTPHandler(s *Server, pr ProtectedResource) http.Handler
```

HTTPHandler serves s over Streamable HTTP at POST /mcp, statelessly, with pr's metadata when it is set.

<a name="LoadPlanKey"></a>
## func [LoadPlanKey](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L59>)

```go
func LoadPlanKey(path string) ([]byte, error)
```

LoadPlanKey reads a run control plan key of at least 32 bytes from path.

<a name="ValidateUIBaseURL"></a>
## func [ValidateUIBaseURL](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L62>)

```go
func ValidateUIBaseURL(s string) error
```

ValidateUIBaseURL reports whether s can be given to WithUIBaseURL.

<a name="Option"></a>
## type [Option](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L46>)

Option configures NewServer.

```go
type Option = imcp.Option
```

<a name="WithRunControl"></a>
### func [WithRunControl](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L56>)

```go
func WithRunControl(key []byte) Option
```

WithRunControl registers the run control tools \(trigger\_run, clear\_task, pause\_dag, unpause\_dag, apply\_plan\), with key signing their plans. A nil key draws a random one, enough for a single stdio process; replicas of an HTTP server must share a key, read with LoadPlanKey.

<a name="WithUIBaseURL"></a>
### func [WithUIBaseURL](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L50>)

```go
func WithUIBaseURL(base string) Option
```

WithUIBaseURL makes results carry web\_url links into the Dexaflow UI served at base. Check base with ValidateUIBaseURL first; "" leaves links off.

<a name="ProtectedResource"></a>
## type [ProtectedResource](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L66>)

ProtectedResource is the OAuth protected resource metadata \(RFC 9728\) the HTTP transport serves; the zero value serves none.

```go
type ProtectedResource = imcp.ProtectedResource
```

<a name="Server"></a>
## type [Server](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L71-L73>)

Server is a Dexaflow MCP server: the stock read tools, resources and prompts, the run control tools when enabled, and any tool added with AddTool.

```go
type Server struct {
    // contains filtered or unexported fields
}
```

<a name="NewServer"></a>
### func [NewServer](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L78>)

```go
func NewServer(api *apiclient.ClientWithResponses, serverURL, version string, transport Transport, opts ...Option) *Server
```

NewServer builds a server that reaches the control plane at serverURL through api. On TransportHTTP, api should carry no token: each request's own bearer is used instead. version is reported to clients.

<a name="Server.SDK"></a>
### func \(\*Server\) [SDK](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L84>)

```go
func (s *Server) SDK() *mcpsdk.Server
```

SDK returns the go\-sdk server, to run it over a transport \(Run with &mcpsdk.StdioTransport\{\}\) or to add resources and prompts.

<a name="ToolHandler"></a>
## type [ToolHandler](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L89>)

ToolHandler handles a call of a tool added with AddTool. api is the control\-plane client of the caller; the other arguments and results are those of a go\-sdk typed tool handler.

```go
type ToolHandler[In, Out any] func(ctx context.Context, api *apiclient.ClientWithResponses, req *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, Out, error)
```

<a name="Transport"></a>
## type [Transport](<https://github.com/dexadata/dexaflow/blob/main/pkg/mcp/mcp.go#L34>)

Transport selects the identity policy of a server.

```go
type Transport int
```

<a name="TransportStdio"></a>

```go
const (
    // TransportStdio serves one local caller whose identity is the token of
    // the client given to NewServer.
    TransportStdio Transport = iota
    // TransportHTTP serves many callers over Streamable HTTP; each request
    // must carry its own bearer, and one without is refused.
    TransportHTTP
)
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
