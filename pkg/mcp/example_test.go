package mcp_test

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
func ExampleAddTool() {
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
	if _, err = s.SDK().Connect(ctx, serverT, nil); err != nil {
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
	// Output: map[owners:[data-eng] url:https://wiki.example.com/runbooks/sales]
}
